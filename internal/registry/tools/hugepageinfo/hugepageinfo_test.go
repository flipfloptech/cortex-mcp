package hugepageinfo

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func TestTool_Contract(t *testing.T) {
	tool := New()

	if tool.Name() != "get_hugepage_info" {
		t.Errorf("expected name 'get_hugepage_info', got '%s'", tool.Name())
	}
	if tool.Category() != "memory" {
		t.Errorf("expected category 'memory', got '%s'", tool.Category())
	}
	if tool.Description() == "" {
		t.Error("expected non-empty description")
	}
	if tool.Help() == "" {
		t.Error("expected non-empty help")
	}
	if params := tool.Parameters(); params != nil {
		t.Errorf("expected nil parameters, got %v", params)
	}
}

func TestTool_Execute(t *testing.T) {
	tool := New()

	// Check IsSupported first
	supported, reason := tool.IsSupported()
	if !supported {
		t.Skipf("skipping execute test: %s", reason)
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.Status == registry.StatusError {
		t.Fatalf("unexpected tool error: %s", string(res.Data))
	}

	// Validate JSON schema
	var data map[string]interface{}
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}

	// Check core schema elements
	if _, ok := data["system_summary"]; !ok {
		t.Error("missing system_summary in JSON")
	}
	if _, ok := data["static_hugepages"]; !ok {
		t.Error("missing static_hugepages in JSON")
	}
}

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
	tool := New()
	supported, _ := tool.IsSupported()
	if !supported {
		b.Skip("skipping benchmark: tool unsupported")
	}

	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, nil)
	}
}
