package diskio

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func TestDiskIOTool_Contract(t *testing.T) {
	t.Parallel()

	tool := New()

	if tool.Name() != "get_disk_io_stats" {
		t.Errorf("Expected name get_disk_io_stats, got %s", tool.Name())
	}
	if tool.Category() != "Storage" {
		t.Errorf("Expected category Storage, got %s", tool.Category())
	}

	params := tool.Parameters()
	if len(params) != 1 {
		t.Fatalf("Expected 1 parameter, got %d", len(params))
	}
	if params[0].Name != "latency_threshold_ms" {
		t.Errorf("Expected latency_threshold_ms parameter, got %s", params[0].Name)
	}
}

func TestDiskIOTool_IsSupported(t *testing.T) {
	t.Parallel()

	tool := New()
	// Override paths for testing
	tool.procfsRoot = t.TempDir()

	supported, reason := tool.IsSupported()
	if supported {
		t.Errorf("Expected unsupported when /proc/diskstats is missing")
	}
	if reason == "" {
		t.Errorf("Expected reason when unsupported")
	}

	// Create mock file
	if err := os.WriteFile(tool.procfsRoot+"/diskstats", []byte("mock"), 0644); err != nil {
		t.Fatalf("Failed to write mock file: %v", err)
	}
	supported, _ = tool.IsSupported()
	if !supported {
		t.Errorf("Expected supported when /proc/diskstats exists")
	}
}

func TestDiskIOTool_Execute(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.procfsRoot = t.TempDir()
	tool.sysfsRoot = t.TempDir()

	// Create mock files
	err := os.MkdirAll(tool.procfsRoot, 0755)
	if err != nil {
		t.Fatalf("Failed to create dir: %v", err)
	}

	diskstatsPath := tool.procfsRoot + "/diskstats"
	mockData := ` 259       0 nvme0n1 100 0 51200 10 200 0 102400 20 5 150 150 0 0 0`
	if err := os.WriteFile(diskstatsPath, []byte(mockData), 0644); err != nil {
		t.Fatalf("Failed to write mock diskstats: %v", err)
	}

	// Since we sleep for 500ms, and we only have 1 data point, the second read will yield the same data.
	// Therefore, IOPS and MBs will be 0, which is perfectly fine for schema validation.

	ctx := context.Background()
	args := json.RawMessage(`{"latency_threshold_ms": 20.0}`)

	res, err := tool.Execute(ctx, args)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if res.Status == registry.StatusError {
		t.Fatalf("Expected successful execution, got error: %s", res.Summary)
	}

	// Verify Data is valid JSON and unmarshals
	var payload map[string]interface{}
	if err := json.Unmarshal(res.Data, &payload); err != nil {
		t.Fatalf("Failed to unmarshal result data: %v", err)
	}

	if _, ok := payload["system_summary"]; !ok {
		t.Errorf("Expected system_summary in payload")
	}
	if _, ok := payload["devices"]; !ok {
		t.Errorf("Expected devices in payload")
	}
}

func BenchmarkExecute(b *testing.B) {
	tool := New()
	tool.procfsRoot = b.TempDir()
	tool.sysfsRoot = b.TempDir()

	diskstatsPath := tool.procfsRoot + "/diskstats"
	mockData := ` 259       0 nvme0n1 100 0 51200 10 200 0 102400 20 5 150 150 0 0 0`
	if err := os.WriteFile(diskstatsPath, []byte(mockData), 0644); err != nil {
		b.Fatalf("Failed to write mock diskstats: %v", err)
	}

	ctx := context.Background()
	args := json.RawMessage(`{"latency_threshold_ms": 20.0}`)

	// Override sleep interval for fast benchmark
	tool.sleepInterval = 1 * time.Millisecond

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, args)
	}
}

func BenchmarkNew(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = New()
	}
}

func BenchmarkName(b *testing.B) {
	t := New()
	for i := 0; i < b.N; i++ {
		_ = t.Name()
	}
}

func BenchmarkDescription(b *testing.B) {
	t := New()
	for i := 0; i < b.N; i++ {
		_ = t.Description()
	}
}

func BenchmarkHelp(b *testing.B) {
	t := New()
	for i := 0; i < b.N; i++ {
		_ = t.Help()
	}
}

func BenchmarkCategory(b *testing.B) {
	t := New()
	for i := 0; i < b.N; i++ {
		_ = t.Category()
	}
}

func BenchmarkHidden(b *testing.B) {
	t := New()
	for i := 0; i < b.N; i++ {
		_ = t.Hidden()
	}
}

func BenchmarkParameters(b *testing.B) {
	t := New()
	for i := 0; i < b.N; i++ {
		_ = t.Parameters()
	}
}

func BenchmarkIsSupported(b *testing.B) {
	t := New()
	t.procfsRoot = b.TempDir()
	if err := os.WriteFile(t.procfsRoot+"/diskstats", []byte("mock"), 0644); err != nil {
		b.Fatalf("write failed: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = t.IsSupported()
	}
}
