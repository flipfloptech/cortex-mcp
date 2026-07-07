package moduleinfo

import (
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

type ModuleInfoData struct {
	Name             string            `json:"name"`
	Filename         string            `json:"filename,omitempty"`
	Author           string            `json:"author,omitempty"`
	Description      string            `json:"description,omitempty"`
	License          string            `json:"license,omitempty"`
	Version          string            `json:"version,omitempty"`
	Depends          []string          `json:"depends,omitempty"`
	Aliases          []string          `json:"aliases,omitempty"`
	SrcVersion       string            `json:"srcversion,omitempty"`
	Taint            string            `json:"taint,omitempty"`
	LiveState        *LiveModuleState  `json:"live_state,omitempty"`
	StaticProperties map[string]string `json:"static_properties,omitempty"`
}

type LiveModuleState struct {
	CoreSize  int64    `json:"core_size_bytes"`
	InitSize  int64    `json:"init_size_bytes"`
	InitState string   `json:"init_state"`
	RefCount  int64    `json:"refcnt"`
	Holders   []string `json:"holders"`
}

type Args struct {
	ModuleName string `json:"module_name"`
}

type QueryKernelModuleInfoTool struct {
	dirExists        func(path string) bool
	readSysfsFile    func(path string) ([]byte, error)
	readSysfsHolders func(path string) ([]string, error)
	execCommand      func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func New() *QueryKernelModuleInfoTool {
	return &QueryKernelModuleInfoTool{
		dirExists: func(path string) bool {
			info, err := os.Stat(path)
			return err == nil && info.IsDir()
		},
		readSysfsFile: func(path string) ([]byte, error) {
			return os.ReadFile(path)
		},
		readSysfsHolders: func(path string) ([]string, error) {
			entries, err := os.ReadDir(path)
			if err != nil {
				return nil, err
			}
			var list []string
			for _, e := range entries {
				list = append(list, e.Name())
			}
			return list, nil
		},
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		},
	}
}

func init() {
	registry.Register(New())
}

func (t *QueryKernelModuleInfoTool) Name() string {
	return "get_kernel_module_info"
}

func (t *QueryKernelModuleInfoTool) Category() string {
	return "system"
}

func (t *QueryKernelModuleInfoTool) Help() string {
	return `Query metadata details and live running state of a specific Linux kernel module/driver.

Natively reads active reference counts, core size, holders, and init states from /sys/module/<name>, complementing it with modinfo output (license, dependencies, version, author, description, aliases).

Parameters:
- module_name: Required string. The exact name of the kernel module to inspect (e.g. 'uinput', 'tcp_diag', 'ext4').`
}

func (t *QueryKernelModuleInfoTool) Description() string {
	return "Details of a specific loaded or available kernel module"
}

func (t *QueryKernelModuleInfoTool) Parameters() []registry.ToolParam {
	return []registry.ToolParam{
		{
			Name:        "module_name",
			Type:        "string",
			Description: "Required: The exact name of the kernel module to inspect.",
			Required:    true,
		},
	}
}

func (t *QueryKernelModuleInfoTool) Hidden() bool { return false }

func (t *QueryKernelModuleInfoTool) IsSupported() (bool, string) {
	_, err := exec.LookPath("modinfo")
	if err != nil {
		return false, "kernel module info not supported (missing modinfo binary)"
	}
	return true, ""
}

func (t *QueryKernelModuleInfoTool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	var parsedArgs Args
	if len(args) > 0 {
		if err := json.Unmarshal(args, &parsedArgs); err != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("invalid arguments: %v", err)), nil
		}
	}

	moduleName := strings.TrimSpace(parsedArgs.ModuleName)
	if moduleName == "" {
		return registry.NewErrorResult(t.Name(), "missing required argument 'module_name'"), nil
	}

	// 1. Check if sysfs has active data for this module (only if loaded)
	var liveState *LiveModuleState
	sysfsPath := "/sys/module/" + moduleName
	if t.dirExists(sysfsPath) {
		liveState = &LiveModuleState{}

		if content, err := t.readSysfsFile(sysfsPath + "/coresize"); err == nil {
			if val, parseErr := strconv.ParseInt(strings.TrimSpace(string(content)), 10, 64); parseErr == nil {
				liveState.CoreSize = val
			}
		}

		if content, err := t.readSysfsFile(sysfsPath + "/initsize"); err == nil {
			if val, parseErr := strconv.ParseInt(strings.TrimSpace(string(content)), 10, 64); parseErr == nil {
				liveState.InitSize = val
			}
		}

		if content, err := t.readSysfsFile(sysfsPath + "/initstate"); err == nil {
			liveState.InitState = strings.TrimSpace(string(content))
		}

		if content, err := t.readSysfsFile(sysfsPath + "/refcnt"); err == nil {
			if val, parseErr := strconv.ParseInt(strings.TrimSpace(string(content)), 10, 64); parseErr == nil {
				liveState.RefCount = val
			}
		}

		if holders, err := t.readSysfsHolders(sysfsPath + "/holders"); err == nil {
			liveState.Holders = holders
		} else {
			liveState.Holders = []string{}
		}
	}

	// 2. Query static properties from modinfo (supports both loaded and unloaded modules)
	cmdOutput, cmdErr := t.execCommand(ctx, "modinfo", moduleName)
	if cmdErr != nil && liveState == nil {
		errMsg := fmt.Sprintf("failed to query kernel module %q: sysfs missing; modinfo err=%v", moduleName, cmdErr)
		return registry.NewErrorResult(t.Name(), errMsg), nil
	}

	var data ModuleInfoData
	if cmdErr == nil {
		data = parseModinfo(cmdOutput)
	} else {
		data.StaticProperties = make(map[string]string)
	}

	data.Name = moduleName
	data.LiveState = liveState

	summaryStr := fmt.Sprintf("Kernel Module Info: queried %q", moduleName)
	if liveState != nil {
		summaryStr += fmt.Sprintf(" (loaded, refcnt=%d)", liveState.RefCount)
	} else {
		summaryStr += " (not loaded)"
	}

	res := registry.NewResult(t.Name(), registry.StatusOK, summaryStr, data)
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()

	return res, nil
}

func parseModinfo(output []byte) ModuleInfoData {
	var data ModuleInfoData
	data.StaticProperties = make(map[string]string)

	lines := strings.Split(string(output), "\n")
	var lastKey string

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}

		// Continuation line
		if (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) && lastKey != "" {
			val := strings.TrimSpace(line)
			if lastKey == "alias" {
				if len(data.Aliases) > 0 {
					data.Aliases[len(data.Aliases)-1] += " " + val
				}
			} else {
				appendToKey(&data, lastKey, " "+val)
			}
			continue
		}

		idx := strings.Index(line, ":")
		if idx <= 0 {
			continue
		}

		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		lastKey = key

		switch key {
		case "filename":
			data.Filename = val
		case "author":
			data.Author = val
		case "description":
			data.Description = val
		case "license":
			data.License = val
		case "version":
			data.Version = val
		case "srcversion":
			data.SrcVersion = val
		case "depends":
			if val != "" {
				parts := strings.Split(val, ",")
				for i, p := range parts {
					parts[i] = strings.TrimSpace(p)
				}
				data.Depends = parts
			}
		case "alias":
			data.Aliases = append(data.Aliases, val)
		default:
			data.StaticProperties[key] = val
		}
	}

	return data
}

func appendToKey(data *ModuleInfoData, key, val string) {
	switch key {
	case "filename":
		data.Filename += val
	case "author":
		data.Author += val
	case "description":
		data.Description += val
	case "license":
		data.License += val
	case "version":
		data.Version += val
	case "srcversion":
		data.SrcVersion += val
	default:
		data.StaticProperties[key] += val
	}
}
