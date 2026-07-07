// Package memoryreclaim implements the get_memory_reclaim_stats tool.
//
// It samples /proc/vmstat twice across a short window and converts the
// kernel's cumulative reclaim/swap/compaction counters into per-second
// rates. Direct reclaim, allocation stalls, swap traffic, and compaction
// stalls are the canonical signals that memory pressure is already costing
// application latency — long before an OOM kill happens.
package memoryreclaim

import (
	"bufio"
	"bytes"
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

// Sampling window bounds (milliseconds). Requests outside the range are
// clamped, never rejected, so callers always get an answer.
const (
	defaultWindowMs = 500
	minWindowMs     = 10
	maxWindowMs     = 5000
)

// Rates holds the per-second event rates observed across the sampling
// window, rounded to 2 decimal places. Counters absent from this kernel's
// vmstat simply produce a 0 rate.
type Rates struct {
	// SwapIn/SwapOut are pages swapped in/out per second (pswpin/pswpout).
	SwapIn  float64 `json:"swap_in"`
	SwapOut float64 `json:"swap_out"`

	// DirectScan is pages scanned by direct reclaim per second — allocating
	// processes doing the kernel's reclaim work synchronously.
	DirectScan float64 `json:"direct_scan"`

	// KswapdScan is pages scanned by the background kswapd daemon per second.
	KswapdScan float64 `json:"kswapd_scan"`

	// DirectSteal/KswapdSteal are pages actually reclaimed per second.
	DirectSteal float64 `json:"direct_steal"`
	KswapdSteal float64 `json:"kswapd_steal"`

	// Allocstall is direct-reclaim allocation stalls per second (sum of all
	// allocstall_* zone counters).
	Allocstall float64 `json:"allocstall"`

	// MajorFaults is major page faults per second (pgmajfault).
	MajorFaults float64 `json:"major_faults"`

	// CompactStall is allocations stalled on memory compaction per second.
	CompactStall float64 `json:"compact_stall"`
}

// Output is the tool's data payload.
type Output struct {
	// WindowMs is the measured wall-clock width of the sampling window.
	WindowMs int64 `json:"window_ms"`

	// RatesPerSec are per-second event rates across the window.
	RatesPerSec Rates `json:"rates_per_sec"`

	// TotalsSinceBoot are the cumulative kernel counters from the second
	// sample. Counters this kernel does not export are omitted.
	TotalsSinceBoot map[string]uint64 `json:"totals_since_boot"`

	// ReclaimEfficiencyPct is pgsteal/pgscan*100 since boot (kswapd +
	// direct). Omitted when nothing has been scanned yet.
	ReclaimEfficiencyPct *float64 `json:"reclaim_efficiency_pct,omitempty"`

	// WarningReasons lists pre-evaluated pressure signals in plain words.
	WarningReasons []string `json:"warning_reasons"`
}

// Tool implements registry.Tool for get_memory_reclaim_stats.
type Tool struct {
	// procfsRoot is the procfs mount point, injectable for hermetic tests.
	procfsRoot string

	// readFile reads a file; injectable so tests can serve two-phase fakes.
	readFile func(name string) ([]byte, error)

	// now supplies wall-clock time; injectable so tests get exact windows.
	now func() time.Time
}

// New returns a memory reclaim tool bound to the real /proc and clock.
func New() *Tool {
	return &Tool{
		procfsRoot: "/proc",
		readFile:   os.ReadFile,
		now:        time.Now,
	}
}

func init() {
	registry.Register(New())
}

// Name returns the unique tool identifier.
func (t *Tool) Name() string { return "get_memory_reclaim_stats" }

// Description returns the one-line summary for tool discovery.
func (t *Tool) Description() string {
	return "Sample /proc/vmstat to measure live memory reclaim, swap, allocation stall, and compaction rates"
}

// Help returns the full tool documentation.
func (t *Tool) Help() string {
	return `get_memory_reclaim_stats — Live Memory Reclaim Dynamics

Samples /proc/vmstat twice across a short window (default 500 ms) and turns
the kernel's cumulative counters into per-second rates. Any non-zero direct
reclaim, allocation stall, swap, or compaction stall activity means processes
are ALREADY paying memory-pressure latency, long before an OOM kill.

Data Sources:
  - /proc/vmstat (sampled twice; "key value" lines)

Counters Used (absent counters are tolerated and omitted from totals):
  - pgscan_kswapd / pgscan_direct, pgsteal_kswapd / pgsteal_direct
  - allocstall_* (all zone variants summed; plain allocstall on old kernels)
  - compact_stall / compact_fail / compact_success
  - pswpin / pswpout, pgmajfault, thp_fault_fallback, oom_kill
  On pre-4.8 kernels the per-zone spellings (e.g. pgscan_kswapd_dma) are
  summed into the modern unsuffixed counter. The pgscan_direct_throttle
  event counter is excluded (it counts throttle events, not pages).

Output:
  - window_ms: measured sampling window width
  - rates_per_sec: swap_in, swap_out, direct_scan, kswapd_scan, direct_steal,
    kswapd_steal, allocstall, major_faults, compact_stall (2 decimal places)
  - totals_since_boot: cumulative counters from the second sample
  - reclaim_efficiency_pct: pgsteal/pgscan * 100 since boot (omitted when
    pgscan is 0); low values mean the kernel scans many pages per page freed
  - warning_reasons: pre-evaluated pressure signals

Warning Heuristics (any activity in the window):
  - direct_scan > 0: processes are direct-reclaiming — allocation latency impact
  - allocstall > 0: allocations stalled waiting for reclaim
  - swap_in/swap_out > 0: active swapping (thrashing risk)
  - compact_stall > 0: allocations stalled on memory compaction

Degradation Profile:
  - IsSupported() is false when /proc/vmstat is missing.
  - Counters not exported by this kernel are omitted from totals_since_boot
    and contribute a 0 rate.
  - Context cancellation during the sampling window returns an error result
    promptly instead of waiting the window out.

Parameters:
  - sample_duration_ms (integer, optional): sampling window in milliseconds.
    Default 500, clamped to [10, 5000].

Supported on: Linux`
}

// Category classifies this tool under memory.
func (t *Tool) Category() registry.Category { return registry.CategoryMemory }

// Parameters describes the accepted arguments.
func (t *Tool) Parameters() []registry.ToolParam {
	return []registry.ToolParam{
		{
			Name:        "sample_duration_ms",
			Type:        "integer",
			Description: "Sampling window in milliseconds (default 500, clamped to [10, 5000]).",
			Required:    false,
			Default:     "500",
		},
	}
}

// Hidden returns false — this tool is LLM-discoverable.
func (t *Tool) Hidden() bool { return false }

// IsSupported requires a readable procfs vmstat file.
func (t *Tool) IsSupported() (bool, string) {
	path := filepath.Join(t.procfsRoot, "vmstat")
	if !registry.PathExists(path) {
		return false, path + " is missing"
	}
	return true, ""
}

// toolArgs is the JSON argument schema.
type toolArgs struct {
	SampleDurationMs *int `json:"sample_duration_ms"`
}

// Execute samples vmstat twice across the requested window and reports
// reclaim dynamics. All failures are encapsulated as error results.
func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	var parsed toolArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &parsed); err != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("failed to parse arguments: %v", err)), nil
		}
	}
	window := resolveWindow(parsed.SampleDurationMs)

	if err := ctx.Err(); err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("context canceled: %v", err)), nil
	}

	path := filepath.Join(t.procfsRoot, "vmstat")
	first, err := t.readFile(path)
	if err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("failed to read %s: %v", path, err)), nil
	}
	before := parseVmstat(first)
	t0 := t.now()

	timer := time.NewTimer(window)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("context canceled during sampling window: %v", ctx.Err())), nil
	case <-timer.C:
	}

	second, err := t.readFile(path)
	if err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("failed to read %s: %v", path, err)), nil
	}
	after := parseVmstat(second)
	elapsed := t.now().Sub(t0)

	out := computeOutput(before, after, elapsed)
	out.WarningReasons = buildWarnings(out.RatesPerSec)

	status := registry.StatusOK
	summary := fmt.Sprintf("Memory reclaim quiet over %dms window", out.WindowMs)
	if len(out.WarningReasons) > 0 {
		status = registry.StatusWarning
		summary = fmt.Sprintf("Memory pressure signals over %dms window: %s",
			out.WindowMs, strings.Join(out.WarningReasons, "; "))
	}

	result := registry.NewResult(t.Name(), status, summary, out)
	result.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	result.Metadata.FilteringMethod = "heuristic"
	return result, nil
}

// resolveWindow clamps the requested sampling window into [10ms, 5000ms],
// defaulting to 500ms when absent.
func resolveWindow(requested *int) time.Duration {
	ms := defaultWindowMs
	if requested != nil {
		ms = *requested
	}
	if ms < minWindowMs {
		ms = minWindowMs
	}
	if ms > maxWindowMs {
		ms = maxWindowMs
	}
	return time.Duration(ms) * time.Millisecond
}

// parseVmstat decodes "key value" lines into a counter map. Malformed or
// non-numeric lines are skipped — vmstat is kernel-generated, so anything
// unparseable is simply a counter we do not understand.
func parseVmstat(data []byte) map[string]uint64 {
	counters := make(map[string]uint64, 128)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		val, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		counters[fields[0]] = val
	}
	return counters
}

// resolveCounter returns the value of a logical counter. The exact key wins
// (modern kernels); otherwise all zone-suffixed variants ("<key>_*") are
// summed for pre-4.8 kernels (e.g. pgscan_kswapd_dma + pgscan_kswapd_normal).
// The "<key>_throttle" event counter is excluded from fallback sums because
// it counts throttle events, not pages. ok reports whether this kernel
// exports the counter in any spelling.
func resolveCounter(counters map[string]uint64, key string) (uint64, bool) {
	if v, exact := counters[key]; exact {
		return v, true
	}
	prefix := key + "_"
	var sum uint64
	found := false
	for name, v := range counters {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		if strings.HasSuffix(name, "_throttle") {
			continue
		}
		sum += v
		found = true
	}
	return sum, found
}

// computeOutput turns two vmstat samples and the elapsed window into rates,
// cumulative totals, and reclaim efficiency.
func computeOutput(before, after map[string]uint64, elapsed time.Duration) Output {
	seconds := elapsed.Seconds()
	rate := func(key string) float64 {
		if seconds <= 0 {
			return 0
		}
		b, _ := resolveCounter(before, key)
		a, _ := resolveCounter(after, key)
		if a <= b {
			return 0
		}
		return round2(float64(a-b) / seconds)
	}

	out := Output{
		WindowMs: elapsed.Milliseconds(),
		RatesPerSec: Rates{
			SwapIn:       rate("pswpin"),
			SwapOut:      rate("pswpout"),
			DirectScan:   rate("pgscan_direct"),
			KswapdScan:   rate("pgscan_kswapd"),
			DirectSteal:  rate("pgsteal_direct"),
			KswapdSteal:  rate("pgsteal_kswapd"),
			Allocstall:   rate("allocstall"),
			MajorFaults:  rate("pgmajfault"),
			CompactStall: rate("compact_stall"),
		},
		TotalsSinceBoot: make(map[string]uint64, 10),
		WarningReasons:  []string{},
	}

	for _, key := range []string{
		"pswpin", "pswpout", "pgscan_direct", "pgscan_kswapd", "allocstall",
		"compact_stall", "compact_fail", "compact_success", "thp_fault_fallback", "oom_kill",
	} {
		if v, ok := resolveCounter(after, key); ok {
			out.TotalsSinceBoot[key] = v
		}
	}

	scanKswapd, _ := resolveCounter(after, "pgscan_kswapd")
	scanDirect, _ := resolveCounter(after, "pgscan_direct")
	stealKswapd, _ := resolveCounter(after, "pgsteal_kswapd")
	stealDirect, _ := resolveCounter(after, "pgsteal_direct")
	if scan := scanKswapd + scanDirect; scan > 0 {
		eff := round2(float64(stealKswapd+stealDirect) / float64(scan) * 100)
		out.ReclaimEfficiencyPct = &eff
	}

	return out
}

// buildWarnings derives warning_reasons from the observed rates: ANY direct
// reclaim, allocation stall, swap, or compaction stall activity within the
// window is a live memory-pressure signal.
func buildWarnings(r Rates) []string {
	warnings := []string{}
	if r.DirectScan > 0 {
		warnings = append(warnings, fmt.Sprintf("direct_scan %.2f pages/s — processes are direct-reclaiming — allocation latency impact", r.DirectScan))
	}
	if r.Allocstall > 0 {
		warnings = append(warnings, fmt.Sprintf("allocstall %.2f stalls/s — allocations blocked waiting for reclaim", r.Allocstall))
	}
	if r.SwapIn > 0 || r.SwapOut > 0 {
		warnings = append(warnings, fmt.Sprintf("swap activity (in %.2f, out %.2f pages/s) — working set exceeds RAM", r.SwapIn, r.SwapOut))
	}
	if r.CompactStall > 0 {
		warnings = append(warnings, fmt.Sprintf("compact_stall %.2f stalls/s — allocations blocked on memory compaction", r.CompactStall))
	}
	return warnings
}

// round2 rounds to 2 decimal places for stable, LLM-friendly output.
func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
