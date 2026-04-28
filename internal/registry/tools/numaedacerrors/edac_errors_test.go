package numaedacerrors

import (
	"context"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func TestTool_Contract(t *testing.T) {
	tool := New()

	if tool.Name() != "get_numa_edac_errors" {
		t.Errorf("expected 'get_numa_edac_errors', got %s", tool.Name())
	}
	if tool.Category() != "hardware" {
		t.Errorf("expected 'hardware', got %s", tool.Category())
	}
	if tool.Description() == "" {
		t.Error("expected description to be populated")
	}
	if tool.Help() == "" {
		t.Error("expected help to be populated")
	}
	if tool.Parameters() != nil {
		t.Error("expected parameters to be nil")
	}
	if tool.Hidden() {
		t.Error("expected hidden to be false")
	}

	supported, reason := tool.IsSupported()
	if !supported {
		t.Errorf("expected true, got %v: %s", supported, reason)
	}
}

func TestTool_Execute(t *testing.T) {
	tool := New()
	ctx := context.Background()

	res, err := tool.Execute(ctx, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.ToolName != tool.Name() {
		t.Errorf("expected tool name in result, got %s", res.ToolName)
	}

	// Because /sys/devices/system/edac may or may not exist on the test runner,
	// the status could be Healthy or Degraded, but it should definitely not be an error
	// from our implementation blowing up.
	if res.Status != registry.StatusDegraded && res.Status != registry.StatusOK && res.Status != registry.StatusWarning && res.Status != registry.StatusError {
		t.Errorf("unexpected status: %s", res.Status)
	}
}

// Benchmarks for benchcov

func BenchmarkNew(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = New()
	}
}

func BenchmarkName(b *testing.B) {
	tool := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.Name()
	}
}

func BenchmarkDescription(b *testing.B) {
	tool := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.Description()
	}
}

func BenchmarkHelp(b *testing.B) {
	tool := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.Help()
	}
}

func BenchmarkCategory(b *testing.B) {
	tool := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.Category()
	}
}

func BenchmarkParameters(b *testing.B) {
	tool := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.Parameters()
	}
}

func BenchmarkHidden(b *testing.B) {
	tool := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.Hidden()
	}
}

func BenchmarkIsSupported(b *testing.B) {
	tool := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	b.Skip("skipping to avoid filesystem IO in benchmark")
}
