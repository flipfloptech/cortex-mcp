// Package scheduledjobs implements the get_scheduled_jobs tool.
//
// It inventories recurring background work: systemd timers (via systemctl,
// JSON output with plain-text fallback) and cron entries parsed natively
// from /etc/crontab, /etc/cron.d/*, and per-user spool crontabs.
package scheduledjobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

const (
	// maxTimers caps the timers list to keep output LLM-sized.
	maxTimers = 50

	// maxCronEntries caps the cron entry list.
	maxCronEntries = 100

	// maxCommandLen truncates long cron command lines.
	maxCommandLen = 120
)

// dateTokenRe matches the date component of systemd's plain-text timestamps
// ("Tue 2025-08-19 00:00:00 UTC").
var dateTokenRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// Timer is one systemd timer unit.
type Timer struct {
	Unit      string `json:"unit"`
	Activates string `json:"activates,omitempty"`

	// NextISO/LastISO are RFC3339 UTC timestamps, or null when the timer
	// has no scheduled next run / has never fired.
	NextISO *string `json:"next_iso"`
	LastISO *string `json:"last_iso"`
}

// CronEntry is one decoded crontab line.
type CronEntry struct {
	// Schedule is the five-field cron expression or a verbatim @special
	// (e.g. "@reboot", "@daily").
	Schedule string `json:"schedule"`

	// User is the executing user (from the system crontab user column, or
	// the spool file name for user crontabs).
	User string `json:"user,omitempty"`

	// Command is the command line, truncated to 120 characters.
	Command string `json:"command"`

	// SourceFile is the crontab file the entry came from.
	SourceFile string `json:"source_file"`
}

// Summary carries the discovery totals (before list capping).
type Summary struct {
	Timers          int `json:"timers"`
	CronEntries     int `json:"cron_entries"`
	CrontabsSkipped int `json:"crontabs_skipped"`
}

// Output is the tool's data payload.
type Output struct {
	Timers      []Timer     `json:"timers"`
	CronEntries []CronEntry `json:"cron_entries"`
	Summary     Summary     `json:"summary"`
}

// Tool implements registry.Tool for get_scheduled_jobs.
// All external dependencies are injectable fields for hermetic tests.
type Tool struct {
	etcRoot      string
	spoolRoots   []string
	execCommand  func(ctx context.Context, name string, args ...string) ([]byte, error)
	execLookPath func(file string) (string, error)
}

// New returns a scheduled jobs tool bound to the real host.
func New() *Tool {
	return &Tool{
		etcRoot: "/etc",
		// Debian/Ubuntu use /var/spool/cron/crontabs; RHEL/SUSE use
		// /var/spool/cron. Both are scanned (files only), so the Debian
		// subdirectory does not double-count.
		spoolRoots: []string{"/var/spool/cron/crontabs", "/var/spool/cron"},
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		},
		execLookPath: exec.LookPath,
	}
}

func init() {
	registry.Register(New())
}

// Name returns the unique tool identifier.
func (t *Tool) Name() string { return "get_scheduled_jobs" }

// Description returns the one-line summary for tool discovery.
func (t *Tool) Description() string {
	return "Inventory recurring background work: systemd timers and cron entries (system, cron.d, user spools)"
}

// Help returns the full tool documentation.
func (t *Tool) Help() string {
	return `get_scheduled_jobs — Scheduled Job Inventory (systemd timers + cron)

Lists everything scheduled to run periodically on this node. Essential for
explaining periodic load spikes, tracing unexpected file changes, and
auditing what automation is active.

Data Sources:
  - systemd timers: "systemctl list-timers --all --no-pager --output=json"
    (microsecond epoch fields converted to RFC3339 UTC). On older systemd
    without JSON output, the plain-text table is parsed as a fallback
    (unit/activates always recovered; timestamps best-effort).
  - System cron (native file parsing): /etc/crontab and /etc/cron.d/*
    ("m h dom mon dow user command" format).
  - User crontabs (native): /var/spool/cron/crontabs/* (Debian) and
    /var/spool/cron/* (RHEL), one file per user, no user column.
  - Comments and environment lines are skipped; @reboot/@daily-style
    specials are preserved verbatim as the schedule.

Formatting / Limits:
  - Timers capped at 50, cron entries at 100; "summary" always carries the
    total counts discovered (which may exceed the capped list lengths).
  - Commands truncated to 120 characters.

Degradation Profile:
  - systemctl missing or failing: timers list is empty, cron still reported.
  - Unreadable user crontab files/dirs (running unprivileged): counted in
    summary.crontabs_skipped, execution still succeeds with status ok.
  - Missing cron paths are simply skipped.

Parameters: None
Supported on: Linux (systemd and/or cron present)`
}

// Category classifies this tool under system.
func (t *Tool) Category() registry.Category { return registry.CategorySystem }

// Parameters returns nil — this tool takes no arguments.
func (t *Tool) Parameters() []registry.ToolParam { return nil }

// Hidden returns false — this tool is LLM-discoverable.
func (t *Tool) Hidden() bool { return false }

// IsSupported requires systemctl in PATH or at least one cron path.
func (t *Tool) IsSupported() (bool, string) {
	if _, err := t.execLookPath("systemctl"); err == nil {
		return true, ""
	}
	cronPaths := []string{
		filepath.Join(t.etcRoot, "crontab"),
		filepath.Join(t.etcRoot, "cron.d"),
	}
	cronPaths = append(cronPaths, t.spoolRoots...)
	for _, p := range cronPaths {
		if registry.PathExists(p) {
			return true, ""
		}
	}
	return false, "neither systemctl nor any cron path (crontab, cron.d, spool) found"
}

// Execute inventories systemd timers and cron entries.
func (t *Tool) Execute(ctx context.Context, _ json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	if err := ctx.Err(); err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("context canceled: %v", err)), nil
	}

	timers := t.collectTimers(ctx)
	cronEntries, skipped := t.collectCron()

	out := Output{
		Timers:      timers,
		CronEntries: cronEntries,
		Summary: Summary{
			Timers:          len(timers),
			CronEntries:     len(cronEntries),
			CrontabsSkipped: skipped,
		},
	}
	if len(out.Timers) > maxTimers {
		out.Timers = out.Timers[:maxTimers]
	}
	if len(out.CronEntries) > maxCronEntries {
		out.CronEntries = out.CronEntries[:maxCronEntries]
	}
	if out.Timers == nil {
		out.Timers = []Timer{}
	}
	if out.CronEntries == nil {
		out.CronEntries = []CronEntry{}
	}

	summary := fmt.Sprintf("Scheduled jobs: %d systemd timers, %d cron entries", out.Summary.Timers, out.Summary.CronEntries)
	if skipped > 0 {
		summary += fmt.Sprintf(" (%d crontabs unreadable, run as root for full inventory)", skipped)
	}

	result := registry.NewResult(t.Name(), registry.StatusOK, summary, out)
	result.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	result.Metadata.FilteringMethod = "deterministic"
	return result, nil
}

// collectTimers lists systemd timers, preferring JSON output and falling
// back to plain-text parsing on older systemd. Returns nil when systemctl
// is unavailable or both formats fail.
func (t *Tool) collectTimers(ctx context.Context) []Timer {
	if _, err := t.execLookPath("systemctl"); err != nil {
		return nil
	}
	if raw, err := t.execCommand(ctx, "systemctl", "list-timers", "--all", "--no-pager", "--output=json"); err == nil {
		if timers, perr := parseTimersJSON(raw); perr == nil {
			return timers
		}
	}
	// Fallback: plain text table (systemd < 246 has no --output=json).
	if raw, err := t.execCommand(ctx, "systemctl", "list-timers", "--all", "--no-pager"); err == nil {
		return parseTimersText(raw)
	}
	return nil
}

// parseTimersJSON decodes "systemctl list-timers --output=json": an array of
// {next, left, last, passed, unit, activates} objects where the time fields
// are microsecond epoch integers or null.
func parseTimersJSON(raw []byte) ([]Timer, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var rows []map[string]interface{}
	if err := dec.Decode(&rows); err != nil {
		return nil, fmt.Errorf("not valid list-timers JSON: %w", err)
	}

	timers := make([]Timer, 0, len(rows))
	for _, row := range rows {
		var tm Timer
		if unit, ok := row["unit"].(string); ok {
			tm.Unit = unit
		}
		switch act := row["activates"].(type) {
		case string:
			tm.Activates = act
		case []interface{}:
			// Newer systemd may report activates as a list of units.
			var names []string
			for _, a := range act {
				if s, ok := a.(string); ok {
					names = append(names, s)
				}
			}
			tm.Activates = strings.Join(names, ", ")
		}
		if usec, ok := asInt64(row["next"]); ok {
			iso := usecToISO(usec)
			tm.NextISO = &iso
		}
		if usec, ok := asInt64(row["last"]); ok {
			iso := usecToISO(usec)
			tm.LastISO = &iso
		}
		timers = append(timers, tm)
	}
	return timers, nil
}

// parseTimersText best-effort parses the plain-text list-timers table.
// The unit and activates columns are always the last two fields; the NEXT
// and LAST timestamps are recovered by locating date tokens.
func parseTimersText(raw []byte) []Timer {
	var timers []Timer
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		tokens := strings.Fields(line)
		if len(tokens) < 2 {
			continue
		}
		unit := tokens[len(tokens)-2]
		if !strings.HasSuffix(unit, ".timer") {
			continue // header, legend, or unrelated line
		}
		tm := Timer{Unit: unit, Activates: tokens[len(tokens)-1]}

		// Timestamps look like "Tue 2025-08-19 00:00:00 UTC": weekday,
		// date, time, zone. The first is NEXT, the second is LAST.
		var stamps []string
		for i, tok := range tokens {
			if i > 0 && i+2 < len(tokens) && dateTokenRe.MatchString(tok) {
				stamps = append(stamps, strings.Join(tokens[i-1:i+3], " "))
			}
		}
		if len(stamps) > 0 {
			if iso, ok := parseSystemdStamp(stamps[0]); ok {
				tm.NextISO = &iso
			}
		}
		if len(stamps) > 1 {
			if iso, ok := parseSystemdStamp(stamps[1]); ok {
				tm.LastISO = &iso
			}
		}
		timers = append(timers, tm)
	}
	return timers
}

// parseSystemdStamp parses a "Mon 2006-01-02 15:04:05 MST" timestamp into
// RFC3339 UTC.
func parseSystemdStamp(s string) (string, bool) {
	ts, err := time.Parse("Mon 2006-01-02 15:04:05 MST", s)
	if err != nil {
		return "", false
	}
	return ts.UTC().Format(time.RFC3339), true
}

// usecToISO converts a microsecond epoch value to RFC3339 UTC.
func usecToISO(usec int64) string {
	return time.UnixMicro(usec).UTC().Format(time.RFC3339)
}

// asInt64 coerces a decoded JSON value (json.Number or string) to int64.
func asInt64(v interface{}) (int64, bool) {
	switch n := v.(type) {
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	case string:
		i, err := strconv.ParseInt(n, 10, 64)
		return i, err == nil
	case float64:
		return int64(n), true
	default:
		return 0, false
	}
}

// collectCron gathers entries from the system crontab, cron.d drop-ins, and
// per-user spool crontabs. Returns the entries and the number of crontab
// files/directories skipped because they were unreadable.
func (t *Tool) collectCron() ([]CronEntry, int) {
	var entries []CronEntry
	skipped := 0

	// System crontab (has a user column).
	sysCrontab := filepath.Join(t.etcRoot, "crontab")
	if raw, err := os.ReadFile(sysCrontab); err == nil {
		entries = append(entries, parseCrontabContent(string(raw), true, "", sysCrontab)...)
	} else if !os.IsNotExist(err) {
		skipped++
	}

	// cron.d drop-ins (same format as the system crontab).
	cronD := filepath.Join(t.etcRoot, "cron.d")
	if dirEntries, err := os.ReadDir(cronD); err == nil {
		for _, de := range dirEntries {
			if de.IsDir() {
				continue
			}
			path := filepath.Join(cronD, de.Name())
			raw, err := os.ReadFile(path)
			if err != nil {
				skipped++
				continue
			}
			entries = append(entries, parseCrontabContent(string(raw), true, "", path)...)
		}
	} else if !os.IsNotExist(err) {
		skipped++
	}

	// Per-user spool crontabs (no user column; user = file name).
	for _, spool := range t.spoolRoots {
		dirEntries, err := os.ReadDir(spool)
		if err != nil {
			if !os.IsNotExist(err) {
				skipped++ // typically EACCES when not root
			}
			continue
		}
		for _, de := range dirEntries {
			if de.IsDir() {
				continue
			}
			path := filepath.Join(spool, de.Name())
			raw, err := os.ReadFile(path)
			if err != nil {
				skipped++
				continue
			}
			entries = append(entries, parseCrontabContent(string(raw), false, de.Name(), path)...)
		}
	}

	return entries, skipped
}

// parseCrontabContent decodes crontab lines. When hasUserField is true the
// sixth column is the executing user (system crontab format); otherwise
// defaultUser is applied (user spool format). Comments, blank lines, and
// environment assignments are skipped; @special schedules are preserved
// verbatim.
func parseCrontabContent(content string, hasUserField bool, defaultUser, sourceFile string) []CronEntry {
	var entries []CronEntry
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		// Environment assignment (SHELL=/bin/sh, MAILTO="") — skip.
		if strings.Contains(fields[0], "=") {
			continue
		}

		var entry CronEntry
		if strings.HasPrefix(fields[0], "@") {
			// @reboot/@daily style specials.
			if hasUserField {
				if len(fields) < 3 {
					continue
				}
				entry = CronEntry{Schedule: fields[0], User: fields[1], Command: strings.Join(fields[2:], " ")}
			} else {
				if len(fields) < 2 {
					continue
				}
				entry = CronEntry{Schedule: fields[0], User: defaultUser, Command: strings.Join(fields[1:], " ")}
			}
		} else {
			// Five-field schedule: m h dom mon dow.
			if hasUserField {
				if len(fields) < 7 {
					continue
				}
				entry = CronEntry{Schedule: strings.Join(fields[:5], " "), User: fields[5], Command: strings.Join(fields[6:], " ")}
			} else {
				if len(fields) < 6 {
					continue
				}
				entry = CronEntry{Schedule: strings.Join(fields[:5], " "), User: defaultUser, Command: strings.Join(fields[5:], " ")}
			}
		}
		entry.Command = truncateCommand(entry.Command)
		entry.SourceFile = sourceFile
		entries = append(entries, entry)
	}
	return entries
}

// truncateCommand caps a command line at maxCommandLen characters,
// terminating truncated commands with "...".
func truncateCommand(cmd string) string {
	if len(cmd) <= maxCommandLen {
		return cmd
	}
	return cmd[:maxCommandLen-3] + "..."
}
