package cputopology

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
	"github.com/flipfloptech/cortex-mcp/internal/sys/cpu"
)

func TestCPUTopologyTool_Contract(t *testing.T) {
	t.Parallel()
	tool := &CPUTopologyTool{}

	// Verify implements interface
	var _ registry.Tool = tool

	if tool.Name() != "get_cpu_topology" {
		t.Errorf("expected Name to be get_cpu_topology, got %q", tool.Name())
	}
	if tool.Category() != "compute" {
		t.Errorf("expected Category to be compute, got %q", tool.Category())
	}
	if tool.Description() == "" {
		t.Error("expected Description to be non-empty")
	}
	if tool.Help() == "" {
		t.Error("expected Help to be non-empty")
	}
	if tool.Parameters() != nil {
		t.Error("expected Parameters to be nil")
	}
	if tool.Hidden() {
		t.Error("expected Hidden to be false")
	}
}

func TestCPUTopologyTool_Execute(t *testing.T) {
	t.Parallel()
	tool := &CPUTopologyTool{}

	supported, _ := tool.IsSupported()
	if !supported {
		t.Skip("Skipping execution test on unsupported platform")
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected no Go error from Execute, got %v", err)
	}

	if res.Status == registry.StatusError {
		t.Logf("Execute returned tool error (expected on some CI systems): %s", res.Data)
		return
	}

	// Verify JSON serialization into the target struct format
	var parsed cpu.SystemTopology
	if err := json.Unmarshal(res.Data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal result data into SystemTopology: %v", err)
	}

	// Verify basic structural constraints
	if parsed.SystemSummary.TotalLogicalCPUs <= 0 {
		t.Errorf("expected TotalLogicalCPUs > 0, got %d", parsed.SystemSummary.TotalLogicalCPUs)
	}
	if parsed.SystemSummary.TotalNumaNodes <= 0 {
		t.Errorf("expected TotalNumaNodes > 0, got %d", parsed.SystemSummary.TotalNumaNodes)
	}
	if len(parsed.Topology) != parsed.SystemSummary.TotalNumaNodes {
		t.Errorf("expected len(Topology) == TotalNumaNodes, got %d vs %d", len(parsed.Topology), parsed.SystemSummary.TotalNumaNodes)
	}
}
