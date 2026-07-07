package blocktopology

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
	"github.com/flipfloptech/cortex-mcp/internal/sys/storage"
)

func TestBlockTopologyTool_ContractCompliance(t *testing.T) {
	t.Parallel()
	tool := New()

	// Verify registry.Tool interface compliance.
	var _ registry.Tool = tool

	if tool.Name() != "get_block_topology" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "get_block_topology")
	}
	if tool.Category() != registry.CategoryStorage {
		t.Errorf("Category() = %q, want %q", tool.Category(), "storage")
	}
	if tool.Hidden() {
		t.Error("Hidden() = true, want false")
	}
	if tool.Parameters() != nil {
		t.Errorf("Parameters() = %v, want nil", tool.Parameters())
	}

	// Help must reference data sources.
	help := tool.Help()
	for _, source := range []string{"/sys/class/block", "/proc/mdstat", "/proc/self/mountinfo", "/proc/swaps"} {
		if !strings.Contains(help, source) {
			t.Errorf("Help() missing data source reference: %q", source)
		}
	}

	desc := tool.Description()
	if desc == "" {
		t.Error("Description() must not be empty")
	}
}

func TestBlockTopologyTool_Execute(t *testing.T) {
	t.Parallel()
	tool := New()

	supported, reason := tool.IsSupported()
	if !supported {
		t.Skipf("tool not supported on this host: %s", reason)
	}

	result, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() returned Go error: %v", err)
	}
	if result == nil {
		t.Fatal("Execute() returned nil result")
	}
	if result.Status != registry.StatusOK {
		t.Errorf("Status = %q, want %q", result.Status, registry.StatusOK)
	}
	if result.ToolName != "get_block_topology" {
		t.Errorf("ToolName = %q, want %q", result.ToolName, "get_block_topology")
	}

	// Verify the Data field unmarshals into the expected schema.
	var topo storage.BlockTopology
	if err := json.Unmarshal(result.Data, &topo); err != nil {
		t.Fatalf("Data does not unmarshal to BlockTopology: %v", err)
	}

	// Physical devices should be a non-nil slice (even if empty).
	if topo.PhysicalDevices == nil {
		t.Error("PhysicalDevices is nil, want non-nil slice")
	}
}

func BenchmarkExecute(b *testing.B) {
	tool := New()
	supported, reason := tool.IsSupported()
	if !supported {
		b.Skipf("tool not supported: %s", reason)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(context.Background(), nil)
	}
}

func BenchmarkNew(b *testing.B) {
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
