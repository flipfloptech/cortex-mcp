package kernelmodules

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

type KernelModule struct {
	Name     string   `json:"name"`
	Size     int64    `json:"size_bytes"`
	RefCount int      `json:"ref_count"`
	UsedBy   []string `json:"used_by"`
	State    string   `json:"state,omitempty"`
	Address  string   `json:"address,omitempty"`
}

type KernelModulesData struct {
	Modules []KernelModule `json:"modules"`
}

type Args struct {
	Filter string `json:"filter"`
}

type QueryKernelModulesTool struct {
	readFile    func(path string) ([]byte, error)
	execCommand func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func New() *QueryKernelModulesTool {
	return &QueryKernelModulesTool{
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

func (t *QueryKernelModulesTool) Name() string {
	return "get_kernel_modules"
}

func (t *QueryKernelModulesTool) Category() registry.Category {
	return registry.CategorySystem
}

func (t *QueryKernelModulesTool) Help() string {
	return `Get status information for loaded kernel modules / drivers.

Queries loaded modules natively from /proc/modules, falling back to lsmod. Parses module name, size, reference count, dependency list, operational state, and address.

Parameters:
- filter: Optional string. Case-insensitive search keyword to filter modules by name.`
}

func (t *QueryKernelModulesTool) Description() string {
	return "Loaded drivers, sizes, and reference counts"
}

func (t *QueryKernelModulesTool) Parameters() []registry.ToolParam {
	return []registry.ToolParam{
		{
			Name:        "filter",
			Type:        "string",
			Description: "Optional: Case-insensitive module name search term to filter results.",
			Required:    false,
		},
	}
}

func (t *QueryKernelModulesTool) Hidden() bool { return false }

func (t *QueryKernelModulesTool) IsSupported() (bool, string) {
	_, err := os.Stat("/proc/modules")
	if err == nil {
		return true, ""
	}
	_, err = exec.LookPath("lsmod")
	if err == nil {
		return true, ""
	}
	return false, "kernel modules query not supported (missing /proc/modules and lsmod binary)"
}

func (t *QueryKernelModulesTool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	var parsedArgs Args
	if len(args) > 0 {
		if err := json.Unmarshal(args, &parsedArgs); err != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("invalid arguments: %v", err)), nil
		}
	}

	var rawContent []byte
	// 1. Primary Native Path
	content, err := t.readFile("/proc/modules")
	if err == nil {
		rawContent = content
	} else {
		// 2. Command Fallback Path
		cmdOutput, cmdErr := t.execCommand(ctx, "lsmod")
		if cmdErr != nil {
			errMsg := fmt.Sprintf("failed to read modules: file err=%v; cmd err=%v", err, cmdErr)
			return registry.NewErrorResult(t.Name(), errMsg), nil
		}
		rawContent = cmdOutput
	}

	allModules := parseModulesBuffer(rawContent)
	var filteredModules []KernelModule

	filterLower := strings.ToLower(strings.TrimSpace(parsedArgs.Filter))
	for _, m := range allModules {
		if filterLower == "" || strings.Contains(strings.ToLower(m.Name), filterLower) {
			filteredModules = append(filteredModules, m)
		}
	}

	summaryStr := fmt.Sprintf("Kernel Modules: queried %d loaded modules", len(filteredModules))
	data := KernelModulesData{Modules: filteredModules}

	res := registry.NewResult(t.Name(), registry.StatusOK, summaryStr, data)
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()

	return res, nil
}

func parseModulesBuffer(output []byte) []KernelModule {
	var modules []KernelModule
	lines := bytes.Split(output, []byte("\n"))

	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}

		tokens := strings.Fields(string(line))
		if len(tokens) == 0 {
			continue
		}

		// Skip lsmod header line
		if tokens[0] == "Module" && len(tokens) >= 3 && tokens[1] == "Size" {
			continue
		}

		if len(tokens) < 3 {
			continue
		}

		var m KernelModule
		m.Name = tokens[0]

		if sz, err := strconv.ParseInt(tokens[1], 10, 64); err == nil {
			m.Size = sz
		}

		if rc, err := strconv.Atoi(tokens[2]); err == nil {
			m.RefCount = rc
		}

		if len(tokens) > 3 {
			usedByStr := strings.TrimSuffix(tokens[3], ",")
			if usedByStr != "-" && usedByStr != "" {
				m.UsedBy = strings.Split(usedByStr, ",")
			} else {
				m.UsedBy = []string{}
			}
		} else {
			m.UsedBy = []string{}
		}

		if len(tokens) > 4 {
			m.State = tokens[4]
		}
		if len(tokens) > 5 {
			m.Address = tokens[5]
		}

		modules = append(modules, m)
	}

	return modules
}
