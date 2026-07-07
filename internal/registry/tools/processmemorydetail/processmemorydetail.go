// Package processmemorydetail implements the get_process_memory_detail tool.
//
// It provides a precise, LLM-ready memory breakdown for a single process:
// RSS vs PSS (shared-page-aware), private/shared split, swap footprint,
// transparent huge page usage, and the rlimits that bound the process —
// all read natively from procfs.
package processmemorydetail

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// Limits reports the process address-space and locked-memory rlimits.
// Each value is either the soft limit in bytes (JSON number), the string
// "unlimited", or "unknown" when /proc/<pid>/limits was unreadable.
type Limits struct {
	AddressSpace interface{} `json:"address_space"`
	LockedMemory interface{} `json:"locked_memory"`
}

// Output is the tool's data payload. All *_mb values are megabytes rounded
// to one decimal place.
type Output struct {
	PID       int     `json:"pid"`
	Comm      string  `json:"comm"`
	Threads   int     `json:"threads"`
	RssMB     float64 `json:"rss_mb"`
	PssMB     float64 `json:"pss_mb"`
	SharedMB  float64 `json:"shared_mb"`  // Shared_Clean + Shared_Dirty
	PrivateMB float64 `json:"private_mb"` // Private_Clean + Private_Dirty
	SwapMB    float64 `json:"swap_mb"`
	ThpMB     float64 `json:"thp_mb"` // AnonHugePages
	Limits    Limits  `json:"limits"`

	// RssPctOfAddressLimit is RSS as a percentage of the soft address
	// space limit. Present only when that limit is finite.
	RssPctOfAddressLimit *float64 `json:"rss_pct_of_address_limit,omitempty"`
}

// rlimit is one parsed row of /proc/<pid>/limits (soft limit).
type rlimit struct {
	known     bool
	unlimited bool
	bytes     int64
}

// Tool implements registry.Tool for get_process_memory_detail.
type Tool struct {
	// procfsRoot is the procfs mount point (normally /proc).
	// Injectable for tests.
	procfsRoot string
}

// New creates the tool with production data sources.
func New() *Tool {
	return &Tool{procfsRoot: "/proc"}
}

func init() {
	registry.Register(New())
}

// Name returns the unique tool identifier.
func (t *Tool) Name() string {
	return "get_process_memory_detail"
}

// Description returns a one-line summary for get_tool_list.
func (t *Tool) Description() string {
	return "Deep memory breakdown (RSS/PSS/shared/private/swap/THP) for one process"
}

// Help returns the full tool documentation.
func (t *Tool) Help() string {
	return `Produces an accurate memory footprint for a single process, distinguishing
truly-owned memory (PSS, private) from shared library pages that naive RSS
readings double-count, plus swap pressure and transparent huge page usage.

Data Sources:
- /proc/<pid>/smaps_rollup: kernel-precomputed totals (Rss, Pss, Shared_Clean,
  Shared_Dirty, Private_Clean, Private_Dirty, Swap, SwapPss, AnonHugePages).
- /proc/<pid>/smaps: per-mapping fallback summed key-by-key on kernels older
  than 4.14 that lack smaps_rollup.
- /proc/<pid>/status: process Name (comm), Threads count, and VmSwap.
- /proc/<pid>/limits: "Max address space" and "Max locked memory" rows
  (soft limit reported; "unlimited" preserved as a string).

Parameters:
- target_pid: REQUIRED integer. The process to inspect.

Mathematical Models / Formatting:
- All sizes converted from kB to MB, rounded to 1 decimal place.
- shared_mb = Shared_Clean + Shared_Dirty; private_mb = Private_Clean + Private_Dirty.
- rss_pct_of_address_limit = RSS / soft address-space limit * 100, emitted
  only when the limit is finite.

Degradation Profile:
- Nonexistent target_pid returns a "process not found" error result.
- Missing smaps_rollup silently falls back to summing smaps.
- Permission denied on smaps data (other users' processes without root or
  CAP_SYS_PTRACE) returns an error result naming the privilege requirement.
- Unreadable limits degrade to "unknown", never fatal.`
}

// Category classifies the tool.
func (t *Tool) Category() registry.Category {
	return registry.CategoryMemory
}

// Parameters returns the parameter schema.
func (t *Tool) Parameters() []registry.ToolParam {
	return []registry.ToolParam{
		{
			Name:        "target_pid",
			Type:        "integer",
			Description: "The process ID to produce the memory breakdown for.",
			Required:    true,
		},
	}
}

// Hidden reports whether the tool is hidden from discovery.
func (t *Tool) Hidden() bool { return false }

// IsSupported checks that procfs status files are readable.
func (t *Tool) IsSupported() (bool, string) {
	if _, err := os.ReadFile(filepath.Join(t.procfsRoot, "self", "status")); err != nil {
		return false, fmt.Sprintf("%s/self/status is not readable (requires Linux procfs)", t.procfsRoot)
	}
	return true, ""
}

// Execute produces the memory breakdown for the target process.
func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	if err := ctx.Err(); err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("context cancelled: %v", err)), nil
	}

	var req struct {
		TargetPID *int `json:"target_pid"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("invalid arguments: %v", err)), nil
		}
	}
	if req.TargetPID == nil || *req.TargetPID <= 0 {
		return registry.NewErrorResult(t.Name(), "target_pid is required and must be > 0"), nil
	}
	pid := *req.TargetPID

	pidDir := filepath.Join(t.procfsRoot, strconv.Itoa(pid))
	if !registry.PathExists(pidDir) {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("process not found: pid %d does not exist", pid)), nil
	}

	statusRaw, err := os.ReadFile(filepath.Join(pidDir, "status"))
	if err != nil {
		if os.IsNotExist(err) {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("process not found: pid %d exited during inspection", pid)), nil
		}
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("failed to read %s/status: %v", pidDir, err)), nil
	}
	comm, threads, vmSwapKB := parseStatus(statusRaw)

	// smaps_rollup first (kernel >= 4.14), summed smaps as fallback.
	var mem map[string]int64
	rollupRaw, rollupErr := os.ReadFile(filepath.Join(pidDir, "smaps_rollup"))
	if rollupErr == nil {
		mem = parseMemKeys(rollupRaw)
	} else {
		smapsRaw, smapsErr := os.ReadFile(filepath.Join(pidDir, "smaps"))
		if smapsErr != nil {
			if os.IsPermission(rollupErr) || os.IsPermission(smapsErr) {
				return registry.NewErrorResult(t.Name(), fmt.Sprintf(
					"permission denied reading smaps data for pid %d: root (or CAP_SYS_PTRACE) privileges are required to inspect processes owned by other users", pid)), nil
			}
			return registry.NewErrorResult(t.Name(), fmt.Sprintf(
				"smaps data unavailable for pid %d: smaps_rollup (%v), smaps (%v)", pid, rollupErr, smapsErr)), nil
		}
		mem = parseMemKeys(smapsRaw)
	}

	swapKB := mem["Swap"]
	if _, ok := mem["Swap"]; !ok {
		swapKB = vmSwapKB // status VmSwap fallback
	}

	out := Output{
		PID:       pid,
		Comm:      comm,
		Threads:   threads,
		RssMB:     kbToMB(mem["Rss"]),
		PssMB:     kbToMB(mem["Pss"]),
		SharedMB:  kbToMB(mem["Shared_Clean"] + mem["Shared_Dirty"]),
		PrivateMB: kbToMB(mem["Private_Clean"] + mem["Private_Dirty"]),
		SwapMB:    kbToMB(swapKB),
		ThpMB:     kbToMB(mem["AnonHugePages"]),
	}

	// Limits degrade to "unknown" when unreadable.
	limitsRaw, _ := os.ReadFile(filepath.Join(pidDir, "limits"))
	addr, locked := parseLimitsFile(limitsRaw)
	out.Limits = Limits{AddressSpace: "unknown", LockedMemory: "unknown"}
	if addr.known {
		if addr.unlimited {
			out.Limits.AddressSpace = "unlimited"
		} else {
			out.Limits.AddressSpace = addr.bytes
		}
	}
	if locked.known {
		if locked.unlimited {
			out.Limits.LockedMemory = "unlimited"
		} else {
			out.Limits.LockedMemory = locked.bytes
		}
	}

	if addr.known && !addr.unlimited && addr.bytes > 0 {
		pct := math.Round(float64(mem["Rss"]*1024)/float64(addr.bytes)*100*10) / 10
		out.RssPctOfAddressLimit = &pct
	}

	res := registry.NewResult(t.Name(), registry.StatusOK,
		fmt.Sprintf("Memory breakdown for pid %d (%s): rss %.1f MB, pss %.1f MB, swap %.1f MB",
			pid, comm, out.RssMB, out.PssMB, out.SwapMB), out)
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	res.Metadata.FilteringMethod = "deterministic"
	return res, nil
}

// parseMemKeys extracts all "Key: <n> kB" lines into a map, summing values
// when a key repeats (per-mapping smaps). Header lines, VmFlags, and
// malformed values are skipped.
func parseMemKeys(data []byte) map[string]int64 {
	mem := make(map[string]int64)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[2] != "kB" || !strings.HasSuffix(fields[0], ":") {
			continue
		}
		val, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		mem[key] += val
	}
	return mem
}

// parseLimitsFile extracts the soft "Max address space" and "Max locked
// memory" limits from /proc/<pid>/limits. Values are bytes; "unlimited" is
// preserved.
func parseLimitsFile(data []byte) (addressSpace, lockedMemory rlimit) {
	for _, line := range strings.Split(string(data), "\n") {
		var target *rlimit
		switch {
		case strings.HasPrefix(line, "Max address space"):
			target = &addressSpace
		case strings.HasPrefix(line, "Max locked memory"):
			target = &lockedMemory
		default:
			continue
		}

		fields := strings.Fields(line)
		// "Max address space <soft> <hard> bytes" -> soft at index 3;
		// "Max locked memory <soft> <hard> bytes" -> soft at index 3.
		if len(fields) < 5 {
			continue
		}
		soft := fields[3]
		if soft == "unlimited" {
			*target = rlimit{known: true, unlimited: true}
			continue
		}
		if v, err := strconv.ParseInt(soft, 10, 64); err == nil {
			*target = rlimit{known: true, bytes: v}
		}
	}
	return addressSpace, lockedMemory
}

// parseStatus extracts Name (comm), Threads, and VmSwap (kB) from
// /proc/<pid>/status.
func parseStatus(data []byte) (comm string, threads int, vmSwapKB int64) {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "Name:":
			comm = fields[1]
		case "Threads:":
			threads, _ = strconv.Atoi(fields[1])
		case "VmSwap:":
			vmSwapKB, _ = strconv.ParseInt(fields[1], 10, 64)
		}
	}
	return comm, threads, vmSwapKB
}

// kbToMB converts kilobytes to megabytes rounded to one decimal place.
func kbToMB(kb int64) float64 {
	return math.Round(float64(kb)/1024*10) / 10
}
