package cputopology

import (
	"context"
	"encoding/json"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
	"github.com/flipfloptech/cortex-mcp/internal/sys/cpu"
)

func init() {
	registry.Register(registry.WithCache(10*time.Minute, &CPUTopologyTool{}))
}

// CPUTopologyTool implements registry.Tool to provide CPU hardware layouts.
type CPUTopologyTool struct{}

func (t *CPUTopologyTool) Name() string { return "get_cpu_topology" }

func (t *CPUTopologyTool) Description() string {
	return "Get hardware compute layout, NUMA mapping, and L3 cache topology"
}

func (t *CPUTopologyTool) Help() string {
	return `get_cpu_topology — Get Hardware Compute Layout

Provides a deterministic, pure-sysfs map of the hardware compute layout, enabling
identification of thread-pinning violations, cross-socket latency bottlenecks, and SMT contention.

Data Sources:
  - NUMA Nodes: /sys/devices/system/node/node*/cpulist
  - HW Topology: /sys/devices/system/cpu/cpu*/topology/
  - L3 Cache: /sys/devices/system/cpu/cpu*/cache/index3/shared_cpu_list

Output is aggregated by NUMA Node -> L3 Cache Domain -> Physical Cores.

Degradation:
  - UMA (Non-NUMA): If NUMA sysfs entries are missing, all cores are grouped under a single node bucket (numa_node_0) and is_numa is false.
  - Virtualization Blindness: If L3 cache sysfs entries are abstracted, cores are grouped into a generic l3_domain_0, and l3_topology_abstracted is true.

Parameters: None
Supported on: Linux`
}

func (t *CPUTopologyTool) Category() registry.Category { return registry.CategoryCompute }

func (t *CPUTopologyTool) Parameters() []registry.ToolParam { return nil }

func (t *CPUTopologyTool) Hidden() bool { return false }

func (t *CPUTopologyTool) IsSupported() (bool, string) {
	if !registry.IsLinux() {
		return false, "requires Linux"
	}
	return true, ""
}

func (t *CPUTopologyTool) Execute(ctx context.Context, _ json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	// 5-second context timeout to prevent hanging on unresponsive virtual filesystems
	ctxTimeout, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	topo, err := cpu.GetTopology(ctxTimeout, "/sys")
	if err != nil {
		return registry.NewErrorResult(t.Name(), err.Error()), nil
	}

	result := registry.NewResult(
		t.Name(),
		registry.StatusOK,
		"CPU Topology",
		topo,
	)
	result.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()

	return result, nil
}
