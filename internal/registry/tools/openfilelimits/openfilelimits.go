package openfilelimits

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

type FileLimitsData struct {
	Allocated    int64   `json:"allocated"`
	Unused       int64   `json:"unused"`
	Max          int64   `json:"max"`
	UsagePercent float64 `json:"usage_percent"`
}

type QueryOpenFileLimitsTool struct {
	readFile    func(path string) ([]byte, error)
	execCommand func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func New() *QueryOpenFileLimitsTool {
	return &QueryOpenFileLimitsTool{
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

func (t *QueryOpenFileLimitsTool) Name() string {
	return "get_open_file_limits"
}

func (t *QueryOpenFileLimitsTool) Category() string {
	return "system"
}

func (t *QueryOpenFileLimitsTool) Help() string {
	return `Query system-wide file descriptor usage and allocation limits.

Reads stats natively from /proc/sys/fs/file-nr, falling back to sysctl fs.file-nr. Reports allocated descriptors, unused descriptors, and system limit. Calculates percentage utilization.

No parameters required.`
}

func (t *QueryOpenFileLimitsTool) Description() string {
	return "Current global file descriptor usage vs. max limits"
}

func (t *QueryOpenFileLimitsTool) Parameters() []registry.ToolParam {
	return []registry.ToolParam{}
}

func (t *QueryOpenFileLimitsTool) Hidden() bool { return false }

func (t *QueryOpenFileLimitsTool) IsSupported() (bool, string) {
	_, err := os.Stat("/proc/sys/fs/file-nr")
	if err == nil {
		return true, ""
	}
	_, err = exec.LookPath("sysctl")
	if err == nil {
		return true, ""
	}
	return false, "open file limits query not supported (missing /proc/sys/fs/file-nr and sysctl binary)"
}

func (t *QueryOpenFileLimitsTool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	var rawContent []byte
	// 1. Primary Native Path
	content, err := t.readFile("/proc/sys/fs/file-nr")
	if err == nil {
		rawContent = content
	} else {
		// 2. Command Fallback Path
		cmdOutput, cmdErr := t.execCommand(ctx, "sysctl", "fs.file-nr")
		if cmdErr != nil {
			errMsg := fmt.Sprintf("failed to read file limits: file err=%v; cmd err=%v", err, cmdErr)
			return registry.NewErrorResult(t.Name(), errMsg), nil
		}
		rawContent = cmdOutput
	}

	data, parseErr := parseFileNr(rawContent)
	if parseErr != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("failed to parse file limits: %v", parseErr)), nil
	}

	summaryStr := fmt.Sprintf("Open File Limits: %d/%d allocated descriptors (%.2f%% usage)", data.Allocated, data.Max, data.UsagePercent)

	res := registry.NewResult(t.Name(), registry.StatusOK, summaryStr, data)
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()

	return res, nil
}

func parseFileNr(output []byte) (FileLimitsData, error) {
	str := strings.TrimSpace(string(output))
	if idx := strings.Index(str, "="); idx >= 0 {
		str = strings.TrimSpace(str[idx+1:])
	}

	tokens := strings.Fields(str)
	if len(tokens) < 3 {
		return FileLimitsData{}, fmt.Errorf("unexpected file-nr output: %q", string(output))
	}

	allocated, err := strconv.ParseInt(tokens[0], 10, 64)
	if err != nil {
		return FileLimitsData{}, fmt.Errorf("invalid allocated value: %v", err)
	}

	unused, err := strconv.ParseInt(tokens[1], 10, 64)
	if err != nil {
		return FileLimitsData{}, fmt.Errorf("invalid unused value: %v", err)
	}

	max, err := strconv.ParseInt(tokens[2], 10, 64)
	if err != nil {
		return FileLimitsData{}, fmt.Errorf("invalid max value: %v", err)
	}

	var usagePercent float64
	if max > 0 {
		usagePercent = (float64(allocated) * 100.0) / float64(max)
	}

	return FileLimitsData{
		Allocated:    allocated,
		Unused:       unused,
		Max:          max,
		UsagePercent: usagePercent,
	}, nil
}
