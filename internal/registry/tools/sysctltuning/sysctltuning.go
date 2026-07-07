package sysctltuning

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

var criticalHPCParams = []string{
	"net.ipv4.conf.all.rp_filter",
	"net.ipv4.conf.default.rp_filter",
	"net.ipv4.tcp_rmem",
	"net.ipv4.tcp_wmem",
	"net.core.rmem_max",
	"net.core.wmem_max",
	"net.core.somaxconn",
	"net.core.netdev_max_backlog",
	"vm.dirty_ratio",
	"vm.dirty_background_ratio",
}

type SysctlData struct {
	Parameters map[string]string `json:"parameters"`
}

type Args struct {
	Keys []string `json:"keys"`
}

type QuerySysctlTuningTool struct {
	readFile    func(path string) ([]byte, error)
	execCommand func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func New() *QuerySysctlTuningTool {
	return &QuerySysctlTuningTool{
		readFile: func(path string) ([]byte, error) {
			return os.ReadFile(path)
		},
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		},
	}
}

func init() {
	registry.Register(New())
}

func (t *QuerySysctlTuningTool) Name() string {
	return "get_sysctl_tuning_state"
}

func (t *QuerySysctlTuningTool) Category() string {
	return "system"
}

func (t *QuerySysctlTuningTool) Help() string {
	return `Targeted audit of critical HPC kernel parameters.

Queries and inspects asymmetric routing limits (rp_filter), TCP BDP buffer values (tcp_rmem), backlog queue parameters, and virtual memory caching dirty ratios.

Parameters:
- keys: Optional list of strings. Specific sysctl parameter keys to inspect (e.g. ['vm.dirty_ratio', 'net.core.somaxconn']). If omitted, audits the default set of critical HPC tuning parameters.`
}

func (t *QuerySysctlTuningTool) Description() string {
	return "Targeted audit of critical HPC kernel parameters"
}

func (t *QuerySysctlTuningTool) Parameters() []registry.ToolParam {
	return []registry.ToolParam{
		{
			Name:        "keys",
			Type:        "array",
			Description: "Optional: List of specific sysctl parameters to inspect.",
			Required:    false,
		},
	}
}

func (t *QuerySysctlTuningTool) Hidden() bool { return false }

func (t *QuerySysctlTuningTool) IsSupported() (bool, string) {
	_, err := os.Stat("/proc/sys")
	if err != nil {
		return false, "sysctl tuning not supported (missing /proc/sys filesystem)"
	}
	return true, ""
}

func (t *QuerySysctlTuningTool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	var parsedArgs Args
	if len(args) > 0 {
		if err := json.Unmarshal(args, &parsedArgs); err != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("invalid arguments: %v", err)), nil
		}
	}

	keysToQuery := parsedArgs.Keys
	if len(keysToQuery) == 0 {
		keysToQuery = criticalHPCParams
	}

	parameters := make(map[string]string)

	for _, key := range keysToQuery {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}

		// 1. Primary Native Path
		path := resolveSysctlPath(key)
		content, err := t.readFile(path)
		if err == nil {
			parameters[key] = strings.TrimSpace(string(content))
			continue
		}

		// 2. Command Fallback Path
		cmdOutput, cmdErr := t.execCommand(ctx, "sysctl", "-n", key)
		if cmdErr == nil {
			parameters[key] = strings.TrimSpace(string(cmdOutput))
			continue
		}

		// Store combined errors if both failed
		parameters[key] = fmt.Sprintf("error: %v", err)
	}

	summaryStr := fmt.Sprintf("Sysctl Tuning State: audited %d kernel parameters", len(parameters))
	data := SysctlData{Parameters: parameters}

	res := registry.NewResult(t.Name(), registry.StatusOK, summaryStr, data)
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()

	return res, nil
}

func resolveSysctlPath(key string) string {
	relPath := strings.ReplaceAll(key, ".", "/")
	return "/proc/sys/" + relPath
}
