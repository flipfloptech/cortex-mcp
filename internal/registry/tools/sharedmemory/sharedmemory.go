// Package sharedmemory implements the get_shared_memory tool.
//
// It inventories the three shared-memory surfaces on a Linux node:
// SysV IPC segments (/proc/sysvipc/shm), POSIX shared memory files
// (/dev/shm), and tmpfs mount usage (/proc/self/mountinfo + statfs).
// Orphaned SysV segments — attached by nobody but still pinning RAM —
// are the classic residue of crashed MPI jobs, and full tmpfs mounts
// silently eat page cache until allocations stall.
package sharedmemory

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// tmpfsUsageWarnPct is the tmpfs used_pct threshold above which a warning
// is emitted.
const tmpfsUsageWarnPct = 80.0

// topEntries caps the SysV segment and POSIX file lists at the largest N
// entries so payloads stay LLM-sized on hosts with thousands of segments.
const topEntries = 20

// SysvSegment is one SysV shared memory segment.
type SysvSegment struct {
	// ShmID is the kernel segment identifier (shmid).
	ShmID int64 `json:"shmid"`

	// SizeMB is the segment size in megabytes (1 decimal place).
	SizeMB float64 `json:"size_mb"`

	// Nattch is the number of processes currently attached.
	Nattch uint64 `json:"nattch"`

	// CreatorPID is the pid that created the segment (cpid).
	CreatorPID int `json:"creator_pid"`

	// CreatorAlive reports whether the creator pid still exists in procfs.
	CreatorAlive bool `json:"creator_alive"`

	// LastPID is the pid of the last shmat/shmdt caller (lpid).
	LastPID int `json:"last_pid"`

	// Orphaned is true when no process is attached (nattch == 0) — the
	// segment holds memory that nobody is using.
	Orphaned bool `json:"orphaned"`
}

// SysvInfo aggregates the SysV IPC view.
type SysvInfo struct {
	// Segments are the top 20 segments by size, descending.
	Segments []SysvSegment `json:"segments"`

	// SegmentCount is the total number of segments (not just the top 20).
	SegmentCount int `json:"segment_count"`

	// TotalMB is the summed size of ALL segments in megabytes.
	TotalMB float64 `json:"total_mb"`

	// OrphanedCount / OrphanedMB describe segments with nattch == 0.
	OrphanedCount int     `json:"orphaned_count"`
	OrphanedMB    float64 `json:"orphaned_mb"`
}

// PosixFile is one file under /dev/shm (POSIX shared memory or semaphore).
type PosixFile struct {
	// Name is the path relative to /dev/shm.
	Name string `json:"name"`

	// SizeMB is the file size in megabytes (1 decimal place).
	SizeMB float64 `json:"size_mb"`

	// UID is the owning user id (from syscall.Stat_t).
	UID uint32 `json:"uid"`

	// MTime is the last modification time (RFC3339).
	MTime string `json:"mtime"`
}

// PosixInfo aggregates the /dev/shm view.
type PosixInfo struct {
	// Files are the top 20 files by size, descending.
	Files []PosixFile `json:"files"`

	// FileCount is the total number of files (not just the top 20).
	FileCount int `json:"file_count"`

	// TotalMB is the summed size of ALL files in megabytes.
	TotalMB float64 `json:"total_mb"`
}

// TmpfsMount is one tmpfs mount with its capacity usage.
type TmpfsMount struct {
	// Mount is the mount point path.
	Mount string `json:"mount"`

	// UsedMB / TotalMB are megabytes (1 decimal place). Zero when hung.
	UsedMB  float64 `json:"used_mb"`
	TotalMB float64 `json:"total_mb"`

	// UsedPct is used/total*100 (1 decimal place). Zero when hung.
	UsedPct float64 `json:"used_pct"`

	// Status is "ok" or "hung" (statfs timed out — excluded from pct math).
	Status string `json:"status"`
}

// Output is the tool's data payload.
type Output struct {
	// Sysv is the SysV IPC segment inventory.
	Sysv SysvInfo `json:"sysv"`

	// Posix is the /dev/shm inventory; omitted when /dev/shm is unreadable
	// (a note explains why).
	Posix *PosixInfo `json:"posix,omitempty"`

	// Tmpfs lists every tmpfs mount with capacity usage.
	Tmpfs []TmpfsMount `json:"tmpfs"`

	// Notes records non-fatal degradations (unreadable /dev/shm, missing
	// mountinfo, hung statfs).
	Notes []string `json:"notes,omitempty"`

	// WarningReasons lists pre-evaluated leak/capacity signals.
	WarningReasons []string `json:"warning_reasons"`
}

// Tool implements registry.Tool for get_shared_memory.
type Tool struct {
	// procfsRoot is the procfs mount point, injectable for hermetic tests.
	procfsRoot string

	// devShmRoot is the POSIX shared memory directory, injectable for tests.
	devShmRoot string

	// statfs is the statfs syscall, injectable so tests can fake or hang it.
	statfs func(path string, buf *syscall.Statfs_t) error

	// statfsTimeout bounds each statfs call (hung network-backed or dying
	// mounts must not wedge the tool).
	statfsTimeout time.Duration
}

// New returns a shared memory tool bound to the real /proc, /dev/shm, and
// statfs syscall.
func New() *Tool {
	return &Tool{
		procfsRoot:    "/proc",
		devShmRoot:    "/dev/shm",
		statfs:        syscall.Statfs,
		statfsTimeout: 2 * time.Second,
	}
}

func init() {
	registry.Register(New())
}

// Name returns the unique tool identifier.
func (t *Tool) Name() string { return "get_shared_memory" }

// Description returns the one-line summary for tool discovery.
func (t *Tool) Description() string {
	return "Inventory SysV shared memory segments, POSIX /dev/shm files, and tmpfs usage to find leaked segments and full mounts"
}

// Help returns the full tool documentation.
func (t *Tool) Help() string {
	return `get_shared_memory — Shared Memory & tmpfs Inventory

Inventories all three shared-memory surfaces on the node and pre-evaluates
the classic failure signatures: orphaned SysV segments (created, then the
owner died without shmctl(IPC_RMID) — the canonical crashed-MPI-job leak)
and tmpfs mounts filling up (they consume RAM and evict page cache).

Data Sources:
  - /proc/sysvipc/shm — SysV segments, parsed by header column names so
    varying kernel column layouts (with/without rss+swap) are handled.
  - /dev/shm — recursive listing of POSIX shared memory files with size,
    owner uid (syscall.Stat_t), and mtime.
  - /proc/self/mountinfo — tmpfs mounts, sized via statfs with a strict
    2-second hung-mount guard per mount.

Output:
  - sysv: top 20 segments by size {shmid, size_mb, nattch, creator_pid,
    creator_alive, last_pid, orphaned}, plus segment_count, total_mb,
    orphaned_count, orphaned_mb. A segment is orphaned when nattch == 0;
    creator_alive checks /proc/<cpid> existence.
  - posix: top 20 /dev/shm files by size {name, size_mb, uid, mtime}, plus
    file_count and total_mb.
  - tmpfs: every tmpfs mount {mount, used_mb, total_mb, used_pct, status}.

Warning Heuristics:
  - orphaned_count > 0: "N orphaned SysV segments (M MB) — likely leaked by
    exited processes (common MPI failure)".
  - any tmpfs used_pct > 80: mount is nearly full.

Degradation Profile:
  - IsSupported() is false when /proc/sysvipc/shm is missing
    ("SysV IPC not available").
  - Unreadable /dev/shm: posix block omitted, explained in notes,
    status degraded.
  - statfs timeout on a mount: entry reported with status "hung", excluded
    from used_pct math and warnings, noted, status degraded.
  - Missing /proc/self/mountinfo: empty tmpfs list plus a note.

Parameters: None
Supported on: Linux`
}

// Category classifies this tool under memory.
func (t *Tool) Category() registry.Category { return registry.CategoryMemory }

// Parameters returns nil — this tool takes no arguments.
func (t *Tool) Parameters() []registry.ToolParam { return nil }

// Hidden returns false — this tool is LLM-discoverable.
func (t *Tool) Hidden() bool { return false }

// IsSupported requires the SysV IPC shm table in procfs.
func (t *Tool) IsSupported() (bool, string) {
	if !registry.PathExists(filepath.Join(t.procfsRoot, "sysvipc", "shm")) {
		return false, "SysV IPC not available"
	}
	return true, ""
}

// Execute inventories SysV segments, /dev/shm files, and tmpfs mounts.
// All failures are encapsulated as error results.
func (t *Tool) Execute(ctx context.Context, _ json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	if err := ctx.Err(); err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("context canceled: %v", err)), nil
	}

	// SysV IPC segments — the gating data source.
	shmPath := filepath.Join(t.procfsRoot, "sysvipc", "shm")
	shmData, err := os.ReadFile(shmPath)
	if err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("failed to read %s: %v", shmPath, err)), nil
	}
	segments, err := parseSysvShm(shmData)
	if err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("failed to parse %s: %v", shmPath, err)), nil
	}

	out := Output{
		Sysv:           buildSysv(segments, t.procfsRoot),
		Tmpfs:          []TmpfsMount{},
		WarningReasons: []string{},
	}

	// POSIX shared memory files — degrade gracefully when unreadable.
	posix, err := listPosixShm(t.devShmRoot)
	if err != nil {
		out.Notes = append(out.Notes, fmt.Sprintf("posix (%s) listing unavailable: %v", t.devShmRoot, err))
	} else {
		out.Posix = posix
	}

	// tmpfs mounts — degrade gracefully when mountinfo is unreadable.
	mountinfoPath := filepath.Join(t.procfsRoot, "self", "mountinfo")
	mountData, err := os.ReadFile(mountinfoPath)
	if err != nil {
		out.Notes = append(out.Notes, fmt.Sprintf("tmpfs mounts unavailable: failed to read %s: %v", mountinfoPath, err))
	} else {
		tmpfs, notes, err := t.collectTmpfs(ctx, parseTmpfsMounts(mountData))
		if err != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("context canceled during tmpfs statfs: %v", err)), nil
		}
		out.Tmpfs = tmpfs
		out.Notes = append(out.Notes, notes...)
	}

	out.WarningReasons = buildWarnings(out.Sysv, out.Tmpfs)

	status := registry.StatusOK
	summary := fmt.Sprintf("%d SysV segments (%.1f MB), %d tmpfs mounts, no shared memory issues",
		out.Sysv.SegmentCount, out.Sysv.TotalMB, len(out.Tmpfs))
	switch {
	case len(out.WarningReasons) > 0:
		status = registry.StatusWarning
		summary = fmt.Sprintf("Shared memory issues: %s", strings.Join(out.WarningReasons, "; "))
	case len(out.Notes) > 0:
		status = registry.StatusDegraded
		summary = fmt.Sprintf("Shared memory inventoried with degradations: %s", strings.Join(out.Notes, "; "))
	}

	result := registry.NewResult(t.Name(), status, summary, out)
	result.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	result.Metadata.FilteringMethod = "heuristic"
	return result, nil
}

// rawSegment is one parsed /proc/sysvipc/shm row.
type rawSegment struct {
	shmID     int64
	sizeBytes uint64
	cpid      int
	lpid      int
	nattch    uint64
}

// parseSysvShm decodes /proc/sysvipc/shm. The first line is a header of
// column names; rows are parsed by header position so kernels with more or
// fewer columns (e.g. rss/swap added later) all work. Malformed rows are
// skipped; a header missing a required column is an error.
func parseSysvShm(data []byte) ([]rawSegment, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	if !scanner.Scan() {
		return nil, fmt.Errorf("empty file")
	}
	header := strings.Fields(scanner.Text())
	col := make(map[string]int, len(header))
	for i, name := range header {
		col[name] = i
	}
	for _, required := range []string{"shmid", "size", "cpid", "lpid", "nattch"} {
		if _, ok := col[required]; !ok {
			return nil, fmt.Errorf("header missing %q column", required)
		}
	}

	field := func(fields []string, name string) (string, bool) {
		idx := col[name]
		if idx >= len(fields) {
			return "", false
		}
		return fields[idx], true
	}

	var segments []rawSegment
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		var (
			seg rawSegment
			ok  = true
		)
		if s, present := field(fields, "shmid"); present {
			v, err := strconv.ParseInt(s, 10, 64)
			ok = ok && err == nil
			seg.shmID = v
		} else {
			ok = false
		}
		if s, present := field(fields, "size"); present {
			v, err := strconv.ParseUint(s, 10, 64)
			ok = ok && err == nil
			seg.sizeBytes = v
		} else {
			ok = false
		}
		if s, present := field(fields, "cpid"); present {
			v, err := strconv.Atoi(s)
			ok = ok && err == nil
			seg.cpid = v
		} else {
			ok = false
		}
		if s, present := field(fields, "lpid"); present {
			v, err := strconv.Atoi(s)
			ok = ok && err == nil
			seg.lpid = v
		} else {
			ok = false
		}
		if s, present := field(fields, "nattch"); present {
			v, err := strconv.ParseUint(s, 10, 64)
			ok = ok && err == nil
			seg.nattch = v
		} else {
			ok = false
		}
		if !ok {
			continue
		}
		segments = append(segments, seg)
	}
	return segments, nil
}

// buildSysv aggregates raw segments: totals over ALL segments, the top 20
// by size (descending, shmid tiebreak) with creator-liveness resolved
// against procfsRoot.
func buildSysv(segments []rawSegment, procfsRoot string) SysvInfo {
	info := SysvInfo{Segments: []SysvSegment{}}

	var totalBytes, orphanedBytes uint64
	for _, seg := range segments {
		totalBytes += seg.sizeBytes
		if seg.nattch == 0 {
			info.OrphanedCount++
			orphanedBytes += seg.sizeBytes
		}
	}
	info.SegmentCount = len(segments)
	info.TotalMB = toMB(totalBytes)
	info.OrphanedMB = toMB(orphanedBytes)

	sorted := make([]rawSegment, len(segments))
	copy(sorted, segments)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].sizeBytes != sorted[j].sizeBytes {
			return sorted[i].sizeBytes > sorted[j].sizeBytes
		}
		return sorted[i].shmID < sorted[j].shmID
	})
	if len(sorted) > topEntries {
		sorted = sorted[:topEntries]
	}

	for _, seg := range sorted {
		info.Segments = append(info.Segments, SysvSegment{
			ShmID:        seg.shmID,
			SizeMB:       toMB(seg.sizeBytes),
			Nattch:       seg.nattch,
			CreatorPID:   seg.cpid,
			CreatorAlive: registry.PathExists(filepath.Join(procfsRoot, strconv.Itoa(seg.cpid))),
			LastPID:      seg.lpid,
			Orphaned:     seg.nattch == 0,
		})
	}
	return info
}

// listPosixShm recursively lists files under the POSIX shared memory root
// (normally /dev/shm). An unreadable root is an error (the caller omits the
// posix block); unreadable entries below the root are skipped silently.
func listPosixShm(root string) (*PosixInfo, error) {
	if _, err := os.ReadDir(root); err != nil {
		return nil, err
	}

	type rawFile struct {
		name      string
		sizeBytes uint64
		uid       uint32
		mtime     time.Time
	}
	var files []rawFile
	var totalBytes uint64

	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return nil //nolint:nilerr // unreadable entries are skipped, not fatal
		}
		fi, err := d.Info()
		if err != nil {
			return nil //nolint:nilerr // entry vanished mid-walk
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil //nolint:nilerr // outside root, ignore
		}
		f := rawFile{name: rel, sizeBytes: uint64(fi.Size()), mtime: fi.ModTime()}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			f.uid = st.Uid
		}
		files = append(files, f)
		totalBytes += f.sizeBytes
		return nil
	})

	info := &PosixInfo{Files: []PosixFile{}, FileCount: len(files), TotalMB: toMB(totalBytes)}

	sort.Slice(files, func(i, j int) bool {
		if files[i].sizeBytes != files[j].sizeBytes {
			return files[i].sizeBytes > files[j].sizeBytes
		}
		return files[i].name < files[j].name
	})
	if len(files) > topEntries {
		files = files[:topEntries]
	}
	for _, f := range files {
		info.Files = append(info.Files, PosixFile{
			Name:   f.name,
			SizeMB: toMB(f.sizeBytes),
			UID:    f.uid,
			MTime:  f.mtime.UTC().Format(time.RFC3339),
		})
	}
	return info, nil
}

// parseTmpfsMounts extracts tmpfs mount points from mountinfo content, in
// order of appearance, deduplicated. The filesystem type is the first field
// after the "-" separator (mountinfo has a variable number of optional
// fields before it).
func parseTmpfsMounts(data []byte) []string {
	var mounts []string
	seen := make(map[string]bool)

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 7 {
			continue
		}
		sepIdx := -1
		for i, f := range fields {
			if f == "-" {
				sepIdx = i
				break
			}
		}
		if sepIdx == -1 || sepIdx+1 >= len(fields) {
			continue
		}
		if fields[sepIdx+1] != "tmpfs" {
			continue
		}
		mountPoint := fields[4]
		if seen[mountPoint] {
			continue
		}
		seen[mountPoint] = true
		mounts = append(mounts, mountPoint)
	}
	return mounts
}

// statfsWithTimeout runs statfs in a goroutine bounded by statfsTimeout so
// a hung mount (dying tmpfs is rare, but the guard is free) cannot wedge
// the tool. Returns used and total bytes.
func (t *Tool) statfsWithTimeout(ctx context.Context, path string) (used, total uint64, err error) {
	type result struct {
		buf syscall.Statfs_t
		err error
	}
	ch := make(chan result, 1)
	fn := t.statfs
	go func() {
		var buf syscall.Statfs_t
		err := fn(path, &buf)
		ch <- result{buf: buf, err: err}
	}()

	timer := time.NewTimer(t.statfsTimeout)
	defer timer.Stop()

	select {
	case res := <-ch:
		if res.err != nil {
			return 0, 0, fmt.Errorf("statfs error: %v", res.err)
		}
		bsize := uint64(res.buf.Bsize) //nolint:gosec // Bsize is never negative
		total = res.buf.Blocks * bsize
		avail := res.buf.Bavail * bsize
		if total > avail {
			used = total - avail
		}
		return used, total, nil
	case <-timer.C:
		return 0, 0, fmt.Errorf("statfs timed out after %dms", t.statfsTimeout.Milliseconds())
	case <-ctx.Done():
		return 0, 0, ctx.Err()
	}
}

// collectTmpfs sizes every tmpfs mount. Hung mounts are reported with
// status "hung" (zero sizes, excluded from pct math) plus a note. A dead
// context aborts the walk with an error.
func (t *Tool) collectTmpfs(ctx context.Context, mounts []string) ([]TmpfsMount, []string, error) {
	entries := []TmpfsMount{}
	var notes []string

	for _, mount := range mounts {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		used, total, err := t.statfsWithTimeout(ctx, mount)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			entries = append(entries, TmpfsMount{Mount: mount, Status: "hung"})
			notes = append(notes, fmt.Sprintf("tmpfs %s: %v", mount, err))
			continue
		}
		entry := TmpfsMount{
			Mount:   mount,
			UsedMB:  toMB(used),
			TotalMB: toMB(total),
			Status:  "ok",
		}
		if total > 0 {
			entry.UsedPct = round1(float64(used) / float64(total) * 100)
		}
		entries = append(entries, entry)
	}
	return entries, notes, nil
}

// buildWarnings derives warning_reasons: orphaned SysV segments (the
// classic leaked-by-crashed-MPI signature) and tmpfs mounts above the
// usage threshold. Hung mounts never warn — their usage is unknown.
func buildWarnings(sysv SysvInfo, tmpfs []TmpfsMount) []string {
	warnings := []string{}
	if sysv.OrphanedCount > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"%d orphaned SysV segments (%.1f MB) — likely leaked by exited processes (common MPI failure)",
			sysv.OrphanedCount, sysv.OrphanedMB))
	}
	for _, m := range tmpfs {
		if m.Status == "ok" && m.UsedPct > tmpfsUsageWarnPct {
			warnings = append(warnings, fmt.Sprintf(
				"tmpfs %s is %.1f%% full (%.1f of %.1f MB)", m.Mount, m.UsedPct, m.UsedMB, m.TotalMB))
		}
	}
	return warnings
}

// toMB converts bytes to megabytes rounded to 1 decimal place.
func toMB(bytes uint64) float64 {
	return round1(float64(bytes) / (1024.0 * 1024.0))
}

// round1 rounds to 1 decimal place for stable, LLM-friendly output.
func round1(v float64) float64 {
	return math.Round(v*10) / 10
}
