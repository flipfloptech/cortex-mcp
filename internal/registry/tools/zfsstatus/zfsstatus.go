// Package zfsstatus implements the get_zfs_status diagnostic tool.
//
// It reports ZFS health natively where the kernel exposes it — ARC
// statistics from /proc/spl/kstat/zfs/arcstats and the module version from
// /sys/module/zfs/version — and shells out to zpool (list -Hp + status)
// for pool health, capacity, scrub/resilver progress, and device error
// counts. JSON output from zpool is only available on ZFS >= 2.3, so the
// stable text/parseable formats are used instead.
package zfsstatus

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// scanPctRe extracts the "83.12% done" progress figure from zpool status.
var scanPctRe = regexp.MustCompile(`(\d+(?:\.\d+)?)% done`)

// ARC summarizes the Adaptive Replacement Cache state.
type ARC struct {
	SizeMB      int64   `json:"size_mb"`
	MaxMB       int64   `json:"max_mb"`
	HitRatioPct float64 `json:"hit_ratio_pct"`
}

// PoolErrors aggregates device error counters across a pool's config tree.
type PoolErrors struct {
	Read  int64 `json:"read"`
	Write int64 `json:"write"`
	Cksum int64 `json:"cksum"`
}

// Pool is the health snapshot of one zpool.
type Pool struct {
	Name           string     `json:"name"`
	Health         string     `json:"health"`
	SizeGB         float64    `json:"size_gb"`
	AllocGB        float64    `json:"alloc_gb"`
	FragPct        float64    `json:"frag_pct"`
	CapacityPct    float64    `json:"capacity_pct"`
	ScanState      string     `json:"scan_state"`
	ScanPct        float64    `json:"scan_pct,omitempty"`
	Errors         PoolErrors `json:"errors"`
	WarningReasons []string   `json:"warning_reasons"`
}

// Output is the tool's data payload.
type Output struct {
	ZFSVersion string `json:"zfs_version,omitempty"`
	ARC        *ARC   `json:"arc,omitempty"`
	Pools      []Pool `json:"pools"`
	Note       string `json:"note,omitempty"`
}

// Tool implements registry.Tool for ZFS pool and ARC health auditing.
type Tool struct {
	procfsRoot  string
	sysfsRoot   string
	execCommand func(ctx context.Context, name string, args ...string) ([]byte, error)
	lookPath    func(file string) (string, error)
}

// New returns a Tool wired to the real procfs/sysfs roots and executables.
func New() *Tool {
	return &Tool{
		procfsRoot: "/proc",
		sysfsRoot:  "/sys",
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
	return "get_zfs_status"
}

// Description returns a one-line summary for get_tool_list output.
func (t *Tool) Description() string {
	return "Audit ZFS pool health, capacity, scrub/resilver progress, device errors, and ARC efficiency."
}

// Help returns the full tool help text.
func (t *Tool) Help() string {
	return `Audits the ZFS storage stack: per-pool health/capacity/fragmentation, scrub or resilver progress, device error counters, and ARC cache efficiency.

ARC statistics (size, max size, hit ratio) are read natively from the kernel; pool state comes from the zpool CLI using its stable parseable output (-Hp) plus the status text for scan progress and per-device read/write/checksum error counts summed across the config tree.

Data Sources:
- /proc/spl/kstat/zfs/arcstats (native: size, c_max, hits, misses)
- /sys/module/zfs/version (native: module version)
- zpool list -Hp -o name,size,alloc,free,frag,cap,health
- zpool status <pool> (scan line + config-table error counts)

If the zpool binary is missing while the kernel module is loaded, the tool degrades to an ARC-only report with an explanatory note.`
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

// IsSupported reports whether ZFS is present on this node.
func (t *Tool) IsSupported() (bool, string) {
	if registry.PathExists(filepath.Join(t.sysfsRoot, "module", "zfs")) {
		return true, ""
	}
	if _, err := t.lookPath("zpool"); err == nil {
		return true, ""
	}
	return false, "ZFS not detected (no /sys/module/zfs and no zpool binary)"
}

// Execute collects the ARC snapshot and per-pool health.
func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	if err := ctx.Err(); err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("execution cancelled: %v", err)), nil
	}

	out := Output{
		ZFSVersion: readFileTrim(filepath.Join(t.sysfsRoot, "module", "zfs", "version")),
		Pools:      []Pool{},
	}

	if data, err := os.ReadFile(filepath.Join(t.procfsRoot, "spl", "kstat", "zfs", "arcstats")); err == nil {
		out.ARC = parseARCStats(data)
	}

	if _, err := t.lookPath("zpool"); err != nil {
		out.Note = "zpool binary not found; pool status unavailable (reporting native ARC/module data only)"
	} else {
		listOut, err := t.execCommand(ctx, "zpool", "list", "-Hp", "-o", "name,size,alloc,free,frag,cap,health")
		if err != nil {
			out.Note = fmt.Sprintf("zpool list failed (%v); pool status unavailable", err)
		} else {
			if pools := parseZpoolList(listOut); pools != nil {
				out.Pools = pools
			}
			for i := range out.Pools {
				if err := ctx.Err(); err != nil {
					return registry.NewErrorResult(t.Name(), fmt.Sprintf("execution cancelled: %v", err)), nil
				}
				// Best-effort enrichment; a status failure keeps the
				// list-derived health/capacity data.
				if statusOut, serr := t.execCommand(ctx, "zpool", "status", out.Pools[i].Name); serr == nil {
					state, pct, errs := parseZpoolStatus(statusOut)
					out.Pools[i].ScanState = state
					out.Pools[i].ScanPct = pct
					out.Pools[i].Errors = errs
				}
				applyPoolWarnings(&out.Pools[i])
			}
		}
	}

	poolsWithWarnings := 0
	for _, p := range out.Pools {
		if len(p.WarningReasons) > 0 {
			poolsWithWarnings++
		}
	}

	status := registry.StatusOK
	summary := fmt.Sprintf("ZFS: %d pools, %d with warnings", len(out.Pools), poolsWithWarnings)
	if out.ARC != nil {
		summary += fmt.Sprintf("; ARC %d MB (hit ratio %.2f%%)", out.ARC.SizeMB, out.ARC.HitRatioPct)
	}
	switch {
	case poolsWithWarnings > 0:
		status = registry.StatusWarning
	case out.Note != "":
		status = registry.StatusDegraded
		summary = "ZFS: " + out.Note
	}

	res := registry.NewResult(t.Name(), status, summary, out)
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	res.Metadata.FilteringMethod = "deterministic"
	return res, nil
}

// parseARCStats extracts size, c_max, hits, and misses from the arcstats
// kstat file (3-column "name type data" rows). Returns nil if none of the
// recognized keys are present.
func parseARCStats(data []byte) *ARC {
	var size, cMax, hits, misses int64
	found := false

	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		value, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "size":
			size, found = value, true
		case "c_max":
			cMax, found = value, true
		case "hits":
			hits, found = value, true
		case "misses":
			misses, found = value, true
		}
	}

	if !found {
		return nil
	}

	arc := &ARC{
		SizeMB: size / (1 << 20),
		MaxMB:  cMax / (1 << 20),
	}
	if total := hits + misses; total > 0 {
		arc.HitRatioPct = math.Round(float64(hits)/float64(total)*100*100) / 100
	}
	return arc
}

// parseZpoolList parses `zpool list -Hp -o name,size,alloc,free,frag,cap,health`
// output (one pool per line, exact byte sizes) into Pool skeletons.
func parseZpoolList(out []byte) []Pool {
	var pools []Pool
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 7 {
			continue
		}

		p := Pool{
			Name:           fields[0],
			Health:         fields[6],
			ScanState:      "none",
			WarningReasons: []string{},
		}
		if size, err := strconv.ParseFloat(fields[1], 64); err == nil {
			p.SizeGB = math.Round(size/(1<<30)*10) / 10
		}
		if alloc, err := strconv.ParseFloat(fields[2], 64); err == nil {
			p.AllocGB = math.Round(alloc/(1<<30)*10) / 10
		}
		if frag, err := strconv.ParseFloat(strings.TrimSuffix(fields[4], "%"), 64); err == nil {
			p.FragPct = frag
		}
		if cap, err := strconv.ParseFloat(strings.TrimSuffix(fields[5], "%"), 64); err == nil {
			p.CapacityPct = cap
		}

		pools = append(pools, p)
	}
	return pools
}

// parseZpoolStatus extracts the scan state, scan progress percentage, and
// the read/write/cksum error counters (summed across every row of the
// config table) from `zpool status <pool>` text output.
func parseZpoolStatus(out []byte) (string, float64, PoolErrors) {
	state := "none"
	var errs PoolErrors
	scanLine := ""
	inConfig := false

	for _, raw := range strings.Split(string(out), "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "scan:"):
			scanLine = strings.TrimSpace(strings.TrimPrefix(line, "scan:"))
		case strings.HasPrefix(line, "config:"):
			inConfig = true
		case strings.HasPrefix(line, "errors:"):
			inConfig = false
		case inConfig:
			fields := strings.Fields(line)
			if len(fields) < 5 || fields[0] == "NAME" {
				continue
			}
			errs.Read += parseZfsCount(fields[2])
			errs.Write += parseZfsCount(fields[3])
			errs.Cksum += parseZfsCount(fields[4])
		}
	}

	switch {
	case strings.Contains(scanLine, "scrub in progress"):
		state = "scrub in progress"
	case strings.Contains(scanLine, "resilver in progress"):
		state = "resilver in progress"
	case strings.Contains(scanLine, "scrub repaired"):
		state = "scrub repaired"
	case strings.Contains(scanLine, "resilvered"):
		state = "resilvered"
	}

	pct := 0.0
	if m := scanPctRe.FindStringSubmatch(string(out)); m != nil {
		pct, _ = strconv.ParseFloat(m[1], 64)
	}

	return state, pct, errs
}

// parseZfsCount parses a zpool error counter that may carry a human-readable
// magnitude suffix (K/M/G/T). Dashes, empty strings, and garbage yield 0.
func parseZfsCount(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "-" {
		return 0
	}

	mult := float64(1)
	switch s[len(s)-1] {
	case 'K':
		mult, s = 1e3, s[:len(s)-1]
	case 'M':
		mult, s = 1e6, s[:len(s)-1]
	case 'G':
		mult, s = 1e9, s[:len(s)-1]
	case 'T':
		mult, s = 1e12, s[:len(s)-1]
	}

	value, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int64(math.Round(value * mult))
}

// applyPoolWarnings populates warning_reasons from the pool's health,
// error counters, and capacity.
func applyPoolWarnings(p *Pool) {
	if p.Health != "ONLINE" {
		p.WarningReasons = append(p.WarningReasons,
			fmt.Sprintf("pool health is %s (not ONLINE)", p.Health))
	}
	if p.Errors.Read > 0 || p.Errors.Write > 0 || p.Errors.Cksum > 0 {
		p.WarningReasons = append(p.WarningReasons,
			fmt.Sprintf("device errors detected: read=%d write=%d cksum=%d", p.Errors.Read, p.Errors.Write, p.Errors.Cksum))
	}
	if p.CapacityPct > 90 {
		p.WarningReasons = append(p.WarningReasons,
			fmt.Sprintf("capacity %.0f%% exceeds 90%% (ZFS performance degrades when nearly full)", p.CapacityPct))
	}
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
