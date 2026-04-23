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

type ThreadWchanResult struct {
	SystemSummary SystemSummary  `json:"system_summary"`
	BlockedWchan  map[string]int `json:"blocked_wchan"`
	ThreadStates  map[string]int `json:"thread_states"`
}

type threadTask struct {
	pid       int
	tid       int
	isKthread bool
}

type threadResult struct {
	wchan     string
	state     string
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

				resultsCh <- threadResult{wchan: wchan, state: state, isKthread: task.isKthread}
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
		BlockedWchan:  make(map[string]int),
		ThreadStates:  make(map[string]int),
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

		if res.wchan == "" || res.wchan == "0" {
			// Degradation or running thread
			result.ThreadStates[res.state]++
		} else {
			// Blocked on kernel function
			result.BlockedWchan[res.wchan]++
			result.SystemSummary.BlockedThreads++
			// Track the D state (or any state) for accuracy if needed,
			// but we also include it in ThreadStates per requirement
			result.ThreadStates[res.state]++
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
