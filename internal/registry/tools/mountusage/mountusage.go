package mountusage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
	"github.com/flipfloptech/cortex-mcp/internal/sys/storage"
)

func init() {
	registry.Register(New())
}

// Tool implements registry.Tool for get_mount_usage.
type Tool struct {
	procfsRoot string
}

// New returns a new instance of the Tool.
func New() *Tool {
	return &Tool{
		procfsRoot: "/proc",
	}
}

// Name returns the unique identifier for this tool.
func (t *Tool) Name() string {
	return "get_mount_usage"
}

// Description provides a concise summary of what this tool does.
func (t *Tool) Description() string {
	return "Get instantaneous capacity and inode metrics for active filesystems."
}

// Help provides detailed documentation.
func (t *Tool) Help() string {
	return `Analyzes active mount points and filesystems using /proc/self/mountinfo and the statfs syscall.
Filters out pseudo-filesystems, keeping block-backed and network filesystems. 
Detects hung mounts (e.g. offline NFS/Lustre servers) via a strict 2-second timeout.
Provides detailed storage and inode usage metrics.`
}

// Category organizes the tool within the registry.
func (t *Tool) Category() string {
	return "Storage"
}

// Hidden hides the tool from some interfaces if true.
func (t *Tool) Hidden() bool {
	return false
}

// Parameters defines the expected input schema.
func (t *Tool) Parameters() []registry.ToolParam {
	return nil
}

// IsSupported checks if the underlying data sources are available.
func (t *Tool) IsSupported() (bool, string) {
	path := t.procfsRoot + "/self/mountinfo"
	if _, err := os.Stat(path); err != nil {
		return false, fmt.Sprintf("%s is missing", path)
	}
	return true, ""
}

type Payload struct {
	SystemSummary SystemSummary        `json:"system_summary"`
	Mounts        []storage.MountStats `json:"mounts"`
}

type SystemSummary struct {
	TotalMountsChecked int `json:"total_mounts_checked"`
	HungMountsDetected int `json:"hung_mounts_detected"`
	CapacityWarnings   int `json:"capacity_warnings"`
	InodeWarnings      int `json:"inode_warnings"`
}

// Execute performs the diagnostic action and returns JSON data.
func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	stats, err := storage.GetMountStats(ctx, t.procfsRoot)
	if err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("failed to get mount stats: %v", err)), nil
	}

	summary := SystemSummary{
		TotalMountsChecked: len(stats),
	}

	for _, s := range stats {
		if s.Status == "hung" {
			summary.HungMountsDetected++
			continue
		}
		if s.Capacity.UsagePct > 85.0 {
			summary.CapacityWarnings++
		}
		if s.Inodes.UsagePct > 85.0 {
			summary.InodeWarnings++
		}
	}

	payload := Payload{
		SystemSummary: summary,
		Mounts:        stats,
	}

	return registry.NewResult(t.Name(), registry.StatusOK, "Successfully analyzed mount usage", payload), nil
}
