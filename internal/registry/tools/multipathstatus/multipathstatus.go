// Package multipathstatus implements the get_multipath_status diagnostic tool.
//
// It audits device-mapper multipath maps natively from sysfs: dm devices
// whose dm/uuid carries the "mpath-" prefix are multipath maps, their
// slaves/ entries are the individual paths, and each path's SCSI state
// comes from /sys/block/<slave>/device/state. The multipathd daemon is
// only consulted (optionally) to enrich maps with the dm suspend state —
// daemon/socket failures never affect the native result.
package multipathstatus

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// Path is a single physical path (slave device) of a multipath map.
type Path struct {
	Dev   string `json:"dev"`
	State string `json:"state"`
}

// Map is the health snapshot of one dm-multipath map.
type Map struct {
	Name           string   `json:"name"`
	UUID           string   `json:"uuid"`
	DMState        string   `json:"dm_state,omitempty"`
	TotalPaths     int      `json:"total_paths"`
	ActivePaths    int      `json:"active_paths"`
	FailedPaths    int      `json:"failed_paths"`
	Paths          []Path   `json:"paths"`
	WarningReasons []string `json:"warning_reasons"`
}

// Summary aggregates counts across all multipath maps.
type Summary struct {
	TotalMaps             int `json:"total_maps"`
	MapsWithFailedPaths   int `json:"maps_with_failed_paths"`
	MapsWithNoActivePaths int `json:"maps_with_no_active_paths"`
}

// Output is the tool's data payload.
type Output struct {
	Maps    []Map   `json:"maps"`
	Summary Summary `json:"summary"`
}

// Tool implements registry.Tool for dm-multipath path health auditing.
type Tool struct {
	sysfsRoot   string
	execCommand func(ctx context.Context, name string, args ...string) ([]byte, error)
	lookPath    func(file string) (string, error)
}

// New returns a Tool wired to the real sysfs root and executables.
func New() *Tool {
	return &Tool{
		sysfsRoot: "/sys",
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		},
		lookPath: exec.LookPath,
	}
}

func init() {
	registry.Register(New())
}

// Name returns the unique tool identifier.
func (t *Tool) Name() string {
	return "get_multipath_status"
}

// Description returns a one-line summary for get_tool_list output.
func (t *Tool) Description() string {
	return "Audit dm-multipath maps for failed or offline SAN paths and lost path redundancy."
}

// Help returns the full tool help text.
func (t *Tool) Help() string {
	return `Audits device-mapper multipath (dm-multipath) maps and their individual SAN paths.

Multipath maps are discovered natively by scanning /sys/block/dm-*/dm/uuid for the "mpath-" prefix; each map's paths come from its slaves/ directory, and per-path SCSI state (running/offline) from /sys/block/<slave>/device/state. Flags any non-running path and treats a map with zero active paths as critical (all I/O to that LUN fails).

Data Sources:
- /sys/block/dm-*/dm/{uuid,name} (native map discovery)
- /sys/block/dm-*/slaves/ (path membership)
- /sys/block/<slave>/device/state (per-path SCSI state)
- multipathd show maps raw format (optional enrichment: dm suspend state; daemon errors are ignored)`
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

// IsSupported reports whether multipath is in use or installed on this node.
func (t *Tool) IsSupported() (bool, string) {
	if _, err := t.lookPath("multipath"); err == nil {
		return true, ""
	}
	if _, err := t.lookPath("multipathd"); err == nil {
		return true, ""
	}
	if hasMpathDevices(t.sysfsRoot) {
		return true, ""
	}
	return false, "no multipath devices and multipathd not installed"
}

// Execute scans sysfs for multipath maps and returns their path health.
func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	if err := ctx.Err(); err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("execution cancelled: %v", err)), nil
	}

	maps := []Map{}
	blockDir := filepath.Join(t.sysfsRoot, "block")
	entries, err := os.ReadDir(blockDir)
	if err != nil {
		// Missing sysfs block tree degrades to zero maps.
		entries = nil
	}

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("execution cancelled: %v", err)), nil
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "dm-") {
			continue
		}
		uuid := readFileTrim(filepath.Join(blockDir, name, "dm", "uuid"))
		if !strings.HasPrefix(uuid, "mpath-") {
			continue
		}
		maps = append(maps, collectMap(t.sysfsRoot, name))
	}

	sort.Slice(maps, func(i, j int) bool { return maps[i].Name < maps[j].Name })

	// Optional enrichment via multipathd; native sysfs stays the source of
	// truth, so any daemon/socket error is ignored.
	if len(maps) > 0 {
		if dmStates := t.multipathdDMStates(ctx); len(dmStates) > 0 {
			for i := range maps {
				maps[i].DMState = dmStates[maps[i].Name]
			}
		}
	}

	out := Output{Maps: maps}
	out.Summary.TotalMaps = len(maps)
	warnings := 0
	for _, m := range maps {
		if m.FailedPaths > 0 {
			out.Summary.MapsWithFailedPaths++
		}
		if m.ActivePaths == 0 && m.TotalPaths > 0 {
			out.Summary.MapsWithNoActivePaths++
		}
		warnings += len(m.WarningReasons)
	}

	status := registry.StatusOK
	summary := fmt.Sprintf("Multipath: %d maps, %d with failed paths, %d with no active paths",
		out.Summary.TotalMaps, out.Summary.MapsWithFailedPaths, out.Summary.MapsWithNoActivePaths)
	switch {
	case out.Summary.MapsWithNoActivePaths > 0:
		status = registry.StatusError
	case warnings > 0:
		status = registry.StatusWarning
	case out.Summary.TotalMaps == 0:
		summary = "Multipath: no multipath maps present"
	}

	res := registry.NewResult(t.Name(), status, summary, out)
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	res.Metadata.FilteringMethod = "deterministic"
	return res, nil
}

// collectMap builds the health snapshot of one dm multipath device.
func collectMap(sysfsRoot, dmName string) Map {
	dmDir := filepath.Join(sysfsRoot, "block", dmName)

	m := Map{
		Name:           readFileTrim(filepath.Join(dmDir, "dm", "name")),
		UUID:           readFileTrim(filepath.Join(dmDir, "dm", "uuid")),
		Paths:          []Path{},
		WarningReasons: []string{},
	}
	if m.Name == "" {
		m.Name = dmName
	}

	slaves, err := os.ReadDir(filepath.Join(dmDir, "slaves"))
	if err != nil {
		slaves = nil
	}
	for _, slave := range slaves {
		dev := slave.Name()
		state := readFileTrim(filepath.Join(sysfsRoot, "block", dev, "device", "state"))
		if state == "" {
			state = "unknown"
		}
		m.Paths = append(m.Paths, Path{Dev: dev, State: state})
	}
	sort.Slice(m.Paths, func(i, j int) bool { return m.Paths[i].Dev < m.Paths[j].Dev })

	m.TotalPaths = len(m.Paths)
	for _, p := range m.Paths {
		if p.State == "running" {
			m.ActivePaths++
		} else {
			m.WarningReasons = append(m.WarningReasons,
				fmt.Sprintf("path %s state=%s (not running)", p.Dev, p.State))
		}
	}
	m.FailedPaths = m.TotalPaths - m.ActivePaths

	if m.ActivePaths == 0 && m.TotalPaths > 0 {
		m.WarningReasons = append(m.WarningReasons,
			fmt.Sprintf("CRITICAL: no active paths remaining on %s — all I/O to this LUN will fail", m.Name))
	}

	return m
}

// hasMpathDevices reports whether any dm device in sysfs is a multipath map.
func hasMpathDevices(sysfsRoot string) bool {
	blockDir := filepath.Join(sysfsRoot, "block")
	entries, err := os.ReadDir(blockDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "dm-") {
			continue
		}
		uuid := readFileTrim(filepath.Join(blockDir, name, "dm", "uuid"))
		if strings.HasPrefix(uuid, "mpath-") {
			return true
		}
	}
	return false
}

// multipathdDMStates queries multipathd for per-map dm states. Returns nil
// when multipathd is absent or unreachable — enrichment is best-effort.
func (t *Tool) multipathdDMStates(ctx context.Context) map[string]string {
	if _, err := t.lookPath("multipathd"); err != nil {
		return nil
	}
	out, err := t.execCommand(ctx, "multipathd", "show", "maps", "raw", "format", "%n %w %N %t")
	if err != nil {
		return nil
	}
	return parseMultipathdMaps(out)
}

// parseMultipathdMaps parses `multipathd show maps raw format "%n %w %N %t"`
// output (name wwid path-count dm-state per line) into a name→dm-state map.
func parseMultipathdMaps(out []byte) map[string]string {
	states := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		states[fields[0]] = fields[3]
	}
	return states
}

// readFileTrim reads a file and returns its trimmed content, or the empty
// string if the file is missing/unreadable.
func readFileTrim(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
