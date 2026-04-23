package processtree

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
	"github.com/flipfloptech/cortex-mcp/internal/sys/process"
)

type ProcessTreeTool struct{}

// New creates a new ProcessTreeTool.
func New() *ProcessTreeTool {
	return &ProcessTreeTool{}
}

func init() {
	registry.Register(New())
}

// Name returns the unique name of the tool.
func (t *ProcessTreeTool) Name() string {
	return "get_process_tree"
}

// Description returns a short summary of the tool.
func (t *ProcessTreeTool) Description() string {
	return "Builds the execution hierarchy to identify workload origins"
}

// Help returns detailed documentation for the tool.
func (t *ProcessTreeTool) Help() string {
	return `Pulls the execution hierarchy for a target process to identify workload origins.
Returns the full ancestry path (up to PID 1) and all descendants of the target PID.

Data Sources:
- /proc/[pid]/stat for parent PID (PPID), state, and CPU timing
- /proc/uptime for precise CPU% delta calculation over a 100ms window
- /proc/[pid]/status for UID and VmRSS (Memory)
- /proc/[pid]/cmdline for process execution arguments

Degradation Profile:
- If target_pid does not exist, returns an error.
- If cmdline is unreadable (e.g. permission denied), the cmdline field is omitted/null.`
}

// Category groups the tool in the catalog.
func (t *ProcessTreeTool) Category() string {
	return "compute"
}

// Hidden indicates whether this tool should be hidden from list_tools.
func (t *ProcessTreeTool) Hidden() bool {
	return false
}

// Parameters defines the expected JSON-RPC arguments.
func (t *ProcessTreeTool) Parameters() []registry.ToolParam {
	return []registry.ToolParam{
		{
			Name:        "target_pid",
			Type:        "integer",
			Description: "The process ID to fetch the tree for. Returns its ancestry and descendants.",
			Required:    true,
		},
	}
}

// IsSupported checks if the tool can run on this host.
func (t *ProcessTreeTool) IsSupported() (bool, string) {
	return process.IsSupported()
}

// Execute runs the tool logic and returns a ToolResult.
func (t *ProcessTreeTool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	var params struct {
		TargetPID int `json:"target_pid"`
	}

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}

	if len(args) > 0 {
		if err := json.Unmarshal(args, &params); err != nil {
			return registry.NewErrorResult(
				t.Name(),
				hostname,
				fmt.Sprintf("Failed to parse arguments: %v", err),
			), nil
		}
	}

	if params.TargetPID <= 0 {
		return registry.NewErrorResult(
			t.Name(),
			hostname,
			"target_pid must be a positive integer",
		), nil
	}

	tree, err := process.GetTree(ctx, params.TargetPID)
	if err != nil {
		return registry.NewErrorResult(
			t.Name(),
			hostname,
			err.Error(),
		), nil
	}

	return registry.NewResult(
		t.Name(),
		hostname,
		registry.StatusOK,
		fmt.Sprintf("Successfully generated process tree for PID %d", params.TargetPID),
		tree,
	), nil
}
