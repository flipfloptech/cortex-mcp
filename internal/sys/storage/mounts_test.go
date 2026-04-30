package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestGetMountStats(t *testing.T) {
	// Create mock proc base
	tmpDir := t.TempDir()
	procBase := filepath.Join(tmpDir, "proc")
	err := os.MkdirAll(filepath.Join(procBase, "self"), 0755)
	if err != nil {
		t.Fatalf("failed to create mock proc: %v", err)
	}

	mountInfoData := `23 28 0:21 / /sys rw,nosuid,nodev,noexec,relatime shared:7 - sysfs sysfs rw
24 28 0:22 / /proc rw,nosuid,nodev,noexec,relatime shared:14 - proc proc rw
25 28 0:5 / /dev rw,nosuid,relatime shared:2 - devtmpfs devtmpfs rw,size=4013144k,nr_inodes=1003286,mode=755
26 25 0:23 / /dev/pts rw,nosuid,noexec,relatime shared:3 - devpts devpts rw,gid=5,mode=620,ptmxmode=000
27 28 0:24 / /run rw,nosuid,nodev,noexec,relatime shared:5 - tmpfs tmpfs rw,size=805096k,mode=755
28 1 259:2 / / rw,relatime shared:1 - ext4 /dev/nvme0n1p2 rw,errors=remount-ro
39 28 0:35 / /sys/fs/cgroup rw,nosuid,nodev,noexec,relatime shared:10 - cgroup2 cgroup2 rw,nsdelegate,memory_recursiveprot
40 28 259:1 / /boot/efi rw,relatime shared:16 - vfat /dev/nvme0n1p1 rw,fmask=0077,dmask=0077,codepage=437,iocharset=iso8859-1,shortname=mixed,errors=remount-ro
41 28 0:36 / /mnt/lustre rw,relatime shared:18 - lustre 10.0.0.51@o2ib:/exafs rw,localflock
42 28 0:37 / /mnt/nfs rw,relatime shared:20 - nfs4 nfs-server:/export rw,vers=4.2
`

	err = os.WriteFile(filepath.Join(procBase, "self", "mountinfo"), []byte(mountInfoData), 0644)
	if err != nil {
		t.Fatalf("failed to write mock mountinfo: %v", err)
	}

	// Mock statfsFunc
	origStatfsFunc := statfsFunc
	defer func() { statfsFunc = origStatfsFunc }()

	statfsFunc = func(path string, buf *unix.Statfs_t) error {
		if path == "/mnt/lustre" {
			// Simulate a hung mount
			time.Sleep(100 * time.Millisecond) // we'll use a short timeout in the test context
			return fmt.Errorf("timeout")
		}

		// Mock typical ext4 stats for /
		if path == "/" {
			buf.Type = 0xEF53
			buf.Bsize = 4096
			buf.Blocks = 26214400 // 100 GiB
			buf.Bfree = 3801088   // 14.5 GiB free -> 85.5 GiB used
			buf.Bavail = 3801088
			buf.Files = 6553600
			buf.Ffree = 6103600 // 450,000 used
			return nil
		}

		// Mock vfat stats for /boot/efi
		if path == "/boot/efi" {
			buf.Type = 0x4d44
			buf.Bsize = 512
			buf.Blocks = 1048576 // 512 MiB
			buf.Bfree = 524288   // 256 MiB free -> 256 MiB used
			buf.Bavail = 524288
			buf.Files = 0 // vfat doesn't use inodes
			buf.Ffree = 0
			return nil
		}

		// Mock nfs stats for /mnt/nfs
		if path == "/mnt/nfs" {
			buf.Type = 0x6969
			buf.Bsize = 1048576  // 1MiB
			buf.Blocks = 1048576 // 1 TiB
			buf.Bfree = 104857   // 10% free -> 90% used
			buf.Bavail = 104857
			buf.Files = 1000000
			buf.Ffree = 100000 // 10% free
			return nil
		}

		return nil
	}

	// We override the default timeout just for testing to speed it up
	origTimeout := defaultStatfsTimeout
	defaultStatfsTimeout = 50 * time.Millisecond
	defer func() { defaultStatfsTimeout = origTimeout }()

	ctx := context.Background()
	stats, err := GetMountStats(ctx, procBase)
	if err != nil {
		t.Fatalf("GetMountStats failed: %v", err)
	}

	// Expected:
	// / (ext4)
	// /boot/efi (vfat)
	// /mnt/lustre (lustre) -> hung
	// /mnt/nfs (nfs4)

	if len(stats) != 4 {
		t.Errorf("expected 4 mounts, got %d", len(stats))
		for _, s := range stats {
			t.Logf("Found mount: %s", s.MountPoint)
		}
	}

	var root, efi, lustre, nfs MountStats
	for _, s := range stats {
		switch s.MountPoint {
		case "/":
			root = s
		case "/boot/efi":
			efi = s
		case "/mnt/lustre":
			lustre = s
		case "/mnt/nfs":
			nfs = s
		}
	}

	// Validate / (ext4)
	if root.Status != "ok" {
		t.Errorf("expected root status 'ok', got %q", root.Status)
	}
	if root.Capacity.TotalGiB != 100.0 {
		t.Errorf("expected root total 100.0 GiB, got %f", root.Capacity.TotalGiB)
	}
	if root.Capacity.UsedGiB != 85.5 {
		t.Errorf("expected root used 85.5 GiB, got %f", root.Capacity.UsedGiB)
	}
	if root.Capacity.FreeGiB != 14.5 {
		t.Errorf("expected root free 14.5 GiB, got %f", root.Capacity.FreeGiB)
	}
	if root.Capacity.UsagePct != 85.5 {
		t.Errorf("expected root usage_pct 85.5, got %f", root.Capacity.UsagePct)
	}
	if root.Inodes.Total != 6553600 {
		t.Errorf("expected root total inodes 6553600, got %d", root.Inodes.Total)
	}
	if root.Inodes.Used != 450000 {
		t.Errorf("expected root used inodes 450000, got %d", root.Inodes.Used)
	}
	// 450000 / 6553600 = 0.06866... -> ~6.9%
	if root.Inodes.UsagePct < 6.8 || root.Inodes.UsagePct > 6.9 {
		t.Errorf("expected root inode usage_pct around 6.9, got %f", root.Inodes.UsagePct)
	}

	// Validate /boot/efi (vfat)
	if efi.Status != "ok" {
		t.Errorf("expected efi status 'ok', got %q", efi.Status)
	}
	if efi.Capacity.UsagePct != 50.0 {
		t.Errorf("expected efi usage_pct 50.0, got %f", efi.Capacity.UsagePct)
	}
	// Inodes should be 0 safely
	if efi.Inodes.Total != 0 || efi.Inodes.UsagePct != 0 {
		t.Errorf("expected efi inodes to be 0, got %v", efi.Inodes)
	}

	// Validate /mnt/lustre (hung)
	if lustre.Status != "hung" {
		t.Errorf("expected lustre status 'hung', got %q", lustre.Status)
	}
	if lustre.Error == "" {
		t.Errorf("expected lustre to have an error message")
	}

	// Validate /mnt/nfs (nfs4)
	if nfs.Status != "ok" {
		t.Errorf("expected nfs status 'ok', got %q", nfs.Status)
	}
	if nfs.Capacity.UsagePct != 90.0 {
		t.Errorf("expected nfs usage_pct 90.0, got %f", nfs.Capacity.UsagePct)
	}
}

func BenchmarkGetMountStats(b *testing.B) {
	// Create mock proc base
	tmpDir := b.TempDir()
	procBase := filepath.Join(tmpDir, "proc")
	if err := os.MkdirAll(filepath.Join(procBase, "self"), 0755); err != nil {
		b.Fatalf("failed to create procBase: %v", err)
	}

	mountInfoData := `28 1 259:2 / / rw,relatime shared:1 - ext4 /dev/nvme0n1p2 rw,errors=remount-ro
`
	if err := os.WriteFile(filepath.Join(procBase, "self", "mountinfo"), []byte(mountInfoData), 0644); err != nil {
		b.Fatalf("failed to write mountinfo: %v", err)
	}

	origStatfsFunc := statfsFunc
	defer func() { statfsFunc = origStatfsFunc }()
	statfsFunc = func(path string, buf *unix.Statfs_t) error {
		buf.Bsize = 4096
		buf.Blocks = 26214400
		buf.Bfree = 3801088
		buf.Bavail = 3801088
		buf.Files = 6553600
		buf.Ffree = 6103600
		return nil
	}

	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = GetMountStats(ctx, procBase)
	}
}

func BenchmarkParseMounts(b *testing.B) {
	tmpDir := b.TempDir()
	procBase := filepath.Join(tmpDir, "proc")
	if err := os.MkdirAll(filepath.Join(procBase, "self"), 0755); err != nil {
		b.Fatalf("failed to create dir: %v", err)
	}
	mountInfoData := `28 1 259:2 / / rw,relatime shared:1 - ext4 /dev/nvme0n1p2 rw,errors=remount-ro\n`
	if err := os.WriteFile(filepath.Join(procBase, "self", "mountinfo"), []byte(mountInfoData), 0644); err != nil {
		b.Fatalf("failed to write file: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseMounts(procBase)
	}
}

func BenchmarkShouldKeepMount(b *testing.B) {
	m := mountEntry{Filesystem: "ext4", Device: "/dev/nvme0n1p2"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = shouldKeepMount(m)
	}
}

func BenchmarkStatfsWithTimeout(b *testing.B) {
	origStatfsFunc := statfsFunc
	defer func() { statfsFunc = origStatfsFunc }()
	statfsFunc = func(path string, buf *unix.Statfs_t) error {
		return nil
	}
	ctx := context.Background()
	var stat MountStats

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = statfsWithTimeout(ctx, "/", &stat)
	}
}

func BenchmarkPopulateCapacity(b *testing.B) {
	var stat MountStats
	var buf unix.Statfs_t
	buf.Bsize = 4096
	buf.Blocks = 26214400
	buf.Bfree = 3801088
	buf.Bavail = 3801088
	buf.Files = 6553600
	buf.Ffree = 6103600

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		populateCapacity(&stat, buf)
	}
}

func BenchmarkRoundGiB(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = roundGiB(1024 * 1024 * 1024 * 1.5)
	}
}
