package mountusage

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func TestMountUsageTool_Contract(t *testing.T) {
	t.Parallel()

	tool := New()

	if tool.Name() != "get_mount_usage" {
		t.Errorf("Expected name get_mount_usage, got %s", tool.Name())
	}
	if tool.Category() != registry.CategoryStorage {
		t.Errorf("Expected category storage, got %s", tool.Category())
	}

	params := tool.Parameters()
	if len(params) != 0 {
		t.Fatalf("Expected 0 parameters, got %d", len(params))
	}
}

func TestMountUsageTool_IsSupported(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.procfsRoot = t.TempDir()

	supported, reason := tool.IsSupported()
	if supported {
		t.Errorf("Expected unsupported when /proc/self/mountinfo is missing")
	}
	if reason == "" {
		t.Errorf("Expected reason when unsupported")
	}

	if err := os.MkdirAll(tool.procfsRoot+"/self", 0755); err != nil {
		t.Fatalf("Failed to create dir: %v", err)
	}
	if err := os.WriteFile(tool.procfsRoot+"/self/mountinfo", []byte("mock"), 0644); err != nil {
		t.Fatalf("Failed to write mock file: %v", err)
	}
	supported, _ = tool.IsSupported()
	if !supported {
		t.Errorf("Expected supported when /proc/self/mountinfo exists")
	}
}

func TestMountUsageTool_Execute(t *testing.T) {
	// Let it run on the real filesystem to test schema output
	tool := New()

	ctx := context.Background()
	args := json.RawMessage(`{}`)

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

	sysSummaryRaw, ok := payload["system_summary"]
	if !ok {
		t.Fatalf("Expected system_summary in payload")
	}
	sysSummary, ok := sysSummaryRaw.(map[string]interface{})
	if !ok {
		t.Fatalf("system_summary is not an object")
	}

	if _, ok := sysSummary["total_mounts_checked"]; !ok {
		t.Errorf("Expected total_mounts_checked in system_summary")
	}
	if _, ok := sysSummary["hung_mounts_detected"]; !ok {
		t.Errorf("Expected hung_mounts_detected in system_summary")
	}

	if _, ok := payload["mounts"]; !ok {
		t.Fatalf("Expected mounts in payload")
	}
}

func BenchmarkExecute(b *testing.B) {
	tool := New()
	ctx := context.Background()
	args := json.RawMessage(`{}`)

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
	if err := os.MkdirAll(t.procfsRoot+"/self", 0755); err != nil {
		b.Fatalf("Failed to create dir: %v", err)
	}
	if err := os.WriteFile(t.procfsRoot+"/self/mountinfo", []byte("mock"), 0644); err != nil {
		b.Fatalf("write failed: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = t.IsSupported()
	}
}
