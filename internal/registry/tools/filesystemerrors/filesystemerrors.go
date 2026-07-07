// Package filesystemerrors implements the get_filesystem_errors tool.
//
// It surfaces filesystem-level error state that usually goes unnoticed
// until a mount flips read-only: the kernel's native per-device ext4 error
// counters from sysfs, unexpected read-only block-device mounts detected
// from /proc/self/mountinfo, and — as a justified optional enrichment when
// the btrfs CLI is installed — per-device btrfs I/O/corruption error
// counters (btrfs keeps these in its own on-disk/ioctl domain with no
// native sysfs equivalent).
package filesystemerrors

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

// btrfsStatRe matches one "[/dev/sda1].write_io_errs   0" counter line.
var btrfsStatRe = regexp.MustCompile(`^\[([^\]]+)\]\.(\w+)\s+(\d+)$`)

// roExemptFstypes are filesystems that are read-only by design; an "ro"
// mount of these is expected, not an error symptom.
var roExemptFstypes = map[string]bool{
	"squashfs": true,
	"iso9660":  true,
	"erofs":    true,
	"cramfs":   true,
	"romfs":    true,
	"udf":      true,
}

// Ext4Device is the native error-counter report for one ext4 device.
type Ext4Device struct {
	Device         string `json:"device"`
	Mount          string `json:"mount,omitempty"`
	ErrorsCount    uint64 `json:"errors_count"`
	FirstError     string `json:"first_error,omitempty"`
	LastError      string `json:"last_error,omitempty"`
	FirstErrorFunc string `json:"first_error_func,omitempty"`
}

// ReadonlyMount is a block-device mount unexpectedly in read-only state.
type ReadonlyMount struct {
	Mount  string `json:"mount"`
	Device string `json:"device"`
	Fstype string `json:"fstype"`
}

// BtrfsDeviceStats is the per-device error counter set reported by
// `btrfs device stats`.
type BtrfsDeviceStats struct {
	Mount          string `json:"mount"`
	Device         string `json:"device"`
	WriteIOErrs    uint64 `json:"write_io_errs"`
	ReadIOErrs     uint64 `json:"read_io_errs"`
	FlushIOErrs    uint64 `json:"flush_io_errs"`
	CorruptionErrs uint64 `json:"corruption_errs"`
	GenerationErrs uint64 `json:"generation_errs"`
}

// Summary aggregates the headline counts.
type Summary struct {
	Ext4DevicesChecked int `json:"ext4_devices_checked"`
	DevicesWithErrors  int `json:"devices_with_errors"`
	ReadonlyCount      int `json:"readonly_count"`
}

// Data is the tool's structured output payload.
type Data struct {
	Ext4           []Ext4Device       `json:"ext4"`
	ReadonlyMounts []ReadonlyMount    `json:"readonly_mounts"`
	Btrfs          []BtrfsDeviceStats `json:"btrfs,omitempty"`
	Summary        Summary            `json:"summary"`
	Note           string             `json:"note,omitempty"`
	WarningReasons []string           `json:"warning_reasons"`
}

// mountEntry is one parsed /proc/self/mountinfo line.
type mountEntry struct {
	mountPoint string
	mountOpts  string // per-mount options (field 6)
	fstype     string
	source     string
	superOpts  string // superblock options (after the "-" separator)
}

// Tool implements registry.Tool for get_filesystem_errors.
type Tool struct {
	sysfsRoot   string
	procfsRoot  string
	execCommand func(ctx context.Context, name string, args ...string) ([]byte, error)
	lookPath    func(file string) (string, error)
}

// New returns a Tool wired to the real sysfs/procfs roots and executables.
func New() *Tool {
	return &Tool{
		sysfsRoot:  "/sys",
		procfsRoot: "/proc",
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
	return "get_filesystem_errors"
}

// Description returns a one-line summary for tool discovery.
func (t *Tool) Description() string {
	return "Detect recorded ext4/btrfs filesystem errors and unexpected read-only remounts"
}

// Help returns the full tool documentation.
func (t *Tool) Help() string {
	return `Surfaces filesystem-level error state: kernel-recorded ext4 error counters, unexpected read-only block-device mounts (the classic symptom of an errors=remount-ro trip), and per-device btrfs error counters.

ext4 counters are read natively from sysfs per device: errors_count,
first_error_time / last_error_time (unix epochs rendered as RFC3339), and
first_error_func. A missing errors_count means the device is healthy.
Devices are mapped to mount points via /proc/self/mountinfo. Read-only
detection token-matches "ro" in the per-mount or superblock option lists,
skipping media that is read-only by design (squashfs, iso9660, erofs,
cramfs, romfs, udf) and non-block sources. When the btrfs CLI is installed,
each btrfs filesystem is enriched with "btrfs device stats <mount>"
counters (write/read/flush I/O, corruption, generation errors) — btrfs
exposes these only through its own tooling, not sysfs. Detection is
deterministic (exact kernel counters and mount flags).

Data Sources:
- /sys/fs/ext4/<dev>/{errors_count,first_error_time,last_error_time,first_error_func} (native)
- /proc/self/mountinfo (native: mount table, per-mount and superblock options)
- btrfs device stats <mount> (optional enrichment when the btrfs binary is present)

Limitations: XFS exposes no cumulative error counters in sysfs; use
query_dmesg for XFS corruption events. LVM/device-mapper ext4 devices
appear under their dm-N name, which may not match the /dev/mapper source in
mountinfo (the mount field is then omitted).`
}

// Category returns the tool taxonomy classification.
func (t *Tool) Category() registry.Category {
	return registry.CategoryStorage
}

// Parameters returns the parameter schema (none).
func (t *Tool) Parameters() []registry.ToolParam {
	return nil
}

// Hidden returns false; this is a public diagnostic tool.
func (t *Tool) Hidden() bool { return false }

// IsSupported requires a readable mountinfo, the backbone of every
// detection this tool performs.
func (t *Tool) IsSupported() (bool, string) {
	path := filepath.Join(t.procfsRoot, "self", "mountinfo")
	if _, err := os.Stat(path); err != nil {
		return false, fmt.Sprintf("%s is missing", path)
	}
	return true, ""
}

// Execute collects ext4 error counters, read-only mounts, and optional
// btrfs device stats. Arguments are ignored: the tool takes no parameters.
func (t *Tool) Execute(ctx context.Context, _ json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	if err := ctx.Err(); err != nil {
		return registry.NewErrorResult(t.Name(), "cancelled: "+err.Error()), nil
	}

	mountinfoPath := filepath.Join(t.procfsRoot, "self", "mountinfo")
	raw, err := os.ReadFile(mountinfoPath)
	if err != nil {
		return registry.NewErrorResult(t.Name(),
			fmt.Sprintf("failed to read %s: %v", mountinfoPath, err)), nil
	}
	entries := parseMountInfo(raw)

	data := Data{
		Ext4:           t.collectExt4(ext4MountMap(entries)),
		ReadonlyMounts: findReadonlyMounts(entries),
	}
	if data.Ext4 == nil {
		data.Ext4 = []Ext4Device{}
	}
	if data.ReadonlyMounts == nil {
		data.ReadonlyMounts = []ReadonlyMount{}
	}

	data.Btrfs, data.Note = t.collectBtrfs(ctx, entries)
	if err := ctx.Err(); err != nil {
		return registry.NewErrorResult(t.Name(), "cancelled: "+err.Error()), nil
	}

	data.Summary.Ext4DevicesChecked = len(data.Ext4)
	data.Summary.ReadonlyCount = len(data.ReadonlyMounts)
	for _, d := range data.Ext4 {
		if d.ErrorsCount > 0 {
			data.Summary.DevicesWithErrors++
		}
	}
	for _, b := range data.Btrfs {
		if b.WriteIOErrs > 0 || b.ReadIOErrs > 0 || b.FlushIOErrs > 0 || b.CorruptionErrs > 0 || b.GenerationErrs > 0 {
			data.Summary.DevicesWithErrors++
		}
	}

	data.WarningReasons = buildWarnings(data.Ext4, data.ReadonlyMounts, data.Btrfs)

	status := registry.StatusOK
	if len(data.WarningReasons) > 0 {
		status = registry.StatusWarning
	}
	summary := fmt.Sprintf("%d ext4 device(s) checked, %d device(s) with errors, %d unexpected read-only mount(s)",
		data.Summary.Ext4DevicesChecked, data.Summary.DevicesWithErrors, data.Summary.ReadonlyCount)

	res := registry.NewResult(t.Name(), status, summary, data)
	res.Metadata.FilteringMethod = "deterministic"
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	return res, nil
}

// parseMountInfo parses /proc/self/mountinfo lines. Malformed lines
// (too short, or missing the "-" separator) are skipped.
func parseMountInfo(data []byte) []mountEntry {
	var entries []mountEntry
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 7 {
			continue
		}

		sep := -1
		for i := 6; i < len(fields); i++ {
			if fields[i] == "-" {
				sep = i
				break
			}
		}
		if sep < 0 || sep+2 >= len(fields) {
			continue
		}

		e := mountEntry{
			mountPoint: fields[4],
			mountOpts:  fields[5],
			fstype:     fields[sep+1],
			source:     fields[sep+2],
		}
		if sep+3 < len(fields) {
			e.superOpts = fields[sep+3]
		}
		entries = append(entries, e)
	}
	return entries
}

// hasMountOpt reports whether opt appears as a whole comma-separated token
// in opts (so "errors=remount-ro" never matches "ro").
func hasMountOpt(opts, opt string) bool {
	for _, o := range strings.Split(opts, ",") {
		if o == opt {
			return true
		}
	}
	return false
}

// findReadonlyMounts flags block-device mounts that carry "ro" in either
// their per-mount or superblock options, excluding read-only-by-design
// filesystem types.
func findReadonlyMounts(entries []mountEntry) []ReadonlyMount {
	var out []ReadonlyMount
	for _, e := range entries {
		if !strings.HasPrefix(e.source, "/dev/") {
			continue
		}
		if roExemptFstypes[e.fstype] {
			continue
		}
		if hasMountOpt(e.mountOpts, "ro") || hasMountOpt(e.superOpts, "ro") {
			out = append(out, ReadonlyMount{
				Mount:  e.mountPoint,
				Device: e.source,
				Fstype: e.fstype,
			})
		}
	}
	return out
}

// ext4MountMap maps ext4 block-device base names (as they appear under
// /sys/fs/ext4) to their mount points. The first mount of a device wins.
func ext4MountMap(entries []mountEntry) map[string]string {
	m := map[string]string{}
	for _, e := range entries {
		if e.fstype != "ext4" || !strings.HasPrefix(e.source, "/dev/") {
			continue
		}
		dev := filepath.Base(e.source)
		if _, ok := m[dev]; !ok {
			m[dev] = e.mountPoint
		}
	}
	return m
}

// collectExt4 reads the native error attributes for every device under
// <sysfs>/fs/ext4. A missing errors_count file means no error has ever
// been recorded (healthy, count 0); an unreadable or unparseable one
// skips the device. The non-device "features" directory is excluded.
func (t *Tool) collectExt4(mounts map[string]string) []Ext4Device {
	base := filepath.Join(t.sysfsRoot, "fs", "ext4")
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}

	var out []Ext4Device
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "features" {
			continue
		}
		dev := e.Name()
		devDir := filepath.Join(base, dev)
		d := Ext4Device{Device: dev, Mount: mounts[dev]}

		raw, err := os.ReadFile(filepath.Join(devDir, "errors_count"))
		switch {
		case err == nil:
			v, perr := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
			if perr != nil {
				continue // unparseable counter: skip the device
			}
			d.ErrorsCount = v
		case os.IsNotExist(err):
			// Absent file = no errors recorded; healthy.
		default:
			continue // unreadable counter: skip the device
		}

		d.FirstError = readEpochRFC3339(filepath.Join(devDir, "first_error_time"))
		d.LastError = readEpochRFC3339(filepath.Join(devDir, "last_error_time"))
		d.FirstErrorFunc = readTrimmed(filepath.Join(devDir, "first_error_func"))
		out = append(out, d)
	}
	return out
}

// readEpochRFC3339 reads a unix-epoch-seconds file and renders it as
// RFC3339 UTC. Missing files, garbage, and the 0 epoch (no error recorded)
// yield "".
func readEpochRFC3339(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	secs, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || secs <= 0 {
		return ""
	}
	return time.Unix(secs, 0).UTC().Format(time.RFC3339)
}

// readTrimmed reads a file and returns its whitespace-trimmed content, or
// "" if the file is missing/unreadable.
func readTrimmed(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// collectBtrfs enriches btrfs mounts with `btrfs device stats` counters.
// Filesystems mounted at multiple points (subvolumes) are queried once per
// source device. Returns a note instead of stats when btrfs mounts exist
// but the CLI is unavailable; per-mount command failures skip that mount.
func (t *Tool) collectBtrfs(ctx context.Context, entries []mountEntry) ([]BtrfsDeviceStats, string) {
	type btrfsMount struct{ mount, source string }
	var mounts []btrfsMount
	seen := map[string]bool{}
	for _, e := range entries {
		if e.fstype != "btrfs" || !strings.HasPrefix(e.source, "/dev/") {
			continue
		}
		if seen[e.source] {
			continue
		}
		seen[e.source] = true
		mounts = append(mounts, btrfsMount{mount: e.mountPoint, source: e.source})
	}
	if len(mounts) == 0 {
		return nil, ""
	}

	if _, err := t.lookPath("btrfs"); err != nil {
		return nil, "btrfs binary not found; btrfs device error counters unavailable"
	}

	var out []BtrfsDeviceStats
	for _, m := range mounts {
		if ctx.Err() != nil {
			return out, ""
		}
		raw, err := t.execCommand(ctx, "btrfs", "device", "stats", m.mount)
		if err != nil {
			continue // best-effort enrichment: skip this filesystem
		}
		out = append(out, parseBtrfsStats(m.mount, raw)...)
	}
	return out, ""
}

// parseBtrfsStats parses `btrfs device stats` output lines of the form
// "[/dev/sda1].write_io_errs   0" into per-device counter sets, preserving
// first-seen device order. Unknown counters and garbage lines are ignored.
func parseBtrfsStats(mount string, out []byte) []BtrfsDeviceStats {
	var stats []BtrfsDeviceStats
	index := map[string]int{}

	for _, line := range strings.Split(string(out), "\n") {
		m := btrfsStatRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		device, counter := m[1], m[2]
		value, err := strconv.ParseUint(m[3], 10, 64)
		if err != nil {
			continue
		}

		i, ok := index[device]
		if !ok {
			stats = append(stats, BtrfsDeviceStats{Mount: mount, Device: device})
			i = len(stats) - 1
			index[device] = i
		}
		switch counter {
		case "write_io_errs":
			stats[i].WriteIOErrs = value
		case "read_io_errs":
			stats[i].ReadIOErrs = value
		case "flush_io_errs":
			stats[i].FlushIOErrs = value
		case "corruption_errs":
			stats[i].CorruptionErrs = value
		case "generation_errs":
			stats[i].GenerationErrs = value
		}
	}
	return stats
}

// buildWarnings derives warning_reasons from the collected findings:
// recorded ext4 errors, unexpected read-only mounts, and nonzero btrfs
// counters.
func buildWarnings(ext4 []Ext4Device, readonly []ReadonlyMount, btrfs []BtrfsDeviceStats) []string {
	warnings := []string{}
	for _, d := range ext4 {
		if d.ErrorsCount > 0 {
			warnings = append(warnings, fmt.Sprintf(
				"ext4 %s has recorded %d filesystem errors since last fsck — check dmesg",
				d.Device, d.ErrorsCount))
		}
	}
	for _, m := range readonly {
		warnings = append(warnings, fmt.Sprintf(
			"%s is mounted read-only — possible error-triggered remount", m.Mount))
	}
	for _, b := range btrfs {
		if b.WriteIOErrs > 0 || b.ReadIOErrs > 0 || b.FlushIOErrs > 0 || b.CorruptionErrs > 0 || b.GenerationErrs > 0 {
			warnings = append(warnings, fmt.Sprintf(
				"btrfs device %s (%s) reports errors: write_io_errs=%d read_io_errs=%d flush_io_errs=%d corruption_errs=%d generation_errs=%d",
				b.Device, b.Mount, b.WriteIOErrs, b.ReadIOErrs, b.FlushIOErrs, b.CorruptionErrs, b.GenerationErrs))
		}
	}
	return warnings
}
