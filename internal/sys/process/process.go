package process

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/user"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"
)

type Process struct {
	PID         int     `json:"pid"`
	UID         int     `json:"uid"`
	User        string  `json:"user"`
	Name        string  `json:"name"`
	State       string  `json:"state"`
	Cmdline     *string `json:"cmdline"` // Nullable if unreadable
	CPUPercent  float64 `json:"cpu_percent"`
	MemoryBytes uint64  `json:"memory_bytes"`
}

type FilterOptions struct {
	SortBy         string // "cpu" or "memory"
	Limit          int
	SampleDuration time.Duration
	UserRegex      *regexp.Regexp
	NameRegex      *regexp.Regexp
	StateRegex     *regexp.Regexp
	CmdlineRegex   *regexp.Regexp
}

type procStat struct {
	utime uint64
	stime uint64
	state string
	name  string
	ppid  int
}

const userHZ = 100.0 // Standard clock ticks per second on Linux

var bufPool = sync.Pool{
	New: func() interface{} {
		b := make([]byte, 8192)
		return &b
	},
}

// getBuf borrows a buffer from the pool.
func getBuf() *[]byte {
	return bufPool.Get().(*[]byte)
}

// putBuf returns a buffer to the pool.
func putBuf(b *[]byte) {
	bufPool.Put(b)
}

// readFileBuffered reads a file into a pooled buffer.
// The returned slice is a subslice of the pooled buffer.
// The caller must call putBuf on the returned pooled pointer.
func readFileBuffered(path string) ([]byte, *[]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = f.Close() }()

	b := getBuf()
	n, err := f.Read(*b)
	if err != nil && err != io.EOF {
		putBuf(b)
		return nil, nil, err
	}
	return (*b)[:n], b, nil
}

// IsSupported checks if the required proc files exist.
func IsSupported() (bool, string) {
	if _, err := os.Stat("/proc/stat"); err != nil {
		return false, "/proc/stat not found"
	}
	if _, err := os.Stat("/proc/uptime"); err != nil {
		return false, "/proc/uptime not found"
	}
	return true, ""
}

// GetList is the main entry point to gather processes.
func GetList(ctx context.Context, opts FilterOptions) ([]Process, error) {
	uptime1, err := readUptime()
	if err != nil {
		return nil, fmt.Errorf("failed to read uptime: %w", err)
	}

	pids := getPIDs()
	stats1 := make(map[int]procStat, len(pids))
	for _, pid := range pids {
		if stat, err := readStat(pid); err == nil {
			stats1[pid] = stat
		}
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(opts.SampleDuration):
	}

	uptime2, err := readUptime()
	if err != nil {
		return nil, fmt.Errorf("failed to read uptime: %w", err)
	}

	deltaUptime := uptime2 - uptime1
	if deltaUptime <= 0 {
		deltaUptime = 0.001 // prevent division by zero
	}

	var results []Process
	userCache := make(map[int]string)

	for pid, stat1 := range stats1 {
		stat2, err := readStat(pid)
		if err != nil {
			continue // Process likely died
		}

		uid, vmRSS, err := readStatus(pid)
		if err != nil {
			continue
		}

		processTimeDelta := float64((stat2.utime - stat1.utime) + (stat2.stime - stat1.stime))
		cpuPercent := 100.0 * (processTimeDelta / userHZ) / deltaUptime

		username := getUsername(uid, userCache)
		if opts.UserRegex != nil && !opts.UserRegex.MatchString(username) {
			continue
		}

		if opts.NameRegex != nil && !opts.NameRegex.MatchString(stat2.name) {
			continue
		}

		if opts.StateRegex != nil && !opts.StateRegex.MatchString(stat2.state) {
			continue
		}

		cmdlineStr, err := readCmdline(pid)
		var cmdline *string
		if err == nil {
			cmdline = &cmdlineStr
		}

		if opts.CmdlineRegex != nil {
			if cmdline == nil || !opts.CmdlineRegex.MatchString(*cmdline) {
				continue
			}
		}

		results = append(results, Process{
			PID:         pid,
			UID:         uid,
			User:        username,
			Name:        stat2.name,
			State:       stat2.state,
			Cmdline:     cmdline,
			CPUPercent:  cpuPercent,
			MemoryBytes: vmRSS,
		})
	}

	if opts.SortBy == "memory" {
		sort.Slice(results, func(i, j int) bool {
			return results[i].MemoryBytes > results[j].MemoryBytes
		})
	} else {
		sort.Slice(results, func(i, j int) bool {
			return results[i].CPUPercent > results[j].CPUPercent
		})
	}

	if opts.Limit > 0 && len(results) > opts.Limit {
		results = results[:opts.Limit]
	}

	return results, nil
}

func readUptime() (float64, error) {
	data, bPtr, err := readFileBuffered("/proc/uptime")
	if err != nil {
		return 0, err
	}
	defer putBuf(bPtr)

	idx := bytes.IndexByte(data, ' ')
	if idx < 0 {
		return 0, fmt.Errorf("invalid /proc/uptime format")
	}

	return strconv.ParseFloat(string(data[:idx]), 64)
}

func getPIDs() []int {
	f, err := os.Open("/proc")
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	names, err := f.Readdirnames(0)
	if err != nil {
		return nil
	}

	var pids []int
	for _, name := range names {
		if name[0] >= '0' && name[0] <= '9' {
			if pid, err := strconv.Atoi(name); err == nil {
				pids = append(pids, pid)
			}
		}
	}
	return pids
}

func parseUintBytes(b []byte) uint64 {
	var n uint64
	for _, c := range b {
		if c >= '0' && c <= '9' {
			n = n*10 + uint64(c-'0')
		}
	}
	return n
}

func parseState(b []byte) string {
	if len(b) > 0 {
		switch b[0] {
		case 'S':
			return "S"
		case 'R':
			return "R"
		case 'I':
			return "I"
		case 'Z':
			return "Z"
		case 'D':
			return "D"
		case 'T':
			return "T"
		case 't':
			return "t"
		case 'X', 'x':
			return "X"
		}
	}
	return string(b)
}

func readStat(pid int) (procStat, error) {
	data, bPtr, err := readFileBuffered("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return procStat{}, err
	}
	defer putBuf(bPtr)

	rparen := bytes.LastIndexByte(data, ')')
	if rparen < 0 {
		return procStat{}, fmt.Errorf("invalid stat format")
	}

	name := string(data[bytes.IndexByte(data, '(')+1 : rparen])

	// Skip the `) `
	pos := rparen + 2
	fieldIdx := 2 // we are at the state field (index 2)

	var state string
	var utime, stime uint64
	var ppid int

	for pos < len(data) {
		end := bytes.IndexByte(data[pos:], ' ')
		if end < 0 {
			end = len(data) - pos
		}

		if fieldIdx == 2 {
			state = parseState(data[pos : pos+end])
		} else if fieldIdx == 3 {
			ppid = int(parseUintBytes(data[pos : pos+end]))
		} else if fieldIdx == 13 {
			utime = parseUintBytes(data[pos : pos+end])
		} else if fieldIdx == 14 {
			stime = parseUintBytes(data[pos : pos+end])
			break // We don't need any fields after stime
		}

		pos += end + 1
		fieldIdx++
	}

	return procStat{
		name:  name,
		state: state,
		ppid:  ppid,
		utime: utime,
		stime: stime,
	}, nil
}

func readStatus(pid int) (int, uint64, error) {
	data, bPtr, err := readFileBuffered("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0, 0, err
	}
	defer putBuf(bPtr)

	var uid int
	var vmRSS uint64
	var foundUid, foundVmRSS bool

	pos := 0
	uidPrefix := []byte("Uid:\t")
	vmRSSPrefix := []byte("VmRSS:\t")

	for pos < len(data) {
		end := bytes.IndexByte(data[pos:], '\n')
		if end < 0 {
			end = len(data) - pos
		}
		line := data[pos : pos+end]

		if bytes.HasPrefix(line, uidPrefix) {
			valStart := len(uidPrefix)
			for valStart < len(line) && (line[valStart] == '\t' || line[valStart] == ' ') {
				valStart++
			}
			valEnd := valStart
			for valEnd < len(line) && line[valEnd] != '\t' && line[valEnd] != ' ' {
				valEnd++
			}
			if valEnd > valStart {
				uid = int(parseUintBytes(line[valStart:valEnd]))
				foundUid = true
			}
		} else if bytes.HasPrefix(line, vmRSSPrefix) {
			valStart := len(vmRSSPrefix)
			for valStart < len(line) && (line[valStart] == '\t' || line[valStart] == ' ') {
				valStart++
			}
			valEnd := valStart
			for valEnd < len(line) && line[valEnd] != '\t' && line[valEnd] != ' ' {
				valEnd++
			}
			if valEnd > valStart {
				kb := parseUintBytes(line[valStart:valEnd])
				vmRSS = kb * 1024
				foundVmRSS = true
			}
		}

		if foundUid && foundVmRSS {
			break
		}

		pos += end + 1
	}

	return uid, vmRSS, nil
}

func readCmdline(pid int) (string, error) {
	data, bPtr, err := readFileBuffered("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return "", err
	}
	defer putBuf(bPtr)

	if len(data) == 0 {
		return "", fmt.Errorf("empty cmdline")
	}

	for i, b := range data {
		if b == 0 {
			data[i] = ' '
		}
	}
	return string(bytes.TrimSpace(data)), nil
}

func getUsername(uid int, cache map[int]string) string {
	if name, ok := cache[uid]; ok {
		return name
	}
	u, err := user.LookupId(strconv.Itoa(uid))
	if err == nil {
		cache[uid] = u.Username
		return u.Username
	}
	uidStr := strconv.Itoa(uid)
	cache[uid] = uidStr
	return uidStr
}
