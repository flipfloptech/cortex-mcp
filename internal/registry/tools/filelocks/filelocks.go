// Package filelocks implements the get_file_locks tool.
//
// It parses /proc/locks to report the kernel's active file-lock table:
// counts by lock type and mode, the processes holding the most locks, and —
// most importantly for hang diagnosis — blocked waiters queued behind held
// locks. Holder identity is resolved via /proc/<pid>/comm.
package filelocks

import (
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

const (
	topHoldersLimit  = 15
	waitersListLimit = 15
)

// LockEntry is one parsed line of /proc/locks.
type LockEntry struct {
	Type    string // POSIX | FLOCK | OFDLCK | LEASE | ...
	Mode    string // READ | WRITE | ...
	PID     int    // -1 for OFD locks without an owner pid
	Blocked bool   // true for "->" waiter lines
}

// Waiter describes a process blocked waiting on a lock.
type Waiter struct {
	PID  int    `json:"pid"`
	Comm string `json:"comm"`
	Type string `json:"type"`
	Mode string `json:"mode"`
}

// Holder describes a process holding one or more locks.
type Holder struct {
	PID       int    `json:"pid"`
	Comm      string `json:"comm"`
	LockCount int    `json:"lock_count"`
}

// ByType counts held locks per lock class.
type ByType struct {
	POSIX  int `json:"POSIX"`
	FLOCK  int `json:"FLOCK"`
	OFDLCK int `json:"OFDLCK"`
	LEASE  int `json:"LEASE"`
}

// ByMode counts held locks per access mode.
type ByMode struct {
	Read  int `json:"READ"`
	Write int `json:"WRITE"`
}

// BlockedWaiters reports lock contention: the exact waiter count plus a
// capped detail list.
type BlockedWaiters struct {
	Count   int      `json:"count"`
	Waiters []Waiter `json:"waiters,omitempty"`
}

// Data is the tool's structured output payload.
type Data struct {
	TotalLocks     int            `json:"total_locks"`
	ByType         ByType         `json:"by_type"`
	ByMode         ByMode         `json:"by_mode"`
	BlockedWaiters BlockedWaiters `json:"blocked_waiters"`
	TopHolders     []Holder       `json:"top_holders,omitempty"`
	WarningReasons []string       `json:"warning_reasons,omitempty"`
}

// Tool implements registry.Tool for get_file_locks.
type Tool struct {
	procfsRoot string
}

// New returns a Tool wired to the real procfs root.
func New() *Tool {
	return &Tool{procfsRoot: "/proc"}
}

func init() {
	registry.Register(New())
}

// Name returns the unique tool identifier.
func (t *Tool) Name() string {
	return "get_file_locks"
}

// Description returns a one-line summary for tool discovery.
func (t *Tool) Description() string {
	return "Summarize kernel file locks and surface blocked waiters contending for them"
}

// Help returns the full tool documentation.
func (t *Tool) Help() string {
	return `Parses the kernel file-lock table to diagnose lock contention and stuck processes.

Reports total held locks, counts by type (POSIX, FLOCK, OFDLCK, LEASE) and
mode (READ, WRITE), and the top-15 lock-holding processes. Blocked waiters
("->" lines) are surfaced with an exact count and a capped detail list; any
blocked waiter flips the result status to warning with a warning_reasons
entry. Filtering is deterministic (exact kernel state, no estimation).

Data Sources:
- /proc/locks (lock table, including "->" blocked waiter lines)
- /proc/<pid>/comm (process name resolution; dead pids degrade to "unknown",
  OFD locks with pid -1 report "OFD (no owner pid)")`
}

// Category returns the tool taxonomy classification.
func (t *Tool) Category() registry.Category {
	return registry.CategorySystem
}

// Parameters returns the parameter schema (none).
func (t *Tool) Parameters() []registry.ToolParam {
	return nil
}

// Hidden returns false; this is a public diagnostic tool.
func (t *Tool) Hidden() bool { return false }

// IsSupported requires the procfs locks table.
func (t *Tool) IsSupported() (bool, string) {
	path := filepath.Join(t.procfsRoot, "locks")
	if !registry.PathExists(path) {
		return false, fmt.Sprintf("%s is missing", path)
	}
	return true, ""
}

// Execute parses the lock table and aggregates contention data.
func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	if err := ctx.Err(); err != nil {
		return registry.NewErrorResult(t.Name(), "cancelled: "+err.Error()), nil
	}

	locksPath := filepath.Join(t.procfsRoot, "locks")
	raw, err := os.ReadFile(locksPath)
	if err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("failed to read %s: %v", locksPath, err)), nil
	}

	data := t.buildData(parseLocks(raw))

	status := registry.StatusOK
	if len(data.WarningReasons) > 0 {
		status = registry.StatusWarning
	}
	summary := fmt.Sprintf("%d file lock(s) held (POSIX %d, FLOCK %d, OFDLCK %d, LEASE %d), %d blocked waiter(s)",
		data.TotalLocks, data.ByType.POSIX, data.ByType.FLOCK, data.ByType.OFDLCK, data.ByType.LEASE,
		data.BlockedWaiters.Count)

	res := registry.NewResult(t.Name(), status, summary, data)
	res.Metadata.FilteringMethod = "deterministic"
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	return res, nil
}

// buildData aggregates parsed lock entries into the LLM-facing payload.
func (t *Tool) buildData(entries []LockEntry) Data {
	var data Data
	holderCounts := map[int]int{}

	for _, e := range entries {
		if e.Blocked {
			data.BlockedWaiters.Count++
			if len(data.BlockedWaiters.Waiters) < waitersListLimit {
				data.BlockedWaiters.Waiters = append(data.BlockedWaiters.Waiters, Waiter{
					PID:  e.PID,
					Comm: t.resolveComm(e.PID),
					Type: e.Type,
					Mode: e.Mode,
				})
			}
			continue
		}

		data.TotalLocks++
		switch e.Type {
		case "POSIX":
			data.ByType.POSIX++
		case "FLOCK":
			data.ByType.FLOCK++
		case "OFDLCK":
			data.ByType.OFDLCK++
		case "LEASE":
			data.ByType.LEASE++
		}
		switch e.Mode {
		case "READ":
			data.ByMode.Read++
		case "WRITE":
			data.ByMode.Write++
		}
		holderCounts[e.PID]++
	}

	holders := make([]Holder, 0, len(holderCounts))
	for pid, count := range holderCounts {
		holders = append(holders, Holder{PID: pid, LockCount: count})
	}
	sort.Slice(holders, func(i, j int) bool {
		if holders[i].LockCount != holders[j].LockCount {
			return holders[i].LockCount > holders[j].LockCount
		}
		return holders[i].PID < holders[j].PID
	})
	if len(holders) > topHoldersLimit {
		holders = holders[:topHoldersLimit]
	}
	for i := range holders {
		holders[i].Comm = t.resolveComm(holders[i].PID)
	}
	data.TopHolders = holders

	if data.BlockedWaiters.Count > 0 {
		data.WarningReasons = append(data.WarningReasons,
			fmt.Sprintf("%d process(es) blocked waiting on file locks", data.BlockedWaiters.Count))
	}

	return data
}

// parseLocks parses every valid line of a /proc/locks table.
func parseLocks(data []byte) []LockEntry {
	var entries []LockEntry
	for _, line := range strings.Split(string(data), "\n") {
		if entry, ok := parseLockLine(line); ok {
			entries = append(entries, entry)
		}
	}
	return entries
}

// parseLockLine parses a single /proc/locks line, e.g.
//
//	1: POSIX  ADVISORY  WRITE 1234 08:01:12345 0 EOF
//	1: -> POSIX ADVISORY WRITE 5678 08:01:12345 0 EOF   (blocked waiter)
func parseLockLine(line string) (LockEntry, bool) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) < 5 {
		return LockEntry{}, false
	}

	var entry LockEntry
	idx := 1
	if fields[idx] == "->" {
		entry.Blocked = true
		idx++
	}
	if len(fields) < idx+4 {
		return LockEntry{}, false
	}

	entry.Type = fields[idx]
	entry.Mode = fields[idx+2]
	pid, err := strconv.Atoi(fields[idx+3])
	if err != nil {
		return LockEntry{}, false
	}
	entry.PID = pid
	return entry, true
}

// resolveComm maps a pid to its process name. OFD locks carry pid -1 (the
// kernel does not track an owner); dead pids degrade to "unknown".
func (t *Tool) resolveComm(pid int) string {
	if pid < 0 {
		return "OFD (no owner pid)"
	}
	raw, err := os.ReadFile(filepath.Join(t.procfsRoot, strconv.Itoa(pid), "comm"))
	if err != nil {
		return "unknown"
	}
	comm := strings.TrimSpace(string(raw))
	if comm == "" {
		return "unknown"
	}
	return comm
}
