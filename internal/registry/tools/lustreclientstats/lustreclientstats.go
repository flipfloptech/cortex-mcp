package lustreclientstats

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

type StatEntry struct {
	Samples uint64  `json:"samples"`
	Unit    string  `json:"unit"`
	Min     *uint64 `json:"min,omitempty"`
	Max     *uint64 `json:"max,omitempty"`
	Sum     *uint64 `json:"sum,omitempty"`
}

type ReadAheadStats struct {
	Hits   uint64            `json:"hits"`
	Misses uint64            `json:"misses"`
	Other  map[string]uint64 `json:"other,omitempty"`
}

type TargetImport struct {
	Name         string `json:"name"`
	Target       string `json:"target"`
	State        string `json:"state"`
	ConnectCount int    `json:"connect_count"`
	Inflight     int    `json:"inflight"`
}

type FilesystemStats struct {
	Name      string               `json:"name"`
	Stats     map[string]StatEntry `json:"stats,omitempty"`
	ReadAhead ReadAheadStats       `json:"read_ahead,omitempty"`
}

type ConnectionsStats struct {
	MDT []TargetImport `json:"mdt,omitempty"`
	OST []TargetImport `json:"ost,omitempty"`
}

type LustreClientStatsSummary struct {
	TotalFilesystems      int `json:"total_filesystems"`
	ActiveMDTConnections  int `json:"active_mdt_connections"`
	TotalMDTConnections   int `json:"total_mdt_connections"`
	ActiveOSTConnections  int `json:"active_ost_connections"`
	TotalOSTConnections   int `json:"total_ost_connections"`
}

type LustreClientStatsData struct {
	LustreVersion string                    `json:"lustre_version,omitempty"`
	Filesystems   []FilesystemStats         `json:"filesystems,omitempty"`
	Connections   ConnectionsStats          `json:"connections,omitempty"`
	Summary       LustreClientStatsSummary  `json:"summary"`
}

type LustreClientStatsTool struct {
	sysfsPath string
}

func New() *LustreClientStatsTool {
	return &LustreClientStatsTool{
		sysfsPath: "/sys/fs/lustre",
	}
}

func init() {
	registry.Register(registry.WithCache(10*time.Second, New()))
}

// Name returns the unique tool identifier.
func (t *LustreClientStatsTool) Name() string {
	return "get_lustre_client_stats"
}

// Category returns the tool category.
func (t *LustreClientStatsTool) Category() string {
	return "storage"
}

// Help returns usage instructions and documentation for LLM consumption.
func (t *LustreClientStatsTool) Help() string {
	return `Collects Lustre client stats, read-ahead performance, and active MDT/OST connections.

Parses VFS client statistics, prefetching hit rates, and tracks the health (state, inflight RPCs, connect counts) of backend targets.

Data Sources:
- /sys/fs/lustre/version (or /proc/fs/lustre/version)
- /sys/fs/lustre/llite/<client>/stats (or /proc/fs/lustre/llite/<client>/stats)
- /sys/fs/lustre/llite/<client>/read_ahead_stats (or /proc/fs/lustre/llite/<client>/read_ahead_stats)
- /sys/fs/lustre/mdc/*/import (or /proc/fs/lustre/mdc/*/import)
- /sys/fs/lustre/osc/*/import (or /proc/fs/lustre/osc/*/import)`
}

// Description returns a brief one-line summary.
func (t *LustreClientStatsTool) Description() string {
	return "Collect Lustre client stats, read-ahead performance, and target connections"
}

// Parameters returns the parameter schema (none).
func (t *LustreClientStatsTool) Parameters() []registry.ToolParam {
	return nil
}

// Hidden returns false; this is a public diagnostic tool.
func (t *LustreClientStatsTool) Hidden() bool { return false }

// IsSupported checks if Lustre client paths are available.
func (t *LustreClientStatsTool) IsSupported() (bool, string) {
	sysfsExists := registry.PathExists(t.sysfsPath)
	procfsExists := registry.PathExists("/proc/fs/lustre")
	if !sysfsExists && !procfsExists {
		return false, "Lustre filesystem not detected (missing /sys/fs/lustre and /proc/fs/lustre)"
	}
	return true, ""
}

func (t *LustreClientStatsTool) resolvePath(subpath string) string {
	sysPath := filepath.Join(t.sysfsPath, subpath)
	if registry.PathExists(sysPath) {
		return sysPath
	}
	procPath := filepath.Join("/proc/fs/lustre", subpath)
	if registry.PathExists(procPath) {
		return procPath
	}
	return ""
}

func (t *LustreClientStatsTool) getSubdirs(subpath string) []string {
	sysPath := filepath.Join(t.sysfsPath, subpath)
	if registry.PathExists(sysPath) {
		return listSubdirs(sysPath)
	}
	procPath := filepath.Join("/proc/fs/lustre", subpath)
	if registry.PathExists(procPath) {
		return listSubdirs(procPath)
	}
	return nil
}

func listSubdirs(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var dirs []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err == nil && info.IsDir() {
			dirs = append(dirs, name)
		}
	}
	return dirs
}

// Execute performs the tool's operation.
func (t *LustreClientStatsTool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	var data LustreClientStatsData

	// Read Version
	versionPath := t.resolvePath("version")
	if versionPath != "" {
		versionBytes, err := os.ReadFile(versionPath)
		if err == nil {
			data.LustreVersion = strings.TrimSpace(string(versionBytes))
		}
	}

	// Read Filesystems (llite)
	clients := t.getSubdirs("llite")
	for _, client := range clients {
		fsStats := FilesystemStats{Name: client}

		// Read stats
		statsPath := t.resolvePath(filepath.Join("llite", client, "stats"))
		if statsPath != "" {
			statsBytes, err := os.ReadFile(statsPath)
			if err == nil {
				parsedStats, err := parseLliteStats(statsBytes)
				if err == nil {
					fsStats.Stats = parsedStats
				}
			}
		}

		// Read read_ahead_stats
		raPath := t.resolvePath(filepath.Join("llite", client, "read_ahead_stats"))
		if raPath != "" {
			raBytes, err := os.ReadFile(raPath)
			if err == nil {
				fsStats.ReadAhead = parseReadAheadStats(raBytes)
			}
		}

		data.Filesystems = append(data.Filesystems, fsStats)
	}

	// Read MDC connections
	mdcSubdirs := t.getSubdirs("mdc")
	for _, mdc := range mdcSubdirs {
		impPath := t.resolvePath(filepath.Join("mdc", mdc, "import"))
		if impPath != "" {
			impBytes, err := os.ReadFile(impPath)
			if err == nil {
				imp := parseImport(mdc, impBytes)
				data.Connections.MDT = append(data.Connections.MDT, imp)
			}
		}
	}

	// Read OSC connections
	oscSubdirs := t.getSubdirs("osc")
	for _, osc := range oscSubdirs {
		impPath := t.resolvePath(filepath.Join("osc", osc, "import"))
		if impPath != "" {
			impBytes, err := os.ReadFile(impPath)
			if err == nil {
				imp := parseImport(osc, impBytes)
				data.Connections.OST = append(data.Connections.OST, imp)
			}
		}
	}

	// Calculate Summary
	data.Summary.TotalFilesystems = len(data.Filesystems)

	for _, mdt := range data.Connections.MDT {
		data.Summary.TotalMDTConnections++
		if strings.ToUpper(mdt.State) == "FULL" {
			data.Summary.ActiveMDTConnections++
		}
	}

	for _, ost := range data.Connections.OST {
		data.Summary.TotalOSTConnections++
		if strings.ToUpper(ost.State) == "FULL" {
			data.Summary.ActiveOSTConnections++
		}
	}

	summaryStr := fmt.Sprintf("Lustre FS Count: %d, Active Connections: MDT %d/%d, OST %d/%d",
		data.Summary.TotalFilesystems,
		data.Summary.ActiveMDTConnections, data.Summary.TotalMDTConnections,
		data.Summary.ActiveOSTConnections, data.Summary.TotalOSTConnections)

	res := registry.NewResult(t.Name(), registry.StatusOK, summaryStr, data)
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()

	return res, nil
}

func parseLliteStats(data []byte) (map[string]StatEntry, error) {
	stats := make(map[string]StatEntry)
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		name := fields[0]
		samples, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		unit := strings.Trim(fields[3], "[]")

		entry := StatEntry{
			Samples: samples,
			Unit:    unit,
		}

		if len(fields) >= 7 {
			min, errMin := strconv.ParseUint(fields[4], 10, 64)
			max, errMax := strconv.ParseUint(fields[5], 10, 64)
			sum, errSum := strconv.ParseUint(fields[6], 10, 64)
			if errMin == nil && errMax == nil && errSum == nil {
				entry.Min = &min
				entry.Max = &max
				entry.Sum = &sum
			}
		}

		stats[name] = entry
	}
	return stats, nil
}

func parseReadAheadStats(data []byte) ReadAheadStats {
	var ra ReadAheadStats
	ra.Other = make(map[string]uint64)
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := fields[0]
		val, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		if name == "hits" {
			ra.Hits = val
		} else if name == "misses" {
			ra.Misses = val
		} else {
			ra.Other[name] = val
		}
	}
	return ra
}

func parseImport(name string, data []byte) TargetImport {
	imp := TargetImport{Name: name}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

		switch key {
		case "target":
			imp.Target = val
		case "state":
			imp.State = val
		case "connect_count", "connect_cnt":
			c, err := strconv.Atoi(val)
			if err == nil {
				imp.ConnectCount = c
			}
		case "inflight":
			i, err := strconv.Atoi(val)
			if err == nil {
				imp.Inflight = i
			}
		}
	}
	return imp
}
