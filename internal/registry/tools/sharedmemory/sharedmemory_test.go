package sharedmemory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// modernShm is a modern-kernel /proc/sysvipc/shm fixture (16 columns).
// Segment 10: 2 MiB, 2 attached, creator pid 1234.
// Segment 11: 8 MiB, 0 attached (orphaned), creator pid 4321, negative key.
const modernShm = `       key      shmid perms                  size  cpid  lpid nattch   uid   gid  cuid  cgid      atime      dtime      ctime                   rss                  swap
         0         10  1600               2097152  1234  1234      2  1000  1000  1000  1000 1783378526          0 1783378526                  4096                     0
-835583472         11   666               8388608  4321  4321      0  1000  1000  1000  1000 1783378526          0 1783378526                  4096                     0
`

// oldShm is an old-kernel fixture without the rss/swap columns (14 columns).
const oldShm = `       key      shmid perms       size  cpid  lpid nattch   uid   gid  cuid  cgid      atime      dtime      ctime
         0          3   1600    1048576   111   222      1     0     0     0     0 1400000000          0 1400000000
`

// mountinfoFixture mixes tmpfs, non-tmpfs, and a duplicate tmpfs mountpoint.
const mountinfoFixture = `36 25 0:31 / /dev/shm rw,nosuid,nodev shared:6 - tmpfs tmpfs rw
37 25 0:32 / /run rw,nosuid,nodev shared:7 master:1 - tmpfs tmpfs rw,mode=755
38 25 8:1 / / rw,relatime shared:1 - ext4 /dev/sda1 rw
39 25 0:33 / /run rw - tmpfs tmpfs rw
40 25 0:5 / /dev rw shared:2 - devtmpfs devtmpfs rw,size=16G
41 25 0:40 / /tmp rw - tmpfs tmpfs rw,size=8G
`

// writeProcTree builds a fake procfs root containing sysvipc/shm,
// self/mountinfo, and alive-pid directories.
func writeProcTree(t testing.TB, shm, mountinfo string, alivePids ...string) string {
	t.Helper()
	root := t.TempDir()
	if shm != "" {
		if err := os.MkdirAll(filepath.Join(root, "sysvipc"), 0o755); err != nil {
			t.Fatalf("mkdir sysvipc: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, "sysvipc", "shm"), []byte(shm), 0o644); err != nil {
			t.Fatalf("write shm: %v", err)
		}
	}
	if mountinfo != "" {
		if err := os.MkdirAll(filepath.Join(root, "self"), 0o755); err != nil {
			t.Fatalf("mkdir self: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, "self", "mountinfo"), []byte(mountinfo), 0o644); err != nil {
			t.Fatalf("write mountinfo: %v", err)
		}
	}
	for _, pid := range alivePids {
		if err := os.MkdirAll(filepath.Join(root, pid), 0o755); err != nil {
			t.Fatalf("mkdir pid %s: %v", pid, err)
		}
	}
	return root
}

// writeDevShmTree builds a fake /dev/shm with files of the given sizes.
func writeDevShmTree(t testing.TB, files map[string]int) string {
	t.Helper()
	root := t.TempDir()
	for name, size := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", name, err)
		}
		if err := os.WriteFile(path, bytes.Repeat([]byte{0xAB}, size), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}

// fakeStatfs returns a statfs stub reporting the given per-path block
// counts with a 1 MiB block size, so MB values are exact.
func fakeStatfs(blocksByPath map[string][2]uint64) func(string, *syscall.Statfs_t) error {
	return func(path string, buf *syscall.Statfs_t) error {
		spec, ok := blocksByPath[path]
		if !ok {
			return fmt.Errorf("no such mount %q", path)
		}
		buf.Bsize = 1024 * 1024
		buf.Blocks = spec[0]
		buf.Bavail = spec[1]
		return nil
	}
}

func TestSharedMemoryTool_ContractCompliance(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "get_shared_memory" {
		t.Errorf("Name() = %q, want get_shared_memory", tool.Name())
	}
	if tool.Category() != registry.CategoryMemory {
		t.Errorf("Category() = %q, want memory", tool.Category())
	}
	if tool.Description() == "" {
		t.Error("Description() must not be empty")
	}
	for _, src := range []string{"/proc/sysvipc/shm", "/dev/shm", "mountinfo"} {
		if !strings.Contains(tool.Help(), src) {
			t.Errorf("Help() must reference data source %q, got: %s", src, tool.Help())
		}
	}
	if !strings.Contains(tool.Help(), "80") {
		t.Errorf("Help() must document the tmpfs usage threshold, got: %s", tool.Help())
	}
	if tool.Parameters() != nil {
		t.Error("Parameters() must be nil (no parameters)")
	}
	if tool.Hidden() {
		t.Error("Hidden() must be false")
	}
}

func TestSharedMemoryTool_IsSupported(t *testing.T) {
	t.Parallel()

	t.Run("unsupported without sysvipc shm", func(t *testing.T) {
		t.Parallel()
		tool := New()
		tool.procfsRoot = t.TempDir()
		ok, reason := tool.IsSupported()
		if ok {
			t.Error("IsSupported() = true, want false when sysvipc/shm is missing")
		}
		if !strings.Contains(reason, "SysV IPC not available") {
			t.Errorf("reason = %q, want mention of SysV IPC not available", reason)
		}
	})

	t.Run("supported with sysvipc shm", func(t *testing.T) {
		t.Parallel()
		tool := New()
		tool.procfsRoot = writeProcTree(t, modernShm, "")
		ok, reason := tool.IsSupported()
		if !ok {
			t.Errorf("IsSupported() = false (%s), want true", reason)
		}
	})
}

func TestParseSysvShm(t *testing.T) {
	t.Parallel()

	t.Run("modern 16 column format", func(t *testing.T) {
		t.Parallel()
		segs, err := parseSysvShm([]byte(modernShm))
		if err != nil {
			t.Fatalf("parseSysvShm() error: %v", err)
		}
		if len(segs) != 2 {
			t.Fatalf("segments = %d, want 2", len(segs))
		}
		if segs[0].shmID != 10 || segs[0].sizeBytes != 2097152 || segs[0].cpid != 1234 || segs[0].lpid != 1234 || segs[0].nattch != 2 {
			t.Errorf("segment[0] = %+v, want shmid=10 size=2097152 cpid=1234 lpid=1234 nattch=2", segs[0])
		}
		if segs[1].shmID != 11 || segs[1].sizeBytes != 8388608 || segs[1].nattch != 0 {
			t.Errorf("segment[1] = %+v, want shmid=11 size=8388608 nattch=0", segs[1])
		}
	})

	t.Run("old 14 column format", func(t *testing.T) {
		t.Parallel()
		segs, err := parseSysvShm([]byte(oldShm))
		if err != nil {
			t.Fatalf("parseSysvShm() error: %v", err)
		}
		if len(segs) != 1 {
			t.Fatalf("segments = %d, want 1", len(segs))
		}
		if segs[0].shmID != 3 || segs[0].sizeBytes != 1048576 || segs[0].cpid != 111 || segs[0].lpid != 222 || segs[0].nattch != 1 {
			t.Errorf("segment[0] = %+v, want shmid=3 size=1048576 cpid=111 lpid=222 nattch=1", segs[0])
		}
	})

	t.Run("header only means zero segments", func(t *testing.T) {
		t.Parallel()
		header := strings.SplitN(modernShm, "\n", 2)[0] + "\n"
		segs, err := parseSysvShm([]byte(header))
		if err != nil {
			t.Fatalf("parseSysvShm() error: %v", err)
		}
		if len(segs) != 0 {
			t.Errorf("segments = %d, want 0", len(segs))
		}
	})

	t.Run("malformed rows are skipped", func(t *testing.T) {
		t.Parallel()
		data := strings.SplitN(modernShm, "\n", 2)[0] + "\n" +
			"0 99 1600 notanumber 1 1 0 0 0 0 0 0 0 0 0 0\n" + // bad size
			"0 12 1600 4096 7 7 1 0 0 0 0 0 0 0 0 0\n" + // valid
			"short row\n"
		segs, err := parseSysvShm([]byte(data))
		if err != nil {
			t.Fatalf("parseSysvShm() error: %v", err)
		}
		if len(segs) != 1 {
			t.Fatalf("segments = %d, want 1 (malformed rows skipped)", len(segs))
		}
		if segs[0].shmID != 12 {
			t.Errorf("segment shmid = %d, want 12", segs[0].shmID)
		}
	})

	t.Run("missing required column is an error", func(t *testing.T) {
		t.Parallel()
		data := "key shmid perms size cpid lpid uid\n0 1 1600 4096 1 1 0\n" // no nattch
		if _, err := parseSysvShm([]byte(data)); err == nil {
			t.Error("parseSysvShm() = nil error, want error for missing nattch column")
		}
	})

	t.Run("empty file is an error", func(t *testing.T) {
		t.Parallel()
		if _, err := parseSysvShm([]byte("")); err == nil {
			t.Error("parseSysvShm() = nil error, want error for empty file")
		}
	})
}

func TestBuildSysv(t *testing.T) {
	t.Parallel()

	t.Run("orphans totals and liveness", func(t *testing.T) {
		t.Parallel()
		procRoot := writeProcTree(t, "", "", "1234") // only pid 1234 alive
		segs := []rawSegment{
			{shmID: 10, sizeBytes: 2 * 1024 * 1024, cpid: 1234, lpid: 1234, nattch: 2},
			{shmID: 11, sizeBytes: 8 * 1024 * 1024, cpid: 4321, lpid: 4321, nattch: 0},
		}
		info := buildSysv(segs, procRoot)

		if info.SegmentCount != 2 {
			t.Errorf("SegmentCount = %d, want 2", info.SegmentCount)
		}
		if info.TotalMB != 10.0 {
			t.Errorf("TotalMB = %v, want 10.0", info.TotalMB)
		}
		if info.OrphanedCount != 1 {
			t.Errorf("OrphanedCount = %d, want 1", info.OrphanedCount)
		}
		if info.OrphanedMB != 8.0 {
			t.Errorf("OrphanedMB = %v, want 8.0", info.OrphanedMB)
		}
		if len(info.Segments) != 2 {
			t.Fatalf("Segments = %d, want 2", len(info.Segments))
		}
		// Sorted by size descending: the 8 MiB orphan first.
		top := info.Segments[0]
		if top.ShmID != 11 || top.SizeMB != 8.0 || !top.Orphaned || top.CreatorAlive {
			t.Errorf("top segment = %+v, want shmid=11 size_mb=8.0 orphaned=true creator_alive=false", top)
		}
		second := info.Segments[1]
		if second.ShmID != 10 || second.SizeMB != 2.0 || second.Orphaned || !second.CreatorAlive {
			t.Errorf("second segment = %+v, want shmid=10 size_mb=2.0 orphaned=false creator_alive=true", second)
		}
		if second.CreatorPID != 1234 || second.LastPID != 1234 {
			t.Errorf("second segment pids = %+v, want creator_pid=1234 last_pid=1234", second)
		}
	})

	t.Run("caps at top 20 by size but counts all", func(t *testing.T) {
		t.Parallel()
		var segs []rawSegment
		for i := 0; i < 25; i++ {
			segs = append(segs, rawSegment{
				shmID:     int64(i),
				sizeBytes: uint64(i+1) * 1024 * 1024,
				cpid:      1,
				nattch:    1,
			})
		}
		info := buildSysv(segs, t.TempDir())
		if len(info.Segments) != 20 {
			t.Fatalf("Segments = %d, want 20", len(info.Segments))
		}
		if info.SegmentCount != 25 {
			t.Errorf("SegmentCount = %d, want 25", info.SegmentCount)
		}
		if info.Segments[0].SizeMB != 25.0 {
			t.Errorf("largest segment = %v MB, want 25.0", info.Segments[0].SizeMB)
		}
		// Sum 1..25 MiB = 325 MB.
		if info.TotalMB != 325.0 {
			t.Errorf("TotalMB = %v, want 325.0", info.TotalMB)
		}
	})

	t.Run("no segments yields empty slice not nil", func(t *testing.T) {
		t.Parallel()
		info := buildSysv(nil, t.TempDir())
		if info.Segments == nil {
			t.Error("Segments must be an empty slice, not nil")
		}
		if info.SegmentCount != 0 || info.TotalMB != 0 {
			t.Errorf("info = %+v, want zeroes", info)
		}
	})
}

func TestListPosixShm(t *testing.T) {
	t.Parallel()

	t.Run("recursive listing with sizes and uid", func(t *testing.T) {
		t.Parallel()
		root := writeDevShmTree(t, map[string]int{
			"pulse-shm-123": 1024 * 1024, // 1.0 MB
			"sem.mysem":     10,          // 0.0 MB
			"app/ring.shm":  512 * 1024,  // 0.5 MB
		})
		info, err := listPosixShm(root)
		if err != nil {
			t.Fatalf("listPosixShm() error: %v", err)
		}
		if info.FileCount != 3 {
			t.Errorf("FileCount = %d, want 3", info.FileCount)
		}
		if info.TotalMB != 1.5 {
			t.Errorf("TotalMB = %v, want 1.5", info.TotalMB)
		}
		if len(info.Files) != 3 {
			t.Fatalf("Files = %d, want 3", len(info.Files))
		}
		top := info.Files[0]
		if top.Name != "pulse-shm-123" || top.SizeMB != 1.0 {
			t.Errorf("top file = %+v, want pulse-shm-123 at 1.0 MB", top)
		}
		if info.Files[1].Name != "app/ring.shm" || info.Files[1].SizeMB != 0.5 {
			t.Errorf("second file = %+v, want app/ring.shm at 0.5 MB", info.Files[1])
		}
		if top.UID != uint32(os.Getuid()) {
			t.Errorf("UID = %d, want %d", top.UID, os.Getuid())
		}
		if _, err := time.Parse(time.RFC3339, top.MTime); err != nil {
			t.Errorf("MTime = %q, want RFC3339: %v", top.MTime, err)
		}
	})

	t.Run("caps at top 20 by size but counts all", func(t *testing.T) {
		t.Parallel()
		files := make(map[string]int, 25)
		for i := 0; i < 25; i++ {
			files[fmt.Sprintf("f%02d", i)] = (i + 1) * 1024
		}
		root := writeDevShmTree(t, files)
		info, err := listPosixShm(root)
		if err != nil {
			t.Fatalf("listPosixShm() error: %v", err)
		}
		if len(info.Files) != 20 {
			t.Errorf("Files = %d, want 20", len(info.Files))
		}
		if info.FileCount != 25 {
			t.Errorf("FileCount = %d, want 25", info.FileCount)
		}
	})

	t.Run("empty directory", func(t *testing.T) {
		t.Parallel()
		info, err := listPosixShm(t.TempDir())
		if err != nil {
			t.Fatalf("listPosixShm() error: %v", err)
		}
		if info.Files == nil {
			t.Error("Files must be an empty slice, not nil")
		}
		if info.FileCount != 0 || info.TotalMB != 0 {
			t.Errorf("info = %+v, want zeroes", info)
		}
	})

	t.Run("unreadable root is an error", func(t *testing.T) {
		t.Parallel()
		if _, err := listPosixShm(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
			t.Error("listPosixShm() = nil error, want error for missing root")
		}
	})
}

func TestParseTmpfsMounts(t *testing.T) {
	t.Parallel()

	t.Run("tmpfs only with dedupe", func(t *testing.T) {
		t.Parallel()
		got := parseTmpfsMounts([]byte(mountinfoFixture))
		want := []string{"/dev/shm", "/run", "/tmp"}
		if len(got) != len(want) {
			t.Fatalf("parseTmpfsMounts() = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("mount[%d] = %q, want %q", i, got[i], want[i])
			}
		}
	})

	t.Run("garbage and empty input", func(t *testing.T) {
		t.Parallel()
		if got := parseTmpfsMounts([]byte("")); len(got) != 0 {
			t.Errorf("parseTmpfsMounts(empty) = %v, want none", got)
		}
		if got := parseTmpfsMounts([]byte("not a mountinfo line\nshort - x\n")); len(got) != 0 {
			t.Errorf("parseTmpfsMounts(garbage) = %v, want none", got)
		}
	})
}

func TestStatfsWithTimeout(t *testing.T) {
	t.Parallel()

	t.Run("success computes used and total bytes", func(t *testing.T) {
		t.Parallel()
		tool := New()
		tool.statfs = fakeStatfs(map[string][2]uint64{"/dev/shm": {100, 10}})
		used, total, err := tool.statfsWithTimeout(context.Background(), "/dev/shm")
		if err != nil {
			t.Fatalf("statfsWithTimeout() error: %v", err)
		}
		if total != 100*1024*1024 {
			t.Errorf("total = %d, want %d", total, 100*1024*1024)
		}
		if used != 90*1024*1024 {
			t.Errorf("used = %d, want %d", used, 90*1024*1024)
		}
	})

	t.Run("hung statfs times out", func(t *testing.T) {
		t.Parallel()
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })

		tool := New()
		tool.statfsTimeout = 5 * time.Millisecond
		tool.statfs = func(string, *syscall.Statfs_t) error {
			<-release
			return nil
		}
		_, _, err := tool.statfsWithTimeout(context.Background(), "/stuck")
		if err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Errorf("err = %v, want timeout error", err)
		}
	})

	t.Run("context cancellation returns promptly", func(t *testing.T) {
		t.Parallel()
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })

		tool := New() // default 2s timeout must NOT be waited out
		tool.statfs = func(string, *syscall.Statfs_t) error {
			<-release
			return nil
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		start := time.Now()
		_, _, err := tool.statfsWithTimeout(ctx, "/stuck")
		if err == nil {
			t.Error("err = nil, want context error")
		}
		if elapsed := time.Since(start); elapsed >= time.Second {
			t.Errorf("statfsWithTimeout took %v, must return promptly on cancellation", elapsed)
		}
	})
}

func TestSharedMemoryTool_Execute_HappyPathWithWarnings(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.procfsRoot = writeProcTree(t, modernShm, mountinfoFixture, "1234")
	tool.devShmRoot = writeDevShmTree(t, map[string]int{
		"pulse-shm-123": 1024 * 1024,
		"app/ring.shm":  512 * 1024,
	})
	tool.statfs = fakeStatfs(map[string][2]uint64{
		"/dev/shm": {100, 10}, // 90% used → warning
		"/run":     {100, 90}, // 10% used
		"/tmp":     {200, 150},
	})

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute returned hard error: %v", err)
	}
	if res.Status != registry.StatusWarning {
		t.Fatalf("Status = %q, want warning (summary: %s)", res.Status, res.Summary)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}

	// SysV block.
	if out.Sysv.SegmentCount != 2 || out.Sysv.OrphanedCount != 1 {
		t.Errorf("sysv = %+v, want 2 segments with 1 orphan", out.Sysv)
	}
	if out.Sysv.Segments[0].ShmID != 11 || !out.Sysv.Segments[0].Orphaned {
		t.Errorf("top sysv segment = %+v, want orphaned shmid 11", out.Sysv.Segments[0])
	}
	if !out.Sysv.Segments[1].CreatorAlive {
		t.Errorf("segment shmid 10 creator (pid 1234) must be alive, got %+v", out.Sysv.Segments[1])
	}

	// POSIX block.
	if out.Posix == nil {
		t.Fatal("posix block missing")
	}
	if out.Posix.FileCount != 2 || out.Posix.TotalMB != 1.5 {
		t.Errorf("posix = %+v, want 2 files totaling 1.5 MB", out.Posix)
	}

	// tmpfs block.
	if len(out.Tmpfs) != 3 {
		t.Fatalf("tmpfs = %+v, want 3 mounts", out.Tmpfs)
	}
	byMount := map[string]TmpfsMount{}
	for _, m := range out.Tmpfs {
		byMount[m.Mount] = m
	}
	devShm := byMount["/dev/shm"]
	if devShm.UsedPct != 90.0 || devShm.UsedMB != 90.0 || devShm.TotalMB != 100.0 || devShm.Status != "ok" {
		t.Errorf("/dev/shm = %+v, want 90.0/100.0 MB at 90.0%% ok", devShm)
	}

	// Warnings: 1 orphan + 1 hot tmpfs.
	if len(out.WarningReasons) != 2 {
		t.Fatalf("warning_reasons = %v, want 2", out.WarningReasons)
	}
	joined := strings.Join(out.WarningReasons, " | ")
	for _, want := range []string{"1 orphaned SysV segments (8.0 MB)", "MPI", "/dev/shm", "90.0%"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings %q must contain %q", joined, want)
		}
	}

	// Wire schema keys.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(res.Data, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	for _, key := range []string{"sysv", "posix", "tmpfs", "warning_reasons"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("payload missing %q key", key)
		}
	}
}

func TestSharedMemoryTool_Execute_QuietSystem(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.procfsRoot = writeProcTree(t, oldShm, mountinfoFixture, "111")
	tool.devShmRoot = writeDevShmTree(t, map[string]int{"x": 10})
	tool.statfs = fakeStatfs(map[string][2]uint64{
		"/dev/shm": {100, 90},
		"/run":     {100, 90},
		"/tmp":     {100, 90},
	})

	res, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute returned hard error: %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("Status = %q, want ok (summary: %s)", res.Status, res.Summary)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if len(out.WarningReasons) != 0 {
		t.Errorf("warning_reasons = %v, want none", out.WarningReasons)
	}
	if out.Sysv.OrphanedCount != 0 {
		t.Errorf("orphaned_count = %d, want 0", out.Sysv.OrphanedCount)
	}
}

func TestSharedMemoryTool_Execute_PosixOmittedWhenUnreadable(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.procfsRoot = writeProcTree(t, oldShm, mountinfoFixture, "111")
	tool.devShmRoot = filepath.Join(t.TempDir(), "missing-dev-shm")
	tool.statfs = fakeStatfs(map[string][2]uint64{
		"/dev/shm": {100, 90},
		"/run":     {100, 90},
		"/tmp":     {100, 90},
	})

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute returned hard error: %v", err)
	}
	if res.Status != registry.StatusDegraded {
		t.Fatalf("Status = %q, want degraded (summary: %s)", res.Status, res.Summary)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if out.Posix != nil {
		t.Errorf("posix = %+v, want omitted", out.Posix)
	}
	if len(out.Notes) == 0 || !strings.Contains(strings.Join(out.Notes, " "), "posix") {
		t.Errorf("notes = %v, want a posix degradation note", out.Notes)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(res.Data, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	if _, present := raw["posix"]; present {
		t.Error("posix key must be omitted from the wire payload")
	}
}

func TestSharedMemoryTool_Execute_HungTmpfsSkippedFromPctMath(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	tool := New()
	tool.procfsRoot = writeProcTree(t, oldShm, mountinfoFixture, "111")
	tool.devShmRoot = writeDevShmTree(t, map[string]int{"x": 10})
	tool.statfsTimeout = 5 * time.Millisecond
	tool.statfs = func(path string, buf *syscall.Statfs_t) error {
		if path == "/run" { // hang only /run
			<-release
			return nil
		}
		return fakeStatfs(map[string][2]uint64{
			"/dev/shm": {100, 95}, // 5% used — no warning
			"/tmp":     {100, 95},
		})(path, buf)
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute returned hard error: %v", err)
	}
	if res.Status != registry.StatusDegraded {
		t.Fatalf("Status = %q, want degraded (summary: %s)", res.Status, res.Summary)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	var hung *TmpfsMount
	for i := range out.Tmpfs {
		if out.Tmpfs[i].Mount == "/run" {
			hung = &out.Tmpfs[i]
		}
	}
	if hung == nil {
		t.Fatalf("tmpfs = %+v, want /run entry present", out.Tmpfs)
	}
	if hung.Status != "hung" {
		t.Errorf("/run status = %q, want hung", hung.Status)
	}
	if hung.UsedPct != 0 || hung.UsedMB != 0 || hung.TotalMB != 0 {
		t.Errorf("/run = %+v, hung mounts must be excluded from pct math", hung)
	}
	if len(out.WarningReasons) != 0 {
		t.Errorf("warning_reasons = %v, want none (hung mount must not warn)", out.WarningReasons)
	}
}

func TestSharedMemoryTool_Execute_MissingMountinfoDegrades(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.procfsRoot = writeProcTree(t, oldShm, "", "111") // no self/mountinfo
	tool.devShmRoot = writeDevShmTree(t, map[string]int{"x": 10})

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute returned hard error: %v", err)
	}
	if res.Status != registry.StatusDegraded {
		t.Fatalf("Status = %q, want degraded (summary: %s)", res.Status, res.Summary)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if len(out.Tmpfs) != 0 {
		t.Errorf("tmpfs = %+v, want empty", out.Tmpfs)
	}
	if out.Tmpfs == nil {
		t.Error("tmpfs must be an empty slice, not nil")
	}
	if len(out.Notes) == 0 || !strings.Contains(strings.Join(out.Notes, " "), "mountinfo") {
		t.Errorf("notes = %v, want a mountinfo degradation note", out.Notes)
	}
}

func TestSharedMemoryTool_Execute_MissingSysvShmIsError(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.procfsRoot = t.TempDir() // no sysvipc/shm

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute must encapsulate errors, got hard error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %q, want error when sysvipc/shm is unreadable", res.Status)
	}
}

func TestSharedMemoryTool_Execute_MalformedSysvHeaderIsError(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.procfsRoot = writeProcTree(t, "totally not a header\n", mountinfoFixture)

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute must encapsulate errors, got hard error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %q, want error for malformed sysvipc/shm", res.Status)
	}
}

func TestSharedMemoryTool_Execute_ContextAlreadyCanceled(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.procfsRoot = writeProcTree(t, modernShm, mountinfoFixture)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := tool.Execute(ctx, nil)
	if err != nil {
		t.Fatalf("Execute must encapsulate cancellation, got hard error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %q, want error on canceled context", res.Status)
	}
}

func TestSharedMemoryTool_Execute_ContextCanceledDuringStatfs(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tool := New() // default 2s statfs timeout must NOT be waited out
	tool.procfsRoot = writeProcTree(t, oldShm, mountinfoFixture, "111")
	tool.devShmRoot = writeDevShmTree(t, map[string]int{"x": 10})
	tool.statfs = func(string, *syscall.Statfs_t) error {
		cancel() // kill the context while Execute is inside the statfs guard
		<-release
		return nil
	}

	start := time.Now()
	res, err := tool.Execute(ctx, nil)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Execute must encapsulate cancellation, got hard error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %q, want error on mid-statfs cancellation", res.Status)
	}
	if !strings.Contains(strings.ToLower(res.Summary), "cancel") {
		t.Errorf("Summary = %q, want mention of cancellation", res.Summary)
	}
	if elapsed >= time.Second {
		t.Errorf("Execute took %v, must return promptly on cancellation", elapsed)
	}
}

func TestBuildWarnings(t *testing.T) {
	t.Parallel()

	t.Run("clean state has no warnings", func(t *testing.T) {
		t.Parallel()
		got := buildWarnings(SysvInfo{}, []TmpfsMount{{Mount: "/run", UsedPct: 50, Status: "ok"}})
		if len(got) != 0 {
			t.Errorf("buildWarnings() = %v, want none", got)
		}
	})

	t.Run("orphans and hot tmpfs warn", func(t *testing.T) {
		t.Parallel()
		got := buildWarnings(
			SysvInfo{OrphanedCount: 3, OrphanedMB: 24.5},
			[]TmpfsMount{
				{Mount: "/dev/shm", UsedPct: 80.1, Status: "ok"},
				{Mount: "/run", UsedPct: 99.0, Status: "hung"}, // hung → no pct warning
			},
		)
		if len(got) != 2 {
			t.Fatalf("buildWarnings() = %v, want 2", got)
		}
		if !strings.Contains(got[0], "3 orphaned SysV segments (24.5 MB)") || !strings.Contains(got[0], "MPI") {
			t.Errorf("orphan warning = %q, want count, MB, and MPI mention", got[0])
		}
		if !strings.Contains(got[1], "/dev/shm") || !strings.Contains(got[1], "80.1%") {
			t.Errorf("tmpfs warning = %q, want mount and pct", got[1])
		}
	})

	t.Run("exactly 80 pct does not warn", func(t *testing.T) {
		t.Parallel()
		got := buildWarnings(SysvInfo{}, []TmpfsMount{{Mount: "/dev/shm", UsedPct: 80.0, Status: "ok"}})
		if len(got) != 0 {
			t.Errorf("buildWarnings() = %v, want none at exactly 80%%", got)
		}
	})
}

func TestToMB(t *testing.T) {
	t.Parallel()

	tests := []struct {
		bytes uint64
		want  float64
	}{
		{bytes: 0, want: 0},
		{bytes: 1024 * 1024, want: 1.0},
		{bytes: 512 * 1024, want: 0.5},
		{bytes: 8 * 1024 * 1024, want: 8.0},
	}
	for _, tt := range tests {
		if got := toMB(tt.bytes); got != tt.want {
			t.Errorf("toMB(%d) = %v, want %v", tt.bytes, got, tt.want)
		}
	}
}

func TestRound1(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   float64
		want float64
	}{
		{in: 90.04, want: 90.0},
		{in: 90.05, want: 90.1},
		{in: 1.50000954, want: 1.5},
		{in: 0, want: 0},
	}
	for _, tt := range tests {
		if got := round1(tt.in); got != tt.want {
			t.Errorf("round1(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
