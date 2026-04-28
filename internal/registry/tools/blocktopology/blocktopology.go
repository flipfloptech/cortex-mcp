package blocktopology

import (
	"context"
	"encoding/json"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
	"github.com/flipfloptech/cortex-mcp/internal/sys/storage"
)

func init() {
	registry.Register(registry.WithCache(60*time.Second, New()))
}

// Tool implements the registry.Tool interface for get_block_topology.
type Tool struct{}

// New returns a new instance of the block topology tool.
func New() *Tool {
	return &Tool{}
}

// Name returns the unique identifier for this tool.
func (t *Tool) Name() string { return "get_block_topology" }

// Description provides a brief summary for the LLM.
func (t *Tool) Description() string {
	return "Map the storage stack from physical block devices through virtual layers to mount points"
}

// Help returns detailed documentation for the tool.
func (t *Tool) Help() string {
	return `get_block_topology — Storage Stack Topology Map

Constructs a hierarchical map of the storage stack, tracing physical block devices
through virtual layers (MDADM, LVM, Device Mapper) to their final mount points
and swap areas.

Data Sources:
  - Physical Devices & Partitions: /sys/class/block/*/
  - Device relationships: /sys/class/block/*/holders/ and /sys/class/block/*/slaves/
  - RAID Status: /proc/mdstat
  - Mount Points: /proc/self/mountinfo (uses major:minor for precise mapping)
  - Swap Areas: /proc/swaps

Stitching Strategy:
  Uses major:minor device numbers as the primary key to link block devices
  to their mount points. Follows holders/slaves directories to trace
  physical→partition→DM/MD→mount relationships.

Degradation Profile:
  - IsSupported() returns false if /sys/class/block/ is missing.
  - If /proc/mdstat is absent, md_arrays is [].
  - If /proc/self/mountinfo is unreadable, mount_point fields are empty.
  - If /proc/swaps is unreadable, swap_devices is [].

Parameters: None
Supported on: Linux`
}

// Category classifies this tool under storage.
func (t *Tool) Category() string { return "storage" }

// Parameters returns nil — this tool takes no arguments.
func (t *Tool) Parameters() []registry.ToolParam { return nil }

// Hidden returns false — this tool is LLM-discoverable.
func (t *Tool) Hidden() bool { return false }

// IsSupported checks for the core sysfs dependency.
func (t *Tool) IsSupported() (bool, string) {
	if !registry.IsLinux() {
		return false, "requires Linux"
	}
	if !registry.PathExists("/sys/class/block") {
		return false, "/sys/class/block is missing"
	}
	return true, ""
}

// Execute performs the block topology discovery.
func (t *Tool) Execute(ctx context.Context, _ json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	topo, err := storage.GetBlockTopology("/sys", "/proc")
	if err != nil {
		return registry.NewErrorResult(t.Name(), err.Error()), nil
	}

	// Ensure non-nil slices for clean JSON.
	if topo.PhysicalDevices == nil {
		topo.PhysicalDevices = []storage.PhysicalDevice{}
	}
	if topo.VirtualLayers.MDArrays == nil {
		topo.VirtualLayers.MDArrays = []storage.MDArray{}
	}
	if topo.VirtualLayers.DeviceMapper == nil {
		topo.VirtualLayers.DeviceMapper = []storage.DeviceMapper{}
	}
	if topo.SwapDevices == nil {
		topo.SwapDevices = []storage.SwapDevice{}
	}

	result := registry.NewResult(
		t.Name(),
		registry.StatusOK,
		"Block topology discovered",
		topo,
	)
	result.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	result.Metadata.FilteringMethod = "deterministic"

	return result, nil
}
