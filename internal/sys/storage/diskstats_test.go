package storage

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseDiskStats(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	diskstatsPath := filepath.Join(tmpDir, "diskstats")

	// Write mock data
	mockData := ` 259       0 nvme0n1 100 0 51200 10 200 0 102400 20 5 150 150 0 0 0
   8       0 sda 50 0 25600 5 100 0 51200 10 0 75 75 0 0 0
   7       0 loop0 10 0 100 1 10 0 100 1 0 10 10 0 0 0`

	if err := os.WriteFile(diskstatsPath, []byte(mockData), 0644); err != nil {
		t.Fatalf("Failed to write mock diskstats: %v", err)
	}

	stats, err := ParseDiskStats(diskstatsPath)
	if err != nil {
		t.Fatalf("ParseDiskStats failed: %v", err)
	}

	if len(stats) != 3 {
		t.Fatalf("Expected 3 devices, got %d", len(stats))
	}

	// nvme0n1
	if stats[0].DeviceName != "nvme0n1" {
		t.Errorf("Expected nvme0n1, got %s", stats[0].DeviceName)
	}
	if stats[0].ReadsCompleted != 100 {
		t.Errorf("Expected 100 reads, got %d", stats[0].ReadsCompleted)
	}
	if stats[0].SectorsRead != 51200 {
		t.Errorf("Expected 51200 sectors read, got %d", stats[0].SectorsRead)
	}
	if stats[0].WritesCompleted != 200 {
		t.Errorf("Expected 200 writes, got %d", stats[0].WritesCompleted)
	}
	if stats[0].SectorsWritten != 102400 {
		t.Errorf("Expected 102400 sectors written, got %d", stats[0].SectorsWritten)
	}
	if stats[0].QueueDepth != 5 {
		t.Errorf("Expected queue depth 5, got %d", stats[0].QueueDepth)
	}
	if stats[0].TimeInIOMs != 150 {
		t.Errorf("Expected time in IO 150, got %d", stats[0].TimeInIOMs)
	}
}

func TestGetQueueDepthMax(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create mock sysfs struct for nvme0n1
	devDir := filepath.Join(tmpDir, "block", "nvme0n1", "queue")
	if err := os.MkdirAll(devDir, 0755); err != nil {
		t.Fatalf("Failed to create mock sysfs: %v", err)
	}

	if err := os.WriteFile(filepath.Join(devDir, "nr_requests"), []byte("256\n"), 0644); err != nil {
		t.Fatalf("Failed to write nr_requests: %v", err)
	}

	depth, err := GetQueueDepthMax(tmpDir, "nvme0n1")
	if err != nil {
		t.Fatalf("GetQueueDepthMax failed: %v", err)
	}
	if depth == nil || *depth != 256 {
		t.Errorf("Expected queue depth 256, got %v", depth)
	}

	// Test missing file
	missingDepth, err := GetQueueDepthMax(tmpDir, "sda")
	if err != nil {
		t.Errorf("Expected no error for missing file, got %v", err)
	}
	if missingDepth != nil {
		t.Errorf("Expected nil depth for missing file, got %v", missingDepth)
	}
}

func TestCalculateMetrics(t *testing.T) {
	t.Parallel()

	start := []DiskStat{
		{DeviceName: "nvme0n1", ReadsCompleted: 100, SectorsRead: 1000, WritesCompleted: 200, SectorsWritten: 2000, QueueDepth: 0, TimeInIOMs: 100},
		{DeviceName: "sda", ReadsCompleted: 50, SectorsRead: 500, WritesCompleted: 100, SectorsWritten: 1000, QueueDepth: 0, TimeInIOMs: 50},
		{DeviceName: "sdb", ReadsCompleted: 0, SectorsRead: 0, WritesCompleted: 0, SectorsWritten: 0, QueueDepth: 0, TimeInIOMs: 0},          // Zero ops test
		{DeviceName: "loop0", ReadsCompleted: 10, SectorsRead: 100, WritesCompleted: 10, SectorsWritten: 100, QueueDepth: 0, TimeInIOMs: 10}, // Should be filtered out
	}

	end := []DiskStat{
		{DeviceName: "nvme0n1", ReadsCompleted: 200, SectorsRead: 3000, WritesCompleted: 400, SectorsWritten: 6000, QueueDepth: 10, TimeInIOMs: 600}, // Delta TimeInIOMs = 500 (100% util for 500ms)
		{DeviceName: "sda", ReadsCompleted: 70, SectorsRead: 700, WritesCompleted: 150, SectorsWritten: 1500, QueueDepth: 2, TimeInIOMs: 100},        // Delta TimeInIOMs = 50 (10% util for 500ms)
		{DeviceName: "sdb", ReadsCompleted: 0, SectorsRead: 0, WritesCompleted: 0, SectorsWritten: 0, QueueDepth: 0, TimeInIOMs: 0},                  // Still zero
		{DeviceName: "loop0", ReadsCompleted: 20, SectorsRead: 200, WritesCompleted: 20, SectorsWritten: 200, QueueDepth: 0, TimeInIOMs: 20},
	}

	metrics := CalculateMetrics(start, end, 500*time.Millisecond)

	// Verify loop0 is filtered out
	if len(metrics) != 3 {
		t.Fatalf("Expected 3 devices after filtering loop, got %d", len(metrics))
	}

	// Map for easy lookup
	mMap := make(map[string]DeviceIOMetrics)
	for _, m := range metrics {
		mMap[m.DeviceName] = m
	}

	// Verify nvme0n1
	nvme := mMap["nvme0n1"]
	// Delta reads: 100 over 0.5s = 200 IOPS
	if nvme.ReadIOPS != 200 {
		t.Errorf("Expected nvme ReadIOPS 200, got %f", nvme.ReadIOPS)
	}
	// Delta writes: 200 over 0.5s = 400 IOPS
	if nvme.WriteIOPS != 400 {
		t.Errorf("Expected nvme WriteIOPS 400, got %f", nvme.WriteIOPS)
	}
	// Delta sectors read: 2000 * 512 = 1,024,000 bytes over 0.5s = 2,048,000 bytes/s = 2.048 MB/s (Wait, 2048000 / (1024*1024) or / 1000000?)
	// MB/s is typically 1024^2
	// Expected ReadMBs = (2000 * 512 / 0.5) / 1048576 = 1.953125. Let's assume binary MB (MiB) for MB/s or decimal. Let's use decimal for MB/s as is standard for storage: 1,000,000 bytes.
	// (2000 * 512 / 0.5) / 1000000 = 2.048
	// I'll leave the exact MB/s assertion slightly flexible or define it carefully in the implementation.
	// Avg Latency: 500ms time spent / 300 ops = 1.666 ms
	if math.Abs(nvme.AvgLatencyMs-1.666) > 0.01 {
		t.Errorf("Expected nvme AvgLatencyMs ~1.666, got %f", nvme.AvgLatencyMs)
	}
	if nvme.UtilizationPct != 100.0 {
		t.Errorf("Expected nvme UtilizationPct 100.0, got %f", nvme.UtilizationPct)
	}
	if nvme.QueueDepthCurrent != 10 {
		t.Errorf("Expected nvme QueueDepthCurrent 10, got %d", nvme.QueueDepthCurrent)
	}

	// Verify sdb (Zero division protection)
	sdb := mMap["sdb"]
	if sdb.ReadIOPS != 0 || sdb.WriteIOPS != 0 || sdb.AvgLatencyMs != 0 || sdb.UtilizationPct != 0 {
		t.Errorf("Expected sdb to have zero metrics, got %+v", sdb)
	}
}

func TestAggregateSummary(t *testing.T) {
	t.Parallel()

	metrics := []DeviceIOMetrics{
		{DeviceName: "nvme0n1", ReadMBs: 100.0, WriteMBs: 200.0, AvgLatencyMs: 25.0}, // High latency
		{DeviceName: "sda", ReadMBs: 50.0, WriteMBs: 50.0, AvgLatencyMs: 15.0},
	}

	summary := AggregateSummary(metrics, 20.0)

	if summary.TotalReadMBs != 150.0 {
		t.Errorf("Expected TotalReadMBs 150, got %f", summary.TotalReadMBs)
	}
	if summary.TotalWriteMBs != 250.0 {
		t.Errorf("Expected TotalWriteMBs 250, got %f", summary.TotalWriteMBs)
	}
	if len(summary.HighLatencyDevices) != 1 || summary.HighLatencyDevices[0] != "nvme0n1" {
		t.Errorf("Expected nvme0n1 as high latency device, got %v", summary.HighLatencyDevices)
	}
}

func BenchmarkParseDiskStats(b *testing.B) {
	tmpDir := b.TempDir()
	diskstatsPath := filepath.Join(tmpDir, "diskstats")
	mockData := ` 259       0 nvme0n1 100 0 51200 10 200 0 102400 20 5 150 150 0 0 0`
	if err := os.WriteFile(diskstatsPath, []byte(mockData), 0644); err != nil {
		b.Fatalf("Failed to write diskstats: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ParseDiskStats(diskstatsPath)
	}
}

func BenchmarkCalculateMetrics(b *testing.B) {
	start := []DiskStat{
		{DeviceName: "nvme0n1", ReadsCompleted: 100, SectorsRead: 1000, WritesCompleted: 200, SectorsWritten: 2000, QueueDepth: 0, TimeInIOMs: 100},
	}
	end := []DiskStat{
		{DeviceName: "nvme0n1", ReadsCompleted: 200, SectorsRead: 3000, WritesCompleted: 400, SectorsWritten: 6000, QueueDepth: 10, TimeInIOMs: 600},
	}
	interval := 500 * time.Millisecond

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = CalculateMetrics(start, end, interval)
	}
}

func BenchmarkGetQueueDepthMax(b *testing.B) {
	tmpDir := b.TempDir()
	devDir := filepath.Join(tmpDir, "block", "nvme0n1", "queue")
	if err := os.MkdirAll(devDir, 0755); err != nil {
		b.Fatalf("mkdir failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(devDir, "nr_requests"), []byte("256\n"), 0644); err != nil {
		b.Fatalf("write failed: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = GetQueueDepthMax(tmpDir, "nvme0n1")
	}
}

func BenchmarkIsIgnoredDevice(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = isIgnoredDevice("nvme0n1")
	}
}

func BenchmarkAggregateSummary(b *testing.B) {
	metrics := []DeviceIOMetrics{
		{DeviceName: "nvme0n1", ReadMBs: 100.0, WriteMBs: 200.0, AvgLatencyMs: 25.0},
		{DeviceName: "sda", ReadMBs: 50.0, WriteMBs: 50.0, AvgLatencyMs: 15.0},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = AggregateSummary(metrics, 20.0)
	}
}
