// Package conntracksummary implements the get_conntrack_summary diagnostic
// tool.
//
// It reports netfilter connection-tracking table pressure (count vs max with
// a precomputed usage percentage) and per-CPU failure counters summed across
// CPUs, all sourced natively from procfs.
package conntracksummary

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

// procfsRoot is the package-level injection point for the procfs mount.
// Tests and benchmarks override the copy held by each Tool instance.
var procfsRoot = "/proc"

// usageWarnThresholdPct is the table fill percentage above which a warning
// is raised.
const usageWarnThresholdPct = 80.0

// Counters holds failure-related conntrack counters summed across all CPUs.
type Counters struct {
	Invalid       uint64 `json:"invalid"`
	InsertFailed  uint64 `json:"insert_failed"`
	Drop          uint64 `json:"drop"`
	EarlyDrop     uint64 `json:"early_drop"`
	SearchRestart uint64 `json:"search_restart"`
}

// Output is the tool-specific data payload.
type Output struct {
	Count             uint64    `json:"count"`
	Max               uint64    `json:"max"`
	UsagePct          float64   `json:"usage_pct"`
	CountersAvailable bool      `json:"counters_available"`
	CPUCount          int       `json:"cpu_count,omitempty"`
	Counters          *Counters `json:"counters,omitempty"`
	WarningReasons    []string  `json:"warning_reasons,omitempty"`
}

// Tool implements registry.Tool for get_conntrack_summary.
type Tool struct {
	procfsRoot string
}

// New constructs the tool with the package-level defaults.
func New() *Tool {
	return &Tool{procfsRoot: procfsRoot}
}

func init() {
	registry.Register(New())
}

func (t *Tool) Name() string {
	return "get_conntrack_summary"
}

func (t *Tool) Description() string {
	return "Summarize netfilter conntrack table pressure: entry count vs max, usage percentage, and drop/failure counters."
}

func (t *Tool) Help() string {
	return `Summarizes the netfilter connection-tracking table, the usual culprit when
a busy node starts silently dropping new connections ("nf_conntrack: table
full, dropping packet").

Deterministic analysis: precomputes usage percentage (2 decimal places) and
sums per-CPU failure counters across all CPUs. Warns when usage exceeds 80%
or when drop / early_drop / insert_failed counters are non-zero.

Data Sources:
- /proc/sys/net/netfilter/nf_conntrack_count (current tracked connections)
- /proc/sys/net/netfilter/nf_conntrack_max (table capacity)
- /proc/net/stat/nf_conntrack (per-CPU hex counter rows, parsed by header
  column names so kernel layout differences are tolerated)

Output: {count, max, usage_pct, counters_available, cpu_count,
counters {invalid, insert_failed, drop, early_drop, search_restart},
warning_reasons[]}.

Caveats: if /proc/net/stat/nf_conntrack is missing the tool degrades to
counts only and sets counters_available=false.`
}

func (t *Tool) Category() registry.Category {
	return registry.CategoryNetwork
}

func (t *Tool) Parameters() []registry.ToolParam {
	return nil
}

func (t *Tool) Hidden() bool {
	return false
}

func (t *Tool) IsSupported() (bool, string) {
	if !registry.PathExists(filepath.Join(t.procfsRoot, "sys", "net", "netfilter", "nf_conntrack_count")) {
		return false, "conntrack not loaded"
	}
	return true, ""
}

func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	if err := ctx.Err(); err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("execution cancelled: %v", err)), nil
	}

	countPath := filepath.Join(t.procfsRoot, "sys", "net", "netfilter", "nf_conntrack_count")
	count, err := readUintFile(countPath)
	if err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf(
			"failed to read nf_conntrack_count (%v); conntrack module may not be loaded", err)), nil
	}

	// Missing/unreadable max degrades to max=0 / usage_pct=0 instead of failing.
	max, err := readUintFile(filepath.Join(t.procfsRoot, "sys", "net", "netfilter", "nf_conntrack_max"))
	if err != nil {
		max = 0
	}

	out := Output{
		Count:    count,
		Max:      max,
		UsagePct: computeUsagePct(count, max),
	}

	if statData, err := os.ReadFile(filepath.Join(t.procfsRoot, "net", "stat", "nf_conntrack")); err == nil {
		if counters, cpus, perr := parseConntrackStat(statData); perr == nil {
			out.Counters = counters
			out.CPUCount = cpus
			out.CountersAvailable = true
		}
	}

	out.WarningReasons = collectWarnings(out.UsagePct, out.Counters)

	status := registry.StatusOK
	if len(out.WarningReasons) > 0 {
		status = registry.StatusWarning
	}

	counterPart := "counters unavailable"
	if out.Counters != nil {
		counterPart = fmt.Sprintf("drop=%d early_drop=%d insert_failed=%d invalid=%d",
			out.Counters.Drop, out.Counters.EarlyDrop, out.Counters.InsertFailed, out.Counters.Invalid)
	}
	summary := fmt.Sprintf("Conntrack: %d/%d entries (%.2f%% used), %s", out.Count, out.Max, out.UsagePct, counterPart)

	res := registry.NewResult(t.Name(), status, summary, out)
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	return res, nil
}

// readUintFile reads a whitespace-trimmed unsigned integer from a file.
func readUintFile(path string) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", path, err)
	}
	return v, nil
}

// parseConntrackStat parses /proc/net/stat/nf_conntrack: a header line of
// column names followed by one row of hex counters per CPU. Column sets
// vary by kernel version, so columns are resolved by header name. Returns
// the failure counters summed across CPUs and the number of CPU rows.
func parseConntrackStat(data []byte) (*Counters, int, error) {
	var lines []string
	for _, raw := range strings.Split(string(data), "\n") {
		if line := strings.TrimSpace(raw); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) < 2 {
		return nil, 0, fmt.Errorf("nf_conntrack stat: expected header plus per-CPU rows, got %d line(s)", len(lines))
	}

	header := strings.Fields(lines[0])
	colIndex := make(map[string]int, len(header))
	for i, name := range header {
		colIndex[strings.ToLower(name)] = i
	}

	sums := make([]uint64, len(header))
	cpus := 0
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		cpus++
		for i := 0; i < len(fields) && i < len(sums); i++ {
			v, err := strconv.ParseUint(fields[i], 16, 64)
			if err != nil {
				continue
			}
			sums[i] += v
		}
	}
	if cpus == 0 {
		return nil, 0, fmt.Errorf("nf_conntrack stat: no per-CPU rows found")
	}

	sumOf := func(name string) uint64 {
		if i, ok := colIndex[name]; ok && i < len(sums) {
			return sums[i]
		}
		return 0
	}

	return &Counters{
		Invalid:       sumOf("invalid"),
		InsertFailed:  sumOf("insert_failed"),
		Drop:          sumOf("drop"),
		EarlyDrop:     sumOf("early_drop"),
		SearchRestart: sumOf("search_restart"),
	}, cpus, nil
}

// computeUsagePct returns count/max as a percentage rounded to 2 decimal
// places. A zero max yields 0 to guard against division by zero.
func computeUsagePct(count, max uint64) float64 {
	if max == 0 {
		return 0
	}
	return math.Round(float64(count)/float64(max)*10000) / 100
}

// collectWarnings derives warning_reasons: table usage above the threshold
// and any non-zero drop / early_drop / insert_failed counters.
func collectWarnings(usagePct float64, counters *Counters) []string {
	var reasons []string
	if usagePct > usageWarnThresholdPct {
		reasons = append(reasons, fmt.Sprintf(
			"conntrack table %.2f%% full (new connections are dropped at 100%%)", usagePct))
	}
	if counters == nil {
		return reasons
	}
	if counters.Drop > 0 {
		reasons = append(reasons, fmt.Sprintf("drop=%d (packets dropped because the conntrack table was full)", counters.Drop))
	}
	if counters.EarlyDrop > 0 {
		reasons = append(reasons, fmt.Sprintf("early_drop=%d (entries evicted early under table pressure)", counters.EarlyDrop))
	}
	if counters.InsertFailed > 0 {
		reasons = append(reasons, fmt.Sprintf("insert_failed=%d (conntrack entry insertions failed)", counters.InsertFailed))
	}
	return reasons
}
