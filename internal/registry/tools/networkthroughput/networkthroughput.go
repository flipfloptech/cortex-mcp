// Package networkthroughput implements the get_network_throughput tool.
//
// It answers "how much traffic is each NIC moving right now, and is any
// link saturated or dropping packets?" — the question behind slow
// transfers, lossy fabrics, and "is this 10G link actually doing 10G?".
// It samples /proc/net/dev twice over a short, context-cancellable window
// and reports per-interface rates plus link-speed utilization from sysfs.
package networkthroughput

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

const (
	// defaultWindow is the sampling window when sample_duration_ms is omitted.
	defaultWindow = 500 * time.Millisecond

	// minWindowMs / maxWindowMs clamp the sample_duration_ms parameter.
	minWindowMs = 10
	maxWindowMs = 5000

	// utilizationWarnPct is the threshold above which a link is flagged
	// as near saturation (strictly greater than).
	utilizationWarnPct = 90.0
)

// ifCounters holds the cumulative kernel counters for one interface, as
// read from one /proc/net/dev sample.
type ifCounters struct {
	rxBytes, rxPackets, rxErrs, rxDrop uint64
	txBytes, txPackets, txErrs, txDrop uint64
}

// wrapped reports whether any counter in second regressed below first —
// the signature of a counter wrap or an interface reset mid-window.
func (c ifCounters) wrapped(second ifCounters) bool {
	return second.rxBytes < c.rxBytes || second.rxPackets < c.rxPackets ||
		second.rxErrs < c.rxErrs || second.rxDrop < c.rxDrop ||
		second.txBytes < c.txBytes || second.txPackets < c.txPackets ||
		second.txErrs < c.txErrs || second.txDrop < c.txDrop
}

// InterfaceRate is the per-interface throughput measurement.
// LinkSpeedMbps and UtilizationPct are omitted when sysfs reports no
// usable speed (absent file or -1 on virtual interfaces).
type InterfaceRate struct {
	Name           string   `json:"name"`
	RxMbps         float64  `json:"rx_mbps"`
	TxMbps         float64  `json:"tx_mbps"`
	RxPps          float64  `json:"rx_pps"`
	TxPps          float64  `json:"tx_pps"`
	RxDropsPerS    float64  `json:"rx_drops_per_s"`
	TxDropsPerS    float64  `json:"tx_drops_per_s"`
	RxErrsPerS     float64  `json:"rx_errs_per_s"`
	TxErrsPerS     float64  `json:"tx_errs_per_s"`
	LinkSpeedMbps  *float64 `json:"link_speed_mbps,omitempty"`
	UtilizationPct *float64 `json:"utilization_pct,omitempty"`
}

// Summary aggregates the measurement across all interfaces.
type Summary struct {
	TotalRxMbps        float64 `json:"total_rx_mbps"`
	TotalTxMbps        float64 `json:"total_tx_mbps"`
	BusiestInterface   string  `json:"busiest_interface,omitempty"`
	InterfacesMeasured int     `json:"interfaces_measured"`
}

// Data is the tool's structured output payload.
type Data struct {
	WindowMs       int64           `json:"window_ms"`
	Interfaces     []InterfaceRate `json:"interfaces"`
	Summary        Summary         `json:"summary"`
	WarningReasons []string        `json:"warning_reasons,omitempty"`
	Notes          []string        `json:"notes,omitempty"`
}

// Tool implements registry.Tool for get_network_throughput.
type Tool struct {
	procfsRoot string
	sysfsRoot  string
}

// New returns a Tool wired to the real procfs and sysfs roots.
func New() *Tool {
	return &Tool{procfsRoot: "/proc", sysfsRoot: "/sys"}
}

func init() {
	registry.Register(New())
}

// Name returns the unique tool identifier.
func (t *Tool) Name() string {
	return "get_network_throughput"
}

// Description returns a one-line summary for tool discovery.
func (t *Tool) Description() string {
	return "Measure live per-interface network throughput, packet rates, drops and link utilization"
}

// Help returns the full tool documentation.
func (t *Tool) Help() string {
	return `Measures live network throughput by sampling /proc/net/dev twice over a short window and computing per-interface deltas — the native answer to "is this link saturated?" and "are we dropping packets right now?".

Data sources: /proc/net/dev (cumulative rx/tx bytes, packets, errs and
drop counters per interface; the loopback interface is excluded) sampled
twice over sample_duration_ms (default 500 ms, clamped to 10–5000 ms; the
wait is context-cancellable), and /sys/class/net/<iface>/speed for the
negotiated link speed in Mbps (absent or -1 on virtual interfaces).

Math (deterministic): rx_mbps/tx_mbps = bytes-delta × 8 / 1e6 / elapsed
seconds, rounded to 2 decimal places; rx_pps/tx_pps and the per-second
drop/error rates are rounded to 1 decimal place; utilization_pct =
max(rx_mbps, tx_mbps) / link speed × 100 at 1 decimal place, omitted
together with link_speed_mbps when the speed is unknown. window_ms
reports the actually elapsed window. The summary precomputes
total_rx_mbps, total_tx_mbps, busiest_interface (highest combined
rx+tx mbps) and interfaces_measured.

Warnings (status elevates to "warning"): any packet drops or interface
errors observed during the window, and utilization above 90% of the
link speed.

Caveats: a counter that wraps or resets mid-window zeroes that
interface's rates for this run (noted in notes[]); interfaces appearing
or disappearing mid-window are skipped and noted. Cancellation during
the sampling window returns a prompt error result.`
}

// Category returns the tool taxonomy classification.
func (t *Tool) Category() registry.Category {
	return registry.CategoryNetwork
}

// Parameters returns the parameter schema.
func (t *Tool) Parameters() []registry.ToolParam {
	return []registry.ToolParam{
		{
			Name:        "sample_duration_ms",
			Type:        "integer",
			Description: "Optional. Sampling window in milliseconds between the two /proc/net/dev reads (default 500, min 10, max 5000).",
			Required:    false,
			Default:     "500",
		},
	}
}

// Hidden returns false; this is a public diagnostic tool.
func (t *Tool) Hidden() bool { return false }

// IsSupported requires the procfs network device counter table.
func (t *Tool) IsSupported() (bool, string) {
	path := filepath.Join(t.procfsRoot, "net", "dev")
	if !registry.PathExists(path) {
		return false, path + " is missing (Linux procfs required)"
	}
	return true, ""
}

// Execute samples /proc/net/dev twice over the requested window and
// reports per-interface rates.
func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	window, err := parseArgs(args)
	if err != nil {
		return registry.NewErrorResult(t.Name(), err.Error()), nil
	}

	first, err := t.readSnapshot()
	if err != nil {
		return registry.NewErrorResult(t.Name(), err.Error()), nil
	}

	sampleStart := time.Now()
	select {
	case <-ctx.Done():
		return registry.NewErrorResult(t.Name(), "context cancelled during sampling window: "+ctx.Err().Error()), nil
	case <-time.After(window):
	}

	second, err := t.readSnapshot()
	if err != nil {
		return registry.NewErrorResult(t.Name(), err.Error()), nil
	}
	elapsed := time.Since(sampleStart)

	speeds := make(map[string]float64, len(second))
	for name := range second {
		if speed, ok := t.readLinkSpeed(name); ok {
			speeds[name] = speed
		}
	}

	interfaces, notes := computeRates(first, second, elapsed, speeds)
	warnings := buildWarnings(interfaces)

	data := Data{
		WindowMs:       elapsed.Milliseconds(),
		Interfaces:     interfaces,
		Summary:        buildSummary(interfaces),
		WarningReasons: warnings,
		Notes:          notes,
	}

	status := registry.StatusOK
	if len(warnings) > 0 {
		status = registry.StatusWarning
	}

	summary := fmt.Sprintf("Measured %d interface(s) over %d ms: total rx %.2f Mbps, tx %.2f Mbps",
		data.Summary.InterfacesMeasured, data.WindowMs, data.Summary.TotalRxMbps, data.Summary.TotalTxMbps)
	if data.Summary.BusiestInterface != "" {
		summary += " (busiest: " + data.Summary.BusiestInterface + ")"
	}
	if len(warnings) > 0 {
		summary += fmt.Sprintf("; %d warning(s)", len(warnings))
	}

	res := registry.NewResult(t.Name(), status, summary, data)
	res.Metadata.FilteringMethod = "deterministic"
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	return res, nil
}

// parseArgs validates the tool arguments: sample_duration_ms defaults to
// 500 and is clamped to [10, 5000].
func parseArgs(args json.RawMessage) (time.Duration, error) {
	var params struct {
		SampleDurationMs *int `json:"sample_duration_ms"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &params); err != nil {
			return 0, fmt.Errorf("failed to parse arguments: %v", err)
		}
	}
	if params.SampleDurationMs == nil {
		return defaultWindow, nil
	}
	ms := *params.SampleDurationMs
	if ms < minWindowMs {
		ms = minWindowMs
	}
	if ms > maxWindowMs {
		ms = maxWindowMs
	}
	return time.Duration(ms) * time.Millisecond, nil
}

// readSnapshot reads and parses one /proc/net/dev sample.
func (t *Tool) readSnapshot() (map[string]ifCounters, error) {
	path := filepath.Join(t.procfsRoot, "net", "dev")
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %v", path, err)
	}
	return parseNetDev(content), nil
}

// parseNetDev decodes /proc/net/dev rows into per-interface counters.
// Row format after "iface:": rx bytes packets errs drop fifo frame
// compressed multicast, then tx bytes packets errs drop fifo colls
// carrier compressed. Headers, loopback and malformed rows are skipped.
func parseNetDev(content []byte) map[string]ifCounters {
	counters := make(map[string]ifCounters)
	for _, line := range strings.Split(string(content), "\n") {
		name, rest, found := strings.Cut(line, ":")
		if !found {
			continue // header or garbage line
		}
		name = strings.TrimSpace(name)
		if name == "" || name == "lo" || strings.Contains(name, " ") {
			continue
		}

		fields := strings.Fields(rest)
		if len(fields) < 12 {
			continue // malformed row
		}
		vals := make([]uint64, 12)
		ok := true
		for i := range vals {
			v, err := strconv.ParseUint(fields[i], 10, 64)
			if err != nil {
				ok = false
				break
			}
			vals[i] = v
		}
		if !ok {
			continue
		}

		counters[name] = ifCounters{
			rxBytes: vals[0], rxPackets: vals[1], rxErrs: vals[2], rxDrop: vals[3],
			txBytes: vals[8], txPackets: vals[9], txErrs: vals[10], txDrop: vals[11],
		}
	}
	return counters
}

// readLinkSpeed reads /sys/class/net/<iface>/speed. ok is false when the
// file is absent, unreadable (virtio NICs return EINVAL), non-numeric, or
// reports a non-positive speed (-1 on virtual interfaces).
func (t *Tool) readLinkSpeed(iface string) (float64, bool) {
	raw, err := os.ReadFile(filepath.Join(t.sysfsRoot, "class", "net", iface, "speed"))
	if err != nil {
		return 0, false
	}
	speed, err := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
	if err != nil || speed <= 0 {
		return 0, false
	}
	return speed, true
}

// computeRates turns two counter snapshots into per-interface rates over
// the elapsed window. Interfaces present in only one snapshot are skipped
// with a note; a counter regression (wrap/reset) zeroes that interface's
// rates with a note. Output is sorted by interface name.
func computeRates(first, second map[string]ifCounters, elapsed time.Duration, speeds map[string]float64) ([]InterfaceRate, []string) {
	var notes []string
	secs := elapsed.Seconds()
	if secs <= 0 {
		secs = 1 // defensive: never divide by zero
	}

	names := make([]string, 0, len(first))
	for name := range first {
		if _, ok := second[name]; ok {
			names = append(names, name)
		} else {
			notes = append(notes, name+": disappeared during sample window; skipped")
		}
	}
	for name := range second {
		if _, ok := first[name]; !ok {
			notes = append(notes, name+": appeared during sample window; skipped")
		}
	}
	sort.Strings(names)
	sort.Strings(notes)

	interfaces := make([]InterfaceRate, 0, len(names))
	for _, name := range names {
		a, b := first[name], second[name]
		rate := InterfaceRate{Name: name}

		if a.wrapped(b) {
			notes = append(notes, name+": counter wrap or reset detected; rates reported as 0 for this window")
		} else {
			rate.RxMbps = round2(float64(b.rxBytes-a.rxBytes) * 8 / 1e6 / secs)
			rate.TxMbps = round2(float64(b.txBytes-a.txBytes) * 8 / 1e6 / secs)
			rate.RxPps = round1(float64(b.rxPackets-a.rxPackets) / secs)
			rate.TxPps = round1(float64(b.txPackets-a.txPackets) / secs)
			rate.RxDropsPerS = round1(float64(b.rxDrop-a.rxDrop) / secs)
			rate.TxDropsPerS = round1(float64(b.txDrop-a.txDrop) / secs)
			rate.RxErrsPerS = round1(float64(b.rxErrs-a.rxErrs) / secs)
			rate.TxErrsPerS = round1(float64(b.txErrs-a.txErrs) / secs)
		}

		if speed, ok := speeds[name]; ok {
			rate.LinkSpeedMbps = &speed
			util := round1(math.Max(rate.RxMbps, rate.TxMbps) / speed * 100)
			rate.UtilizationPct = &util
		}
		interfaces = append(interfaces, rate)
	}
	return interfaces, notes
}

// buildWarnings flags drops, errors and near-saturated links observed
// during the sampling window.
func buildWarnings(interfaces []InterfaceRate) []string {
	var warnings []string
	for _, i := range interfaces {
		if i.RxDropsPerS > 0 || i.TxDropsPerS > 0 {
			warnings = append(warnings, fmt.Sprintf("%s: packet drops observed during sample window (rx %.1f/s, tx %.1f/s)",
				i.Name, i.RxDropsPerS, i.TxDropsPerS))
		}
		if i.RxErrsPerS > 0 || i.TxErrsPerS > 0 {
			warnings = append(warnings, fmt.Sprintf("%s: interface errors observed during sample window (rx %.1f/s, tx %.1f/s)",
				i.Name, i.RxErrsPerS, i.TxErrsPerS))
		}
		if i.UtilizationPct != nil && *i.UtilizationPct > utilizationWarnPct {
			warnings = append(warnings, fmt.Sprintf("%s: utilization %.1f%% exceeds %.0f%% of link speed",
				i.Name, *i.UtilizationPct, utilizationWarnPct))
		}
	}
	return warnings
}

// buildSummary aggregates totals and picks the busiest interface by
// combined rx+tx mbps (first name in sort order wins ties).
func buildSummary(interfaces []InterfaceRate) Summary {
	s := Summary{InterfacesMeasured: len(interfaces)}
	var totalRx, totalTx, busiest float64
	for idx, i := range interfaces {
		totalRx += i.RxMbps
		totalTx += i.TxMbps
		combined := i.RxMbps + i.TxMbps
		if idx == 0 || combined > busiest {
			busiest = combined
			s.BusiestInterface = i.Name
		}
	}
	s.TotalRxMbps = round2(totalRx)
	s.TotalTxMbps = round2(totalTx)
	return s
}

// round1 rounds to 1 decimal place.
func round1(v float64) float64 {
	return math.Round(v*10) / 10
}

// round2 rounds to 2 decimal places.
func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
