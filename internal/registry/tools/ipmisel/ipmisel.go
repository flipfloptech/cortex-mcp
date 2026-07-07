// Package ipmisel implements the query_ipmi_sel diagnostic tool.
//
// It queries the BMC System Event Log through ipmitool — the justified
// primary data source, since SEL access requires the vendor IPMI protocol
// over the kernel's /dev/ipmi* character device. Records are normalized
// (RFC3339 timestamps, sensor-type histogram) and heuristically scanned for
// Critical / Non-recoverable severities and SEL capacity exhaustion.
package ipmisel

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// Package-level seams so tests can fake binaries, effective UID and the /dev
// tree without ever executing real commands or requiring BMC hardware.
var (
	execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		// CombinedOutput: permission diagnostics from sudo/ipmitool land on
		// stderr and are needed for the Unauthorized classification.
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	}
	execLookPath = exec.LookPath
	geteuid      = os.Geteuid
	devRoot      = "/dev"
)

const (
	defaultLastN = 25
	maxLastN     = 200
	// maxCriticalWarnings bounds the per-record warning list so a flooded SEL
	// cannot blow up the LLM token payload.
	maxCriticalWarnings = 10

	unauthorizedMsg  = "Unauthorized: Root or passwordless sudo privileges required for BMC access."
	missingBinaryMsg = "'ipmitool' binary not found in $PATH"
	missingDeviceMsg = "no BMC character device present (/dev/ipmi0, /dev/ipmi/0, /dev/ipmidev/0)"
)

// bmcDeviceCandidates are the well-known BMC character device paths,
// relative to devRoot.
var bmcDeviceCandidates = []string{"ipmi0", filepath.Join("ipmi", "0"), filepath.Join("ipmidev", "0")}

// intRe extracts the first unsigned integer from tolerant `sel info` values
// such as "8144 bytes" or "20%".
var intRe = regexp.MustCompile(`\d+`)

// permissionMarkers classify command failures caused by missing privileges.
var permissionMarkers = []string{
	"permission denied",
	"operation not permitted",
	"password is required",
	"a terminal is required",
	"not allowed to execute",
	"insufficient privilege",
}

// SELInfo summarizes `ipmitool sel info`. Fields the BMC reports as
// unspecified are omitted.
type SELInfo struct {
	Entries        *int64 `json:"entries,omitempty"`
	FreeSpaceBytes *int64 `json:"free_space_bytes,omitempty"`
	PercentUsed    *int64 `json:"percent_used,omitempty"`
}

// Record is a single normalized SEL event from `ipmitool sel elist`.
type Record struct {
	ID string `json:"id"`
	// Timestamp is RFC3339 when the BMC clock was valid; pre-clock events
	// ("Pre-Init") keep their raw representation.
	Timestamp string `json:"timestamp"`
	Sensor    string `json:"sensor"`
	Event     string `json:"event"`
	Direction string `json:"direction,omitempty"`
}

// Output is the tool's data payload.
type Output struct {
	SELInfo            *SELInfo       `json:"sel_info,omitempty"`
	Records            []Record       `json:"records"`
	CountsBySensorType map[string]int `json:"counts_by_sensor_type"`
	WarningReasons     []string       `json:"warning_reasons"`
}

// Args is the tool's parameter schema.
type Args struct {
	LastN int `json:"last_n"`
}

// Tool implements registry.Tool for query_ipmi_sel.
type Tool struct{}

// New constructs the tool.
func New() *Tool {
	return &Tool{}
}

// Name returns the unique tool identifier.
func (t *Tool) Name() string {
	return "query_ipmi_sel"
}

// Description returns the one-line summary for get_tool_list.
func (t *Tool) Description() string {
	return "Query the BMC System Event Log (IPMI SEL) for hardware events, severities and log capacity."
}

// Help returns the full tool help text.
func (t *Tool) Help() string {
	return `Queries the baseboard management controller's System Event Log to surface out-of-band hardware events (temperature excursions, fan failures, ECC faults, PSU state changes) recorded independently of the host OS.

Data sources:
1. ipmitool sel elist — the event records, executed via passwordless sudo (sudo -n) when not running as root. Rows are parsed into {id, timestamp, sensor, event, direction}.
2. ipmitool sel info — SEL capacity metadata (entries, free space, percent used), parsed tolerantly since field labels vary between BMC firmwares.

Requires the ipmitool binary and a BMC character device (/dev/ipmi0, /dev/ipmi/0 or /dev/ipmidev/0).

Timestamps are converted to RFC3339; events logged before BMC clock initialization keep their raw "Pre-Init" marker. The full SEL is aggregated into counts_by_sensor_type, and heuristic warning_reasons flag records containing "Critical" or "Non-recoverable" severities plus SEL usage above 75%.

Parameters:
- last_n: Optional integer. Number of most recent SEL records to return (default 25, max 200). Aggregates always cover the entire log.`
}

// Category classifies the tool as hardware diagnostics.
func (t *Tool) Category() registry.Category {
	return registry.CategoryHardware
}

// Parameters returns the parameter schema.
func (t *Tool) Parameters() []registry.ToolParam {
	return []registry.ToolParam{
		{
			Name:        "last_n",
			Type:        "integer",
			Description: "Optional: Number of most recent SEL records to return (default 25, max 200).",
			Required:    false,
			Default:     "25",
		},
	}
}

// Hidden returns false; query_ipmi_sel is a public tool.
func (t *Tool) Hidden() bool {
	return false
}

// IsSupported requires both the ipmitool binary and a BMC character device.
func (t *Tool) IsSupported() (bool, string) {
	_, binErr := execLookPath("ipmitool")
	_, devOK := bmcDevicePath()
	switch {
	case binErr != nil && !devOK:
		return false, missingBinaryMsg + "; " + missingDeviceMsg
	case binErr != nil:
		return false, missingBinaryMsg
	case !devOK:
		return false, missingDeviceMsg
	}
	return true, ""
}

// Execute queries the SEL and returns the analyzed event log.
func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	if err := ctx.Err(); err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("execution cancelled: %v", err)), nil
	}

	parsedArgs := Args{LastN: defaultLastN}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &parsedArgs); err != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("invalid arguments: %v", err)), nil
		}
	}
	if parsedArgs.LastN <= 0 {
		parsedArgs.LastN = defaultLastN
	}
	if parsedArgs.LastN > maxLastN {
		parsedArgs.LastN = maxLastN
	}

	if _, err := execLookPath("ipmitool"); err != nil {
		return registry.NewErrorResult(t.Name(), missingBinaryMsg), nil
	}
	if _, ok := bmcDevicePath(); !ok {
		return registry.NewErrorResult(t.Name(), missingDeviceMsg), nil
	}

	// Primary: the event records.
	name, cmdArgs := buildIpmitoolCommand("sel", "elist")
	out, err := execCommand(ctx, name, cmdArgs...)
	if err != nil {
		if isPermissionError(out, err) {
			return registry.NewErrorResult(t.Name(), unauthorizedMsg), nil
		}
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("ipmitool sel elist failed: %v", err)), nil
	}
	records := parseSELList(out)
	counts, warnings := analyzeRecords(records)

	// Secondary: capacity metadata. Failure degrades to records-only output.
	var selInfo *SELInfo
	if ctx.Err() == nil {
		name, cmdArgs = buildIpmitoolCommand("sel", "info")
		if infoOut, infoErr := execCommand(ctx, name, cmdArgs...); infoErr == nil {
			selInfo = parseSELInfo(infoOut)
		}
	}
	if selInfo != nil && selInfo.PercentUsed != nil && *selInfo.PercentUsed > 75 {
		warnings = append(warnings, fmt.Sprintf("SEL is %d%% full (>75%%); oldest events may be overwritten soon", *selInfo.PercentUsed))
	}

	total := len(records)
	if len(records) > parsedArgs.LastN {
		records = records[len(records)-parsedArgs.LastN:]
	}
	if records == nil {
		records = []Record{}
	}
	if warnings == nil {
		warnings = []string{}
	}

	status := registry.StatusOK
	if len(warnings) > 0 {
		status = registry.StatusWarning
	}
	summary := fmt.Sprintf("IPMI SEL: %d record(s) total, returning %d most recent, %d warning(s)", total, len(records), len(warnings))

	res := registry.NewResult(t.Name(), status, summary, Output{
		SELInfo:            selInfo,
		Records:            records,
		CountsBySensorType: counts,
		WarningReasons:     warnings,
	})
	res.Metadata.FilteringMethod = "heuristic"
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	return res, nil
}

// bmcDevicePath returns the first present BMC character device.
func bmcDevicePath() (string, bool) {
	for _, rel := range bmcDeviceCandidates {
		full := filepath.Join(devRoot, rel)
		if _, err := os.Stat(full); err == nil {
			return full, true
		}
	}
	return "", false
}

// buildIpmitoolCommand wraps an ipmitool invocation in `sudo -n` when the
// process is not running as root and sudo is available.
func buildIpmitoolCommand(subArgs ...string) (string, []string) {
	if geteuid() != 0 {
		if _, err := execLookPath("sudo"); err == nil {
			return "sudo", append([]string{"-n", "ipmitool"}, subArgs...)
		}
	}
	return "ipmitool", subArgs
}

// isPermissionError classifies a failed command as a privilege problem by
// scanning the error and the combined output for well-known markers.
func isPermissionError(out []byte, err error) bool {
	if err == nil {
		return false
	}
	combined := strings.ToLower(err.Error() + " " + string(out))
	for _, marker := range permissionMarkers {
		if strings.Contains(combined, marker) {
			return true
		}
	}
	return false
}

// parseSELList parses `ipmitool sel elist` output, skipping informational
// lines (e.g. "SEL has no entries") and malformed rows.
func parseSELList(out []byte) []Record {
	var records []Record
	for _, line := range strings.Split(string(out), "\n") {
		if rec, ok := parseSELRecord(line); ok {
			records = append(records, rec)
		}
	}
	return records
}

// parseSELRecord parses a single elist row of the form
//
//	1 | 05/01/2026 | 13:02:11 | Temperature #0x30 | Upper Critical going high | Asserted
//
// The direction column is optional; extra trailing columns are ignored.
func parseSELRecord(line string) (Record, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return Record{}, false
	}
	parts := strings.Split(line, "|")
	if len(parts) < 5 {
		return Record{}, false
	}
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	if parts[0] == "" || parts[3] == "" {
		return Record{}, false
	}

	rec := Record{
		ID:        parts[0],
		Timestamp: parseTimestamp(parts[1], parts[2]),
		Sensor:    parts[3],
		Event:     parts[4],
	}
	if len(parts) >= 6 {
		rec.Direction = parts[5]
	}
	return rec, true
}

// parseTimestamp converts the elist date+time columns (MM/DD/YYYY HH:MM:SS)
// to RFC3339. Unparseable values — notably "Pre-Init" events logged before
// the BMC clock was set — are preserved raw.
func parseTimestamp(date, tm string) string {
	date = strings.TrimSpace(date)
	tm = strings.TrimSpace(tm)

	combined := strings.TrimSpace(date + " " + tm)
	if ts, err := time.Parse("01/02/2006 15:04:05", combined); err == nil {
		return ts.Format(time.RFC3339)
	}
	if strings.EqualFold(date, tm) {
		return date // e.g. "Pre-Init" duplicated across both columns
	}
	return combined
}

// sensorType reduces a sensor label like "Temperature #0x30" to its type
// ("Temperature") for the histogram.
func sensorType(sensor string) string {
	if i := strings.LastIndex(sensor, " #"); i > 0 {
		return sensor[:i]
	}
	return sensor
}

// parseSELInfo tolerantly parses `ipmitool sel info` key/value lines. Labels
// vary slightly across BMC firmwares, so keys are matched by substring.
// Returns nil when no recognizable field is present.
func parseSELInfo(out []byte) *SELInfo {
	info := &SELInfo{}
	found := false
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(k))
		val := strings.TrimSpace(v)
		switch {
		case strings.Contains(key, "entries"):
			if n := firstInt(val); n != nil {
				info.Entries = n
				found = true
			}
		case strings.Contains(key, "free space"):
			if n := firstInt(val); n != nil {
				info.FreeSpaceBytes = n
				found = true
			}
		case strings.Contains(key, "percent"):
			if n := firstInt(val); n != nil {
				info.PercentUsed = n
				found = true
			}
		}
	}
	if !found {
		return nil
	}
	return info
}

// firstInt extracts the first unsigned integer from a string such as
// "8144 bytes" or "20%". Returns nil when none is present.
func firstInt(s string) *int64 {
	m := intRe.FindString(s)
	if m == "" {
		return nil
	}
	v, err := strconv.ParseInt(m, 10, 64)
	if err != nil {
		return nil
	}
	return &v
}

// analyzeRecords builds the sensor-type histogram over the full SEL and
// flags Critical / Non-recoverable events. The matching is case-sensitive on
// purpose: IPMI prints warning-level thresholds as "Non-critical", which
// must not trip the Critical heuristic. The itemized list is capped at
// maxCriticalWarnings with an aggregate overflow entry.
func analyzeRecords(records []Record) (map[string]int, []string) {
	counts := make(map[string]int, 8)
	warnings := []string{}
	critical := 0
	for _, r := range records {
		counts[sensorType(r.Sensor)]++
		if strings.Contains(r.Event, "Critical") || strings.Contains(r.Event, "Non-recoverable") {
			critical++
			if critical <= maxCriticalWarnings {
				warnings = append(warnings, fmt.Sprintf("SEL record %s: %s: %s", r.ID, r.Sensor, r.Event))
			}
		}
	}
	if critical > maxCriticalWarnings {
		warnings = append(warnings, fmt.Sprintf("%d additional Critical/Non-recoverable SEL record(s) not itemized", critical-maxCriticalWarnings))
	}
	return counts, warnings
}

func init() {
	registry.Register(New())
}
