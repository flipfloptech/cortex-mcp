// Package raidhealth implements the get_raid_health diagnostic tool.
//
// It audits Linux software RAID (md) arrays natively via sysfs
// (/sys/block/md*/md/) with /proc/mdstat used as the support gate,
// surfacing degraded arrays, rebuild progress, faulty members, and
// mismatch counts without shelling out to mdadm.
package raidhealth

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// Member is a single component device of an md array.
type Member struct {
	Dev   string `json:"dev"`
	State string `json:"state"`
}

// Array is the health snapshot of one md array.
type Array struct {
	Name           string   `json:"name"`
	Level          string   `json:"level"`
	ArrayState     string   `json:"array_state"`
	Degraded       bool     `json:"degraded"`
	SyncAction     string   `json:"sync_action"`
	RebuildPct     float64  `json:"rebuild_pct"`
	MismatchCnt    int64    `json:"mismatch_cnt"`
	RaidDisks      int      `json:"raid_disks"`
	Members        []Member `json:"members"`
	FailedMembers  []string `json:"failed_members"`
	WarningReasons []string `json:"warning_reasons"`
}

// Summary aggregates fleet-level counts across all arrays.
type Summary struct {
	Total           int `json:"total"`
	DegradedCount   int `json:"degraded_count"`
	RebuildingCount int `json:"rebuilding_count"`
}

// Output is the tool's data payload.
type Output struct {
	Arrays  []Array `json:"arrays"`
	Summary Summary `json:"summary"`
}

// Tool implements registry.Tool for software RAID health auditing.
type Tool struct {
	procfsRoot string
	sysfsRoot  string
}

// New returns a Tool wired to the real procfs/sysfs roots.
func New() *Tool {
	return &Tool{
		procfsRoot: "/proc",
		sysfsRoot:  "/sys",
	}
}

func init() {
	registry.Register(New())
}

// Name returns the unique tool identifier.
func (t *Tool) Name() string {
	return "get_raid_health"
}

// Description returns a one-line summary for get_tool_list output.
func (t *Tool) Description() string {
	return "Audit Linux software RAID (md) arrays for degradation, rebuilds, faulty members, and mismatches."
}

// Help returns the full tool help text.
func (t *Tool) Help() string {
	return `Audits Linux software RAID (md) array health natively from the kernel, without mdadm.

Reports per-array state, degradation, rebuild/resync progress (percent complete), mismatch counts, and per-member device states, flagging faulty members and arrays running at reduced redundancy.

Data Sources:
- /proc/mdstat (presence gate: md driver loaded)
- /sys/block/md*/md/{array_state,degraded,sync_action,sync_completed,mismatch_cnt,raid_disks,level}
- /sys/block/md*/md/dev-*/state (per-member: in_sync, faulty, spare, ...)`
}

// Category classifies this tool as a storage diagnostic.
func (t *Tool) Category() registry.Category {
	return registry.CategoryStorage
}

// Parameters returns the parameter schema (none).
func (t *Tool) Parameters() []registry.ToolParam {
	return nil
}

// Hidden reports whether the tool is hidden from LLM discovery.
func (t *Tool) Hidden() bool {
	return false
}

// IsSupported reports whether the md driver is present on this node.
func (t *Tool) IsSupported() (bool, string) {
	if !registry.PathExists(filepath.Join(t.procfsRoot, "mdstat")) {
		return false, "software RAID not supported (missing /proc/mdstat)"
	}
	return true, ""
}

// Execute scans sysfs for md arrays and returns their health snapshot.
func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	if err := ctx.Err(); err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("execution cancelled: %v", err)), nil
	}

	arrays := []Array{}
	blockDir := filepath.Join(t.sysfsRoot, "block")
	entries, err := os.ReadDir(blockDir)
	if err != nil {
		// Missing sysfs block tree degrades to zero arrays.
		entries = nil
	}

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("execution cancelled: %v", err)), nil
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "md") {
			continue
		}
		// The md/ subdirectory is the definitive marker of an md array
		// (excludes partitions like md0p1 and unrelated md* names).
		if !registry.PathExists(filepath.Join(blockDir, name, "md")) {
			continue
		}
		arrays = append(arrays, collectArray(t.sysfsRoot, name))
	}

	sort.Slice(arrays, func(i, j int) bool { return arrays[i].Name < arrays[j].Name })

	out := Output{Arrays: arrays}
	out.Summary.Total = len(arrays)
	warnings := 0
	for _, a := range arrays {
		if a.Degraded {
			out.Summary.DegradedCount++
		}
		if a.SyncAction == "recover" || a.SyncAction == "resync" {
			out.Summary.RebuildingCount++
		}
		warnings += len(a.WarningReasons)
	}

	status := registry.StatusOK
	summary := fmt.Sprintf("RAID health: %d md arrays, %d degraded, %d rebuilding",
		out.Summary.Total, out.Summary.DegradedCount, out.Summary.RebuildingCount)
	switch {
	case out.Summary.Total == 0:
		summary = "RAID health: no md arrays present"
	case warnings > 0:
		status = registry.StatusWarning
	}

	res := registry.NewResult(t.Name(), status, summary, out)
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	res.Metadata.FilteringMethod = "deterministic"
	return res, nil
}

// collectArray reads all health attributes for a single md array.
func collectArray(sysfsRoot, name string) Array {
	mdDir := filepath.Join(sysfsRoot, "block", name, "md")

	a := Array{
		Name:           name,
		Level:          readAttr(mdDir, "level"),
		ArrayState:     readAttr(mdDir, "array_state"),
		Degraded:       readAttr(mdDir, "degraded") == "1",
		SyncAction:     readAttr(mdDir, "sync_action"),
		RebuildPct:     parseSyncCompleted(readAttr(mdDir, "sync_completed")),
		Members:        []Member{},
		FailedMembers:  []string{},
		WarningReasons: []string{},
	}
	a.MismatchCnt, _ = strconv.ParseInt(readAttr(mdDir, "mismatch_cnt"), 10, 64)
	a.RaidDisks, _ = strconv.Atoi(readAttr(mdDir, "raid_disks"))

	a.Members = append(a.Members, readMembers(mdDir)...)
	for _, m := range a.Members {
		if strings.Contains(m.State, "faulty") {
			a.FailedMembers = append(a.FailedMembers, m.Dev)
			a.WarningReasons = append(a.WarningReasons, fmt.Sprintf("member %s is faulty", m.Dev))
		}
	}

	if a.Degraded {
		a.WarningReasons = append(a.WarningReasons,
			fmt.Sprintf("array %s is degraded (array_state=%s)", a.Name, a.ArrayState))
	}
	if a.SyncAction == "recover" || a.SyncAction == "resync" {
		a.WarningReasons = append(a.WarningReasons,
			fmt.Sprintf("redundancy rebuild in progress: sync_action=%s (%.1f%% complete)", a.SyncAction, a.RebuildPct))
	}
	if a.MismatchCnt > 0 {
		a.WarningReasons = append(a.WarningReasons,
			fmt.Sprintf("mismatch_cnt=%d: blocks inconsistent between mirrors/parity (run a check/repair)", a.MismatchCnt))
	}

	return a
}

// readMembers lists the component devices of an array from md/dev-* entries,
// sorted by device name. Returns nil if the md directory is unreadable.
func readMembers(mdDir string) []Member {
	entries, err := os.ReadDir(mdDir)
	if err != nil {
		return nil
	}

	var members []Member
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "dev-") {
			continue
		}
		members = append(members, Member{
			Dev:   strings.TrimPrefix(name, "dev-"),
			State: readAttr(filepath.Join(mdDir, name), "state"),
		})
	}

	sort.Slice(members, func(i, j int) bool { return members[i].Dev < members[j].Dev })
	return members
}

// readAttr reads a single sysfs attribute file, returning the trimmed
// content or the empty string if the attribute is missing/unreadable.
func readAttr(dir, name string) string {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// parseSyncCompleted converts the sysfs sync_completed fraction ("N / M")
// into a percentage rounded to one decimal place. "none", garbage, or a
// zero denominator all yield 0.
func parseSyncCompleted(raw string) float64 {
	parts := strings.Split(raw, "/")
	if len(parts) != 2 {
		return 0
	}
	num, errN := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	den, errD := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if errN != nil || errD != nil || den <= 0 {
		return 0
	}
	return math.Round(num/den*1000) / 10
}
