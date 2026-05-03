package blockscheduler

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func TestBlockSchedulerTool_Contract(t *testing.T) {
	t.Parallel()

	tool := New()

	if tool.Name() != "get_block_scheduler_info" {
		t.Errorf("Name = %q, want %q", tool.Name(), "get_block_scheduler_info")
	}
	if tool.Category() != "storage" {
		t.Errorf("Category = %q, want %q", tool.Category(), "storage")
	}
	if tool.Description() == "" {
		t.Error("Description must not be empty")
	}
	if tool.Hidden() {
		t.Error("tool should not be hidden")
	}

	params := tool.Parameters()
	if params != nil {
		t.Errorf("Parameters = %v, want nil", params)
	}

	help := tool.Help()
	if help == "" {
		t.Fatal("Help must not be empty")
	}
	// Help must reference data sources per workflow contract.
	for _, source := range []string{"/sys/block", "queue/scheduler", "queue/read_ahead_kb"} {
		if !strings.Contains(help, source) {
			t.Errorf("Help missing data source reference: %q", source)
		}
	}
}

func TestBlockSchedulerTool_IsSupported(t *testing.T) {
	t.Parallel()

	t.Run("missing_sysfs", func(t *testing.T) {
		t.Parallel()
		tool := New()
		tool.sysfsRoot = t.TempDir()

		supported, reason := tool.IsSupported()
		if supported {
			t.Error("expected unsupported when /sys/block is missing")
		}
		if reason == "" {
			t.Error("expected reason when unsupported")
		}
	})

	t.Run("present_sysfs", func(t *testing.T) {
		t.Parallel()
		tool := New()
		tool.sysfsRoot = t.TempDir()

		if err := os.MkdirAll(filepath.Join(tool.sysfsRoot, "block"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		supported, _ := tool.IsSupported()
		if !supported {
			t.Error("expected supported when /sys/block exists")
		}
	})
}

func TestBlockSchedulerTool_Execute(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.sysfsRoot = t.TempDir()

	// Create synthetic devices.
	blockDir := filepath.Join(tool.sysfsRoot, "block")

	// NVMe with none scheduler.
	setupDevice(t, blockDir, "nvme0n1", "[none] mq-deadline kyber bfq", "128")

	// SATA with high read-ahead.
	setupDevice(t, blockDir, "sda", "[mq-deadline] kyber bfq none", "4096")

	ctx := context.Background()
	res, err := tool.Execute(ctx, nil)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if res.Status == registry.StatusError {
		t.Fatalf("Execute returned error status: %s", res.Summary)
	}

	// Validate JSON schema.
	var payload map[string]interface{}
	if err := json.Unmarshal(res.Data, &payload); err != nil {
		t.Fatalf("failed to unmarshal result data: %v", err)
	}

	if _, ok := payload["system_summary"]; !ok {
		t.Error("expected system_summary in payload")
	}
	if _, ok := payload["devices"]; !ok {
		t.Error("expected devices in payload")
	}

	// Verify device count from system_summary.
	summary, ok := payload["system_summary"].(map[string]interface{})
	if !ok {
		t.Fatal("system_summary is not a map")
	}
	audited, ok := summary["devices_audited"].(float64)
	if !ok {
		t.Fatal("devices_audited is not a number")
	}
	if int(audited) != 2 {
		t.Errorf("devices_audited = %d, want 2", int(audited))
	}

	// Verify execution time metadata is set.
	if res.Metadata.ExecutionTimeMs < 0 {
		t.Error("expected non-negative execution time")
	}
}

func TestBlockSchedulerTool_Execute_MissingBlockDir(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.sysfsRoot = t.TempDir()

	ctx := context.Background()
	res, err := tool.Execute(ctx, nil)
	if err != nil {
		t.Fatalf("Execute should not return Go error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("expected error status for missing block dir, got %s", res.Status)
	}
}

// --- test helper ---

func setupDevice(t *testing.T, blockDir, name, scheduler, readAhead string) {
	t.Helper()
	queueDir := filepath.Join(blockDir, name, "queue")
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", queueDir, err)
	}
	if err := os.WriteFile(filepath.Join(queueDir, "scheduler"), []byte(scheduler+"\n"), 0o644); err != nil {
		t.Fatalf("write scheduler: %v", err)
	}
	if err := os.WriteFile(filepath.Join(queueDir, "read_ahead_kb"), []byte(readAhead+"\n"), 0o644); err != nil {
		t.Fatalf("write read_ahead_kb: %v", err)
	}
}

// --- Benchmarks ---

func BenchmarkNew(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = New()
	}
}

func BenchmarkName(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Name()
	}
}

func BenchmarkDescription(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Description()
	}
}

func BenchmarkHelp(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Help()
	}
}

func BenchmarkCategory(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Category()
	}
}

func BenchmarkHidden(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Hidden()
	}
}

func BenchmarkParameters(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Parameters()
	}
}

func BenchmarkIsSupported(b *testing.B) {
	tool := New()
	tool.sysfsRoot = b.TempDir()
	if err := os.MkdirAll(filepath.Join(tool.sysfsRoot, "block"), 0o755); err != nil {
		b.Fatalf("mkdir: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	tool := New()
	tool.sysfsRoot = b.TempDir()
	blockDir := filepath.Join(tool.sysfsRoot, "block")

	setupDeviceB(b, blockDir, "nvme0n1", "[none] mq-deadline kyber bfq", "128")
	setupDeviceB(b, blockDir, "sda", "[mq-deadline] kyber bfq none", "4096")

	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, nil)
	}
}

func setupDeviceB(b *testing.B, blockDir, name, scheduler, readAhead string) {
	b.Helper()
	queueDir := filepath.Join(blockDir, name, "queue")
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		b.Fatalf("mkdir %s: %v", queueDir, err)
	}
	if err := os.WriteFile(filepath.Join(queueDir, "scheduler"), []byte(scheduler+"\n"), 0o644); err != nil {
		b.Fatalf("write scheduler: %v", err)
	}
	if err := os.WriteFile(filepath.Join(queueDir, "read_ahead_kb"), []byte(readAhead+"\n"), 0o644); err != nil {
		b.Fatalf("write read_ahead_kb: %v", err)
	}
}
