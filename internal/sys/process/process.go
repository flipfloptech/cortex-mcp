package process

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/user"
	"regexp"
	"sort"
	"strconv"
	"strings"
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
}

const userHZ = 100.0 // Standard clock ticks per second on Linux

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
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, err
	}
	parts := bytes.Fields(data)
	if len(parts) == 0 {
		return 0, fmt.Errorf("empty /proc/uptime")
	}
	return strconv.ParseFloat(string(parts[0]), 64)
}

func getPIDs() []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var pids []int
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if pid, err := strconv.Atoi(entry.Name()); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

func readStat(pid int) (procStat, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return procStat{}, err
	}
	// /proc/[pid]/stat format: pid (comm) state ppid pgrp session tty_nr tpgid flags minflt cminflt majflt cmajflt utime stime ...
	// The comm field can contain spaces, so we must find the last ')' to correctly parse the rest.
	rparen := bytes.LastIndexByte(data, ')')
	if rparen < 0 {
		return procStat{}, fmt.Errorf("invalid stat format")
	}

	name := string(data[bytes.IndexByte(data, '(')+1 : rparen])
	fields := bytes.Fields(data[rparen+2:])
	if len(fields) < 13 {
		return procStat{}, fmt.Errorf("not enough fields in stat")
	}

	state := string(fields[0])
	utime, _ := strconv.ParseUint(string(fields[11]), 10, 64)
	stime, _ := strconv.ParseUint(string(fields[12]), 10, 64)

	return procStat{
		name:  name,
		state: state,
		utime: utime,
		stime: stime,
	}, nil
}

func readStatus(pid int) (int, uint64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, 0, err
	}

	var uid int
	var vmRSS uint64

	lines := bytes.Split(data, []byte("\n"))
	for _, line := range lines {
		if bytes.HasPrefix(line, []byte("Uid:\t")) {
			fields := bytes.Fields(line)
			if len(fields) > 1 {
				uid, _ = strconv.Atoi(string(fields[1]))
			}
		} else if bytes.HasPrefix(line, []byte("VmRSS:\t")) {
			fields := bytes.Fields(line)
			if len(fields) > 1 {
				kb, _ := strconv.ParseUint(string(fields[1]), 10, 64)
				vmRSS = kb * 1024
			}
		}
	}

	return uid, vmRSS, nil
}

func readCmdline(pid int) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", fmt.Errorf("empty cmdline")
	}
	// cmdline is null-separated
	data = bytes.ReplaceAll(data, []byte{0}, []byte{' '})
	return strings.TrimSpace(string(data)), nil
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
