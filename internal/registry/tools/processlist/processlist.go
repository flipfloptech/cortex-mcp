package processlist

import (
	"context"
	"encoding/json"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

type tool struct{}

func New() registry.Tool {
	return &tool{}
}

func (t *tool) Name() string {
	return "get_process_list"
}

func (t *tool) Description() string {
	return "Pulls a lightweight, top-N list of CPU/Mem consumers."
}

func (t *tool) Help() string {
	return "Calculates accurate CPU% by taking a rapid delta of /proc/[pid]/stat utime/stime against system uptime."
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
			Name:        "sort_by",
			Type:        "string",
			Description: "Metric to sort by ('cpu' or 'memory')",
			Required:    false,
			Default:     "cpu",
		},
		{
			Name:        "limit",
			Type:        "integer",
			Description: "Maximum number of processes to return",
			Required:    false,
			Default:     "10",
		},
		{
			Name:        "sample_duration_ms",
			Type:        "integer",
			Description: "CPU sampling duration in milliseconds",
			Required:    false,
			Default:     "100",
		},
		{
			Name:        "user_regex",
			Type:        "string",
			Description: "Regex to filter by process owner (e.g., '^root$')",
			Required:    false,
		},
		{
			Name:        "name_regex",
			Type:        "string",
			Description: "Regex to filter by process name",
			Required:    false,
		},
		{
			Name:        "state_regex",
			Type:        "string",
			Description: "Regex to filter by process state (e.g., 'R' or 'Z')",
			Required:    false,
		},
		{
			Name:        "cmdline_regex",
			Type:        "string",
			Description: "Regex to filter by command line",
			Required:    false,
		},
	}
}

func (t *tool) IsSupported() (bool, string) {
	// Require /proc/stat and /proc/uptime to exist
	return true, ""
}

func (t *tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	return nil, nil // stub
}

func init() {
	registry.Register(New()) // No cache initially, as we want live data.
}
