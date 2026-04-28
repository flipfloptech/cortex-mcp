package storage

import (
	"os"
	"path/filepath"
	"testing"
)

// setupBenchSysfs creates a realistic synthetic sysfs for benchmarking.
func setupBenchSysfs(b *testing.B) (string, string) {
	b.Helper()
	sysfs := b.TempDir()
	proc := b.TempDir()

	// NVMe device with partition and DM holder
	dev := filepath.Join(sysfs, "class/block/nvme0n1")
	mkdirAllB(b, filepath.Join(dev, "device"))
	writeFileB(b, filepath.Join(dev, "dev"), "259:0\n")
	writeFileB(b, filepath.Join(dev, "size"), "1953525168\n")
	writeFileB(b, filepath.Join(dev, "device/model"), "Samsung SSD 980 PRO\n")

	part := filepath.Join(sysfs, "class/block/nvme0n1p1")
	mkdirAllB(b, filepath.Join(part, "device"))
	writeFileB(b, filepath.Join(part, "dev"), "259:1\n")
	writeFileB(b, filepath.Join(part, "size"), "1953523712\n")
	writeFileB(b, filepath.Join(part, "partition"), "1\n")
	mkdirAllB(b, filepath.Join(part, "holders/dm-0"))
	mkdirAllB(b, filepath.Join(dev, "nvme0n1p1"))
	writeFileB(b, filepath.Join(dev, "nvme0n1p1/partition"), "1\n")

	// DM device
	dm := filepath.Join(sysfs, "class/block/dm-0")
	mkdirAllB(b, filepath.Join(dm, "dm"))
	writeFileB(b, filepath.Join(dm, "dev"), "253:0\n")
	writeFileB(b, filepath.Join(dm, "size"), "1953523712\n")
	writeFileB(b, filepath.Join(dm, "dm/name"), "vg_sys-lv_root\n")
	mkdirAllB(b, filepath.Join(dm, "slaves/nvme0n1p1"))

	// Proc files
	mkdirAllB(b, filepath.Join(proc, "self"))
	writeFileB(b, filepath.Join(proc, "self/mountinfo"),
		"22 1 253:0 / / rw,relatime shared:1 - ext4 /dev/mapper/vg_sys-lv_root rw\n")
	writeFileB(b, filepath.Join(proc, "swaps"),
		"Filename\t\t\t\tType\t\tSize\t\tUsed\t\tPriority\n")
	writeFileB(b, filepath.Join(proc, "mdstat"),
		"Personalities :\nunused devices: <none>\n")

	return sysfs, proc
}

func mkdirAllB(b *testing.B, path string) {
	b.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		b.Fatalf("mkdirAll %s: %v", path, err)
	}
}

func writeFileB(b *testing.B, path, content string) {
	b.Helper()
	mkdirAllB(b, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		b.Fatalf("writeFile %s: %v", path, err)
	}
}

func BenchmarkGetBlockTopology(b *testing.B) {
	sysfs, proc := setupBenchSysfs(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = GetBlockTopology(sysfs, proc)
	}
}

func BenchmarkDiscoverPhysicalDevices(b *testing.B) {
	sysfs, _ := setupBenchSysfs(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = discoverPhysicalDevices(sysfs)
	}
}

func BenchmarkDiscoverPartitions(b *testing.B) {
	sysfs, _ := setupBenchSysfs(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = discoverPartitions(sysfs, "nvme0n1")
	}
}

func BenchmarkParseMountInfo(b *testing.B) {
	_, proc := setupBenchSysfs(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseMountInfo(proc)
	}
}

func BenchmarkParseSwaps(b *testing.B) {
	_, proc := setupBenchSysfs(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseSwaps(proc)
	}
}

func BenchmarkParseMDStat(b *testing.B) {
	_, proc := setupBenchSysfs(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseMDStat(proc)
	}
}

func BenchmarkDiscoverDeviceMapper(b *testing.B) {
	sysfs, proc := setupBenchSysfs(b)
	mounts := parseMountInfo(proc)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = discoverDeviceMapper(sysfs, mounts)
	}
}

func BenchmarkDetectTransport(b *testing.B) {
	sysfs, _ := setupBenchSysfs(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = detectTransport(sysfs, "nvme0n1")
	}
}

func BenchmarkReadMajorMinor(b *testing.B) {
	sysfs, _ := setupBenchSysfs(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = readMajorMinor(sysfs, "nvme0n1")
	}
}

func BenchmarkSectorsToGiB(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = sectorsToGiB(1953525168)
	}
}

func BenchmarkReadModel(b *testing.B) {
	sysfs, _ := setupBenchSysfs(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = readModel(sysfs, "nvme0n1")
	}
}

func BenchmarkReadSizeGiB(b *testing.B) {
	sysfs, _ := setupBenchSysfs(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = readSizeGiB(sysfs, "nvme0n1")
	}
}

func BenchmarkReadHolders(b *testing.B) {
	sysfs, _ := setupBenchSysfs(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = readHolders(sysfs, "nvme0n1p1")
	}
}

func BenchmarkReadSlaves(b *testing.B) {
	sysfs, _ := setupBenchSysfs(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = readSlaves(sysfs, "dm-0")
	}
}

func BenchmarkIsPhysicalDevice(b *testing.B) {
	sysfs, _ := setupBenchSysfs(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = isPhysicalDevice(sysfs, "nvme0n1")
	}
}

func BenchmarkIsPartition(b *testing.B) {
	sysfs, _ := setupBenchSysfs(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = isPartition(sysfs, "nvme0n1p1")
	}
}

func BenchmarkEnrichMDArrays(b *testing.B) {
	sysfs, proc := setupBenchSysfs(b)
	mounts := parseMountInfo(proc)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = enrichMDArrays(sysfs, proc, mounts)
	}
}
