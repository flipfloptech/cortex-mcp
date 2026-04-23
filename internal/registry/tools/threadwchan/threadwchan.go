package threadwchan

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
	"github.com/flipfloptech/cortex-mcp/internal/sys/process"
)

type tool struct{}

func New() registry.Tool {
	return &tool{}
}

func (t *tool) Name() string {
	return "get_thread_wchan"
}

func (t *tool) Description() string {
	return "Diagnoses system hangs by showing exactly which kernel function threads are blocked on."
}

func (t *tool) Help() string {
	return "Reads /proc/[pid]/wchan and /proc/[pid]/status. If wchan is restricted or 0, it falls back to raw thread state."
}

func (t *tool) Category() string {
	return "compute"
}

func (t *tool) Hidden() bool {
	return false
}

func (t *tool) Parameters() []registry.ToolParam {
	return []registry.ToolParam{
		{
			Name:        "target_pid",
			Type:        "integer",
			Description: "Optional. If provided, scans only threads of this PID. If omitted, scans all threads across all processes.",
			Required:    false,
		},
	}
}

func (t *tool) IsSupported() (bool, string) {
	if _, err := os.Stat("/proc/1/wchan"); err != nil {
		return false, "/proc/[pid]/wchan is missing or inaccessible"
	}
	return true, ""
}

func (t *tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}

	var argsMap map[string]interface{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argsMap); err != nil {
			return registry.NewErrorResult(t.Name(), hostname, "failed to parse arguments: "+err.Error()), nil
		}
	}

	var targetPid *int
	if val, ok := argsMap["target_pid"]; ok {
		if f, ok := val.(float64); ok {
			p := int(f)
			targetPid = &p
		}
	}

	res, err := process.GetThreadWchan(ctx, targetPid)
	if err != nil {
		return registry.NewErrorResult(t.Name(), hostname, fmt.Sprintf("failed to get thread wchan data: %v", err)), nil
	}

	summary := fmt.Sprintf("Scanned %d threads (%d blocked)", res.SystemSummary.TotalThreads, res.SystemSummary.BlockedThreads)
	return registry.NewResult(t.Name(), hostname, registry.StatusOK, summary, res), nil
}

func init() {
	registry.Register(New())
}
