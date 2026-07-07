// Package timesync implements the get_time_sync_status tool.
//
// It reads the kernel clock discipline state natively via the adjtimex(2)
// syscall (read-only, modes=0), inspects the active clocksource through
// sysfs, and enriches the picture with NTP daemon state from chronyc or
// timedatectl when those binaries are available.
package timesync

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

const (
	// staUnsync is the STA_UNSYNC bit of the adjtimex status word: set when
	// the kernel considers the clock unsynchronized.
	staUnsync = 0x0040

	// staNano is the STA_NANO bit: when set, the adjtimex offset field is in
	// nanoseconds instead of microseconds.
	staNano = 0x2000

	// offsetWarnMs is the absolute clock offset (in milliseconds) above
	// which a warning is raised.
	offsetWarnMs = 100.0
)

// adjtimexSyscall is the native read-only adjtimex(2) wrapper. It is the
// default value for Tool.adjtimex and is replaced in tests.
var adjtimexSyscall = func(tx *syscall.Timex) (int, error) {
	return syscall.Adjtimex(tx)
}

// Clocksource describes the kernel clocksource selection.
type Clocksource struct {
	Current   string   `json:"current"`
	Available []string `json:"available,omitempty"`
}

// NTPDaemon describes the userspace time daemon state, when one is found.
type NTPDaemon struct {
	// Name is "chrony", "systemd-timesyncd", or "unknown".
	Name string `json:"name"`

	// Stratum is the NTP stratum reported by chrony (0 when unknown).
	Stratum int `json:"stratum,omitempty"`

	// RefSource is the reference clock name or address (chrony only).
	RefSource string `json:"ref_source,omitempty"`

	// OffsetMs is the daemon-reported system time offset in milliseconds
	// (positive = local clock ahead of NTP time). Chrony only.
	OffsetMs *float64 `json:"offset_ms,omitempty"`
}

// Output is the tool's data payload.
type Output struct {
	// Synchronized is true when the kernel STA_UNSYNC bit is clear, or —
	// when kernel status is unavailable — when the NTP daemon reports sync.
	Synchronized bool `json:"synchronized"`

	// KernelStatusAvailable is false when adjtimex(2) was denied (common in
	// unprivileged containers); daemon data is then the only source.
	KernelStatusAvailable bool `json:"kernel_status_available"`

	// OffsetMs is the kernel clock offset in milliseconds (2 decimal places,
	// sign preserved; STA_NANO nanosecond/microsecond units are normalized).
	OffsetMs *float64 `json:"offset_ms,omitempty"`

	// EstErrorUs and MaxErrorUs are the kernel's estimated and maximum clock
	// error bounds in microseconds.
	EstErrorUs int64 `json:"est_error_us,omitempty"`
	MaxErrorUs int64 `json:"max_error_us,omitempty"`

	Clocksource *Clocksource `json:"clocksource,omitempty"`
	NTPDaemon   *NTPDaemon   `json:"ntp_daemon,omitempty"`

	// WarningReasons lists pre-evaluated problems in plain words.
	WarningReasons []string `json:"warning_reasons"`
}

// Tool implements registry.Tool for get_time_sync_status.
// All external dependencies are injectable fields for hermetic tests.
type Tool struct {
	sysfsRoot    string
	goarch       string
	adjtimex     func(*syscall.Timex) (int, error)
	execCommand  func(ctx context.Context, name string, args ...string) ([]byte, error)
	execLookPath func(file string) (string, error)
}

// New returns a time sync tool bound to the real host.
func New() *Tool {
	return &Tool{
		sysfsRoot: "/sys",
		goarch:    runtime.GOARCH,
		adjtimex:  adjtimexSyscall,
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		},
		execLookPath: exec.LookPath,
	}
}

func init() {
	registry.Register(New())
}

// Name returns the unique tool identifier.
func (t *Tool) Name() string { return "get_time_sync_status" }

// Description returns the one-line summary for tool discovery.
func (t *Tool) Description() string {
	return "Audit system clock synchronization: kernel adjtimex state, clocksource, and NTP daemon health"
}

// Help returns the full tool documentation.
func (t *Tool) Help() string {
	return `get_time_sync_status — System Clock Synchronization Audit

Determines whether the system clock is disciplined and by how much it drifts.
Clock skew silently breaks TLS, Kerberos, distributed locks, log correlation,
and parallel filesystem lease logic, so this is a first-line diagnostic.

Data Sources (native first):
  - adjtimex(2) syscall, read-only (modes=0): synchronization flag
    (STA_UNSYNC), clock offset (nanoseconds when STA_NANO is set, otherwise
    microseconds — normalized to milliseconds), estimated/maximum error.
  - /sys/devices/system/clocksource/clocksource0/current_clocksource and
    available_clocksource.
  - Enrichment (binaries, optional): "chronyc -c tracking" (stratum,
    reference source, daemon offset, leap status) or "timedatectl show"
    (NTP=/NTPSynchronized= flags, attributed to systemd-timesyncd).

Warning Heuristics:
  - Clock not synchronized (kernel STA_UNSYNC set, or daemon says so).
  - Absolute clock offset > 100 ms.
  - Current clocksource is not "tsc" on amd64 hosts. Caveat: on virtual
    machines a paravirtual clocksource (kvm-clock, hyperv_clocksource) is
    normal and this warning can be ignored there.

Degradation Profile:
  - adjtimex denied (EPERM, common in unprivileged containers): falls back to
    daemon queries only and sets "kernel_status_available": false.
  - No chronyc/timedatectl binaries: kernel + clocksource data only, the
    ntp_daemon object is omitted.
  - Missing clocksource sysfs files: clocksource object is omitted.

Parameters: None
Supported on: Linux`
}

// Category classifies this tool under system.
func (t *Tool) Category() registry.Category { return registry.CategorySystem }

// Parameters returns nil — this tool takes no arguments.
func (t *Tool) Parameters() []registry.ToolParam { return nil }

// Hidden returns false — this tool is LLM-discoverable.
func (t *Tool) Hidden() bool { return false }

// IsSupported requires Linux; every data source degrades gracefully at
// execution time.
func (t *Tool) IsSupported() (bool, string) {
	if !registry.IsLinux() {
		return false, "requires Linux"
	}
	return true, ""
}

// Execute gathers kernel, clocksource, and daemon state.
func (t *Tool) Execute(ctx context.Context, _ json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	if err := ctx.Err(); err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("context canceled: %v", err)), nil
	}

	out := Output{WarningReasons: []string{}}

	t.collectKernel(&out)
	out.Clocksource = t.readClocksource()
	daemon, daemonSyncHint := t.collectDaemon(ctx)
	out.NTPDaemon = daemon

	// When adjtimex is unavailable, the daemon report is the only sync signal.
	if !out.KernelStatusAvailable {
		out.Synchronized = daemonSyncHint == "yes"
	}

	if !out.Synchronized {
		out.WarningReasons = append(out.WarningReasons, "system clock is not synchronized")
	}
	offset := out.OffsetMs
	if offset == nil && daemon != nil {
		offset = daemon.OffsetMs
	}
	if offset != nil && math.Abs(*offset) > offsetWarnMs {
		out.WarningReasons = append(out.WarningReasons, fmt.Sprintf("clock offset %.2f ms exceeds %.0f ms", *offset, offsetWarnMs))
	}
	if t.goarch == "amd64" && out.Clocksource != nil && out.Clocksource.Current != "tsc" {
		out.WarningReasons = append(out.WarningReasons, fmt.Sprintf(
			"clocksource is %q, not tsc, on amd64 — slower or less stable timekeeping (expected on some hypervisors)", out.Clocksource.Current))
	}

	status := registry.StatusOK
	syncWord := "synchronized"
	if !out.Synchronized {
		syncWord = "NOT synchronized"
	}
	summary := fmt.Sprintf("Clock %s", syncWord)
	if out.OffsetMs != nil {
		summary += fmt.Sprintf(", kernel offset %.2f ms", *out.OffsetMs)
	}
	if daemon != nil {
		summary += fmt.Sprintf(", daemon %s", daemon.Name)
	}
	if len(out.WarningReasons) > 0 {
		status = registry.StatusWarning
		summary += fmt.Sprintf(" — %d warnings: %s", len(out.WarningReasons), strings.Join(out.WarningReasons, "; "))
	}

	result := registry.NewResult(t.Name(), status, summary, out)
	result.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	result.Metadata.FilteringMethod = "heuristic"
	return result, nil
}

// collectKernel queries adjtimex(2) read-only and decodes the result into
// out. On failure (e.g. EPERM in containers) it marks kernel status as
// unavailable and leaves the offset fields empty.
func (t *Tool) collectKernel(out *Output) {
	var tx syscall.Timex // Modes = 0 => read-only query
	if _, err := t.adjtimex(&tx); err != nil {
		out.KernelStatusAvailable = false
		return
	}
	out.KernelStatusAvailable = true
	out.Synchronized = tx.Status&staUnsync == 0

	// STA_NANO selects nanosecond offset units; microseconds otherwise.
	var offsetMs float64
	if tx.Status&staNano != 0 {
		offsetMs = float64(tx.Offset) / 1e6
	} else {
		offsetMs = float64(tx.Offset) / 1e3
	}
	rounded := round2(offsetMs)
	out.OffsetMs = &rounded
	out.EstErrorUs = int64(tx.Esterror)
	out.MaxErrorUs = int64(tx.Maxerror)
}

// readClocksource reads the current and available kernel clocksources from
// sysfs. Returns nil when the current clocksource cannot be read.
func (t *Tool) readClocksource() *Clocksource {
	dir := filepath.Join(t.sysfsRoot, "devices", "system", "clocksource", "clocksource0")
	current, err := os.ReadFile(filepath.Join(dir, "current_clocksource"))
	if err != nil {
		return nil
	}
	cs := &Clocksource{Current: strings.TrimSpace(string(current))}
	if available, err := os.ReadFile(filepath.Join(dir, "available_clocksource")); err == nil {
		cs.Available = strings.Fields(string(available))
	}
	return cs
}

// collectDaemon queries chronyc first, then timedatectl, returning daemon
// details and a sync hint ("yes", "no", or "" when undetermined).
func (t *Tool) collectDaemon(ctx context.Context) (*NTPDaemon, string) {
	if _, err := t.execLookPath("chronyc"); err == nil {
		if raw, err := t.execCommand(ctx, "chronyc", "-c", "tracking"); err == nil {
			if daemon, leap, err := parseChronyTracking(raw); err == nil {
				hint := ""
				switch {
				case strings.EqualFold(leap, "Not synchronised") || strings.EqualFold(leap, "Not synchronized"):
					hint = "no"
				case leap != "":
					hint = "yes" // Normal / Insert second / Delete second
				}
				return daemon, hint
			}
		}
	}

	if _, err := t.execLookPath("timedatectl"); err == nil {
		if raw, err := t.execCommand(ctx, "timedatectl", "show"); err == nil {
			props := parseTimedatectlShow(raw)
			hint := ""
			switch props["NTPSynchronized"] {
			case "yes":
				hint = "yes"
			case "no":
				hint = "no"
			}
			if props["NTP"] == "yes" {
				return &NTPDaemon{Name: "systemd-timesyncd"}, hint
			}
			return nil, hint
		}
	}

	return nil, ""
}

// parseChronyTracking decodes "chronyc -c tracking" CSV output. Fields:
// [0] reference ID (hex), [1] reference name/address, [2] stratum,
// [3] ref time, [4] system time offset in seconds (positive = local clock
// ahead), ..., [13] leap status.
func parseChronyTracking(raw []byte) (*NTPDaemon, string, error) {
	line := strings.TrimSpace(string(raw))
	if line == "" {
		return nil, "", fmt.Errorf("empty chronyc tracking output")
	}
	fields := strings.Split(line, ",")
	if len(fields) < 5 {
		return nil, "", fmt.Errorf("malformed chronyc tracking output: want >= 5 CSV fields, got %d", len(fields))
	}

	stratum, err := strconv.Atoi(strings.TrimSpace(fields[2]))
	if err != nil {
		return nil, "", fmt.Errorf("malformed chrony stratum %q: %w", fields[2], err)
	}
	offsetSec, err := strconv.ParseFloat(strings.TrimSpace(fields[4]), 64)
	if err != nil {
		return nil, "", fmt.Errorf("malformed chrony system time offset %q: %w", fields[4], err)
	}

	refSource := strings.TrimSpace(fields[1])
	if refSource == "" {
		refSource = strings.TrimSpace(fields[0])
	}

	offsetMs := round2(offsetSec * 1000)
	daemon := &NTPDaemon{
		Name:      "chrony",
		Stratum:   stratum,
		RefSource: refSource,
		OffsetMs:  &offsetMs,
	}

	leap := ""
	if len(fields) >= 14 {
		leap = strings.TrimSpace(fields[13])
	}
	return daemon, leap, nil
}

// parseTimedatectlShow decodes "timedatectl show" KEY=VALUE output.
func parseTimedatectlShow(raw []byte) map[string]string {
	props := make(map[string]string)
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		props[parts[0]] = parts[1]
	}
	return props
}

// round2 rounds to two decimal places, preserving sign.
func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
