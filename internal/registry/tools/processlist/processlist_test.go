package processlist

import (
	"context"
	"encoding/json"
	"github.com/flipfloptech/cortex-mcp/internal/registry"
	"testing"
)

func TestProcessListTool_ContractCompliance(t *testing.T) {
	tl := New()

	if tl.Name() != "get_process_list" {
		t.Errorf("expected name get_process_list, got %s", tl.Name())
	}

	if tl.Category() != registry.CategoryCompute {
		t.Errorf("expected category compute, got %s", tl.Category())
	}

	if tl.Description() == "" {
		t.Error("expected non-empty description")
	}

	if tl.Help() == "" {
		t.Error("expected non-empty help")
	}

	params := tl.Parameters()
	if len(params) == 0 {
		t.Error("expected parameters, got none")
	}
}

func TestProcessListTool_Execute(t *testing.T) {
	tl := New()
	supported, _ := tl.IsSupported()
	if !supported {
		t.Skip("tool not supported on this system")
	}

	args := json.RawMessage(`{"limit": 2, "sample_duration_ms": 10}`)
	res, err := tl.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res == nil {
		t.Fatal("expected non-nil result")
	}

	// Wait, we returned nil, nil in the stub, so it should be skipped or fail nicely?
	// But res == nil check fails currently if err is nil.
	// We'll leave it to fail for now since it's TDD.
}

func BenchmarkProcessListTool_Execute(b *testing.B) {
	tl := New()
	supported, _ := tl.IsSupported()
	if !supported {
		b.Skip("tool not supported")
	}

	ctx := context.Background()
	args := json.RawMessage(`{"limit": 1, "sample_duration_ms": 1}`) // Very short duration for benchmark

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tl.Execute(ctx, args)
	}
}
