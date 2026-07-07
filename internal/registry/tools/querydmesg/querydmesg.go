package querydmesg

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

var logLevels = map[int]string{
	0: "emerg",
	1: "alert",
	2: "crit",
	3: "err",
	4: "warn",
	5: "notice",
	6: "info",
	7: "debug",
}

type DmesgEntry struct {
	Timestamp float64 `json:"timestamp_sec"`
	Message   string  `json:"message"`
	Level     string  `json:"level,omitempty"`
	Facility  string  `json:"facility,omitempty"`
}

type DmesgData struct {
	Entries []DmesgEntry `json:"entries"`
}

type Args struct {
	Lines int    `json:"lines"`
	Grep  string `json:"grep"`
	Level string `json:"level"`
}

type QueryDmesgTool struct {
	sysKlogctlSize func(action int) (int, error)
	sysKlogctlRead func(action int, buf []byte) (int, error)
	execCommand    func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func New() *QueryDmesgTool {
	return &QueryDmesgTool{
		sysKlogctlSize: func(action int) (int, error) {
			return syscall.Klogctl(action, nil)
		},
		sysKlogctlRead: func(action int, buf []byte) (int, error) {
			return syscall.Klogctl(action, buf)
		},
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		},
	}
}

func init() {
	registry.Register(New())
}

func (t *QueryDmesgTool) Name() string {
	return "query_dmesg"
}

func (t *QueryDmesgTool) Category() registry.Category {
	return registry.CategorySystem
}

func (t *QueryDmesgTool) Help() string {
	return `Query the kernel log ring buffer (dmesg) with level and regex filters.

Reads log buffer records natively using klogctl syscalls, falling back to dmesg -r commands. Parses severity levels, syslog facilities, kernel timestamps, and log messages.

Parameters:
- lines: Optional integer. Maximum number of log lines to query (default: 100, max: 1000).
- grep: Optional string. Regex pattern to match against log message payloads.
- level: Optional string. Filter logs by severity level (e.g. 'err', 'warn', 'info', or list).`
}

func (t *QueryDmesgTool) Description() string {
	return "Filterable query of the kernel ring buffer"
}

func (t *QueryDmesgTool) Parameters() []registry.ToolParam {
	return []registry.ToolParam{
		{
			Name:        "lines",
			Type:        "integer",
			Description: "Optional: Number of lines to return (default 100, max 1000).",
			Required:    false,
		},
		{
			Name:        "grep",
			Type:        "string",
			Description: "Optional: Regexp pattern to match against log messages.",
			Required:    false,
		},
		{
			Name:        "level",
			Type:        "string",
			Description: "Optional: Severity priority filter (e.g. 'emerg', 'alert', 'crit', 'err', 'warn', 'notice', 'info', 'debug').",
			Required:    false,
		},
	}
}

func (t *QueryDmesgTool) Hidden() bool { return false }

func (t *QueryDmesgTool) IsSupported() (bool, string) {
	// Either klogctl works or dmesg binary is in PATH
	_, err := exec.LookPath("dmesg")
	if err == nil {
		return true, ""
	}
	// Try native check
	_, err = t.sysKlogctlSize(10)
	if err == nil {
		return true, ""
	}
	return false, "dmesg query not supported (missing dmesg binary and klogctl blocked)"
}

func (t *QueryDmesgTool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	var parsedArgs Args
	parsedArgs.Lines = 100 // default

	if len(args) > 0 {
		if err := json.Unmarshal(args, &parsedArgs); err != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("invalid arguments: %v", err)), nil
		}
	}

	if parsedArgs.Lines <= 0 {
		parsedArgs.Lines = 100
	}
	if parsedArgs.Lines > 1000 {
		parsedArgs.Lines = 1000
	}

	var grepRegexp *regexp.Regexp
	if parsedArgs.Grep != "" {
		var err error
		grepRegexp, err = regexp.Compile(parsedArgs.Grep)
		if err != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("invalid regex pattern: %v", err)), nil
		}
	}

	var rawLogs []byte
	// Try native syscall first
	size, err := t.sysKlogctlSize(10)
	if err == nil && size > 0 {
		buf := make([]byte, size)
		n, readErr := t.sysKlogctlRead(3, buf)
		if readErr == nil && n > 0 {
			rawLogs = buf[:n]
		}
	}

	// Fallback to dmesg command if native read failed or returned empty
	if len(rawLogs) == 0 {
		cmdOutput, cmdErr := t.execCommand(ctx, "dmesg", "-r")
		if cmdErr != nil {
			// If both failed, return execution error
			errMsg := fmt.Sprintf("failed to read kernel log: syscall err=%v; cmd err=%v", err, cmdErr)
			return registry.NewErrorResult(t.Name(), errMsg), nil
		}
		rawLogs = cmdOutput
	}

	var entries []DmesgEntry
	lines := bytes.Split(rawLogs, []byte("\n"))
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}

		entry := parseDmesgLine(string(line))

		// Apply Level filter
		if parsedArgs.Level != "" && !strings.EqualFold(entry.Level, parsedArgs.Level) {
			continue
		}

		// Apply grep filter
		if grepRegexp != nil && !grepRegexp.MatchString(entry.Message) {
			continue
		}

		entries = append(entries, entry)
	}

	// Slice to get last 'lines' entries
	if len(entries) > parsedArgs.Lines {
		entries = entries[len(entries)-parsedArgs.Lines:]
	}

	summaryStr := fmt.Sprintf("Dmesg Query: parsed %d log entries", len(entries))
	data := DmesgData{Entries: entries}

	res := registry.NewResult(t.Name(), registry.StatusOK, summaryStr, data)
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()

	return res, nil
}

func parseDmesgLine(line string) DmesgEntry {
	entry := DmesgEntry{}
	line = strings.TrimSpace(line)

	// 1. Parse priority prefix (e.g. "<4>")
	if strings.HasPrefix(line, "<") {
		idx := strings.Index(line, ">")
		if idx > 0 && idx < 5 {
			prioValStr := line[1:idx]
			if prioVal, err := strconv.Atoi(prioValStr); err == nil {
				levelNum := prioVal & 7
				entry.Level = logLevels[levelNum]
				entry.Facility = strconv.Itoa(prioVal >> 3)
			}
			line = line[idx+1:]
		}
	}

	// 2. Parse timestamp prefix (e.g. "[  261.273849]")
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "[") {
		idx := strings.Index(line, "]")
		if idx > 0 {
			tsStr := strings.TrimSpace(line[1:idx])
			if ts, err := strconv.ParseFloat(tsStr, 64); err == nil {
				entry.Timestamp = ts
			}
			line = line[idx+1:]
		}
	}

	entry.Message = strings.TrimSpace(line)
	return entry
}
