package filesystemerrors

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func BenchmarkNew(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = New()
	}
}

func BenchmarkName(b *testing.B) {
	t := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Name()
	}
}

func BenchmarkDescription(b *testing.B) {
	t := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Description()
	}
}

func BenchmarkHelp(b *testing.B) {
	t := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Help()
	}
}

func BenchmarkCategory(b *testing.B) {
	t := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Category()
	}
}

func BenchmarkParameters(b *testing.B) {
	t := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Parameters()
	}
}

func BenchmarkHidden(b *testing.B) {
	t := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Hidden()
	}
}

func BenchmarkIsSupported(b *testing.B) {
	tool := newFixtureTool(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	tool := newFixtureTool(b)
	ctx := context.Background()
	args := json.RawMessage(`{}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, args)
	}
}

func BenchmarkParseMountInfo(b *testing.B) {
	content := []byte(mountinfoFixture)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseMountInfo(content)
	}
}

func BenchmarkHasMountOpt(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = hasMountOpt("rw,errors=remount-ro", "ro")
	}
}

func BenchmarkFindReadonlyMounts(b *testing.B) {
	entries := parseMountInfo([]byte(mountinfoFixture))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = findReadonlyMounts(entries)
	}
}

func BenchmarkExt4MountMap(b *testing.B) {
	entries := parseMountInfo([]byte(mountinfoFixture))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ext4MountMap(entries)
	}
}

func BenchmarkCollectExt4(b *testing.B) {
	tool := newFixtureTool(b)
	entries := parseMountInfo([]byte(mountinfoFixture))
	mounts := ext4MountMap(entries)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.collectExt4(mounts)
	}
}

func BenchmarkReadEpochRFC3339(b *testing.B) {
	tool := newFixtureTool(b)
	path := filepath.Join(tool.sysfsRoot, "fs", "ext4", "sda1", "first_error_time")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = readEpochRFC3339(path)
	}
}

func BenchmarkReadTrimmed(b *testing.B) {
	tool := newFixtureTool(b)
	path := filepath.Join(tool.sysfsRoot, "fs", "ext4", "sda1", "first_error_func")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = readTrimmed(path)
	}
}

func BenchmarkCollectBtrfs(b *testing.B) {
	tool := newFixtureTool(b)
	entries := parseMountInfo([]byte(mountinfoFixture))
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.collectBtrfs(ctx, entries)
	}
}

func BenchmarkParseBtrfsStats(b *testing.B) {
	content := []byte(btrfsStatsOutput)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseBtrfsStats("/mnt/btr", content)
	}
}

func BenchmarkBuildWarnings(b *testing.B) {
	ext4 := []Ext4Device{{Device: "sda1", ErrorsCount: 3}}
	ro := []ReadonlyMount{{Mount: "/backup", Device: "/dev/sdb1", Fstype: "ext4"}}
	btrfs := []BtrfsDeviceStats{{Mount: "/mnt/btr", Device: "/dev/sdd1", WriteIOErrs: 3, CorruptionErrs: 1}}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buildWarnings(ext4, ro, btrfs)
	}
}
