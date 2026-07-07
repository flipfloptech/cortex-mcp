package queryjournalctl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

type JournalEntry struct {
	Timestamp  string `json:"timestamp"`
	Message    string `json:"message"`
	Identifier string `json:"identifier,omitempty"`
	Priority   string `json:"priority,omitempty"`
	Hostname   string `json:"hostname,omitempty"`
	Pid        string `json:"pid,omitempty"`
}

type BootInfo struct {
	Index      int    `json:"index"`
	BootID     string `json:"boot_id"`
	FirstEntry string `json:"first_entry"`
	LastEntry  string `json:"last_entry"`
}

type JournalctlData struct {
	Entries []JournalEntry `json:"entries,omitempty"`
	Boots   []BootInfo     `json:"boots,omitempty"`
}

type Args struct {
	Lines     int    `json:"lines"`
	Unit      string `json:"unit"`
	Since     string `json:"since"`
	Until     string `json:"until"`
	Priority  string `json:"priority"`
	Grep      string `json:"grep"`
	Boot      string `json:"boot"`
	ListBoots bool   `json:"list_boots"`
}

type QueryJournalctlTool struct {
	execCommand func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func New() *QueryJournalctlTool {
	return &QueryJournalctlTool{
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		},
	}
}

func init() {
	registry.Register(New())
}

func (t *QueryJournalctlTool) Name() string {
	return "query_journalctl"
}

func (t *QueryJournalctlTool) Category() registry.Category {
	return registry.CategorySystem
}

func (t *QueryJournalctlTool) Help() string {
	return `Query the systemd journal logs with time-bounds and regex filtering.

Collects log entries from the journal database, parsing priority/severity, unit identifiers, process IDs, hostnames, and message payloads.

Parameters:
- lines: Optional integer. Maximum number of log lines to query (default: 100, max: 1000).
- unit: Optional string. Filter logs by systemd service/unit name (e.g. 'ssh', 'docker').
- since: Optional string. Start date/time window (e.g. '2026-07-07 09:00:00', '1h', '15m').
- until: Optional string. End date/time window.
- priority: Optional string. Filter logs by severity level (e.g. 'err', 'warning', or 0-7 numeric).
- grep: Optional string. Regex pattern to filter log message payloads natively.
- boot: Optional string. Filter logs by boot ID or offset (e.g. '0' for current boot, '-1' for previous boot, or 32-char hex).
- list_boots: Optional boolean. If true, lists the boot history with indexes, IDs, and time ranges instead of querying logs.`
}

func (t *QueryJournalctlTool) Description() string {
	return "Time-bounded, regex-filterable query of systemd journal"
}

func (t *QueryJournalctlTool) Parameters() []registry.ToolParam {
	return []registry.ToolParam{
		{
			Name:        "lines",
			Type:        "integer",
			Description: "Optional: Number of lines to return (default 100, max 1000).",
			Required:    false,
		},
		{
			Name:        "unit",
			Type:        "string",
			Description: "Optional: Filter logs by systemd unit name (e.g., 'ssh', 'docker').",
			Required:    false,
		},
		{
			Name:        "since",
			Type:        "string",
			Description: "Optional: Start time filter (e.g., '2026-07-07 09:00:00', '1h', '15m').",
			Required:    false,
		},
		{
			Name:        "until",
			Type:        "string",
			Description: "Optional: End time filter.",
			Required:    false,
		},
		{
			Name:        "priority",
			Type:        "string",
			Description: "Optional: Severity priority filter (e.g., 'err', 'warning', or 0-7).",
			Required:    false,
		},
		{
			Name:        "grep",
			Type:        "string",
			Description: "Optional: Regexp pattern to match against log messages.",
			Required:    false,
		},
		{
			Name:        "boot",
			Type:        "string",
			Description: "Optional: Filter logs by boot ID or offset (e.g., '0', '-1', or 32-char hex ID).",
			Required:    false,
		},
		{
			Name:        "list_boots",
			Type:        "boolean",
			Description: "Optional: If true, lists system boot history instead of returning log entries.",
			Required:    false,
		},
	}
}

func (t *QueryJournalctlTool) Hidden() bool { return false }

func (t *QueryJournalctlTool) IsSupported() (bool, string) {
	_, err := exec.LookPath("journalctl")
	if err != nil {
		return false, "journalctl query not supported (missing journalctl binary)"
	}
	return true, ""
}

func (t *QueryJournalctlTool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	var parsedArgs Args
	parsedArgs.Lines = 100 // default

	if len(args) > 0 {
		if err := json.Unmarshal(args, &parsedArgs); err != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("invalid arguments: %v", err)), nil
		}
	}

	// 1. Handle boots list request
	if parsedArgs.ListBoots {
		output, err := t.execCommand(ctx, "journalctl", "--list-boots")
		if err != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("failed to list boots: %v", err)), nil
		}
		boots := parseListBoots(output)
		summaryStr := fmt.Sprintf("Journalctl: %d boots listed", len(boots))
		res := registry.NewResult(t.Name(), registry.StatusOK, summaryStr, JournalctlData{Boots: boots})
		res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
		return res, nil
	}

	// 2. Handle logs query
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

	cmdArgs := []string{"-o", "json"}
	cmdArgs = append(cmdArgs, "-n", strconv.Itoa(parsedArgs.Lines))

	if parsedArgs.Boot != "" {
		cmdArgs = append(cmdArgs, "-b", parsedArgs.Boot)
	}
	if parsedArgs.Unit != "" {
		cmdArgs = append(cmdArgs, "-u", parsedArgs.Unit)
	}
	if parsedArgs.Since != "" {
		cmdArgs = append(cmdArgs, "--since", parsedArgs.Since)
	}
	if parsedArgs.Until != "" {
		cmdArgs = append(cmdArgs, "--until", parsedArgs.Until)
	}
	if parsedArgs.Priority != "" {
		cmdArgs = append(cmdArgs, "-p", parsedArgs.Priority)
	}

	output, err := t.execCommand(ctx, "journalctl", cmdArgs...)
	if err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("failed to run journalctl: %v", err)), nil
	}

	var entries []JournalEntry
	lines := bytes.Split(output, []byte("\n"))
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}

		entry, parseErr := parseJournalLine(line)
		if parseErr == nil {
			if grepRegexp != nil && !grepRegexp.MatchString(entry.Message) {
				continue
			}
			entries = append(entries, entry)
		}
	}

	summaryStr := fmt.Sprintf("Journalctl Query: parsed %d log entries", len(entries))
	data := JournalctlData{Entries: entries}

	res := registry.NewResult(t.Name(), registry.StatusOK, summaryStr, data)
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()

	return res, nil
}

type rawJournalEntry struct {
	RealtimeTimestamp string          `json:"__REALTIME_TIMESTAMP"`
	Message           json.RawMessage `json:"MESSAGE"`
	SyslogIdentifier  string          `json:"SYSLOG_IDENTIFIER"`
	SystemdUnit       string          `json:"_SYSTEMD_UNIT"`
	Priority          string          `json:"PRIORITY"`
	Hostname          string          `json:"_HOSTNAME"`
	Pid               string          `json:"_PID"`
}

func parseJournalLine(line []byte) (JournalEntry, error) {
	var raw rawJournalEntry
	if err := json.Unmarshal(line, &raw); err != nil {
		return JournalEntry{}, err
	}

	var message string
	if len(raw.Message) > 0 {
		if raw.Message[0] == '"' {
			if err := json.Unmarshal(raw.Message, &message); err != nil {
				message = string(raw.Message)
			}
		} else if raw.Message[0] == '[' {
			var bytesVal []byte
			if err := json.Unmarshal(raw.Message, &bytesVal); err == nil {
				message = string(bytesVal)
			} else {
				message = string(raw.Message)
			}
		} else {
			message = string(raw.Message)
		}
	}

	ts := formatRealtimeTimestamp(raw.RealtimeTimestamp)

	identifier := raw.SyslogIdentifier
	if identifier == "" && raw.SystemdUnit != "" {
		identifier = strings.TrimSuffix(raw.SystemdUnit, ".service")
	}

	return JournalEntry{
		Timestamp:  ts,
		Message:    message,
		Identifier: identifier,
		Priority:   raw.Priority,
		Hostname:   raw.Hostname,
		Pid:        raw.Pid,
	}, nil
}

func formatRealtimeTimestamp(tsStr string) string {
	if tsStr == "" {
		return ""
	}
	val, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return ""
	}
	sec := val / 1000000
	usec := val % 1000000
	t := time.Unix(sec, usec*1000).UTC()
	return t.Format("2006-01-02T15:04:05.000000Z")
}

func parseListBoots(output []byte) []BootInfo {
	var boots []BootInfo
	lines := bytes.Split(output, []byte("\n"))

	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if bytes.HasPrefix(line, []byte("IDX")) {
			continue // skip header
		}

		// Split line into fields
		tokens := strings.Fields(string(line))
		if len(tokens) < 10 {
			continue
		}

		idx, err := strconv.Atoi(tokens[0])
		if err != nil {
			continue
		}

		bootID := tokens[1]
		firstEntry := strings.Join(tokens[2:6], " ")
		lastEntry := strings.Join(tokens[6:10], " ")

		boots = append(boots, BootInfo{
			Index:      idx,
			BootID:     bootID,
			FirstEntry: firstEntry,
			LastEntry:  lastEntry,
		})
	}
	return boots
}
