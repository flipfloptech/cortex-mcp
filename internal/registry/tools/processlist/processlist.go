package processlist

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
	"github.com/flipfloptech/cortex-mcp/internal/sys/process"
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

func (t *tool) Category() registry.Category {
	return registry.CategoryCompute
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
	if _, err := os.Stat("/proc/stat"); err != nil {
		return false, "/proc/stat not found"
	}
	if _, err := os.Stat("/proc/uptime"); err != nil {
		return false, "/proc/uptime not found"
	}
	return true, ""
}

func parseRegexOpt(argsMap map[string]interface{}, key string) (*regexp.Regexp, error) {
	if val, ok := argsMap[key]; ok {
		str, ok := val.(string)
		if !ok {
			return nil, fmt.Errorf("%s must be a string", key)
		}
		if str == "" {
			return nil, nil
		}
		re, err := regexp.Compile(str)
		if err != nil {
			return nil, fmt.Errorf("invalid regex for %s: %w", key, err)
		}
		return re, nil
	}
	return nil, nil
}

func (t *tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {

	var argsMap map[string]interface{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argsMap); err != nil {
			return registry.NewErrorResult(t.Name(), "failed to parse arguments: "+err.Error()), nil
		}
	}

	opts := process.FilterOptions{
		SortBy:         "cpu",
		Limit:          10,
		SampleDuration: 100 * time.Millisecond,
	}

	if val, ok := argsMap["sort_by"]; ok {
		if s, ok := val.(string); ok && (s == "cpu" || s == "memory") {
			opts.SortBy = s
		}
	}

	if val, ok := argsMap["limit"]; ok {
		if f, ok := val.(float64); ok {
			opts.Limit = int(f)
		}
	}

	if val, ok := argsMap["sample_duration_ms"]; ok {
		if f, ok := val.(float64); ok {
			opts.SampleDuration = time.Duration(f) * time.Millisecond
		}
	}

	var err error
	opts.UserRegex, err = parseRegexOpt(argsMap, "user_regex")
	if err != nil {
		return registry.NewErrorResult(t.Name(), err.Error()), nil
	}
	opts.NameRegex, err = parseRegexOpt(argsMap, "name_regex")
	if err != nil {
		return registry.NewErrorResult(t.Name(), err.Error()), nil
	}
	opts.StateRegex, err = parseRegexOpt(argsMap, "state_regex")
	if err != nil {
		return registry.NewErrorResult(t.Name(), err.Error()), nil
	}
	opts.CmdlineRegex, err = parseRegexOpt(argsMap, "cmdline_regex")
	if err != nil {
		return registry.NewErrorResult(t.Name(), err.Error()), nil
	}

	processes, err := process.GetList(ctx, opts)
	if err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("failed to get process list: %v", err)), nil
	}

	summary := fmt.Sprintf("Retrieved %d processes", len(processes))
	return registry.NewResult(t.Name(), registry.StatusOK, summary, processes), nil
}

func init() {
	registry.Register(New()) // No cache initially, as we want live data.
}
