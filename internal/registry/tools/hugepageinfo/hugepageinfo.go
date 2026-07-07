package hugepageinfo

import (
	"context"
	"encoding/json"
	"os"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
	"github.com/flipfloptech/cortex-mcp/internal/sys/memory"
)

// Tool implements the registry.Tool interface for get_hugepage_info.
type Tool struct{}

// New returns a new instance of the hugepageinfo tool.
func New() *Tool {
	return &Tool{}
}

func init() {
	registry.Register(New())
}

// Name returns the tool name.
func (t *Tool) Name() string {
	return "get_hugepage_info"
}

// Description returns a short summary of the tool.
func (t *Tool) Description() string {
	return "Provide a comprehensive audit of memory paging efficiency by identifying the allocation status of static HugePages and the current operational mode of Transparent HugePages (THP)."
}

// Help returns detailed documentation for the tool.
func (t *Tool) Help() string {
	return `Objective:
Provide a comprehensive audit of memory paging efficiency by identifying the allocation status of static HugePages and the current operational mode of Transparent HugePages (THP) to diagnose memory allocation stalls.

Data Sources:
- Static HugePages: /proc/meminfo
- Transparent HugePages (THP) Settings: /sys/kernel/mm/transparent_hugepage/enabled, /sys/kernel/mm/transparent_hugepage/defrag
- THP Performance Impact: /proc/vmstat

Returns a JSON object detailing static hugepage utilization and THP allocation/fallback statistics.`
}

// Category returns the tool classification.
func (t *Tool) Category() registry.Category {
	return registry.CategoryMemory
}

// Parameters returns the parameter schema. This tool takes no parameters.
func (t *Tool) Parameters() []registry.ToolParam {
	return nil
}

// Hidden indicates whether this tool should be hidden from general LLM discovery.
func (t *Tool) Hidden() bool {
	return false
}

// IsSupported returns true if the host supports hugepages.
// It checks for the existence of /proc/meminfo as the core dependency.
func (t *Tool) IsSupported() (bool, string) {
	if _, err := os.Stat("/proc/meminfo"); err != nil {
		return false, "failed to access /proc/meminfo"
	}
	return true, ""
}

// Execute performs the diagnostic logic and returns the structured result.
func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	info, err := memory.GetHugePageInfo()
	if err != nil {
		return registry.NewErrorResult(t.Name(), err.Error()), nil
	}

	summary := "HugePage diagnostic telemetry captured"
	if info.THPStats != nil && info.THPStats.IsStallingRisk {
		summary = "THP Stalling Risk Detected"
	}

	return registry.NewResult(t.Name(), registry.StatusOK, summary, info), nil
}
