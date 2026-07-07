package filelocks

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

const locksNoWaiters = `1: POSIX  ADVISORY  READ  100 08:01:7864448 128 128
2: FLOCK  ADVISORY  WRITE 200 08:01:7864554 0 EOF
3: POSIX  ADVISORY  WRITE 100 00:16:28457 0 EOF
4: OFDLCK ADVISORY  WRITE -1 08:01:8713209 128 191
5: LEASE  ACTIVE    READ  300 08:01:12345 0 EOF
6: POSIX  ADVISORY  READ  100 08:01:7867240 1 1
`

const locksWithWaiters = locksNoWaiters + `2: -> FLOCK  ADVISORY  WRITE 201 08:01:7864554 0 EOF
`

func mustWrite(tb testing.TB, path, content string) {
	tb.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		tb.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		tb.Fatalf("write %s: %v", path, err)
	}
}

// writeLocksFixture builds a fake procfs with a locks file and comm entries
// for pids 100 (app-one), 200 (locker), and 201 (waiterproc). Pid 300 has
// no comm file (dead process).
func writeLocksFixture(tb testing.TB, locksContent string) *Tool {
	tb.Helper()
	procRoot := filepath.Join(tb.TempDir(), "proc")
	mustWrite(tb, filepath.Join(procRoot, "locks"), locksContent)
	mustWrite(tb, filepath.Join(procRoot, "100", "comm"), "app-one\n")
	mustWrite(tb, filepath.Join(procRoot, "200", "comm"), "locker\n")
	mustWrite(tb, filepath.Join(procRoot, "201", "comm"), "waiterproc\n")
	return &Tool{procfsRoot: procRoot}
}

func executeJSON(tb testing.TB, tool *Tool, args string) (*registry.ToolResult, Data) {
	tb.Helper()
	res, err := tool.Execute(context.Background(), json.RawMessage(args))
	if err != nil {
		tb.Fatalf("Execute returned hard error: %v", err)
	}
	var data Data
	if res.Status != registry.StatusError {
		if err := json.Unmarshal(res.Data, &data); err != nil {
			tb.Fatalf("unmarshal result data: %v", err)
		}
	}
	return res, data
}

func TestContractCompliance(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "get_file_locks" {
		t.Errorf("Name() = %q, want get_file_locks", tool.Name())
	}
	if tool.Category() != registry.CategorySystem {
		t.Errorf("Category() = %q, want system", tool.Category())
	}
	if tool.Description() == "" {
		t.Error("Description() must be non-empty")
	}
	if tool.Hidden() {
		t.Error("Hidden() must be false")
	}
	help := tool.Help()
	for _, src := range []string{"/proc/locks", "comm"} {
		if !strings.Contains(help, src) {
			t.Errorf("Help() missing data source reference %q", src)
		}
	}
	if len(tool.Parameters()) != 0 {
		t.Errorf("Parameters() = %+v, want none", tool.Parameters())
	}
}

func TestIsSupported(t *testing.T) {
	t.Parallel()

	tool := writeLocksFixture(t, locksNoWaiters)
	if ok, reason := tool.IsSupported(); !ok {
		t.Errorf("IsSupported() = false (%s), want true with fake locks file", reason)
	}

	missing := &Tool{procfsRoot: filepath.Join(t.TempDir(), "nope")}
	ok, reason := missing.IsSupported()
	if ok {
		t.Error("IsSupported() = true without locks file, want false")
	}
	if reason == "" {
		t.Error("unsupported verdict must carry a reason")
	}
}

func TestExecuteHappyPath(t *testing.T) {
	t.Parallel()
	tool := writeLocksFixture(t, locksNoWaiters)

	res, data := executeJSON(t, tool, `{}`)
	if res.Status != registry.StatusOK {
		t.Fatalf("Status = %s (summary %q), want ok", res.Status, res.Summary)
	}

	if data.TotalLocks != 6 {
		t.Errorf("total_locks = %d, want 6", data.TotalLocks)
	}
	if data.ByType.POSIX != 3 || data.ByType.FLOCK != 1 || data.ByType.OFDLCK != 1 || data.ByType.LEASE != 1 {
		t.Errorf("by_type = %+v, want POSIX:3 FLOCK:1 OFDLCK:1 LEASE:1", data.ByType)
	}
	if data.ByMode.Read != 3 || data.ByMode.Write != 3 {
		t.Errorf("by_mode = %+v, want READ:3 WRITE:3", data.ByMode)
	}
	if data.BlockedWaiters.Count != 0 || len(data.BlockedWaiters.Waiters) != 0 {
		t.Errorf("blocked_waiters = %+v, want empty", data.BlockedWaiters)
	}
	if len(data.WarningReasons) != 0 {
		t.Errorf("warning_reasons = %v, want empty", data.WarningReasons)
	}

	if len(data.TopHolders) != 4 {
		t.Fatalf("top_holders len = %d, want 4", len(data.TopHolders))
	}
	top := data.TopHolders[0]
	if top.PID != 100 || top.Comm != "app-one" || top.LockCount != 3 {
		t.Errorf("top holder = %+v, want pid 100/app-one/3", top)
	}

	byPID := map[int]Holder{}
	for _, h := range data.TopHolders {
		byPID[h.PID] = h
	}
	if h := byPID[-1]; h.Comm != "OFD (no owner pid)" {
		t.Errorf("OFDLCK holder comm = %q, want 'OFD (no owner pid)'", h.Comm)
	}
	if h := byPID[300]; h.Comm != "unknown" {
		t.Errorf("dead pid comm = %q, want unknown", h.Comm)
	}
}

func TestExecuteBlockedWaiters(t *testing.T) {
	t.Parallel()
	tool := writeLocksFixture(t, locksWithWaiters)

	res, data := executeJSON(t, tool, `{}`)
	if res.Status != registry.StatusWarning {
		t.Fatalf("Status = %s, want warning with blocked waiters", res.Status)
	}

	// Waiter line must not count as a held lock.
	if data.TotalLocks != 6 {
		t.Errorf("total_locks = %d, want 6 (waiters excluded)", data.TotalLocks)
	}
	if data.BlockedWaiters.Count != 1 {
		t.Fatalf("blocked_waiters.count = %d, want 1", data.BlockedWaiters.Count)
	}
	w := data.BlockedWaiters.Waiters[0]
	if w.PID != 201 || w.Comm != "waiterproc" || w.Type != "FLOCK" || w.Mode != "WRITE" {
		t.Errorf("waiter = %+v, want pid 201/waiterproc/FLOCK/WRITE", w)
	}
	if len(data.WarningReasons) == 0 {
		t.Error("warning_reasons must flag blocked waiters")
	}
}

func TestExecuteCaps(t *testing.T) {
	t.Parallel()

	var sb strings.Builder
	id := 1
	// 17 distinct holder pids -> top_holders capped at 15.
	for pid := 1000; pid < 1017; pid++ {
		fmt.Fprintf(&sb, "%d: POSIX ADVISORY WRITE %d 08:01:%d 0 EOF\n", id, pid, pid)
		id++
	}
	// Holder with the most locks must survive the cap.
	for i := 0; i < 3; i++ {
		fmt.Fprintf(&sb, "%d: POSIX ADVISORY READ 999 08:01:42 %d %d\n", id, i, i)
		id++
	}
	// 17 blocked waiters -> list capped at 15 but count stays 17.
	for pid := 2000; pid < 2017; pid++ {
		fmt.Fprintf(&sb, "%d: -> POSIX ADVISORY WRITE %d 08:01:99 0 EOF\n", id, pid)
		id++
	}

	tool := writeLocksFixture(t, sb.String())
	res, data := executeJSON(t, tool, `{}`)
	if res.Status != registry.StatusWarning {
		t.Fatalf("Status = %s, want warning", res.Status)
	}
	if len(data.TopHolders) != 15 {
		t.Errorf("top_holders len = %d, want cap 15", len(data.TopHolders))
	}
	if data.TopHolders[0].PID != 999 || data.TopHolders[0].LockCount != 3 {
		t.Errorf("top holder = %+v, want pid 999 with 3 locks", data.TopHolders[0])
	}
	if data.BlockedWaiters.Count != 17 {
		t.Errorf("blocked_waiters.count = %d, want 17", data.BlockedWaiters.Count)
	}
	if len(data.BlockedWaiters.Waiters) != 15 {
		t.Errorf("blocked_waiters list len = %d, want cap 15", len(data.BlockedWaiters.Waiters))
	}
}

func TestExecuteEmptyLocks(t *testing.T) {
	t.Parallel()
	tool := writeLocksFixture(t, "")

	res, data := executeJSON(t, tool, `{}`)
	if res.Status != registry.StatusOK {
		t.Fatalf("Status = %s, want ok for zero locks", res.Status)
	}
	if data.TotalLocks != 0 || len(data.TopHolders) != 0 {
		t.Errorf("expected empty aggregates, got %+v", data)
	}
}

func TestExecuteMissingLocksFile(t *testing.T) {
	t.Parallel()
	tool := &Tool{procfsRoot: filepath.Join(t.TempDir(), "proc")}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute must not return hard error, got %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %s, want error when locks file missing", res.Status)
	}
}

func TestExecuteContextCancelled(t *testing.T) {
	t.Parallel()
	tool := writeLocksFixture(t, locksNoWaiters)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := tool.Execute(ctx, nil)
	if err != nil {
		t.Fatalf("Execute must encapsulate cancellation, got hard error %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %s, want error on cancelled context", res.Status)
	}
}

func TestParseLockLine(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		line string
		want LockEntry
		ok   bool
	}{
		{
			name: "posix write",
			line: "1: POSIX  ADVISORY  WRITE 1234 08:01:12345 0 EOF",
			want: LockEntry{Type: "POSIX", Mode: "WRITE", PID: 1234},
			ok:   true,
		},
		{
			name: "blocked waiter",
			line: "1: -> POSIX ADVISORY WRITE 5678 08:01:12345 0 EOF",
			want: LockEntry{Type: "POSIX", Mode: "WRITE", PID: 5678, Blocked: true},
			ok:   true,
		},
		{
			name: "ofd lock without owner",
			line: "8: OFDLCK ADVISORY  WRITE -1 08:01:8713209 128 191",
			want: LockEntry{Type: "OFDLCK", Mode: "WRITE", PID: -1},
			ok:   true,
		},
		{
			name: "lease read",
			line: "5: LEASE  ACTIVE    READ  300 08:01:12345 0 EOF",
			want: LockEntry{Type: "LEASE", Mode: "READ", PID: 300},
			ok:   true,
		},
		{name: "empty line", line: "", ok: false},
		{name: "whitespace", line: "   ", ok: false},
		{name: "too few fields", line: "1: POSIX ADVISORY", ok: false},
		{name: "non-numeric pid", line: "1: POSIX ADVISORY WRITE abc 08:01:1 0 EOF", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseLockLine(tc.line)
			if ok != tc.ok {
				t.Fatalf("ok = %t, want %t", ok, tc.ok)
			}
			if !ok {
				return
			}
			if got.Type != tc.want.Type || got.Mode != tc.want.Mode || got.PID != tc.want.PID || got.Blocked != tc.want.Blocked {
				t.Errorf("entry = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseLocks(t *testing.T) {
	t.Parallel()
	entries := parseLocks([]byte(locksWithWaiters))
	if len(entries) != 7 {
		t.Fatalf("entries len = %d, want 7", len(entries))
	}
	blocked := 0
	for _, e := range entries {
		if e.Blocked {
			blocked++
		}
	}
	if blocked != 1 {
		t.Errorf("blocked entries = %d, want 1", blocked)
	}

	if got := parseLocks(nil); len(got) != 0 {
		t.Errorf("parseLocks(nil) = %+v, want empty", got)
	}
}

func TestResolveComm(t *testing.T) {
	t.Parallel()
	tool := writeLocksFixture(t, locksNoWaiters)

	cases := []struct {
		name string
		pid  int
		want string
	}{
		{"live pid", 100, "app-one"},
		{"dead pid", 4242, "unknown"},
		{"ofd owner", -1, "OFD (no owner pid)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tool.resolveComm(tc.pid); got != tc.want {
				t.Errorf("resolveComm(%d) = %q, want %q", tc.pid, got, tc.want)
			}
		})
	}
}
