// Package oomevents implements the query_oom_events diagnostic tool.
// It reads the kernel ring buffer natively via the klogctl syscall
// (falling back to the dmesg binary), extracts OOM-killer records, pairs
// the kernel's oom-kill context line with its Killed-process line, and
// converts boot-relative timestamps to wallclock RFC3339 using the btime
// field of /proc/stat.
package oomevents

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func init() {
	registry.Register(New())
}

const (
	// defaultLastN is how many of the newest events are returned when the
	// caller does not specify last_n.
	defaultLastN = 10
	// maxLastN caps last_n so LLM payloads stay bounded.
	maxLastN = 50
	// fallbackBufSize is used when SYSLOG_ACTION_SIZE_BUFFER is unavailable.
	fallbackBufSize = 1 << 20 // 1 MB
)

// Tool implements registry.Tool for query_oom_events.
type Tool struct {
	procfsRoot  string
	klogRead    func() ([]byte, error)
	execCommand func(ctx context.Context, name string, args ...string) ([]byte, error)
	lookPath    func(file string) (string, error)
}

// New returns a new instance of the Tool wired to the real kernel ring
// buffer, PATH, and procfs.
func New() *Tool {
	return &Tool{
		procfsRoot: "/proc",
		klogRead: func() ([]byte, error) {
			// SYSLOG_ACTION_SIZE_BUFFER (10) sizes the buffer; fall back
			// to 1MB when the kernel refuses to report it.
			size, err := syscall.Klogctl(10, nil)
			if err != nil || size <= 0 {
				size = fallbackBufSize
			}
			buf := make([]byte, size)
			// SYSLOG_ACTION_READ_ALL (3) is non-destructive: it does not
			// consume the ring buffer.
			n, err := syscall.Klogctl(3, buf)
			if err != nil {
				return nil, err
			}
			return buf[:n], nil
		},
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		},
		lookPath: exec.LookPath,
	}
}

// Name returns the unique identifier for this tool.
func (t *Tool) Name() string {
	return "query_oom_events"
}

// Description provides a concise summary of what this tool does.
func (t *Tool) Description() string {
	return "Extract OOM-killer events from the kernel ring buffer with victim, memory footprint, cgroup and wallclock time."
}

// Help provides detailed documentation.
func (t *Tool) Help() string {
	return `Queries the kernel ring buffer for OOM-killer activity and returns
structured events instead of raw log lines.

Data sources:
- Kernel ring buffer, read natively via the klogctl syscall
  (SYSLOG_ACTION_READ_ALL, non-destructive), falling back to the dmesg
  binary ('dmesg -r') when the syscall is blocked
- /proc/stat 'btime' (boot time) to convert boot-relative [offset]
  timestamps into wallclock RFC3339; when either the btime or the [offset]
  prefix is unavailable the event is preserved with time=null

Parsing pairs the kernel's adjacent records by pid:
  oom-kill:constraint=...,oom_memcg=...,task=...,pid=...   (context line)
  Out of memory: Killed process PID (comm) total-vm:...kB  (global scope)
  Memory cgroup out of memory: Killed process ...          (cgroup scope)

Each event precomputes total_vm_mb, anon_rss_mb, file_rss_mb and
shmem_rss_mb (kB -> MB, 1 decimal), carries constraint, memcg and scope
(global|cgroup), and events are returned newest-first. count_by_comm
aggregates every event found, not just the ones shown.

Parameters:
- last_n: Optional integer. Number of newest events to return
  (default 10, max 50).

Caveat: the kernel ring buffer covers recent history only; older OOM
events rotate out. Reading it may require elevated privileges
(CAP_SYSLOG) on kernels with dmesg_restrict=1.`
}

// Category organizes the tool within the registry.
func (t *Tool) Category() registry.Category {
	return registry.CategoryMemory
}

// Hidden hides the tool from general LLM discovery if true.
func (t *Tool) Hidden() bool {
	return false
}

// Parameters defines the expected input schema.
func (t *Tool) Parameters() []registry.ToolParam {
	return []registry.ToolParam{
		{
			Name:        "last_n",
			Type:        "integer",
			Description: "Optional: Number of newest OOM events to return (default 10, max 50).",
			Required:    false,
			Default:     "10",
		},
	}
}

// IsSupported checks that at least one ring buffer source is available.
func (t *Tool) IsSupported() (bool, string) {
	if _, err := t.klogRead(); err == nil {
		return true, ""
	}
	if _, err := t.lookPath("dmesg"); err == nil {
		return true, ""
	}
	return false, "OOM event query not supported (missing dmesg binary and klogctl blocked)"
}

// OOMEvent is one OOM-killer incident extracted from the ring buffer.
type OOMEvent struct {
	Time       *string `json:"time"` // RFC3339, or null when unresolvable
	VictimPID  int     `json:"victim_pid"`
	VictimComm string  `json:"victim_comm"`
	TotalVMMB  float64 `json:"total_vm_mb"`
	AnonRSSMB  float64 `json:"anon_rss_mb"`
	FileRSSMB  float64 `json:"file_rss_mb"`
	ShmemRSSMB float64 `json:"shmem_rss_mb"`
	Constraint string  `json:"constraint,omitempty"`
	Memcg      string  `json:"memcg,omitempty"`
	Scope      string  `json:"scope"` // "global" or "cgroup"
}

// Output is the tool's data payload.
type Output struct {
	Events      []OOMEvent     `json:"events"` // newest first
	TotalFound  int            `json:"total_found"`
	CountByComm map[string]int `json:"count_by_comm"`
	Note        string         `json:"note"`
}

// args is the accepted parameter payload.
type args struct {
	LastN int `json:"last_n"`
}

// Execute reads the ring buffer, extracts OOM events and returns the
// newest last_n of them.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	if err := ctx.Err(); err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("context canceled before execution: %v", err)), nil
	}

	parsed := args{LastN: defaultLastN}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("invalid arguments: %v", err)), nil
		}
	}
	if parsed.LastN <= 0 {
		parsed.LastN = defaultLastN
	}
	if parsed.LastN > maxLastN {
		parsed.LastN = maxLastN
	}

	// Native klogctl read first; dmesg -r fallback when blocked or empty.
	buf, klogErr := t.klogRead()
	if klogErr != nil || len(buf) == 0 {
		out, cmdErr := t.execCommand(ctx, "dmesg", "-r")
		if cmdErr != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf(
				"failed to read kernel ring buffer (klogctl: %v; dmesg: %v) — reading the kernel log may require elevated privileges (CAP_SYSLOG or root, see kernel.dmesg_restrict)",
				klogErr, cmdErr)), nil
		}
		buf = out
	}

	btime, haveBtime := readBtime(t.procfsRoot)
	all := parseOOMEvents(buf, btime, haveBtime)

	countByComm := map[string]int{}
	for _, ev := range all {
		countByComm[ev.VictimComm]++
	}

	// Ring buffer order is oldest-first; present newest-first.
	events := make([]OOMEvent, 0, len(all))
	for i := len(all) - 1; i >= 0; i-- {
		events = append(events, all[i])
	}
	if len(events) > parsed.LastN {
		events = events[:parsed.LastN]
	}

	out := Output{
		Events:      events,
		TotalFound:  len(all),
		CountByComm: countByComm,
		Note:        "kernel ring buffer covers recent history only; older OOM events may have rotated out",
	}

	status := registry.StatusOK
	summary := "No OOM kill events found in kernel ring buffer"
	if len(all) > 0 {
		status = registry.StatusWarning
		summary = fmt.Sprintf("%d OOM kill event(s) found (showing %d, newest first)", len(all), len(events))
	}

	res := registry.NewResult(t.Name(), status, summary, out)
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	res.Metadata.FilteringMethod = "deterministic"
	return res, nil
}

// killedRecord is a parsed "Killed process" line.
type killedRecord struct {
	pid         int
	comm        string
	totalVMKB   int64
	anonRSSKB   int64
	fileRSSKB   int64
	shmemRSSKB  int64
	cgroupScope bool
}

// oomKillRecord is a parsed "oom-kill:" context line.
type oomKillRecord struct {
	constraint string
	oomMemcg   string
	taskMemcg  string
	task       string
	pid        int
}

// parseOOMEvents walks ring buffer lines (oldest first) and emits one
// OOMEvent per incident. Adjacent oom-kill context and Killed-process
// records are merged when their pids match; unpaired records of either
// kind still surface as standalone events.
func parseOOMEvents(raw []byte, btime int64, haveBtime bool) []OOMEvent {
	events := []OOMEvent{}

	var pending *oomKillRecord
	var pendingOffset float64
	var pendingHasOffset bool

	timeOf := func(offset float64, hasOffset bool) *string {
		if !hasOffset || !haveBtime {
			return nil
		}
		s := offsetToRFC3339(btime, offset)
		return &s
	}

	flushPending := func() {
		if pending == nil {
			return
		}
		ev := OOMEvent{
			Time:       timeOf(pendingOffset, pendingHasOffset),
			VictimPID:  pending.pid,
			VictimComm: pending.task,
			Constraint: pending.constraint,
			Memcg:      pending.oomMemcg,
			Scope:      "global",
		}
		if ev.Memcg == "" {
			ev.Memcg = pending.taskMemcg
		}
		if pending.constraint == "CONSTRAINT_MEMCG" {
			ev.Scope = "cgroup"
		}
		events = append(events, ev)
		pending = nil
	}

	for _, line := range bytes.Split(raw, []byte("\n")) {
		offset, hasOffset, msg := parseLinePrefix(string(bytes.TrimSpace(line)))
		if msg == "" {
			continue
		}

		if kill, ok := parseOOMKillLine(msg); ok {
			flushPending() // a previous context line never found its Killed line
			pending = &kill
			pendingOffset, pendingHasOffset = offset, hasOffset
			continue
		}

		killed, ok := parseKilledLine(msg)
		if !ok {
			continue
		}
		ev := OOMEvent{
			Time:       timeOf(offset, hasOffset),
			VictimPID:  killed.pid,
			VictimComm: killed.comm,
			TotalVMMB:  kbToMB(killed.totalVMKB),
			AnonRSSMB:  kbToMB(killed.anonRSSKB),
			FileRSSMB:  kbToMB(killed.fileRSSKB),
			ShmemRSSMB: kbToMB(killed.shmemRSSKB),
			Scope:      "global",
		}
		if killed.cgroupScope {
			ev.Scope = "cgroup"
		}
		if pending != nil && pending.pid == killed.pid {
			ev.Constraint = pending.constraint
			ev.Memcg = pending.oomMemcg
			if ev.Memcg == "" {
				ev.Memcg = pending.taskMemcg
			}
			if pending.constraint == "CONSTRAINT_MEMCG" {
				ev.Scope = "cgroup"
			}
			pending = nil
		} else {
			flushPending() // pid mismatch: the context belongs to another kill
		}
		events = append(events, ev)
	}
	flushPending()

	return events
}

// parseLinePrefix strips the optional klogctl/dmesg-r priority prefix
// ("<3>") and the optional boot-relative timestamp prefix ("[12345.678901]")
// from a ring buffer line, returning the offset (when present) and the
// remaining message.
func parseLinePrefix(line string) (offset float64, hasOffset bool, msg string) {
	msg = strings.TrimSpace(line)

	if strings.HasPrefix(msg, "<") {
		if idx := strings.Index(msg, ">"); idx > 0 && idx <= 4 {
			if _, err := strconv.Atoi(msg[1:idx]); err == nil {
				msg = strings.TrimSpace(msg[idx+1:])
			}
		}
	}

	if strings.HasPrefix(msg, "[") {
		if idx := strings.Index(msg, "]"); idx > 0 {
			tsStr := strings.TrimSpace(msg[1:idx])
			if ts, err := strconv.ParseFloat(tsStr, 64); err == nil {
				offset = ts
				hasOffset = true
				msg = strings.TrimSpace(msg[idx+1:])
			}
		}
	}
	return offset, hasOffset, msg
}

// killedGlobalPrefix / killedCgroupPrefix are the two kernel formats of an
// OOM kill record (mm/oom_kill.c).
const (
	killedGlobalPrefix = "Out of memory: Killed process "
	killedCgroupPrefix = "Memory cgroup out of memory: Killed process "
)

// parseKilledLine parses a "Killed process" record:
//
//	Out of memory: Killed process 4321 (myapp) total-vm:1234kB, anon-rss:567kB, ...
func parseKilledLine(msg string) (killedRecord, bool) {
	var rec killedRecord
	switch {
	case strings.HasPrefix(msg, killedGlobalPrefix):
		msg = msg[len(killedGlobalPrefix):]
	case strings.HasPrefix(msg, killedCgroupPrefix):
		msg = msg[len(killedCgroupPrefix):]
		rec.cgroupScope = true
	default:
		return killedRecord{}, false
	}

	sp := strings.IndexByte(msg, ' ')
	if sp <= 0 {
		return killedRecord{}, false
	}
	pid, err := strconv.Atoi(msg[:sp])
	if err != nil {
		return killedRecord{}, false
	}
	rec.pid = pid

	rest := msg[sp+1:]
	if !strings.HasPrefix(rest, "(") {
		return killedRecord{}, false
	}
	// comm may contain spaces; anchor on the ") total-vm:" separator and
	// fall back to the last ')' for older record layouts.
	end := strings.Index(rest, ") total-vm:")
	if end < 0 {
		end = strings.LastIndexByte(rest, ')')
	}
	if end <= 0 {
		return killedRecord{}, false
	}
	rec.comm = rest[1:end]

	for _, field := range strings.Fields(rest[end+1:]) {
		kv := strings.SplitN(strings.TrimSuffix(field, ","), ":", 2)
		if len(kv) != 2 {
			continue
		}
		val, convErr := strconv.ParseInt(strings.TrimSuffix(kv[1], "kB"), 10, 64)
		if convErr != nil {
			continue
		}
		switch kv[0] {
		case "total-vm":
			rec.totalVMKB = val
		case "anon-rss":
			rec.anonRSSKB = val
		case "file-rss":
			rec.fileRSSKB = val
		case "shmem-rss":
			rec.shmemRSSKB = val
		}
	}
	return rec, true
}

// parseOOMKillLine parses the kernel's oom-kill context record:
//
//	oom-kill:constraint=CONSTRAINT_MEMCG,...,oom_memcg=/a,task_memcg=/a,task=x,pid=1,uid=0
func parseOOMKillLine(msg string) (oomKillRecord, bool) {
	var rec oomKillRecord
	if !strings.HasPrefix(msg, "oom-kill:") {
		return oomKillRecord{}, false
	}
	for _, part := range strings.Split(msg[len("oom-kill:"):], ",") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue // e.g. the bare "global_oom" marker
		}
		switch kv[0] {
		case "constraint":
			rec.constraint = kv[1]
		case "oom_memcg":
			rec.oomMemcg = kv[1]
		case "task_memcg":
			rec.taskMemcg = kv[1]
		case "task":
			rec.task = kv[1]
		case "pid":
			rec.pid, _ = strconv.Atoi(kv[1])
		}
	}
	return rec, true
}

// kbToMB converts kilobytes to megabytes rounded to one decimal place so
// the LLM never has to do unit math.
func kbToMB(kb int64) float64 {
	return math.Round(float64(kb)/1024.0*10) / 10
}

// readBtime extracts the boot time (seconds since epoch) from the btime
// line of <procfsRoot>/stat.
func readBtime(procfsRoot string) (int64, bool) {
	data, err := os.ReadFile(filepath.Join(procfsRoot, "stat"))
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "btime ") {
			continue
		}
		v, convErr := strconv.ParseInt(strings.TrimSpace(line[len("btime "):]), 10, 64)
		if convErr != nil {
			return 0, false
		}
		return v, true
	}
	return 0, false
}

// offsetToRFC3339 converts a boot-relative offset (seconds) into a
// wallclock RFC3339 string using the boot time.
func offsetToRFC3339(btime int64, offset float64) string {
	return time.Unix(btime, 0).UTC().
		Add(time.Duration(offset * float64(time.Second))).
		Format(time.RFC3339)
}
