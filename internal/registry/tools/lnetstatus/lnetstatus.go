// Package lnetstatus implements the get_lnet_status diagnostic tool.
//
// It reports the health of the LNet fabric layer used by Lustre: local
// network interfaces (NIs) with status and credit levels, peer count, and
// global message counters. Native debugfs files are the primary source;
// lnetctl command output is the fallback when debugfs is unreadable.
package lnetstatus

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// Package-level injection points. Tests and benchmarks override the copies
// held by each Tool instance; production code uses these defaults.
var (
	execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	}
	execLookPath = exec.LookPath
	debugfsRoot  = "/sys/kernel/debug"
	sysfsRoot    = "/sys"
)

// maxListedNids caps the number of NIDs enumerated inside a single
// warning reason so LLM-facing output stays compact.
const maxListedNids = 5

// NI describes one local LNet network interface row.
// Credit fields are pointers because they are only available from debugfs;
// the lnetctl fallback cannot report them and omits them instead of
// emitting misleading zeros.
type NI struct {
	NID        string `json:"nid"`
	Status     string `json:"status"`
	Refs       int64  `json:"refs,omitempty"`
	MaxCredits *int64 `json:"max_credits,omitempty"`
	TxCredits  *int64 `json:"tx_credits,omitempty"`
	MinCredits *int64 `json:"min_credits,omitempty"`
}

// Stats holds the global LNet message counters, decoded from the
// positional /sys/kernel/debug/lnet/stats line or lnetctl key/value output.
type Stats struct {
	MsgsAlloc   uint64 `json:"msgs_alloc"`
	MsgsMax     uint64 `json:"msgs_max"`
	Errors      uint64 `json:"errors"`
	SendCount   uint64 `json:"send_count"`
	RecvCount   uint64 `json:"recv_count"`
	RouteCount  uint64 `json:"route_count"`
	DropCount   uint64 `json:"drop_count"`
	SendLength  uint64 `json:"send_length_bytes,omitempty"`
	RecvLength  uint64 `json:"recv_length_bytes,omitempty"`
	RouteLength uint64 `json:"route_length_bytes,omitempty"`
	DropLength  uint64 `json:"drop_length_bytes,omitempty"`
}

// Output is the tool-specific data payload.
type Output struct {
	Source         string   `json:"source"`
	NIs            []NI     `json:"nis"`
	NICount        int      `json:"ni_count"`
	NIsDown        int      `json:"nis_down"`
	PeerCount      int      `json:"peer_count"`
	PeersAvailable bool     `json:"peers_available"`
	Stats          *Stats   `json:"stats,omitempty"`
	StatsAvailable bool     `json:"stats_available"`
	WarningReasons []string `json:"warning_reasons,omitempty"`
}

// Tool implements registry.Tool for get_lnet_status.
type Tool struct {
	debugfsRoot  string
	sysfsRoot    string
	execCommand  func(ctx context.Context, name string, args ...string) ([]byte, error)
	execLookPath func(file string) (string, error)
}

// New constructs the tool with the package-level defaults.
func New() *Tool {
	return &Tool{
		debugfsRoot:  debugfsRoot,
		sysfsRoot:    sysfsRoot,
		execCommand:  execCommand,
		execLookPath: execLookPath,
	}
}

func init() {
	registry.Register(New())
}

func (t *Tool) Name() string {
	return "get_lnet_status"
}

func (t *Tool) Description() string {
	return "Report LNet fabric health: NI up/down status, credit levels, peer count, and message/drop counters."
}

func (t *Tool) Help() string {
	return `Reports the health of the LNet fabric layer used by Lustre: local network
interfaces (NIs) with up/down status and tx credit levels, known peer count,
and global message counters (send/recv/route/drop/errors).

Deterministic analysis: flags NIs that are not up, negative minimum tx
credits (credit starvation on a congested fabric), and non-zero drop counts.

Data Sources:
- /sys/kernel/debug/lnet/nis (NI status, refs, max/tx/min credits; debugfs typically requires root)
- /sys/kernel/debug/lnet/peers (peer count)
- /sys/kernel/debug/lnet/stats (positional message counters)
- Fallback: 'lnetctl net show' and 'lnetctl stats show' command output (simple key/value parsing)

Output: nis[] {nid, status, refs, max/tx/min credits}, ni_count, nis_down,
peer_count, stats {msgs_alloc, msgs_max, errors, send_count, recv_count,
route_count, drop_count, *_length_bytes}, warning_reasons[], and 'source'
identifying which data path was used.

Caveats: credit levels and peer count are only available from debugfs; the
lnetctl fallback omits them. Multi-CPT NIs may appear once per CPT row.`
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
	if registry.PathExists(filepath.Join(t.sysfsRoot, "module", "lnet")) {
		return true, ""
	}
	if _, err := t.execLookPath("lnetctl"); err == nil {
		return true, ""
	}
	return false, "LNet not loaded and lnetctl not found"
}

func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	if err := ctx.Err(); err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("execution cancelled: %v", err)), nil
	}

	var out Output

	nisPath := filepath.Join(t.debugfsRoot, "lnet", "nis")
	nisData, nisErr := os.ReadFile(nisPath)
	if nisErr == nil {
		out.Source = "debugfs"
		out.NIs = parseNIS(nisData)

		if peerData, err := os.ReadFile(filepath.Join(t.debugfsRoot, "lnet", "peers")); err == nil {
			out.PeerCount = parsePeerCount(peerData)
			out.PeersAvailable = true
		}
		if statsData, err := os.ReadFile(filepath.Join(t.debugfsRoot, "lnet", "stats")); err == nil {
			if st, perr := parseStats(statsData); perr == nil {
				out.Stats = st
				out.StatsAvailable = true
			}
		}
	} else {
		// Degradation path: debugfs unreadable (usually non-root) → lnetctl.
		if _, lookErr := t.execLookPath("lnetctl"); lookErr != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf(
				"cannot read %s (%v) and lnetctl is not installed; reading LNet debugfs requires root — run as root or install lnetctl",
				nisPath, nisErr)), nil
		}

		netOut, cmdErr := t.runLnetctl(ctx, "net", "show")
		if cmdErr != nil {
			if isPermissionError(netOut, cmdErr) {
				return registry.NewErrorResult(t.Name(),
					"Unauthorized: root or passwordless sudo privileges required to query LNet state (debugfs and lnetctl both denied)."), nil
			}
			return registry.NewErrorResult(t.Name(), fmt.Sprintf(
				"debugfs unreadable (%v) and 'lnetctl net show' failed: %v", nisErr, cmdErr)), nil
		}
		out.Source = "lnetctl"
		out.NIs = parseLnetctlNet(netOut)

		if statsOut, statsErr := t.runLnetctl(ctx, "stats", "show"); statsErr == nil {
			if st, perr := parseLnetctlStats(statsOut); perr == nil {
				out.Stats = st
				out.StatsAvailable = true
			}
		}
	}

	out.NICount = len(out.NIs)
	for _, ni := range out.NIs {
		if ni.Status != "up" {
			out.NIsDown++
		}
	}
	out.WarningReasons = collectWarnings(out.NIs, out.Stats)

	status := registry.StatusOK
	if len(out.WarningReasons) > 0 {
		status = registry.StatusWarning
	}

	peerPart := "peers unavailable"
	if out.PeersAvailable {
		peerPart = fmt.Sprintf("%d peer(s)", out.PeerCount)
	}
	statsPart := "stats unavailable"
	if out.Stats != nil {
		statsPart = fmt.Sprintf("send=%d recv=%d drops=%d errors=%d",
			out.Stats.SendCount, out.Stats.RecvCount, out.Stats.DropCount, out.Stats.Errors)
	}
	summary := fmt.Sprintf("LNet [%s]: %d NI(s), %d down, %s, %s",
		out.Source, out.NICount, out.NIsDown, peerPart, statsPart)

	res := registry.NewResult(t.Name(), status, summary, out)
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	return res, nil
}

// runLnetctl executes an lnetctl subcommand, wrapping with 'sudo -n' when
// not running as root and passwordless sudo is available.
func (t *Tool) runLnetctl(ctx context.Context, args ...string) ([]byte, error) {
	if os.Geteuid() != 0 {
		if _, err := t.execLookPath("sudo"); err == nil {
			return t.execCommand(ctx, "sudo", append([]string{"-n", "lnetctl"}, args...)...)
		}
	}
	return t.execCommand(ctx, "lnetctl", args...)
}

// parseNIS parses /sys/kernel/debug/lnet/nis rows:
//
//	nid status alive refs peer rtr max tx min
//
// The header line and malformed rows are skipped.
func parseNIS(data []byte) []NI {
	var nis []NI
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 9 || fields[0] == "nid" {
			continue
		}
		refs, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil {
			continue
		}
		ni := NI{
			NID:    fields[0],
			Status: strings.ToLower(fields[1]),
			Refs:   refs,
		}
		if maxC, err := strconv.ParseInt(fields[6], 10, 64); err == nil {
			ni.MaxCredits = &maxC
		}
		if txC, err := strconv.ParseInt(fields[7], 10, 64); err == nil {
			ni.TxCredits = &txC
		}
		if minC, err := strconv.ParseInt(fields[8], 10, 64); err == nil {
			ni.MinCredits = &minC
		}
		nis = append(nis, ni)
	}
	return nis
}

// parsePeerCount counts data rows in /sys/kernel/debug/lnet/peers.
func parsePeerCount(data []byte) int {
	count := 0
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] == "nid" {
			continue
		}
		count++
	}
	return count
}

// parseStats decodes the single positional counter line of
// /sys/kernel/debug/lnet/stats:
//
//	msgs_alloc msgs_max errors send_count recv_count route_count drop_count
//	send_length recv_length route_length drop_length
func parseStats(data []byte) (*Stats, error) {
	fields := strings.Fields(strings.TrimSpace(string(data)))
	if len(fields) < 7 {
		return nil, fmt.Errorf("lnet stats: expected at least 7 counters, got %d", len(fields))
	}
	vals := make([]uint64, len(fields))
	for i, f := range fields {
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("lnet stats: field %d %q is not a counter: %w", i, f, err)
		}
		vals[i] = v
	}
	st := &Stats{
		MsgsAlloc:  vals[0],
		MsgsMax:    vals[1],
		Errors:     vals[2],
		SendCount:  vals[3],
		RecvCount:  vals[4],
		RouteCount: vals[5],
		DropCount:  vals[6],
	}
	if len(vals) >= 11 {
		st.SendLength = vals[7]
		st.RecvLength = vals[8]
		st.RouteLength = vals[9]
		st.DropLength = vals[10]
	}
	return st, nil
}

// parseLnetctlNet extracts NIs from 'lnetctl net show' YAML-ish output by
// simple "key: value" scanning (no YAML library).
func parseLnetctlNet(data []byte) []NI {
	var nis []NI
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimPrefix(strings.TrimSpace(raw), "- ")
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "nid":
			if val != "" {
				nis = append(nis, NI{NID: val, Status: "unknown"})
			}
		case "status":
			if len(nis) > 0 && nis[len(nis)-1].Status == "unknown" {
				nis[len(nis)-1].Status = strings.ToLower(val)
			}
		}
	}
	return nis
}

// parseLnetctlStats extracts counters from 'lnetctl stats show' output by
// simple "key: value" scanning.
func parseLnetctlStats(data []byte) (*Stats, error) {
	values := make(map[string]uint64)
	for _, raw := range strings.Split(string(data), "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(raw), ":")
		if !ok {
			continue
		}
		v, err := strconv.ParseUint(strings.TrimSpace(val), 10, 64)
		if err != nil {
			continue
		}
		values[strings.TrimSpace(key)] = v
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("lnetctl stats: no counters found in output")
	}
	return &Stats{
		MsgsAlloc:   values["msgs_alloc"],
		MsgsMax:     values["msgs_max"],
		Errors:      values["errors"],
		SendCount:   values["send_count"],
		RecvCount:   values["recv_count"],
		RouteCount:  values["route_count"],
		DropCount:   values["drop_count"],
		SendLength:  values["send_length"],
		RecvLength:  values["recv_length"],
		RouteLength: values["route_length"],
		DropLength:  values["drop_length"],
	}, nil
}

// collectWarnings derives warning_reasons from parsed state:
// any NI not up, negative minimum tx credits (credit starvation), and
// non-zero drop counts.
func collectWarnings(nis []NI, stats *Stats) []string {
	var reasons []string

	var down []string
	var starved []string
	for _, ni := range nis {
		if ni.Status != "up" {
			down = append(down, ni.NID)
		}
		if ni.MinCredits != nil && *ni.MinCredits < 0 {
			starved = append(starved, fmt.Sprintf("%s (min=%d)", ni.NID, *ni.MinCredits))
		}
	}
	if n := len(down); n > 0 {
		if n > maxListedNids {
			down = append(down[:maxListedNids:maxListedNids], "...")
		}
		reasons = append(reasons, fmt.Sprintf("%d LNet NI(s) not up: %s", n, strings.Join(down, ", ")))
	}
	if n := len(starved); n > 0 {
		if n > maxListedNids {
			starved = append(starved[:maxListedNids:maxListedNids], "...")
		}
		reasons = append(reasons, fmt.Sprintf("credit starvation (negative min tx credits) on %d NI(s): %s", n, strings.Join(starved, ", ")))
	}
	if stats != nil && stats.DropCount > 0 {
		reasons = append(reasons, fmt.Sprintf("lnet drop_count=%d (messages dropped)", stats.DropCount))
	}
	return reasons
}

// isPermissionError classifies command failures caused by missing
// root / passwordless-sudo privileges.
func isPermissionError(out []byte, err error) bool {
	combined := strings.ToLower(string(out))
	if err != nil {
		combined += " " + strings.ToLower(err.Error())
	}
	return strings.Contains(combined, "permission denied") ||
		strings.Contains(combined, "operation not permitted") ||
		strings.Contains(combined, "password is required")
}
