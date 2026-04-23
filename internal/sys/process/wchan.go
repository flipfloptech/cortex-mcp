package process

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

type SystemSummary struct {
	TotalThreads   int `json:"total_threads"`
	BlockedThreads int `json:"blocked_threads"`
	KernelThreads  int `json:"kernel_threads"`
}

type WchanProcessDetails struct {
	Count       int   `json:"count"`
	ExamplePids []int `json:"example_pids"`
	OmittedPids int   `json:"omitted_pids,omitempty"`
}

type WchanDetails struct {
	TotalThreads      int                             `json:"total_threads"`
	AffectedProcesses map[string]*WchanProcessDetails `json:"affected_processes"`
}

type ThreadDetail struct {
	TID   int    `json:"tid"`
	Comm  string `json:"comm"`
	Wchan string `json:"wchan"`
	State string `json:"state"`
}

type ThreadWchanResult struct {
	SystemSummary SystemSummary            `json:"system_summary"`
	ThreadStates  map[string]int           `json:"thread_states,omitempty"`
	BlockedWchan  map[string]*WchanDetails `json:"blocked_wchan,omitempty"`
	Threads       []ThreadDetail           `json:"threads,omitempty"`
}

type threadTask struct {
	pid       int
	tid       int
	isKthread bool
}

type threadResult struct {
	wchan     string
	state     string
	comm      string
	tid       int
	isKthread bool
}

// GetThreadWchan scans threads to determine their wchan or fallback thread state.
func GetThreadWchan(ctx context.Context, targetPid *int) (*ThreadWchanResult, error) {
	type pidInfo struct {
		pid       int
		isKthread bool
	}
	var pids []pidInfo

	if targetPid != nil {
		isKthread := false
		if stat, err := readStat(*targetPid); err == nil && stat.ppid == 2 {
			isKthread = true
		}
		pids = append(pids, pidInfo{pid: *targetPid, isKthread: isKthread})
	} else {
		// Use getPIDs() from process.go
		allPids := getPIDs()
		for _, pid := range allPids {
			isKthread := false
			if stat, err := readStat(pid); err == nil && stat.ppid == 2 {
				isKthread = true
			}
			pids = append(pids, pidInfo{pid: pid, isKthread: isKthread})
		}
	}

	tasksCh := make(chan threadTask, 1024)
	resultsCh := make(chan threadResult, 1024)

	numWorkers := runtime.NumCPU() * 2
	var wg sync.WaitGroup

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range tasksCh {
				select {
				case <-ctx.Done():
					return
				default:
				}

				wchanPath := fmt.Sprintf("/proc/%d/task/%d/wchan", task.pid, task.tid)
				wchanData, bPtr1, err := readFileBuffered(wchanPath)
				var wchan string
				if err == nil {
					wchan = strings.TrimSpace(string(wchanData))
				}
				if bPtr1 != nil {
					putBuf(bPtr1)
				}

				statusPath := fmt.Sprintf("/proc/%d/task/%d/status", task.pid, task.tid)
				statusData, bPtr2, err := readFileBuffered(statusPath)
				var state string
				if err == nil {
					state = parseStateFromStatus(statusData)
				}
				if bPtr2 != nil {
					putBuf(bPtr2)
				}

				if state == "" {
					continue // Task died or inaccessible
				}

				commPath := fmt.Sprintf("/proc/%d/task/%d/comm", task.pid, task.tid)
				commData, bPtr3, err := readFileBuffered(commPath)
				var comm string
				if err == nil {
					comm = strings.TrimSpace(string(commData))
				}
				if bPtr3 != nil {
					putBuf(bPtr3)
				}

				resultsCh <- threadResult{wchan: wchan, state: state, comm: comm, tid: task.tid, isKthread: task.isKthread}
			}
		}()
	}

	// Producer
	go func() {
		defer close(tasksCh)
		for _, info := range pids {
			select {
			case <-ctx.Done():
				return
			default:
			}
			tids := getTIDs(info.pid)
			for _, tid := range tids {
				tasksCh <- threadTask{pid: info.pid, tid: tid, isKthread: info.isKthread}
			}
		}
	}()

	// Closer
	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	result := &ThreadWchanResult{
		SystemSummary: SystemSummary{},
		ThreadStates:  make(map[string]int),
	}

	isMode1 := targetPid == nil
	if isMode1 {
		result.BlockedWchan = make(map[string]*WchanDetails)
	} else {
		result.Threads = make([]ThreadDetail, 0)
	}

	for res := range resultsCh {
		result.SystemSummary.TotalThreads++

		if res.isKthread {
			result.SystemSummary.KernelThreads++
			res.state = "[kthread] " + res.state
			if res.wchan != "" && res.wchan != "0" {
				res.wchan = "[kthread] " + res.wchan
			}
		}

		if isMode1 {
			// Mode 1: Group by wchan, comm, truncate example pids to 15
			if res.wchan == "" || res.wchan == "0" {
				result.ThreadStates[res.state]++
			} else {
				result.SystemSummary.BlockedThreads++
				result.ThreadStates[res.state]++ // Also track state for baseline perspective

				wDetails, ok := result.BlockedWchan[res.wchan]
				if !ok {
					wDetails = &WchanDetails{
						AffectedProcesses: make(map[string]*WchanProcessDetails),
					}
					result.BlockedWchan[res.wchan] = wDetails
				}
				wDetails.TotalThreads++

				procDetails, ok := wDetails.AffectedProcesses[res.comm]
				if !ok {
					procDetails = &WchanProcessDetails{
						ExamplePids: make([]int, 0, 15),
					}
					wDetails.AffectedProcesses[res.comm] = procDetails
				}

				procDetails.Count++
				if len(procDetails.ExamplePids) < 15 {
					procDetails.ExamplePids = append(procDetails.ExamplePids, res.tid)
				} else {
					procDetails.OmittedPids++
				}
			}
		} else {
			// Mode 2: Flat threads array, no aggregation
			if res.wchan != "" && res.wchan != "0" {
				result.SystemSummary.BlockedThreads++
			}
			result.Threads = append(result.Threads, ThreadDetail{
				TID:   res.tid,
				Comm:  res.comm,
				Wchan: res.wchan,
				State: res.state,
			})
		}
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	return result, nil
}

func getTIDs(pid int) []int {
	path := fmt.Sprintf("/proc/%d/task", pid)
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	names, err := f.Readdirnames(0)
	if err != nil {
		return nil
	}

	var tids []int
	for _, name := range names {
		if name[0] >= '0' && name[0] <= '9' {
			if tid, err := strconv.Atoi(name); err == nil {
				tids = append(tids, tid)
			}
		}
	}
	return tids
}

func parseStateFromStatus(data []byte) string {
	pos := 0
	prefix := []byte("State:\t")
	for pos < len(data) {
		end := bytes.IndexByte(data[pos:], '\n')
		if end < 0 {
			end = len(data) - pos
		}
		line := data[pos : pos+end]

		if bytes.HasPrefix(line, prefix) {
			return string(line[len(prefix):])
		}
		pos += end + 1
	}
	return ""
}
