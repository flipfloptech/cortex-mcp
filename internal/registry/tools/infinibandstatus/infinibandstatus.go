// Package infinibandstatus implements the get_infiniband_status diagnostic
// tool. It reads InfiniBand/RoCE HCA port state, physical state, rate, and
// error counters natively from sysfs (no external binaries required).
package infinibandstatus

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func init() {
	registry.Register(New())
}

// counterFiles are the per-port error counters audited under
// /sys/class/infiniband/<hca>/ports/<n>/counters/. Any file may be absent
// (e.g., on RoCE ports); absent counters are omitted from the output.
var counterFiles = []string{
	"symbol_error",
	"link_downed",
	"link_error_recovery",
	"port_rcv_errors",
	"port_xmit_discards",
}

// Tool implements registry.Tool for get_infiniband_status.
type Tool struct {
	sysfsRoot string
}

// New returns a new instance of the Tool rooted at the real sysfs.
func New() *Tool {
	return &Tool{sysfsRoot: "/sys"}
}

// Name returns the unique identifier for this tool.
func (t *Tool) Name() string {
	return "get_infiniband_status"
}

// Description provides a concise summary of what this tool does.
func (t *Tool) Description() string {
	return "Audit InfiniBand/RoCE HCA port health: link state, physical state, rate, LID, and error counters."
}

// Help provides detailed documentation.
func (t *Tool) Help() string {
	return `Reads InfiniBand HCA port status natively from /sys/class/infiniband/<hca>/ports/<n>/.

For every port it decodes state ("4: ACTIVE" -> ACTIVE), phys_state ("5: LinkUp" -> LinkUp),
rate (with a precomputed rate_gbps number), lid (hex-decoded), and link_layer.
Error counters are read from the per-port counters/ directory: symbol_error, link_downed,
link_error_recovery, port_rcv_errors, port_xmit_discards. Absent counter files are omitted.

Warnings are raised deterministically when a port state is not ACTIVE, the physical state
is not LinkUp, or any error counter is nonzero. RoCE ports (link_layer=Ethernet) are
reported factually with the same fields.`
}

// Category organizes the tool within the registry.
func (t *Tool) Category() registry.Category {
	return registry.CategoryNetwork
}

// Hidden hides the tool from general LLM discovery if true.
func (t *Tool) Hidden() bool {
	return false
}

// Parameters defines the expected input schema (none for this tool).
func (t *Tool) Parameters() []registry.ToolParam {
	return nil
}

// IsSupported checks whether any InfiniBand devices are exposed via sysfs.
func (t *Tool) IsSupported() (bool, string) {
	if !registry.PathExists(filepath.Join(t.sysfsRoot, "class", "infiniband")) {
		return false, "no InfiniBand devices (sysfs path missing)"
	}
	return true, ""
}

// PortStatus is the per-port diagnostic entry.
type PortStatus struct {
	Device         string            `json:"device"`
	Port           int               `json:"port"`
	State          string            `json:"state"`
	PhysState      string            `json:"phys_state"`
	Rate           string            `json:"rate"`
	RateGbps       float64           `json:"rate_gbps"`
	LID            int64             `json:"lid"`
	LinkLayer      string            `json:"link_layer"`
	Counters       map[string]uint64 `json:"counters,omitempty"`
	WarningReasons []string          `json:"warning_reasons,omitempty"`
}

// SystemSummary aggregates fleet-level port health for fast LLM triage.
type SystemSummary struct {
	HCACount        int `json:"hca_count"`
	TotalPorts      int `json:"total_ports"`
	ActivePorts     int `json:"active_ports"`
	PortsWithErrors int `json:"ports_with_errors"`
}

// Output is the tool's data payload.
type Output struct {
	SystemSummary SystemSummary `json:"system_summary"`
	Ports         []PortStatus  `json:"ports"`
}

// Execute walks every HCA port under sysfs and reports decoded status.
func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	if err := ctx.Err(); err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("context canceled before execution: %v", err)), nil
	}

	classDir := filepath.Join(t.sysfsRoot, "class", "infiniband")
	hcas, err := os.ReadDir(classDir)
	if err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("cannot read %s: %v", classDir, err)), nil
	}

	ports := []PortStatus{}
	hcaCount := 0
	for _, hca := range hcas {
		if err := ctx.Err(); err != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("context canceled during sysfs walk: %v", err)), nil
		}
		hcaCount++
		portsDir := filepath.Join(classDir, hca.Name(), "ports")
		portEntries, err := os.ReadDir(portsDir)
		if err != nil {
			// HCA without a readable ports dir: degrade by skipping it.
			continue
		}
		for _, pe := range portEntries {
			portNum, convErr := strconv.Atoi(pe.Name())
			if convErr != nil {
				continue // non-numeric entries are not ports
			}
			portDir := filepath.Join(portsDir, pe.Name())
			if info, statErr := os.Stat(portDir); statErr != nil || !info.IsDir() {
				continue
			}
			ports = append(ports, t.collectPort(hca.Name(), portNum, portDir))
		}
	}

	summary := SystemSummary{HCACount: hcaCount, TotalPorts: len(ports)}
	warningCount := 0
	for _, p := range ports {
		if p.State == "ACTIVE" {
			summary.ActivePorts++
		}
		hasCounterError := false
		for _, v := range p.Counters {
			if v > 0 {
				hasCounterError = true
				break
			}
		}
		if hasCounterError {
			summary.PortsWithErrors++
		}
		warningCount += len(p.WarningReasons)
	}

	status := registry.StatusOK
	if warningCount > 0 {
		status = registry.StatusWarning
	}

	summaryText := fmt.Sprintf("InfiniBand: %d/%d ports ACTIVE across %d HCAs, %d ports with error counters",
		summary.ActivePorts, summary.TotalPorts, summary.HCACount, summary.PortsWithErrors)

	res := registry.NewResult(t.Name(), status, summaryText, Output{SystemSummary: summary, Ports: ports})
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	res.Metadata.FilteringMethod = "deterministic"
	return res, nil
}

// collectPort reads one port directory and builds its decoded status entry,
// including deterministic warning reasons.
func (t *Tool) collectPort(device string, portNum int, portDir string) PortStatus {
	entry := PortStatus{Device: device, Port: portNum}

	if raw, ok := readFileTrim(filepath.Join(portDir, "state")); ok {
		entry.State = parseStateLine(raw)
	}
	if raw, ok := readFileTrim(filepath.Join(portDir, "phys_state")); ok {
		entry.PhysState = parseStateLine(raw)
	}
	if raw, ok := readFileTrim(filepath.Join(portDir, "rate")); ok {
		entry.Rate = raw
		entry.RateGbps = parseRateGbps(raw)
	}
	if raw, ok := readFileTrim(filepath.Join(portDir, "lid")); ok {
		entry.LID = parseLID(raw)
	}
	if raw, ok := readFileTrim(filepath.Join(portDir, "link_layer")); ok {
		entry.LinkLayer = raw
	}
	entry.Counters = readCounters(filepath.Join(portDir, "counters"))

	if entry.State != "ACTIVE" {
		entry.WarningReasons = append(entry.WarningReasons,
			fmt.Sprintf("port state is %q (expected ACTIVE)", entry.State))
	}
	if entry.PhysState != "LinkUp" {
		entry.WarningReasons = append(entry.WarningReasons,
			fmt.Sprintf("physical state is %q (expected LinkUp)", entry.PhysState))
	}
	for _, name := range counterFiles {
		if v, ok := entry.Counters[name]; ok && v > 0 {
			entry.WarningReasons = append(entry.WarningReasons,
				fmt.Sprintf("error counter %s is nonzero (%d)", name, v))
		}
	}
	return entry
}

// readFileTrim reads a sysfs attribute file and returns its whitespace-trimmed
// content. ok is false when the file is absent or unreadable.
func readFileTrim(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(data)), true
}

// parseStateLine decodes sysfs "<code>: <NAME>" lines (e.g., "4: ACTIVE" ->
// "ACTIVE"). Lines without a colon are returned trimmed as-is.
func parseStateLine(raw string) string {
	if i := strings.Index(raw, ":"); i >= 0 {
		return strings.TrimSpace(raw[i+1:])
	}
	return strings.TrimSpace(raw)
}

// parseLID decodes the sysfs lid attribute, which is typically hex ("0x3").
// Malformed values decode to 0.
func parseLID(raw string) int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 0, 64)
	if err != nil {
		return 0
	}
	return v
}

// parseRateGbps extracts the leading numeric speed from a sysfs rate string
// such as "100 Gb/sec (4X EDR)". Malformed values decode to 0.
func parseRateGbps(raw string) float64 {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return 0
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return v
}

// readCounters reads the known error counter files from a port's counters/
// directory. Absent or malformed counter files are omitted from the map;
// a nil map is returned when nothing could be read.
func readCounters(dir string) map[string]uint64 {
	counters := make(map[string]uint64, len(counterFiles))
	for _, name := range counterFiles {
		raw, ok := readFileTrim(filepath.Join(dir, name))
		if !ok {
			continue
		}
		v, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			continue
		}
		counters[name] = v
	}
	if len(counters) == 0 {
		return nil
	}
	return counters
}
