// Package processstates implements the get_process_states diagnostic tool.
// It scans /proc/[pid]/stat natively to classify every process by scheduler
// state (running, sleeping, disk_sleep, zombie, stopped, traced, idle),
// surfaces unreaped zombies together with the parent that is failing to
// reap them, and lists D-state (uninterruptible sleep) processes with the
// kernel function they are blocked in (/proc/[pid]/wchan).
package processstates

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func init() {
	registry.Register(New())
}

const (
	// maxZombies caps the itemized zombie list so LLM payloads stay small;
	// counts.zombie always carries the untruncated total.
	maxZombies = 25
	// maxDState caps the itemized D-state list; counts.disk_sleep always
	// carries the untruncated total.
	maxDState = 20
	// dStateWarnThreshold: more than this many D-state processes raises a
	// warning (a handful is normal under I/O; a storm indicates a stall).
	dStateWarnThreshold = 5
	// cmdlineMaxLen truncates long command lines to keep payloads bounded.
	cmdlineMaxLen = 100
)

// Tool implements registry.Tool for get_process_states.
type Tool struct {
	procfsRoot string
}

// New returns a new instance of the Tool wired to the real procfs.
func New() *Tool {
	return &Tool{procfsRoot: "/proc"}
}

// Name returns the unique identifier for this tool.
func (t *Tool) Name() string {
	return "get_process_states"
}

// Description provides a concise summary of what this tool does.
func (t *Tool) Description() string {
	return "Classify all processes by scheduler state, surfacing unreaped zombies and D-state (uninterruptible I/O) processes."
}

// Help provides detailed documentation.
func (t *Tool) Help() string {
	return `Scans every /proc/[pid]/stat entry natively and aggregates processes by
scheduler state: R=running, S=sleeping, D=disk_sleep (uninterruptible),
Z=zombie, T=stopped, t=traced, I=idle; unrecognized state characters are
counted under "other".

Data sources:
- /proc/[pid]/stat  — state (field 3) and PPID (field 4), parsed after the
  last ')' so comm values containing spaces or parentheses cannot skew fields
- /proc/[pid]/comm  — process name (falls back to the comm embedded in stat)
- /proc/[pid]/cmdline — full command line for D-state processes (NUL bytes
  joined with spaces, truncated to 100 chars)
- /proc/[pid]/wchan — kernel wait channel for D-state processes ("?" when
  unreadable)

Output precomputes per-state counts, itemizes zombies (capped at 25) with
their parent's comm and state, groups parents_with_zombies (is_init flags
zombies awaiting PID 1 reaping), and lists d_state processes (capped at 20)
with wchan and cmdline. warning_reasons flag zombies whose living parent is
not PID 1 (a reaping bug in that parent) and more than 5 D-state processes
(possible I/O or lock stall). PIDs that vanish mid-scan are skipped.`
}

// Category organizes the tool within the registry.
func (t *Tool) Category() registry.Category {
	return registry.CategoryCompute
}

// Hidden hides the tool from general LLM discovery if true.
func (t *Tool) Hidden() bool {
	return false
}

// Parameters defines the expected input schema (none for this tool).
func (t *Tool) Parameters() []registry.ToolParam {
	return nil
}

// IsSupported checks that per-process stat entries are readable.
func (t *Tool) IsSupported() (bool, string) {
	if _, err := os.ReadFile(filepath.Join(t.procfsRoot, "self", "stat")); err != nil {
		return false, fmt.Sprintf("process state scan not supported (%s/self/stat not readable)", t.procfsRoot)
	}
	return true, ""
}

// StateCounts aggregates processes per scheduler state.
type StateCounts struct {
	Running   int `json:"running"`
	Sleeping  int `json:"sleeping"`
	DiskSleep int `json:"disk_sleep"`
	Zombie    int `json:"zombie"`
	Stopped   int `json:"stopped"`
	Traced    int `json:"traced"`
	Idle      int `json:"idle"`
	Other     int `json:"other"`
	Total     int `json:"total"`
}

// ZombieProc is one zombie process with its (non-)reaping parent.
type ZombieProc struct {
	PID         int    `json:"pid"`
	Comm        string `json:"comm"`
	PPID        int    `json:"ppid"`
	ParentComm  string `json:"parent_comm"`
	ParentState string `json:"parent_state"`
}

// ZombieParent groups zombies by the parent that should reap them.
type ZombieParent struct {
	PPID        int    `json:"ppid"`
	Comm        string `json:"comm"`
	ZombieCount int    `json:"zombie_count"`
	IsInit      bool   `json:"is_init"`
}

// DStateProc is one process in uninterruptible (D) sleep.
type DStateProc struct {
	PID     int    `json:"pid"`
	Comm    string `json:"comm"`
	Wchan   string `json:"wchan"`
	Cmdline string `json:"cmdline"`
}

// Output is the tool's data payload.
type Output struct {
	Counts             StateCounts    `json:"counts"`
	Zombies            []ZombieProc   `json:"zombies"`
	ParentsWithZombies []ZombieParent `json:"parents_with_zombies"`
	DState             []DStateProc   `json:"d_state"`
	WarningReasons     []string       `json:"warning_reasons"`
}

// procEntry is one scanned process.
type procEntry struct {
	pid   int
	comm  string
	state byte
	ppid  int
}

// Execute scans the procfs tree and classifies every process by state.
func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	if err := ctx.Err(); err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("context canceled before execution: %v", err)), nil
	}

	pids, err := listPids(t.procfsRoot)
	if err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("cannot scan %s: %v", t.procfsRoot, err)), nil
	}

	entries := make(map[int]procEntry, len(pids))
	order := make([]int, 0, len(pids))
	for _, pid := range pids {
		// Cheap cancellation check between pid iterations so a huge
		// process table cannot pin the tool past its deadline.
		if err := ctx.Err(); err != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("context canceled during process scan: %v", err)), nil
		}

		dir := filepath.Join(t.procfsRoot, strconv.Itoa(pid))
		data, readErr := os.ReadFile(filepath.Join(dir, "stat"))
		if readErr != nil {
			continue // pid vanished mid-scan
		}
		si, parseErr := parseStat(data)
		if parseErr != nil {
			continue
		}
		comm := readComm(dir)
		if comm == "" {
			comm = si.comm
		}
		entries[pid] = procEntry{pid: pid, comm: comm, state: si.state, ppid: si.ppid}
		order = append(order, pid)
	}

	var counts StateCounts
	zombies := []ZombieProc{}
	dState := []DStateProc{}
	zombiesByParent := map[int]int{}
	parentOrder := []int{}

	for _, pid := range order {
		e := entries[pid]
		counts.Total++
		switch e.state {
		case 'R':
			counts.Running++
		case 'S':
			counts.Sleeping++
		case 'D':
			counts.DiskSleep++
			if len(dState) < maxDState {
				dir := filepath.Join(t.procfsRoot, strconv.Itoa(pid))
				dState = append(dState, DStateProc{
					PID:     pid,
					Comm:    e.comm,
					Wchan:   readWchan(dir),
					Cmdline: readCmdline(dir),
				})
			}
		case 'Z':
			counts.Zombie++
			if _, seen := zombiesByParent[e.ppid]; !seen {
				parentOrder = append(parentOrder, e.ppid)
			}
			zombiesByParent[e.ppid]++
			if len(zombies) < maxZombies {
				parentComm, parentState := "?", "gone"
				if parent, ok := entries[e.ppid]; ok {
					parentComm = parent.comm
					parentState = stateName(parent.state)
				}
				zombies = append(zombies, ZombieProc{
					PID:         pid,
					Comm:        e.comm,
					PPID:        e.ppid,
					ParentComm:  parentComm,
					ParentState: parentState,
				})
			}
		case 'T':
			counts.Stopped++
		case 't':
			counts.Traced++
		case 'I':
			counts.Idle++
		default:
			counts.Other++
		}
	}

	parents := []ZombieParent{}
	for _, ppid := range parentOrder {
		comm := "?"
		if parent, ok := entries[ppid]; ok {
			comm = parent.comm
		}
		parents = append(parents, ZombieParent{
			PPID:        ppid,
			Comm:        comm,
			ZombieCount: zombiesByParent[ppid],
			IsInit:      ppid == 1,
		})
	}
	sort.SliceStable(parents, func(i, j int) bool {
		if parents[i].ZombieCount != parents[j].ZombieCount {
			return parents[i].ZombieCount > parents[j].ZombieCount
		}
		return parents[i].PPID < parents[j].PPID
	})

	warnings := []string{}
	for _, p := range parents {
		if p.IsInit {
			continue // PID 1 reaps orphans as part of normal operation
		}
		if _, alive := entries[p.PPID]; !alive {
			continue // parent already gone; init will inherit the zombies
		}
		warnings = append(warnings,
			fmt.Sprintf("%d zombie(s) not being reaped by %s (pid %d)", p.ZombieCount, p.Comm, p.PPID))
	}
	if counts.DiskSleep > dStateWarnThreshold {
		warnings = append(warnings,
			fmt.Sprintf("%d processes in uninterruptible (D-state) sleep — possible I/O or lock stall", counts.DiskSleep))
	}

	status := registry.StatusOK
	if len(warnings) > 0 {
		status = registry.StatusWarning
	}

	out := Output{
		Counts:             counts,
		Zombies:            zombies,
		ParentsWithZombies: parents,
		DState:             dState,
		WarningReasons:     warnings,
	}

	summary := fmt.Sprintf("Scanned %d processes: %d running, %d sleeping, %d disk_sleep, %d zombie, %d stopped, %d traced, %d idle",
		counts.Total, counts.Running, counts.Sleeping, counts.DiskSleep, counts.Zombie, counts.Stopped, counts.Traced, counts.Idle)

	res := registry.NewResult(t.Name(), status, summary, out)
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	res.Metadata.FilteringMethod = "deterministic"
	return res, nil
}

// statInfo is the subset of /proc/[pid]/stat this tool needs.
type statInfo struct {
	comm  string
	state byte
	ppid  int
}

// parseStat extracts comm, state (field 3) and ppid (field 4) from a
// /proc/[pid]/stat line. Fields after the comm are located relative to the
// LAST ')' because comm itself may contain spaces and parentheses.
func parseStat(data []byte) (statInfo, error) {
	lp := bytes.IndexByte(data, '(')
	rp := bytes.LastIndexByte(data, ')')
	if lp < 0 || rp < 0 || rp < lp {
		return statInfo{}, fmt.Errorf("malformed stat line: comm parentheses not found")
	}
	fields := strings.Fields(string(data[rp+1:]))
	if len(fields) < 2 {
		return statInfo{}, fmt.Errorf("malformed stat line: %d fields after comm, want >= 2", len(fields))
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return statInfo{}, fmt.Errorf("malformed stat line: ppid %q: %w", fields[1], err)
	}
	return statInfo{
		comm:  string(data[lp+1 : rp]),
		state: fields[0][0],
		ppid:  ppid,
	}, nil
}

// listPids returns all numeric directory names under root, sorted ascending.
func listPids(root string) ([]int, error) {
	dirEntries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	pids := make([]int, 0, len(dirEntries))
	for _, de := range dirEntries {
		if !de.IsDir() {
			continue
		}
		pid, convErr := strconv.Atoi(de.Name())
		if convErr != nil || pid <= 0 {
			continue
		}
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	return pids, nil
}

// readComm reads /proc/[pid]/comm, returning "" when unreadable so the
// caller can fall back to the comm embedded in stat.
func readComm(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "comm"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// readCmdline reads /proc/[pid]/cmdline, joining NUL-separated arguments
// with spaces and truncating to cmdlineMaxLen characters. Kernel threads
// (empty cmdline) yield "".
func readCmdline(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "cmdline"))
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(strings.ReplaceAll(string(data), "\x00", " "))
	if len(s) > cmdlineMaxLen {
		s = s[:cmdlineMaxLen]
	}
	return s
}

// readWchan reads /proc/[pid]/wchan; unreadable or empty values degrade
// to "?" so a restricted kernel never aborts the scan.
func readWchan(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "wchan"))
	if err != nil {
		return "?"
	}
	s := strings.TrimSpace(string(data))
	if s == "" {
		return "?"
	}
	return s
}

// stateName maps a /proc stat state character to its human-readable name.
func stateName(state byte) string {
	switch state {
	case 'R':
		return "running"
	case 'S':
		return "sleeping"
	case 'D':
		return "disk_sleep"
	case 'Z':
		return "zombie"
	case 'T':
		return "stopped"
	case 't':
		return "traced"
	case 'I':
		return "idle"
	default:
		return "other"
	}
}
