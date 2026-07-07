// Package loggedinsessions implements the get_logged_in_sessions tool.
//
// It enumerates interactive login sessions (local TTYs and remote SSH/PTY
// logins) natively by parsing the binary /run/utmp database, falling back
// to `loginctl list-sessions --output=json` when utmp is unavailable.
package loggedinsessions

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// userProcess is the ut_type value for an active interactive login
// (USER_PROCESS in <utmp.h>).
const userProcess = 7

// maxSessions caps the reported session list to keep LLM payloads bounded.
const maxSessions = 100

// utmpRecord mirrors the glibc struct utmp layout on little-endian x86_64
// (384 bytes). ut_type is a C short but alignment padding widens the field
// to 4 bytes; ut_exit is two C shorts; the timeval uses 32-bit fields for
// cross-architecture file compatibility.
type utmpRecord struct {
	Type    int32
	Pid     int32
	Line    [32]byte
	ID      [4]byte
	User    [32]byte
	Host    [256]byte
	Exit    [2]int16
	Session int32
	TvSec   int32
	TvUsec  int32
	AddrV6  [4]int32
	Unused  [20]byte
}

// utmpRecordSize is the on-disk size of one utmp record (384 bytes).
var utmpRecordSize = binary.Size(utmpRecord{})

// Session is one interactive login session.
type Session struct {
	// User is the login name.
	User string `json:"user"`

	// TTY is the controlling terminal line (e.g. "tty2", "pts/0").
	TTY string `json:"tty"`

	// RemoteHost is the origin host/IP for remote logins. Empty for
	// local console sessions.
	RemoteHost string `json:"remote_host"`

	// LoginTime is the session start time in RFC3339 (UTC). Empty when
	// the source does not expose it (loginctl fallback).
	LoginTime string `json:"login_time"`

	// PID is the session leader process ID (0 when unknown).
	PID int `json:"pid"`
}

// Output is the tool's data payload.
type Output struct {
	Sessions []Session `json:"sessions"`
	Count    int       `json:"count"`
	Source   string    `json:"source"`
}

// Tool implements registry.Tool for get_logged_in_sessions.
type Tool struct {
	// runRoot is the directory containing the utmp database
	// (normally /run). Injectable for tests.
	runRoot string

	// execCommand runs a binary and returns its stdout. Injectable.
	execCommand func(ctx context.Context, name string, args ...string) ([]byte, error)

	// execLookPath resolves a binary in PATH. Injectable.
	execLookPath func(file string) (string, error)
}

// New creates the tool with production data sources.
func New() *Tool {
	return &Tool{
		runRoot: "/run",
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
func (t *Tool) Name() string {
	return "get_logged_in_sessions"
}

// Description returns a one-line summary for get_tool_list.
func (t *Tool) Description() string {
	return "List active interactive login sessions (local TTYs and remote SSH logins)"
}

// Help returns the full tool documentation.
func (t *Tool) Help() string {
	return `Enumerates interactive login sessions to reveal who is on the node, from where, and since when.

Data Sources:
- Native: /run/utmp binary session database. Each 384-byte record is decoded
  (little-endian x86_64 glibc layout) and only USER_PROCESS entries — real
  interactive logins — are reported. Reboot/runlevel/dead-process records are
  filtered out.
- Fallback: 'loginctl list-sessions --output=json' (systemd-logind) when the
  utmp database is missing or unreadable. loginctl output carries no login
  timestamp or leader PID, so those fields degrade to empty/0.

Output: sessions[] {user, tty, remote_host (empty for local logins),
login_time (RFC3339 UTC), pid}, count, and the source used (utmp|loginctl).
The list is capped at 100 sessions.

Degradation Profile:
- Malformed or truncated utmp records are skipped, never fatal.
- Zero active sessions is a valid OK result (headless nodes).
- Deterministic: no heuristics, records are reported as stored by the kernel/login stack.`
}

// Category classifies the tool.
func (t *Tool) Category() registry.Category {
	return registry.CategorySystem
}

// Parameters returns the parameter schema (none).
func (t *Tool) Parameters() []registry.ToolParam {
	return []registry.ToolParam{}
}

// Hidden reports whether the tool is hidden from discovery.
func (t *Tool) Hidden() bool { return false }

// IsSupported checks that at least one session source exists.
func (t *Tool) IsSupported() (bool, string) {
	if registry.PathExists(filepath.Join(t.runRoot, "utmp")) {
		return true, ""
	}
	if _, err := t.execLookPath("loginctl"); err == nil {
		return true, ""
	}
	return false, "no utmp database and no loginctl binary found"
}

// Execute reads the active login sessions.
func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	if err := ctx.Err(); err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("context cancelled: %v", err)), nil
	}

	var sessions []Session
	source := ""

	utmpPath := filepath.Join(t.runRoot, "utmp")
	if data, err := os.ReadFile(utmpPath); err == nil {
		sessions = parseUtmp(data)
		source = "utmp"
	} else if _, lookErr := t.execLookPath("loginctl"); lookErr == nil {
		out, cmdErr := t.execCommand(ctx, "loginctl", "list-sessions", "--output=json")
		if cmdErr != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("utmp unreadable (%v) and loginctl failed: %v", err, cmdErr)), nil
		}
		parsed, parseErr := parseLoginctlJSON(out)
		if parseErr != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("failed to parse loginctl output: %v", parseErr)), nil
		}
		sessions = parsed
		source = "loginctl"
	} else {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("no session source available: %s unreadable (%v) and loginctl not in PATH", utmpPath, err)), nil
	}

	if len(sessions) > maxSessions {
		sessions = sessions[:maxSessions]
	}

	out := Output{
		Sessions: sessions,
		Count:    len(sessions),
		Source:   source,
	}

	res := registry.NewResult(t.Name(), registry.StatusOK,
		fmt.Sprintf("%d active login sessions (source: %s)", out.Count, source), out)
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	res.Metadata.FilteringMethod = "deterministic"
	return res, nil
}

// parseUtmp decodes the binary utmp database, returning only USER_PROCESS
// records (active interactive logins). Truncated or malformed trailing
// records are skipped.
func parseUtmp(data []byte) []Session {
	sessions := []Session{}
	if utmpRecordSize <= 0 {
		return sessions
	}
	for off := 0; off+utmpRecordSize <= len(data); off += utmpRecordSize {
		var rec utmpRecord
		if err := binary.Read(bytes.NewReader(data[off:off+utmpRecordSize]), binary.LittleEndian, &rec); err != nil {
			continue
		}
		if rec.Type != userProcess {
			continue
		}
		user := cString(rec.User[:])
		if user == "" {
			continue
		}
		loginTime := ""
		if rec.TvSec > 0 {
			loginTime = time.Unix(int64(rec.TvSec), int64(rec.TvUsec)*1000).UTC().Format(time.RFC3339)
		}
		sessions = append(sessions, Session{
			User:       user,
			TTY:        cString(rec.Line[:]),
			RemoteHost: cString(rec.Host[:]),
			LoginTime:  loginTime,
			PID:        int(rec.Pid),
		})
	}
	return sessions
}

// loginctlSession mirrors one element of `loginctl list-sessions
// --output=json`.
type loginctlSession struct {
	Session string `json:"session"`
	UID     int    `json:"uid"`
	User    string `json:"user"`
	Seat    string `json:"seat"`
	TTY     string `json:"tty"`
}

// parseLoginctlJSON converts loginctl JSON output into sessions. loginctl
// does not expose login timestamps or leader PIDs in list output, so those
// fields are left zero-valued.
func parseLoginctlJSON(data []byte) ([]Session, error) {
	var raw []loginctlSession
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	sessions := make([]Session, 0, len(raw))
	for _, r := range raw {
		if r.User == "" {
			continue
		}
		sessions = append(sessions, Session{
			User: r.User,
			TTY:  r.TTY,
		})
	}
	return sessions, nil
}

// cString trims a NUL-padded C char array to a Go string.
func cString(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		return string(b[:i])
	}
	return string(b)
}
