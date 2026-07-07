// Package pressurestall implements the get_pressure_stall_info tool.
//
// It reads the kernel Pressure Stall Information (PSI) accounting from
// /proc/pressure/{cpu,memory,io,irq} and decodes it into an LLM-friendly
// structure with pre-evaluated warning reasons.
package pressurestall

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

// Warning thresholds (percent of wall-clock time over the 10-second window).
// These are heuristics: sustained values above them indicate that tasks are
// measurably stalled waiting on the resource.
const (
	// cpuSomeAvg10Threshold flags CPU contention when at least one task was
	// stalled on CPU for more than this percentage of the last 10 seconds.
	cpuSomeAvg10Threshold = 40.0

	// memoryFullAvg10Threshold flags memory pressure when ALL non-idle tasks
	// were simultaneously stalled on memory (reclaim/thrashing) for more than
	// this percentage of the last 10 seconds.
	memoryFullAvg10Threshold = 10.0

	// ioFullAvg10Threshold flags I/O saturation when ALL non-idle tasks were
	// simultaneously stalled on I/O for more than this percentage of the last
	// 10 seconds.
	ioFullAvg10Threshold = 10.0
)

// psiResources are the PSI resource files probed under <procfs>/pressure,
// in stable output order. "irq" only exists on kernels >= 6.1 with
// CONFIG_IRQ_TIME_ACCOUNTING and is optional.
var psiResources = []string{"cpu", "memory", "io", "irq"}

// PSILine is one decoded PSI record ("some" or "full" line).
type PSILine struct {
	// Avg10/Avg60/Avg300 are the percentage of time stalled over the last
	// 10/60/300 seconds (exponentially weighted moving averages).
	Avg10  float64 `json:"avg10"`
	Avg60  float64 `json:"avg60"`
	Avg300 float64 `json:"avg300"`

	// TotalUsec is the absolute stall time in microseconds since boot.
	TotalUsec uint64 `json:"total_usec"`
}

// ResourcePressure groups the "some" and "full" records of one resource.
// Full is absent for the cpu resource on kernels older than 5.13.
type ResourcePressure struct {
	Some *PSILine `json:"some,omitempty"`
	Full *PSILine `json:"full,omitempty"`
}

// Output is the tool's data payload.
type Output struct {
	// Resources maps resource name (cpu, memory, io, irq) to its pressure
	// records. Resources whose PSI file is absent are omitted.
	Resources map[string]*ResourcePressure `json:"resources"`

	// Notes records non-fatal degradations (e.g. a malformed PSI file that
	// was skipped).
	Notes []string `json:"notes,omitempty"`

	// WarningReasons lists pre-evaluated threshold breaches in plain words.
	WarningReasons []string `json:"warning_reasons"`
}

// Tool implements registry.Tool for get_pressure_stall_info.
type Tool struct {
	// procfsRoot is the procfs mount point, injectable for hermetic tests.
	procfsRoot string
}

// New returns a pressure stall tool bound to the real /proc.
func New() *Tool {
	return &Tool{procfsRoot: "/proc"}
}

func init() {
	registry.Register(New())
}

// Name returns the unique tool identifier.
func (t *Tool) Name() string { return "get_pressure_stall_info" }

// Description returns the one-line summary for tool discovery.
func (t *Tool) Description() string {
	return "Read kernel PSI (Pressure Stall Information) to quantify CPU, memory, I/O, and IRQ contention"
}

// Help returns the full tool documentation.
func (t *Tool) Help() string {
	return `get_pressure_stall_info — Kernel Pressure Stall Information (PSI)

Reads the kernel's PSI accounting to quantify how much wall-clock time tasks
spend stalled waiting for CPU, memory, I/O, and (kernel >= 6.1) IRQ. PSI is the
canonical saturation signal: non-zero "full" pressure means every non-idle task
was blocked simultaneously, i.e. pure lost throughput.

Data Sources:
  - /proc/pressure/cpu
  - /proc/pressure/memory
  - /proc/pressure/io
  - /proc/pressure/irq (optional, kernel >= 6.1)

Each file provides "some" (at least one task stalled) and "full" (all non-idle
tasks stalled) records with avg10/avg60/avg300 percentages and a cumulative
total in microseconds. The cpu resource has no "full" record on kernels older
than 5.13.

Warning Heuristics (evaluated on the 10-second averages):
  - cpu some avg10 > 40%: significant CPU contention (run-queue delays).
  - memory full avg10 > 10%: severe memory pressure (reclaim/thrashing stalls).
  - io full avg10 > 10%: severe I/O saturation (storage cannot keep up).

Degradation Profile:
  - IsSupported() is false when /proc/pressure/cpu is missing
    (kernel < 4.20 or booted with psi=0).
  - A missing irq file is normal on kernels < 6.1 and is silently omitted.
  - A malformed resource file is skipped and reported in "notes".

Parameters: None
Supported on: Linux (kernel >= 4.20 with PSI enabled)`
}

// Category classifies this tool under system.
func (t *Tool) Category() registry.Category { return registry.CategorySystem }

// Parameters returns nil — this tool takes no arguments.
func (t *Tool) Parameters() []registry.ToolParam { return nil }

// Hidden returns false — this tool is LLM-discoverable.
func (t *Tool) Hidden() bool { return false }

// IsSupported requires the PSI cpu file, present on kernels >= 4.20 with PSI
// accounting enabled.
func (t *Tool) IsSupported() (bool, string) {
	if !registry.IsLinux() {
		return false, "requires Linux"
	}
	if !registry.PathExists(filepath.Join(t.procfsRoot, "pressure", "cpu")) {
		return false, "PSI not available (kernel < 4.20 or psi=0)"
	}
	return true, ""
}

// Execute reads and decodes every available PSI resource file.
func (t *Tool) Execute(ctx context.Context, _ json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	if err := ctx.Err(); err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("context canceled: %v", err)), nil
	}

	out := Output{
		Resources:      make(map[string]*ResourcePressure, len(psiResources)),
		WarningReasons: []string{},
	}

	for _, res := range psiResources {
		path := filepath.Join(t.procfsRoot, "pressure", res)
		data, err := os.ReadFile(path)
		if err != nil {
			// Absent resources (e.g. irq on kernels < 6.1) are omitted.
			continue
		}
		rp, err := parsePressureFile(data)
		if err != nil {
			out.Notes = append(out.Notes, fmt.Sprintf("skipped %s: %v", res, err))
			continue
		}
		out.Resources[res] = &rp
	}

	if len(out.Resources) == 0 {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("no PSI resources readable under %s/pressure", t.procfsRoot)), nil
	}

	out.WarningReasons = evaluateWarnings(out.Resources)

	status := registry.StatusOK
	summary := fmt.Sprintf("PSI: %d resources read, no pressure thresholds breached", len(out.Resources))
	if len(out.WarningReasons) > 0 {
		status = registry.StatusWarning
		summary = fmt.Sprintf("PSI: %d resources read, %d pressure warnings: %s",
			len(out.Resources), len(out.WarningReasons), strings.Join(out.WarningReasons, "; "))
	}

	result := registry.NewResult(t.Name(), status, summary, out)
	result.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	result.Metadata.FilteringMethod = "heuristic"
	return result, nil
}

// parsePressureFile decodes a full PSI file (one or two lines).
func parsePressureFile(data []byte) (ResourcePressure, error) {
	var rp ResourcePressure
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	parsed := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		kind, rec, err := parsePSILine(line)
		if err != nil {
			return ResourcePressure{}, err
		}
		recCopy := rec
		switch kind {
		case "some":
			rp.Some = &recCopy
		case "full":
			rp.Full = &recCopy
		}
		parsed++
	}
	if parsed == 0 {
		return ResourcePressure{}, fmt.Errorf("empty PSI file")
	}
	return rp, nil
}

// parsePSILine decodes one PSI record line, e.g.
// "some avg10=0.00 avg60=0.00 avg300=0.00 total=12345".
// It returns the record kind ("some" or "full") and the decoded values.
func parsePSILine(line string) (string, PSILine, error) {
	fields := strings.Fields(line)
	if len(fields) != 5 {
		return "", PSILine{}, fmt.Errorf("malformed PSI line %q: want 5 fields, got %d", line, len(fields))
	}
	kind := fields[0]
	if kind != "some" && kind != "full" {
		return "", PSILine{}, fmt.Errorf("malformed PSI line %q: unknown record kind %q", line, kind)
	}

	var rec PSILine
	for _, kv := range fields[1:] {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			return "", PSILine{}, fmt.Errorf("malformed PSI field %q in line %q", kv, line)
		}
		key, val := parts[0], parts[1]
		switch key {
		case "avg10", "avg60", "avg300":
			f, err := strconv.ParseFloat(val, 64)
			if err != nil {
				return "", PSILine{}, fmt.Errorf("malformed PSI %s value %q: %w", key, val, err)
			}
			switch key {
			case "avg10":
				rec.Avg10 = f
			case "avg60":
				rec.Avg60 = f
			case "avg300":
				rec.Avg300 = f
			}
		case "total":
			u, err := strconv.ParseUint(val, 10, 64)
			if err != nil {
				return "", PSILine{}, fmt.Errorf("malformed PSI total value %q: %w", val, err)
			}
			rec.TotalUsec = u
		default:
			return "", PSILine{}, fmt.Errorf("unknown PSI field %q in line %q", key, line)
		}
	}
	return kind, rec, nil
}

// evaluateWarnings applies the documented avg10 thresholds and returns
// human-readable breach descriptions in stable (sorted) order.
func evaluateWarnings(resources map[string]*ResourcePressure) []string {
	warnings := []string{}
	if cpu := resources["cpu"]; cpu != nil && cpu.Some != nil && cpu.Some.Avg10 > cpuSomeAvg10Threshold {
		warnings = append(warnings, fmt.Sprintf("cpu some avg10 %.2f%% exceeds %.0f%% (significant CPU contention)", cpu.Some.Avg10, cpuSomeAvg10Threshold))
	}
	if mem := resources["memory"]; mem != nil && mem.Full != nil && mem.Full.Avg10 > memoryFullAvg10Threshold {
		warnings = append(warnings, fmt.Sprintf("memory full avg10 %.2f%% exceeds %.0f%% (severe memory pressure)", mem.Full.Avg10, memoryFullAvg10Threshold))
	}
	if io := resources["io"]; io != nil && io.Full != nil && io.Full.Avg10 > ioFullAvg10Threshold {
		warnings = append(warnings, fmt.Sprintf("io full avg10 %.2f%% exceeds %.0f%% (severe I/O saturation)", io.Full.Avg10, ioFullAvg10Threshold))
	}
	sort.Strings(warnings)
	return warnings
}
