package scheduledjobs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

const timersJSON = `[
  {"next": 1755561600000000, "left": 19380000000, "last": 1755475200000000, "passed": 67020000000, "unit": "logrotate.timer", "activates": "logrotate.service"},
  {"next": null, "left": null, "last": null, "passed": null, "unit": "apt-daily.timer", "activates": ["apt-daily.service"]}
]`

const timersText = `NEXT                        LEFT           LAST                        PASSED       UNIT                         ACTIVATES
Tue 2025-08-19 00:00:00 UTC 5h 23min left  Mon 2025-08-18 00:00:00 UTC 18h ago      logrotate.timer              logrotate.service
n/a                         n/a            n/a                         n/a          motd-news.timer              motd-news.service

2 timers listed.
`

// newTestTool returns a tool with hermetic empty roots and failing exec
// hooks. Individual tests override what they need.
func newTestTool(t *testing.T) *Tool {
	t.Helper()
	tool := New()
	tool.etcRoot = t.TempDir()
	tool.spoolRoots = []string{filepath.Join(t.TempDir(), "spool")}
	tool.execLookPath = func(file string) (string, error) {
		return "", fmt.Errorf("%s: not found", file)
	}
	tool.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, fmt.Errorf("unexpected exec of %s", name)
	}
	return tool
}

// withSystemctl wires the exec hooks so systemctl exists and list-timers
// returns jsonOut for --output=json (or jsonErr) and textOut otherwise.
func withSystemctl(tool *Tool, jsonOut []byte, jsonErr error, textOut []byte) {
	tool.execLookPath = func(file string) (string, error) {
		if file == "systemctl" {
			return "/usr/bin/systemctl", nil
		}
		return "", fmt.Errorf("%s: not found", file)
	}
	tool.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "systemctl" {
			return nil, fmt.Errorf("unexpected command %s", name)
		}
		for _, a := range args {
			if a == "--output=json" {
				return jsonOut, jsonErr
			}
		}
		return textOut, nil
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScheduledJobsTool_ContractCompliance(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "get_scheduled_jobs" {
		t.Errorf("Name() = %q, want get_scheduled_jobs", tool.Name())
	}
	if tool.Category() != registry.CategorySystem {
		t.Errorf("Category() = %q, want system", tool.Category())
	}
	if tool.Description() == "" {
		t.Error("Description() must not be empty")
	}
	help := tool.Help()
	for _, src := range []string{"systemctl list-timers", "/etc/crontab", "cron.d", "/var/spool/cron"} {
		if !strings.Contains(help, src) {
			t.Errorf("Help() must reference data source %q", src)
		}
	}
	if tool.Parameters() != nil {
		t.Error("Parameters() must be nil (no parameters)")
	}
	if tool.Hidden() {
		t.Error("Hidden() must be false")
	}
}

func TestScheduledJobsTool_IsSupported(t *testing.T) {
	t.Parallel()

	t.Run("supported via systemctl", func(t *testing.T) {
		t.Parallel()
		tool := newTestTool(t)
		tool.execLookPath = func(file string) (string, error) { return "/usr/bin/" + file, nil }
		if ok, reason := tool.IsSupported(); !ok {
			t.Errorf("IsSupported() = false (%s), want true with systemctl", reason)
		}
	})

	t.Run("supported via cron files", func(t *testing.T) {
		t.Parallel()
		tool := newTestTool(t)
		mustWrite(t, filepath.Join(tool.etcRoot, "crontab"), "# empty\n")
		if ok, reason := tool.IsSupported(); !ok {
			t.Errorf("IsSupported() = false (%s), want true with /etc/crontab", reason)
		}
	})

	t.Run("unsupported with neither", func(t *testing.T) {
		t.Parallel()
		tool := newTestTool(t)
		if ok, _ := tool.IsSupported(); ok {
			t.Error("IsSupported() = true, want false with no systemctl and no cron paths")
		}
	})
}

func TestParseTimersJSON(t *testing.T) {
	t.Parallel()

	t.Run("valid array", func(t *testing.T) {
		t.Parallel()
		timers, err := parseTimersJSON([]byte(timersJSON))
		if err != nil {
			t.Fatalf("parseTimersJSON error: %v", err)
		}
		if len(timers) != 2 {
			t.Fatalf("got %d timers, want 2", len(timers))
		}
		if timers[0].Unit != "logrotate.timer" || timers[0].Activates != "logrotate.service" {
			t.Errorf("timer[0] = %+v", timers[0])
		}
		if timers[0].NextISO == nil || *timers[0].NextISO != "2025-08-19T00:00:00Z" {
			t.Errorf("next_iso = %v, want 2025-08-19T00:00:00Z", timers[0].NextISO)
		}
		if timers[0].LastISO == nil || *timers[0].LastISO != "2025-08-18T00:00:00Z" {
			t.Errorf("last_iso = %v, want 2025-08-18T00:00:00Z", timers[0].LastISO)
		}
		if timers[1].NextISO != nil || timers[1].LastISO != nil {
			t.Errorf("timer[1] ISO fields = %v/%v, want null/null", timers[1].NextISO, timers[1].LastISO)
		}
		if timers[1].Activates != "apt-daily.service" {
			t.Errorf("array-valued activates = %q, want apt-daily.service", timers[1].Activates)
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		t.Parallel()
		if _, err := parseTimersJSON([]byte("List of timers:\nnot json")); err == nil {
			t.Error("want error for non-JSON input")
		}
	})

	t.Run("empty array", func(t *testing.T) {
		t.Parallel()
		timers, err := parseTimersJSON([]byte("[]"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(timers) != 0 {
			t.Errorf("got %d timers, want 0", len(timers))
		}
	})
}

func TestParseTimersText(t *testing.T) {
	t.Parallel()

	timers := parseTimersText([]byte(timersText))
	if len(timers) != 2 {
		t.Fatalf("got %d timers, want 2: %+v", len(timers), timers)
	}
	if timers[0].Unit != "logrotate.timer" || timers[0].Activates != "logrotate.service" {
		t.Errorf("timer[0] = %+v", timers[0])
	}
	if timers[0].NextISO == nil || *timers[0].NextISO != "2025-08-19T00:00:00Z" {
		t.Errorf("next_iso = %v, want 2025-08-19T00:00:00Z", timers[0].NextISO)
	}
	if timers[0].LastISO == nil || *timers[0].LastISO != "2025-08-18T00:00:00Z" {
		t.Errorf("last_iso = %v, want 2025-08-18T00:00:00Z", timers[0].LastISO)
	}
	if timers[1].Unit != "motd-news.timer" {
		t.Errorf("timer[1].Unit = %q, want motd-news.timer", timers[1].Unit)
	}
	if timers[1].NextISO != nil {
		t.Errorf("timer[1].NextISO = %v, want nil for n/a", timers[1].NextISO)
	}
}

func TestParseCrontabContent(t *testing.T) {
	t.Parallel()

	t.Run("system crontab with user field", func(t *testing.T) {
		t.Parallel()
		content := `# /etc/crontab: system-wide crontab
SHELL=/bin/sh
PATH=/usr/local/sbin:/usr/local/bin:/sbin:/bin

17 *	* * *	root    cd / && run-parts --report /etc/cron.hourly
@reboot root /usr/local/bin/warmup-cache.sh
bogus line
`
		entries := parseCrontabContent(content, true, "", "/etc/crontab")
		if len(entries) != 2 {
			t.Fatalf("got %d entries, want 2: %+v", len(entries), entries)
		}
		if entries[0].Schedule != "17 * * * *" || entries[0].User != "root" {
			t.Errorf("entry[0] = %+v", entries[0])
		}
		if entries[0].Command != "cd / && run-parts --report /etc/cron.hourly" {
			t.Errorf("entry[0].Command = %q", entries[0].Command)
		}
		if entries[0].SourceFile != "/etc/crontab" {
			t.Errorf("entry[0].SourceFile = %q", entries[0].SourceFile)
		}
		if entries[1].Schedule != "@reboot" || entries[1].User != "root" || entries[1].Command != "/usr/local/bin/warmup-cache.sh" {
			t.Errorf("entry[1] = %+v", entries[1])
		}
	})

	t.Run("user crontab without user field", func(t *testing.T) {
		t.Parallel()
		content := `MAILTO=""
*/5 * * * * /home/alice/bin/sync.sh --fast
@daily /home/alice/bin/report.sh
`
		entries := parseCrontabContent(content, false, "alice", "/var/spool/cron/crontabs/alice")
		if len(entries) != 2 {
			t.Fatalf("got %d entries, want 2: %+v", len(entries), entries)
		}
		if entries[0].Schedule != "*/5 * * * *" || entries[0].User != "alice" {
			t.Errorf("entry[0] = %+v", entries[0])
		}
		if entries[0].Command != "/home/alice/bin/sync.sh --fast" {
			t.Errorf("entry[0].Command = %q", entries[0].Command)
		}
		if entries[1].Schedule != "@daily" || entries[1].Command != "/home/alice/bin/report.sh" {
			t.Errorf("entry[1] = %+v", entries[1])
		}
	})

	t.Run("empty and comment-only", func(t *testing.T) {
		t.Parallel()
		if got := parseCrontabContent("# nothing\n\n", true, "", "x"); len(got) != 0 {
			t.Errorf("got %+v, want none", got)
		}
	})
}

func TestTruncateCommand(t *testing.T) {
	t.Parallel()

	short := "/usr/bin/short"
	if got := truncateCommand(short); got != short {
		t.Errorf("short command modified: %q", got)
	}

	long := strings.Repeat("x", 300)
	got := truncateCommand(long)
	if len(got) != 120 {
		t.Errorf("truncated length = %d, want 120", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("truncated command must end with ellipsis, got %q", got[110:])
	}
}

func TestUsecToISO(t *testing.T) {
	t.Parallel()

	if got := usecToISO(1755561600000000); got != "2025-08-19T00:00:00Z" {
		t.Errorf("usecToISO = %q, want 2025-08-19T00:00:00Z", got)
	}
	if got := usecToISO(0); got != "1970-01-01T00:00:00Z" {
		t.Errorf("usecToISO(0) = %q, want epoch", got)
	}
}

func TestScheduledJobsTool_Execute_HappyPath(t *testing.T) {
	t.Parallel()

	tool := newTestTool(t)
	withSystemctl(tool, []byte(timersJSON), nil, nil)

	mustWrite(t, filepath.Join(tool.etcRoot, "crontab"),
		"SHELL=/bin/sh\n17 * * * * root run-parts /etc/cron.hourly\n")
	mustWrite(t, filepath.Join(tool.etcRoot, "cron.d", "backup"),
		"# nightly\n0 2 * * * backup /usr/local/bin/backup.sh\n")
	mustWrite(t, filepath.Join(tool.spoolRoots[0], "alice"),
		"*/10 * * * * /home/alice/poll.sh\n")

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute returned hard error: %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("Status = %q, want ok (summary: %s)", res.Status, res.Summary)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}

	if len(out.Timers) != 2 {
		t.Fatalf("Timers = %+v, want 2", out.Timers)
	}
	if out.Timers[0].NextISO == nil || *out.Timers[0].NextISO != "2025-08-19T00:00:00Z" {
		t.Errorf("timer next_iso = %v", out.Timers[0].NextISO)
	}

	if len(out.CronEntries) != 3 {
		t.Fatalf("CronEntries = %+v, want 3", out.CronEntries)
	}
	byUser := map[string]CronEntry{}
	for _, e := range out.CronEntries {
		byUser[e.User] = e
	}
	if e, ok := byUser["root"]; !ok || e.Schedule != "17 * * * *" {
		t.Errorf("root entry = %+v", e)
	}
	if e, ok := byUser["backup"]; !ok || !strings.HasSuffix(e.SourceFile, "cron.d/backup") {
		t.Errorf("backup entry = %+v", e)
	}
	if e, ok := byUser["alice"]; !ok || e.Command != "/home/alice/poll.sh" {
		t.Errorf("alice entry = %+v", e)
	}

	if out.Summary.Timers != 2 || out.Summary.CronEntries != 3 || out.Summary.CrontabsSkipped != 0 {
		t.Errorf("Summary = %+v, want {2 3 0}", out.Summary)
	}
}

func TestScheduledJobsTool_Execute_TextFallback(t *testing.T) {
	t.Parallel()

	tool := newTestTool(t)
	withSystemctl(tool, nil, fmt.Errorf("unknown option --output"), []byte(timersText))

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute returned hard error: %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("Status = %q, want ok (summary: %s)", res.Status, res.Summary)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if len(out.Timers) != 2 {
		t.Fatalf("Timers = %+v, want 2 from text fallback", out.Timers)
	}
	if out.Timers[0].Unit != "logrotate.timer" {
		t.Errorf("timer[0].Unit = %q", out.Timers[0].Unit)
	}
}

func TestScheduledJobsTool_Execute_NoSystemctlCronOnly(t *testing.T) {
	t.Parallel()

	tool := newTestTool(t)
	mustWrite(t, filepath.Join(tool.etcRoot, "crontab"),
		"0 4 * * * root /usr/sbin/logrotate\n")

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute returned hard error: %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("Status = %q, want ok (summary: %s)", res.Status, res.Summary)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if len(out.Timers) != 0 {
		t.Errorf("Timers = %+v, want none without systemctl", out.Timers)
	}
	if len(out.CronEntries) != 1 {
		t.Errorf("CronEntries = %+v, want 1", out.CronEntries)
	}
}

func TestScheduledJobsTool_Execute_UnreadableSpoolSkipped(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("permission test meaningless as root")
	}

	tool := newTestTool(t)
	spool := tool.spoolRoots[0]
	if err := os.MkdirAll(spool, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(spool, "bob"), "@hourly /home/bob/job.sh\n")
	if err := os.Chmod(spool, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(spool, 0o755) })

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute returned hard error: %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("Status = %q, want ok despite unreadable spool", res.Status)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if out.Summary.CrontabsSkipped < 1 {
		t.Errorf("CrontabsSkipped = %d, want >= 1", out.Summary.CrontabsSkipped)
	}
	if len(out.CronEntries) != 0 {
		t.Errorf("CronEntries = %+v, want none", out.CronEntries)
	}
}

func TestScheduledJobsTool_Execute_CronEntriesCapped(t *testing.T) {
	t.Parallel()

	tool := newTestTool(t)
	var sb strings.Builder
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&sb, "%d * * * * root /usr/bin/job-%d\n", i%60, i)
	}
	mustWrite(t, filepath.Join(tool.etcRoot, "crontab"), sb.String())

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute returned hard error: %v", err)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if len(out.CronEntries) != 100 {
		t.Errorf("CronEntries length = %d, want capped at 100", len(out.CronEntries))
	}
	if out.Summary.CronEntries != 120 {
		t.Errorf("Summary.CronEntries = %d, want 120 (total discovered)", out.Summary.CronEntries)
	}
}

func TestScheduledJobsTool_Execute_ContextCanceled(t *testing.T) {
	t.Parallel()

	tool := newTestTool(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := tool.Execute(ctx, nil)
	if err != nil {
		t.Fatalf("Execute must encapsulate cancellation, got hard error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %q, want error on canceled context", res.Status)
	}
}
