package blockscheduler

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

// Tool implements the registry.Tool interface for get_block_scheduler_info.
type Tool struct {
	sysfsRoot string
}

// New returns a new instance of the block scheduler tool.
func New() *Tool {
	return &Tool{
		sysfsRoot: "/sys",
	}
}

// Name returns the unique identifier for this tool.
func (t *Tool) Name() string { return "get_block_scheduler_info" }

// Description provides a brief summary for the LLM.
func (t *Tool) Description() string {
	return "Audit block device I/O scheduler and read-ahead tuning to identify software-induced latency"
}

// Help returns detailed documentation for the tool.
func (t *Tool) Help() string {
	return `get_block_scheduler_info — Block Device Queue Tuning Audit

Audits block device I/O scheduler and read-ahead settings to identify
software-induced latency (schedulers) and inefficient caching (read-ahead)
that throttle high-performance storage hardware.

Data Sources:
  - Scheduler Status: /sys/block/<dev>/queue/scheduler
  - Read-Ahead: /sys/block/<dev>/queue/read_ahead_kb
  - Device Discovery: /sys/block/ directory iteration

Device Filtering:
  Includes physical disks (NVMe, SATA, SAS, VirtIO), DM, and MD logical
  volumes. Excludes loop, ram, zram, nbd pseudo-devices and partitions
  (detected via sysfs partition marker file).

Tuning Warning Heuristics:
  1. NVMe devices using schedulers other than 'none' — PCIe NVMe drives have
     massive internal parallel queues; OS-level scheduling wastes CPU time
     re-ordering I/O the drive firmware handles natively.
  2. Read-ahead >= 4096 KB — causes cache thrashing, especially for Lustre
     OSS nodes that implement their own read-ahead algorithms.

Degradation Profile:
  - IsSupported() returns false if /sys/block is missing.
  - Missing scheduler file: active_scheduler="none", available_schedulers=["none"].
  - Missing read_ahead_kb file: read_ahead_kb=0.
  - DM/MD devices with "none" scheduler (no brackets) are handled gracefully.

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
	if !registry.PathExists(t.sysfsRoot + "/block") {
		return false, "/sys/block is missing"
	}
	return true, ""
}

// Execute performs the block device scheduler and read-ahead audit.
func (t *Tool) Execute(_ context.Context, _ json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	payload, err := storage.GetSchedulerInfo(t.sysfsRoot)
	if err != nil {
		return registry.NewErrorResult(t.Name(), err.Error()), nil
	}

	result := registry.NewResult(
		t.Name(),
		registry.StatusOK,
		"Block scheduler audit complete",
		payload,
	)
	result.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()

	return result, nil
}
