// Package lustrejobstats implements the get_lustre_job_stats tool.
//
// It attributes Lustre server I/O and metadata load to the jobs that
// generated it by parsing the per-target job_stats files exposed on OSS
// (obdfilter) and MDS (mdt) nodes. Per-job counters are aggregated across
// every target and condensed into three leaderboards: top writers, top
// readers, and top metadata consumers. Files are resolved sysfs-first
// (/sys/fs/lustre) with a per-file procfs fallback (/proc/fs/lustre),
// matching the split layout of real Lustre releases.
package lustrejobstats

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

const (
	defaultLimit = 15
	maxLimit     = 50

	// notEnabledNote is emitted when targets exist but no job is tracked,
	// the signature of job statistics being disabled cluster-wide.
	notEnabledNote = "Lustre job stats not enabled (set jobid_var, e.g. lctl conf_param <fs>.sys.jobid_var=procname_uid)"
)

// jobStatsTargetTypes maps on-disk directory names to server roles, in
// report order. MGS carries no job_stats file so it is not scanned.
var jobStatsTargetTypes = []string{"obdfilter", "mdt"}

var (
	// jobIDRe matches a "- job_id: <id>" block-start line (trimmed).
	jobIDRe = regexp.MustCompile(`^-\s*job_id:\s*(.*)$`)

	// counterRe matches a "key: { ... }" counter line (trimmed).
	counterRe = regexp.MustCompile(`^([A-Za-z_]\w*):\s*\{(.*)\}\s*$`)

	// samplesRe / sumRe extract fields from a counter's brace body.
	samplesRe = regexp.MustCompile(`samples:\s*(\d+)`)
	sumRe     = regexp.MustCompile(`sum:\s*(\d+)`)
)

// JobIOStats is one entry of the top_jobs_by_write / top_jobs_by_read
// leaderboards, aggregated across all scanned targets.
type JobIOStats struct {
	JobID    string   `json:"job_id"`
	WriteMB  float64  `json:"write_mb"`
	WriteOps uint64   `json:"write_ops"`
	ReadMB   float64  `json:"read_mb"`
	ReadOps  uint64   `json:"read_ops"`
	Targets  []string `json:"targets"`
}

// JobMetadataStats is one entry of the top_jobs_by_metadata_ops
// leaderboard, aggregated across MDT targets only.
type JobMetadataStats struct {
	JobID    string   `json:"job_id"`
	TotalOps uint64   `json:"total_ops"`
	TopOp    string   `json:"top_op"`
	Targets  []string `json:"targets"`
}

// Data is the tool's structured output payload.
type Data struct {
	TargetsScanned       int                `json:"targets_scanned"`
	TotalJobsTracked     int                `json:"total_jobs_tracked"`
	TopJobsByWrite       []JobIOStats       `json:"top_jobs_by_write"`
	TopJobsByRead        []JobIOStats       `json:"top_jobs_by_read"`
	TopJobsByMetadataOps []JobMetadataStats `json:"top_jobs_by_metadata_ops,omitempty"`
	ParseErrors          int                `json:"parse_errors,omitempty"`
	Note                 string             `json:"note,omitempty"`
	WarningReasons       []string           `json:"warning_reasons"`
}

// jobCounter is one parsed "key: { samples: N, ..., sum: M }" counter.
type jobCounter struct {
	Samples uint64
	Sum     uint64
}

// jobRecord is one parsed job block from a single job_stats file.
type jobRecord struct {
	JobID    string
	Counters map[string]jobCounter
}

// jobAggregate accumulates one job's counters across every scanned target.
type jobAggregate struct {
	jobID      string
	readBytes  uint64
	readOps    uint64
	writeBytes uint64
	writeOps   uint64
	mdOps      map[string]uint64
	totalMDOps uint64
	ioTargets  map[string]struct{}
	mdTargets  map[string]struct{}
}

// Tool implements registry.Tool for get_lustre_job_stats.
type Tool struct {
	sysfsRoot  string
	procfsRoot string
}

// New returns a Tool wired to the real sysfs/procfs roots.
func New() *Tool {
	return &Tool{
		sysfsRoot:  "/sys",
		procfsRoot: "/proc",
	}
}

func init() {
	registry.Register(New())
}

// Name returns the unique tool identifier.
func (t *Tool) Name() string {
	return "get_lustre_job_stats"
}

// Description returns a one-line summary for tool discovery.
func (t *Tool) Description() string {
	return "Attribute Lustre server I/O and metadata load to jobs via per-target job_stats leaderboards"
}

// Help returns the full tool documentation.
func (t *Tool) Help() string {
	return `Attributes Lustre server load to the jobs generating it ("who is hammering the filesystem?").

Parses the YAML-ish per-target job_stats files on OSS and MDS nodes,
aggregates each job's counters across all targets, and reports three
leaderboards: top jobs by write volume, by read volume (write_mb/read_mb in
MiB at 1 decimal, plus read/write op counts), and by metadata operations
(MDT targets only; total_ops sums every non-I/O op counter such as
open/close/getattr/mkdir/unlink, top_op names the dominant one). Each entry
lists the targets where the job was observed. Jobs with zero activity in a
dimension are excluded from that leaderboard. Parsing is deterministic;
malformed job blocks are skipped and surfaced via parse_errors.

Data Sources (each file resolved sysfs-first with a procfs fallback):
- /sys/fs/lustre/obdfilter/<target>/job_stats (or /proc/fs/lustre/obdfilter/<target>/job_stats) — OSS read_bytes/write_bytes per job
- /sys/fs/lustre/mdt/<target>/job_stats (or /proc/fs/lustre/mdt/<target>/job_stats) — MDS metadata op counters per job

Parameters:
- target (optional string): restrict scanning to a single target name (e.g. "testfs-OST0000").
- limit (optional integer): leaderboard size, default 15, capped at 50.

Caveats: job statistics must be enabled via the jobid_var tunable (e.g.
lctl conf_param <fs>.sys.jobid_var=procname_uid); when targets exist but no
job is tracked the result degrades with an enablement hint. Entries expire
after job_cleanup_interval, so leaderboards reflect recent activity only.`
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
			Description: "Optional. Restrict scanning to a single Lustre target name (e.g. 'testfs-OST0000', 'testfs-MDT0000').",
			Required:    false,
		},
		{
			Name:        "limit",
			Type:        "integer",
			Description: "Optional. Maximum entries per leaderboard (default 15, capped at 50).",
			Required:    false,
			Default:     "15",
		},
	}
}

// Hidden returns false; this is a public diagnostic tool.
func (t *Tool) Hidden() bool { return false }

// IsSupported requires an active Lustre OSS or MDS role, the only roles
// that expose job_stats files.
func (t *Tool) IsSupported() (bool, string) {
	roles := registry.DetectNodeRoles()
	if roles.IsOSS || roles.IsMDS {
		return true, ""
	}
	return false, "node has no Lustre server targets (not OSS/MDS)"
}

// resolvePath returns the sysfs path for subpath under fs/lustre if it
// exists, otherwise the procfs fallback path, otherwise "".
func (t *Tool) resolvePath(subpath string) string {
	sysPath := filepath.Join(t.sysfsRoot, "fs", "lustre", subpath)
	if registry.PathExists(sysPath) {
		return sysPath
	}
	procPath := filepath.Join(t.procfsRoot, "fs", "lustre", subpath)
	if registry.PathExists(procPath) {
		return procPath
	}
	return ""
}

// getSubdirs lists target directories under fs/lustre/<subpath>,
// preferring sysfs over procfs.
func (t *Tool) getSubdirs(subpath string) []string {
	sysPath := filepath.Join(t.sysfsRoot, "fs", "lustre", subpath)
	if registry.PathExists(sysPath) {
		return listSubdirs(sysPath)
	}
	procPath := filepath.Join(t.procfsRoot, "fs", "lustre", subpath)
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

// Execute scans job_stats across all (or one) server targets and builds
// the per-job leaderboards.
func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	var params struct {
		Target string `json:"target"`
		Limit  int    `json:"limit"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &params); err != nil {
			return registry.NewErrorResult(t.Name(), "failed to parse arguments: "+err.Error()), nil
		}
	}
	limit := params.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	type targetRef struct {
		ttype string
		name  string
	}
	var targets []targetRef
	for _, ttype := range jobStatsTargetTypes {
		for _, name := range t.getSubdirs(ttype) {
			if params.Target != "" && name != params.Target {
				continue
			}
			targets = append(targets, targetRef{ttype: ttype, name: name})
		}
	}
	if params.Target != "" && len(targets) == 0 {
		return registry.NewErrorResult(t.Name(),
			fmt.Sprintf("target %q not found on this node", params.Target)), nil
	}

	aggs := map[string]*jobAggregate{}
	parseErrors := 0
	hasMDT := false

	for _, ref := range targets {
		if err := ctx.Err(); err != nil {
			return registry.NewErrorResult(t.Name(), "cancelled: "+err.Error()), nil
		}
		if ref.ttype == "mdt" {
			hasMDT = true
		}
		path := t.resolvePath(filepath.Join(ref.ttype, ref.name, "job_stats"))
		if path == "" {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		records, errs := parseJobStats(raw)
		parseErrors += errs
		aggregateRecords(aggs, records, ref.name, ref.ttype == "mdt")
	}

	data := Data{
		TargetsScanned:   len(targets),
		TotalJobsTracked: len(aggs),
		TopJobsByWrite:   buildIOTop(aggs, limit, true),
		TopJobsByRead:    buildIOTop(aggs, limit, false),
		ParseErrors:      parseErrors,
		WarningReasons:   []string{},
	}
	if hasMDT {
		data.TopJobsByMetadataOps = buildMetaTop(aggs, limit)
	}

	status := registry.StatusOK
	summary := fmt.Sprintf("%d job(s) tracked across %d Lustre server target(s)",
		data.TotalJobsTracked, data.TargetsScanned)
	switch {
	case data.TargetsScanned == 0:
		status = registry.StatusWarning
		summary = "no Lustre server targets found"
	case data.TotalJobsTracked == 0:
		status = registry.StatusDegraded
		data.Note = notEnabledNote
		summary = notEnabledNote
	}

	res := registry.NewResult(t.Name(), status, summary, data)
	res.Metadata.FilteringMethod = "deterministic"
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	return res, nil
}

// parseJobStats parses the YAML-ish job_stats format line by line. Blocks
// with an empty/unparseable job_id and counter lines whose brace body has
// no parseable samples field are skipped and counted as parse errors.
func parseJobStats(data []byte) ([]jobRecord, int) {
	var jobs []jobRecord
	parseErrors := 0
	var cur *jobRecord

	flush := func() {
		if cur != nil {
			jobs = append(jobs, *cur)
			cur = nil
		}
	}

	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || trimmed == "job_stats:" {
			continue
		}

		if strings.HasPrefix(trimmed, "-") {
			flush()
			id := ""
			if m := jobIDRe.FindStringSubmatch(trimmed); m != nil {
				id = unquoteJobID(strings.TrimSpace(m[1]))
			}
			if id == "" {
				parseErrors++
				continue
			}
			cur = &jobRecord{JobID: id, Counters: map[string]jobCounter{}}
			continue
		}

		if cur == nil {
			// Continuation of a skipped block: already counted once.
			continue
		}
		if strings.HasPrefix(trimmed, "snapshot_time:") {
			continue
		}

		m := counterRe.FindStringSubmatch(trimmed)
		if m == nil {
			if strings.Contains(trimmed, "{") {
				parseErrors++
			}
			continue
		}
		key, body := m[1], m[2]
		sm := samplesRe.FindStringSubmatch(body)
		if sm == nil {
			parseErrors++
			continue
		}
		samples, err := strconv.ParseUint(sm[1], 10, 64)
		if err != nil {
			parseErrors++
			continue
		}
		c := jobCounter{Samples: samples}
		if su := sumRe.FindStringSubmatch(body); su != nil {
			if v, err := strconv.ParseUint(su[1], 10, 64); err == nil {
				c.Sum = v
			}
		}
		cur.Counters[key] = c
	}
	flush()

	return jobs, parseErrors
}

// unquoteJobID strips one pair of matching surrounding quotes from a job
// identifier (job_id values may be quoted and contain dots, spaces, or
// user names).
func unquoteJobID(s string) string {
	if len(s) >= 2 {
		first, last := s[0], s[len(s)-1]
		if first == last && (first == '"' || first == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// aggregateRecords merges one target's job records into the cross-target
// aggregate map. read_bytes/write_bytes feed the I/O totals on any target
// type; every other counter is a metadata op and is only accumulated from
// MDT targets.
func aggregateRecords(aggs map[string]*jobAggregate, records []jobRecord, targetName string, isMDT bool) {
	for _, rec := range records {
		agg := aggs[rec.JobID]
		if agg == nil {
			agg = &jobAggregate{
				jobID:     rec.JobID,
				mdOps:     map[string]uint64{},
				ioTargets: map[string]struct{}{},
				mdTargets: map[string]struct{}{},
			}
			aggs[rec.JobID] = agg
		}
		for name, c := range rec.Counters {
			switch name {
			case "read_bytes":
				agg.readOps += c.Samples
				agg.readBytes += c.Sum
				if c.Samples > 0 || c.Sum > 0 {
					agg.ioTargets[targetName] = struct{}{}
				}
			case "write_bytes":
				agg.writeOps += c.Samples
				agg.writeBytes += c.Sum
				if c.Samples > 0 || c.Sum > 0 {
					agg.ioTargets[targetName] = struct{}{}
				}
			default:
				if isMDT && c.Samples > 0 {
					agg.mdOps[name] += c.Samples
					agg.totalMDOps += c.Samples
					agg.mdTargets[targetName] = struct{}{}
				}
			}
		}
	}
}

// buildIOTop builds the write (byWrite=true) or read leaderboard: jobs
// with activity in that dimension, sorted by bytes desc, then ops desc,
// then job_id asc for deterministic output, truncated to limit.
func buildIOTop(aggs map[string]*jobAggregate, limit int, byWrite bool) []JobIOStats {
	selected := make([]*jobAggregate, 0, len(aggs))
	for _, agg := range aggs {
		if byWrite {
			if agg.writeBytes > 0 || agg.writeOps > 0 {
				selected = append(selected, agg)
			}
		} else {
			if agg.readBytes > 0 || agg.readOps > 0 {
				selected = append(selected, agg)
			}
		}
	}

	sort.Slice(selected, func(i, j int) bool {
		bi, bj := selected[i].readBytes, selected[j].readBytes
		oi, oj := selected[i].readOps, selected[j].readOps
		if byWrite {
			bi, bj = selected[i].writeBytes, selected[j].writeBytes
			oi, oj = selected[i].writeOps, selected[j].writeOps
		}
		if bi != bj {
			return bi > bj
		}
		if oi != oj {
			return oi > oj
		}
		return selected[i].jobID < selected[j].jobID
	})
	if len(selected) > limit {
		selected = selected[:limit]
	}

	out := make([]JobIOStats, 0, len(selected))
	for _, agg := range selected {
		out = append(out, JobIOStats{
			JobID:    agg.jobID,
			WriteMB:  mbFromBytes(agg.writeBytes),
			WriteOps: agg.writeOps,
			ReadMB:   mbFromBytes(agg.readBytes),
			ReadOps:  agg.readOps,
			Targets:  sortedTargets(agg.ioTargets),
		})
	}
	return out
}

// buildMetaTop builds the metadata-op leaderboard: jobs with MDT ops,
// sorted by total_ops desc then job_id asc, truncated to limit.
func buildMetaTop(aggs map[string]*jobAggregate, limit int) []JobMetadataStats {
	selected := make([]*jobAggregate, 0, len(aggs))
	for _, agg := range aggs {
		if agg.totalMDOps > 0 {
			selected = append(selected, agg)
		}
	}

	sort.Slice(selected, func(i, j int) bool {
		if selected[i].totalMDOps != selected[j].totalMDOps {
			return selected[i].totalMDOps > selected[j].totalMDOps
		}
		return selected[i].jobID < selected[j].jobID
	})
	if len(selected) > limit {
		selected = selected[:limit]
	}

	out := make([]JobMetadataStats, 0, len(selected))
	for _, agg := range selected {
		out = append(out, JobMetadataStats{
			JobID:    agg.jobID,
			TotalOps: agg.totalMDOps,
			TopOp:    topOp(agg.mdOps),
			Targets:  sortedTargets(agg.mdTargets),
		})
	}
	return out
}

// topOp returns the op name with the highest sample count, ties broken
// alphabetically for deterministic output. Empty maps yield "".
func topOp(ops map[string]uint64) string {
	best := ""
	var bestCount uint64
	for name, count := range ops {
		if count > bestCount || (count == bestCount && (best == "" || name < best)) {
			best = name
			bestCount = count
		}
	}
	return best
}

// mbFromBytes converts a byte count to MiB rounded to one decimal place.
func mbFromBytes(b uint64) float64 {
	return math.Round(float64(b)/(1<<20)*10) / 10
}

// sortedTargets flattens a target set into a sorted slice for
// deterministic JSON output.
func sortedTargets(set map[string]struct{}) []string {
	targets := make([]string, 0, len(set))
	for name := range set {
		targets = append(targets, name)
	}
	sort.Strings(targets)
	return targets
}
