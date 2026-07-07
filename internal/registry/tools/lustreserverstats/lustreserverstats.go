// Package lustreserverstats implements the get_lustre_server_stats tool.
//
// It reports per-target health for Lustre *server* roles (OSS/MDS/MGS):
// export counts, capacity and inode utilization, top RPC/IO counters, and
// the OSS disk-I/O-size distribution condensed from brw_stats. Files are
// resolved sysfs-first (/sys/fs/lustre) with a procfs fallback
// (/proc/fs/lustre), matching the split layout of real Lustre releases.
package lustreserverstats

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
	topStatsLimit  = 10
	brwBucketLimit = 5
)

// targetTypes maps on-disk directory names to server roles, in report order.
var targetTypes = []string{"obdfilter", "mdt", "mgs"}

// StatCounter is a single named counter from a stats/md_stats file.
type StatCounter struct {
	Name  string `json:"name"`
	Count uint64 `json:"count"`
}

// BrwBucket is one I/O-size bucket from the brw_stats histogram.
type BrwBucket struct {
	Size string  `json:"size"`
	Pct  float64 `json:"pct"`
}

// BrwIOSizes condenses the "disk I/O size" section of brw_stats to the
// dominant buckets per direction.
type BrwIOSizes struct {
	Read  []BrwBucket `json:"read,omitempty"`
	Write []BrwBucket `json:"write,omitempty"`
}

// Capacity reports block-space usage for a target in kilobytes.
type Capacity struct {
	KBytesTotal uint64  `json:"kbytes_total"`
	KBytesFree  uint64  `json:"kbytes_free"`
	UsedPct     float64 `json:"used_pct"`
}

// Inodes reports inode usage for a target.
type Inodes struct {
	Total   uint64  `json:"total"`
	Free    uint64  `json:"free"`
	UsedPct float64 `json:"used_pct"`
}

// TargetStats is the per-target report.
type TargetStats struct {
	Name       string        `json:"name"`
	Type       string        `json:"type"` // obdfilter | mdt | mgs
	NumExports uint64        `json:"num_exports"`
	Capacity   Capacity      `json:"capacity"`
	Inodes     Inodes        `json:"inodes"`
	TopStats   []StatCounter `json:"top_stats,omitempty"`
	BrwIOSizes *BrwIOSizes   `json:"brw_io_sizes,omitempty"`
}

// RoleSummary flags which Lustre server roles are active on this node.
type RoleSummary struct {
	IsOSS bool `json:"is_oss"`
	IsMDS bool `json:"is_mds"`
	IsMGS bool `json:"is_mgs"`
}

// Data is the tool's structured output payload.
type Data struct {
	RoleSummary RoleSummary   `json:"role_summary"`
	Targets     []TargetStats `json:"targets"`
}

// Tool implements registry.Tool for get_lustre_server_stats.
type Tool struct {
	sysfsPath  string
	procfsPath string
}

// New returns a Tool wired to the real sysfs/procfs Lustre roots.
func New() *Tool {
	return &Tool{
		sysfsPath:  "/sys/fs/lustre",
		procfsPath: "/proc/fs/lustre",
	}
}

func init() {
	registry.Register(New())
}

// Name returns the unique tool identifier.
func (t *Tool) Name() string {
	return "get_lustre_server_stats"
}

// Description returns a one-line summary for tool discovery.
func (t *Tool) Description() string {
	return "Collect per-target Lustre server (OSS/MDS/MGS) exports, capacity, top counters, and I/O size distribution"
}

// Help returns the full tool documentation.
func (t *Tool) Help() string {
	return `Collects per-target statistics for Lustre server roles: OSS (obdfilter), MDS (mdt), and MGS (mgs).

For each target it reports the export count, capacity and inode utilization
(precomputed used_pct), and the top-10 stats counters by invocation count.
For OSS targets it additionally condenses the brw_stats histogram down to the
top-5 "disk I/O size" buckets per direction (read/write) with percentages.
Filtering is deterministic (exact kernel counters); missing per-target files
degrade to zeros instead of failing.

Data Sources (sysfs first, procfs fallback):
- /sys/fs/lustre/obdfilter/<target>/{num_exports,kbytestotal,kbytesfree,filestotal,filesfree,stats,brw_stats} (or /proc/fs/lustre/...)
- /sys/fs/lustre/mdt/<target>/{num_exports,stats,md_stats} (or /proc/fs/lustre/...)
- /sys/fs/lustre/mgs/MGS/{num_exports,stats} (or /proc/fs/lustre/...)

Parameters:
- target (optional string): restrict output to a single target name (e.g. "testfs-OST0000").`
}

// Category returns the tool taxonomy classification.
func (t *Tool) Category() registry.Category {
	return registry.CategoryStorage
}

// Parameters returns the parameter schema.
func (t *Tool) Parameters() []registry.ToolParam {
	return []registry.ToolParam{
		{
			Name:        "target",
			Type:        "string",
			Description: "Optional. Restrict output to a single Lustre target name (e.g. 'testfs-OST0000', 'testfs-MDT0000', 'MGS').",
			Required:    false,
		},
	}
}

// Hidden returns false; this is a public diagnostic tool.
func (t *Tool) Hidden() bool { return false }

// IsSupported requires at least one active Lustre server role.
func (t *Tool) IsSupported() (bool, string) {
	roles := registry.DetectNodeRoles()
	if roles.IsOSS || roles.IsMDS || roles.IsMGS {
		return true, ""
	}
	return false, "node has no Lustre server targets (not OSS/MDS/MGS)"
}

// resolvePath returns the sysfs path for subpath if it exists, otherwise the
// procfs fallback path, otherwise "".
func (t *Tool) resolvePath(subpath string) string {
	sysPath := filepath.Join(t.sysfsPath, subpath)
	if registry.PathExists(sysPath) {
		return sysPath
	}
	procPath := filepath.Join(t.procfsPath, subpath)
	if registry.PathExists(procPath) {
		return procPath
	}
	return ""
}

// getSubdirs lists target directories under subpath, preferring sysfs.
func (t *Tool) getSubdirs(subpath string) []string {
	sysPath := filepath.Join(t.sysfsPath, subpath)
	if registry.PathExists(sysPath) {
		return listSubdirs(sysPath)
	}
	procPath := filepath.Join(t.procfsPath, subpath)
	if registry.PathExists(procPath) {
		return listSubdirs(procPath)
	}
	return nil
}

// listSubdirs returns the names of non-hidden subdirectories of dir.
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
		info, err := os.Stat(filepath.Join(dir, name))
		if err == nil && info.IsDir() {
			dirs = append(dirs, name)
		}
	}
	return dirs
}

// Execute collects per-target Lustre server statistics.
func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	var params struct {
		Target string `json:"target"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &params); err != nil {
			return registry.NewErrorResult(t.Name(), "failed to parse arguments: "+err.Error()), nil
		}
	}

	var data Data
	counts := map[string]int{}

	for _, ttype := range targetTypes {
		if err := ctx.Err(); err != nil {
			return registry.NewErrorResult(t.Name(), "cancelled: "+err.Error()), nil
		}

		names := t.getSubdirs(ttype)
		switch ttype {
		case "obdfilter":
			data.RoleSummary.IsOSS = len(names) > 0
		case "mdt":
			data.RoleSummary.IsMDS = len(names) > 0
		case "mgs":
			data.RoleSummary.IsMGS = len(names) > 0
		}

		for _, name := range names {
			if err := ctx.Err(); err != nil {
				return registry.NewErrorResult(t.Name(), "cancelled: "+err.Error()), nil
			}
			if params.Target != "" && name != params.Target {
				continue
			}
			data.Targets = append(data.Targets, t.collectTarget(ttype, name))
			counts[ttype]++
		}
	}

	if params.Target != "" && len(data.Targets) == 0 {
		return registry.NewErrorResult(t.Name(),
			fmt.Sprintf("target %q not found on this node", params.Target)), nil
	}

	status := registry.StatusOK
	summary := fmt.Sprintf("%d Lustre server target(s) (OST: %d, MDT: %d, MGS: %d)",
		len(data.Targets), counts["obdfilter"], counts["mdt"], counts["mgs"])
	if len(data.Targets) == 0 {
		status = registry.StatusWarning
		summary = "no Lustre server targets found"
	}

	res := registry.NewResult(t.Name(), status, summary, data)
	res.Metadata.FilteringMethod = "deterministic"
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	return res, nil
}

// collectTarget builds the report for a single target directory. Missing
// files degrade to zero values so a partially exposed target still yields
// a usable entry.
func (t *Tool) collectTarget(ttype, name string) TargetStats {
	base := filepath.Join(ttype, name)
	tgt := TargetStats{Name: name, Type: ttype}

	if v, ok := readUintFile(t.resolvePath(filepath.Join(base, "num_exports"))); ok {
		tgt.NumExports = v
	}
	if v, ok := readUintFile(t.resolvePath(filepath.Join(base, "kbytestotal"))); ok {
		tgt.Capacity.KBytesTotal = v
	}
	if v, ok := readUintFile(t.resolvePath(filepath.Join(base, "kbytesfree"))); ok {
		tgt.Capacity.KBytesFree = v
	}
	tgt.Capacity.UsedPct = usedPct(tgt.Capacity.KBytesTotal, tgt.Capacity.KBytesFree)

	if v, ok := readUintFile(t.resolvePath(filepath.Join(base, "filestotal"))); ok {
		tgt.Inodes.Total = v
	}
	if v, ok := readUintFile(t.resolvePath(filepath.Join(base, "filesfree"))); ok {
		tgt.Inodes.Free = v
	}
	tgt.Inodes.UsedPct = usedPct(tgt.Inodes.Total, tgt.Inodes.Free)

	counters := map[string]uint64{}
	if statsPath := t.resolvePath(filepath.Join(base, "stats")); statsPath != "" {
		if raw, err := os.ReadFile(statsPath); err == nil {
			for k, v := range parseStatsCounters(raw) {
				counters[k] = v
			}
		}
	}
	if ttype == "mdt" {
		if mdPath := t.resolvePath(filepath.Join(base, "md_stats")); mdPath != "" {
			if raw, err := os.ReadFile(mdPath); err == nil {
				for k, v := range parseStatsCounters(raw) {
					counters[k] = v
				}
			}
		}
	}
	tgt.TopStats = topCounters(counters, topStatsLimit)

	if ttype == "obdfilter" {
		if brwPath := t.resolvePath(filepath.Join(base, "brw_stats")); brwPath != "" {
			if raw, err := os.ReadFile(brwPath); err == nil {
				tgt.BrwIOSizes = parseBrwIOSizes(raw, brwBucketLimit)
			}
		}
	}

	return tgt
}

// readUintFile reads a single unsigned integer from a file. The empty path
// (unresolved) and unparseable content report ok=false.
func readUintFile(path string) (uint64, bool) {
	if path == "" {
		return 0, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// parseStatsCounters extracts "name count samples ..." counter lines from a
// Lustre stats/md_stats file. Non-counter lines (snapshot_time, malformed
// rows) are skipped.
func parseStatsCounters(data []byte) map[string]uint64 {
	counters := make(map[string]uint64)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 3 || fields[2] != "samples" {
			continue
		}
		count, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		counters[fields[0]] = count
	}
	return counters
}

// topCounters returns the n largest counters by count, ties broken by name
// for deterministic output.
func topCounters(counters map[string]uint64, n int) []StatCounter {
	if len(counters) == 0 {
		return nil
	}
	top := make([]StatCounter, 0, len(counters))
	for name, count := range counters {
		top = append(top, StatCounter{Name: name, Count: count})
	}
	sort.Slice(top, func(i, j int) bool {
		if top[i].Count != top[j].Count {
			return top[i].Count > top[j].Count
		}
		return top[i].Name < top[j].Name
	})
	if len(top) > n {
		top = top[:n]
	}
	return top
}

// parseBrwIOSizes extracts the "disk I/O size" section of a brw_stats
// histogram and returns the top-N non-zero buckets per direction. Returns
// nil when the section is missing or unparseable so callers omit the block.
func parseBrwIOSizes(data []byte, topN int) *BrwIOSizes {
	type bucket struct {
		size  string
		count uint64
		pct   float64
	}
	var reads, writes []bucket

	inSection := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if !inSection {
			if strings.Contains(trimmed, "disk I/O size") {
				inSection = true
			}
			continue
		}
		if trimmed == "" {
			break
		}
		left, right, found := strings.Cut(trimmed, "|")
		if !found {
			break
		}
		lf := strings.Fields(left)
		rf := strings.Fields(right)
		if len(lf) < 3 || len(rf) < 2 || !strings.HasSuffix(lf[0], ":") {
			break
		}
		size := strings.TrimSuffix(lf[0], ":")

		rCount, err1 := strconv.ParseUint(lf[1], 10, 64)
		rPct, err2 := strconv.ParseFloat(strings.TrimSuffix(lf[2], "%"), 64)
		if err1 == nil && err2 == nil && rCount > 0 {
			reads = append(reads, bucket{size: size, count: rCount, pct: rPct})
		}
		wCount, err3 := strconv.ParseUint(rf[0], 10, 64)
		wPct, err4 := strconv.ParseFloat(strings.TrimSuffix(rf[1], "%"), 64)
		if err3 == nil && err4 == nil && wCount > 0 {
			writes = append(writes, bucket{size: size, count: wCount, pct: wPct})
		}
	}

	if len(reads) == 0 && len(writes) == 0 {
		return nil
	}

	condense := func(buckets []bucket) []BrwBucket {
		sort.SliceStable(buckets, func(i, j int) bool {
			return buckets[i].count > buckets[j].count
		})
		if len(buckets) > topN {
			buckets = buckets[:topN]
		}
		out := make([]BrwBucket, 0, len(buckets))
		for _, b := range buckets {
			out = append(out, BrwBucket{Size: b.size, Pct: b.pct})
		}
		return out
	}

	return &BrwIOSizes{Read: condense(reads), Write: condense(writes)}
}

// usedPct computes (total-free)/total as a percentage rounded to two
// decimals. Degenerate inputs (zero total, free > total) yield 0.
func usedPct(total, free uint64) float64 {
	if total == 0 || free > total {
		return 0
	}
	pct := float64(total-free) / float64(total) * 100.0
	return math.Round(pct*100) / 100
}
