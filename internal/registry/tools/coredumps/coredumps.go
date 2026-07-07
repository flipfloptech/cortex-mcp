// Package coredumps implements the query_coredumps tool.
//
// It reports recent application crashes (core dumps) captured by
// systemd-coredump, primarily via `coredumpctl list --json=short` and
// falling back to scanning /var/lib/systemd/coredump filenames when the
// binary is unavailable.
package coredumps

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

const (
	// defaultLastN is how many dumps are returned when last_n is omitted.
	defaultLastN = 20

	// maxLastN caps last_n to keep LLM payloads bounded.
	maxLastN = 100
)

// Dump describes one captured core dump.
type Dump struct {
	// Time is the crash time in RFC3339 (UTC).
	Time string `json:"time"`

	// PID is the crashed process ID.
	PID int `json:"pid"`

	// UID is the owning user ID.
	UID int `json:"uid"`

	// SignalName is the decoded fatal signal (e.g. SIGSEGV). Empty when
	// the source does not expose it (filename fallback).
	SignalName string `json:"signal_name,omitempty"`

	// Executable is the crashed binary path (coredumpctl) or comm name
	// (filename fallback).
	Executable string `json:"executable"`

	// CorefilePresent reports whether the core file is still on disk.
	CorefilePresent bool `json:"corefile_present"`

	// timeUsec is the raw µs-epoch timestamp used for newest-first
	// sorting. Not serialized.
	timeUsec int64
}

// Output is the tool's data payload.
type Output struct {
	Dumps             []Dump         `json:"dumps"`
	CountByExecutable map[string]int `json:"count_by_executable"`
	Total             int            `json:"total"`
}

// Tool implements registry.Tool for query_coredumps.
type Tool struct {
	// coredumpRoot is the systemd coredump spool directory
	// (normally /var/lib/systemd/coredump). Injectable for tests.
	coredumpRoot string

	// execCommand runs a binary and returns its combined output.
	// Injectable.
	execCommand func(ctx context.Context, name string, args ...string) ([]byte, error)

	// execLookPath resolves a binary in PATH. Injectable.
	execLookPath func(file string) (string, error)
}

// New creates the tool with production data sources.
func New() *Tool {
	return &Tool{
		coredumpRoot: "/var/lib/systemd/coredump",
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			// CombinedOutput: "No coredumps found." is printed to
			// stderr with exit status 1 and must be inspectable.
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		},
		execLookPath: exec.LookPath,
	}
}

func init() {
	registry.Register(New())
}

// Name returns the unique tool identifier.
func (t *Tool) Name() string {
	return "query_coredumps"
}

// Description returns a one-line summary for get_tool_list.
func (t *Tool) Description() string {
	return "Query recent application crashes (core dumps) with signal decoding"
}

// Help returns the full tool documentation.
func (t *Tool) Help() string {
	return `Queries recent application crashes captured by systemd-coredump, identifying
crash-looping executables and the fatal signals that killed them.

Data Sources:
- Primary: 'coredumpctl list --json=short --no-pager' (JSON array with
  µs-epoch time, pid, uid, sig, corefile state, and executable path).
  An exit status of 1 with "No coredumps found" is a valid empty result.
- Fallback: filename scan of /var/lib/systemd/coredump, parsing
  "core.<comm>.<uid>.<boot-id>.<pid>.<timestamp>[.zst]" entries when the
  coredumpctl binary is unavailable or fails.

Parameters:
- last_n: optional integer, how many of the newest dumps to return
  (default 20, capped at 100).

Output: {dumps[] newest-first {time RFC3339 UTC, pid, uid, signal_name
(SIGSEGV/SIGABRT/SIGBUS/SIGILL/SIGFPE decoded, otherwise "SIG<n>"),
executable, corefile_present}, count_by_executable (over ALL dumps found),
total}.

Degradation Profile:
- "No coredumps found" and an empty spool directory are StatusOK with zero dumps.
- Unparseable spool filenames are skipped, never fatal.
- Filename-fallback entries carry no signal information (signal_name omitted).`
}

// Category classifies the tool.
func (t *Tool) Category() registry.Category {
	return registry.CategorySystem
}

// Parameters returns the parameter schema.
func (t *Tool) Parameters() []registry.ToolParam {
	return []registry.ToolParam{
		{
			Name:        "last_n",
			Type:        "integer",
			Description: "Number of newest dumps to return (default 20, max 100).",
			Required:    false,
			Default:     "20",
		},
	}
}

// Hidden reports whether the tool is hidden from discovery.
func (t *Tool) Hidden() bool { return false }

// IsSupported checks that at least one coredump source exists.
func (t *Tool) IsSupported() (bool, string) {
	if _, err := t.execLookPath("coredumpctl"); err == nil {
		return true, ""
	}
	if registry.PathExists(t.coredumpRoot) {
		return true, ""
	}
	return false, "no coredumpctl binary and no systemd coredump directory found"
}

// Execute queries recent core dumps.
func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	if err := ctx.Err(); err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("context cancelled: %v", err)), nil
	}

	var req struct {
		LastN *int `json:"last_n"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("invalid arguments: %v", err)), nil
		}
	}
	lastN := defaultLastN
	if req.LastN != nil && *req.LastN > 0 {
		lastN = *req.LastN
	}
	if lastN > maxLastN {
		lastN = maxLastN
	}

	var dumps []Dump
	source := ""

	if _, err := t.execLookPath("coredumpctl"); err == nil {
		out, cmdErr := t.execCommand(ctx, "coredumpctl", "list", "--json=short", "--no-pager")
		switch {
		case cmdErr == nil:
			parsed, parseErr := parseCoredumpctlJSON(out)
			if parseErr == nil {
				dumps = parsed
				source = "coredumpctl"
			}
		case strings.Contains(strings.ToLower(string(out)), "no coredumps found"):
			// Valid empty result: exit status 1 by design.
			dumps = []Dump{}
			source = "coredumpctl"
		}
	}

	if source == "" {
		if !registry.PathExists(t.coredumpRoot) {
			return registry.NewErrorResult(t.Name(),
				fmt.Sprintf("coredumpctl unavailable and %s does not exist", t.coredumpRoot)), nil
		}
		dumps = scanCoredumpDir(t.coredumpRoot)
		source = "directory-scan"
	}

	sort.SliceStable(dumps, func(i, j int) bool {
		return dumps[i].timeUsec > dumps[j].timeUsec
	})

	countByExe := map[string]int{}
	for _, d := range dumps {
		countByExe[d.Executable]++
	}

	total := len(dumps)
	returned := dumps
	if len(returned) > lastN {
		returned = returned[:lastN]
	}

	out := Output{
		Dumps:             returned,
		CountByExecutable: countByExe,
		Total:             total,
	}

	res := registry.NewResult(t.Name(), registry.StatusOK,
		fmt.Sprintf("%d core dumps found, returning newest %d (source: %s)", total, len(returned), source), out)
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	res.Metadata.FilteringMethod = "deterministic"
	return res, nil
}

// rawCoredumpEntry mirrors one element of `coredumpctl list --json=short`.
type rawCoredumpEntry struct {
	Time     int64  `json:"time"` // µs since epoch
	PID      int    `json:"pid"`
	UID      int    `json:"uid"`
	GID      int    `json:"gid"`
	Sig      int    `json:"sig"`
	Corefile string `json:"corefile"` // "present" | "missing" | "none"
	Exe      string `json:"exe"`
}

// parseCoredumpctlJSON converts coredumpctl JSON output into dumps.
func parseCoredumpctlJSON(data []byte) ([]Dump, error) {
	var raw []rawCoredumpEntry
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	dumps := make([]Dump, 0, len(raw))
	for _, r := range raw {
		dumps = append(dumps, Dump{
			Time:            time.Unix(r.Time/1000000, (r.Time%1000000)*1000).UTC().Format(time.RFC3339),
			PID:             r.PID,
			UID:             r.UID,
			SignalName:      signalName(r.Sig),
			Executable:      r.Exe,
			CorefilePresent: r.Corefile == "present",
			timeUsec:        r.Time,
		})
	}
	return dumps, nil
}

// scanCoredumpDir walks the systemd coredump spool directory and parses
// dump metadata out of the filenames. Unparseable names are skipped.
func scanCoredumpDir(dir string) []Dump {
	dumps := []Dump{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return dumps
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if dump, ok := parseCoreFilename(e.Name()); ok {
			dumps = append(dumps, dump)
		}
	}
	return dumps
}

// parseCoreFilename parses systemd coredump spool names of the form
// "core.<comm>.<uid>.<boot-id>.<pid>.<timestamp>[.zst]". The comm segment
// may itself contain dots, so fixed fields are anchored from the right.
func parseCoreFilename(name string) (Dump, bool) {
	name = strings.TrimSuffix(name, ".zst")

	fields := strings.Split(name, ".")
	// core + comm(>=1 segment) + uid + boot + pid + ts = at least 6 fields.
	if len(fields) < 6 || fields[0] != "core" {
		return Dump{}, false
	}

	// Right-anchored fixed fields: [... comm ...] uid, boot-id, pid, ts.
	n := len(fields)
	tsRaw, pidRaw, uidRaw := fields[n-1], fields[n-2], fields[n-4]

	pid, err := strconv.Atoi(pidRaw)
	if err != nil {
		return Dump{}, false
	}
	uid, err := strconv.Atoi(uidRaw)
	if err != nil {
		return Dump{}, false
	}
	ts, err := strconv.ParseInt(tsRaw, 10, 64)
	if err != nil {
		return Dump{}, false
	}

	comm := strings.Join(fields[1:n-4], ".")
	if comm == "" {
		return Dump{}, false
	}

	// Timestamps are µs-epoch on modern systemd; older versions used
	// seconds. Normalize on magnitude.
	usec := ts
	if ts < 1e14 {
		usec = ts * 1000000
	}

	return Dump{
		Time:            time.Unix(usec/1000000, (usec%1000000)*1000).UTC().Format(time.RFC3339),
		PID:             pid,
		UID:             uid,
		Executable:      comm,
		CorefilePresent: true, // the file itself is the core dump
		timeUsec:        usec,
	}, true
}

// signalName decodes common fatal signal numbers; unknown numbers render
// as "SIG<n>".
func signalName(sig int) string {
	switch sig {
	case 4:
		return "SIGILL"
	case 6:
		return "SIGABRT"
	case 7:
		return "SIGBUS"
	case 8:
		return "SIGFPE"
	case 11:
		return "SIGSEGV"
	default:
		return fmt.Sprintf("SIG%d", sig)
	}
}
