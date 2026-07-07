package processstates

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// fakeProc describes one process entry in a synthetic procfs tree.
type fakeProc struct {
	pid      int
	statComm string // comm embedded in stat (may contain spaces/parens)
	comm     string // content of /comm ("" = omit the file)
	state    string // single state character
	ppid     int
	wchan    string // "" = omit the file
	cmdline  string // raw content incl. NUL separators ("" = omit)
	noStat   bool   // simulate a pid that vanished mid-scan
}

// writeProcFixture builds a fake procfs root in a temp dir. It always
// includes self/stat so IsSupported() passes, plus non-numeric noise
// entries that a correct scanner must skip.
func writeProcFixture(tb testing.TB, procs []fakeProc) string {
	tb.Helper()
	root := tb.TempDir()

	if err := os.MkdirAll(filepath.Join(root, "self"), 0o755); err != nil {
		tb.Fatalf("mkdir self: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "self", "stat"),
		[]byte("999999 (cortex-mcp) S 1 999999 999999 0 -1 4194560 0 0 0 0 0 0 0 0 20 0 1 0 100 1000 100 18446744073709551615\n"), 0o644); err != nil {
		tb.Fatalf("write self/stat: %v", err)
	}

	// Noise the scanner must ignore: non-numeric dir and a plain file.
	if err := os.MkdirAll(filepath.Join(root, "acpi"), 0o755); err != nil {
		tb.Fatalf("mkdir acpi: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "uptime"), []byte("100.0 200.0\n"), 0o644); err != nil {
		tb.Fatalf("write uptime: %v", err)
	}

	for _, p := range procs {
		dir := filepath.Join(root, strconv.Itoa(p.pid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			tb.Fatalf("mkdir %s: %v", dir, err)
		}
		if !p.noStat {
			stat := fmt.Sprintf("%d (%s) %s %d 1 1 0 -1 4194560 0 0 0 0 0 0 0 0 20 0 1 0 100 1000 100 18446744073709551615\n",
				p.pid, p.statComm, p.state, p.ppid)
			if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
				tb.Fatalf("write stat: %v", err)
			}
		}
		if p.comm != "" {
			if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(p.comm+"\n"), 0o644); err != nil {
				tb.Fatalf("write comm: %v", err)
			}
		}
		if p.wchan != "" {
			if err := os.WriteFile(filepath.Join(dir, "wchan"), []byte(p.wchan), 0o644); err != nil {
				tb.Fatalf("write wchan: %v", err)
			}
		}
		if p.cmdline != "" {
			if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(p.cmdline), 0o644); err != nil {
				tb.Fatalf("write cmdline: %v", err)
			}
		}
	}
	return root
}

// newFixtureTool returns a Tool pointed at a synthetic procfs tree.
func newFixtureTool(tb testing.TB, procs []fakeProc) *Tool {
	tb.Helper()
	tool := New()
	tool.procfsRoot = writeProcFixture(tb, procs)
	return tool
}

func decodeOutput(t *testing.T, res *registry.ToolResult) Output {
	t.Helper()
	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("cannot unmarshal result data: %v", err)
	}
	return out
}

func TestContractCompliance(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "get_process_states" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "get_process_states")
	}
	if tool.Category() != registry.CategoryCompute {
		t.Errorf("Category() = %q, want compute", tool.Category())
	}
	if tool.Hidden() {
		t.Error("Hidden() = true, want false")
	}
	if tool.Parameters() != nil {
		t.Errorf("Parameters() = %v, want nil (no parameters)", tool.Parameters())
	}
	if tool.Description() == "" {
		t.Error("Description() must not be empty")
	}
	for _, ref := range []string{"/proc/[pid]/stat", "/proc/[pid]/comm", "/proc/[pid]/cmdline", "/proc/[pid]/wchan"} {
		if !strings.Contains(tool.Help(), ref) {
			t.Errorf("Help() must reference data source %q", ref)
		}
	}
}

func TestIsSupported(t *testing.T) {
	t.Parallel()

	t.Run("supported with readable self/stat", func(t *testing.T) {
		t.Parallel()
		tool := newFixtureTool(t, nil)
		if ok, reason := tool.IsSupported(); !ok {
			t.Errorf("IsSupported() = false (%q), want true", reason)
		}
	})

	t.Run("unsupported without self/stat", func(t *testing.T) {
		t.Parallel()
		tool := New()
		tool.procfsRoot = t.TempDir()
		ok, reason := tool.IsSupported()
		if ok {
			t.Error("IsSupported() = true, want false")
		}
		if !strings.Contains(reason, "self/stat") {
			t.Errorf("reason = %q, want mention of self/stat", reason)
		}
	})
}

func TestExecuteHappyPath(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t, []fakeProc{
		{pid: 1, statComm: "systemd", comm: "systemd", state: "S", ppid: 0},
		{pid: 100, statComm: "bash", comm: "bash", state: "S", ppid: 1},
		{pid: 101, statComm: "zombie-child", comm: "zombie-child", state: "Z", ppid: 100},
		{pid: 102, statComm: "zombie-child2", comm: "zombie-child2", state: "Z", ppid: 100},
		{pid: 200, statComm: "kworker/0:1", comm: "kworker/0:1", state: "I", ppid: 2},
		{pid: 300, statComm: "dd", comm: "dd", state: "D", ppid: 100,
			wchan: "wait_on_page_bit", cmdline: "dd\x00if=/dev/zero\x00of=/tmp/x\x00"},
		{pid: 400, statComm: "runner", comm: "runner", state: "R", ppid: 1},
		{pid: 500, statComm: "paused", comm: "paused", state: "T", ppid: 1},
		{pid: 501, statComm: "debugged", comm: "debugged", state: "t", ppid: 1},
		{pid: 600, statComm: "weird", comm: "weird", state: "X", ppid: 1},
	})

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil (errors must be encapsulated)", err)
	}
	if res.Status != registry.StatusWarning {
		t.Errorf("Status = %q, want warning (unreaped zombies present)", res.Status)
	}
	if res.Summary == "" {
		t.Error("Summary must not be empty")
	}

	out := decodeOutput(t, res)

	wantCounts := StateCounts{
		Running: 1, Sleeping: 2, DiskSleep: 1, Zombie: 2,
		Stopped: 1, Traced: 1, Idle: 1, Other: 1, Total: 10,
	}
	if out.Counts != wantCounts {
		t.Errorf("counts = %+v, want %+v", out.Counts, wantCounts)
	}

	if len(out.Zombies) != 2 {
		t.Fatalf("len(zombies) = %d, want 2", len(out.Zombies))
	}
	wantZ := ZombieProc{PID: 101, Comm: "zombie-child", PPID: 100, ParentComm: "bash", ParentState: "sleeping"}
	if out.Zombies[0] != wantZ {
		t.Errorf("zombies[0] = %+v, want %+v", out.Zombies[0], wantZ)
	}

	if len(out.ParentsWithZombies) != 1 {
		t.Fatalf("len(parents_with_zombies) = %d, want 1", len(out.ParentsWithZombies))
	}
	wantP := ZombieParent{PPID: 100, Comm: "bash", ZombieCount: 2, IsInit: false}
	if out.ParentsWithZombies[0] != wantP {
		t.Errorf("parents_with_zombies[0] = %+v, want %+v", out.ParentsWithZombies[0], wantP)
	}

	if len(out.DState) != 1 {
		t.Fatalf("len(d_state) = %d, want 1", len(out.DState))
	}
	wantD := DStateProc{PID: 300, Comm: "dd", Wchan: "wait_on_page_bit", Cmdline: "dd if=/dev/zero of=/tmp/x"}
	if out.DState[0] != wantD {
		t.Errorf("d_state[0] = %+v, want %+v", out.DState[0], wantD)
	}

	if len(out.WarningReasons) != 1 {
		t.Fatalf("warning_reasons = %v, want exactly 1", out.WarningReasons)
	}
	if !strings.Contains(out.WarningReasons[0], "not being reaped by bash (pid 100)") {
		t.Errorf("warning = %q, want mention of unreaping parent bash (pid 100)", out.WarningReasons[0])
	}
	if !strings.HasPrefix(out.WarningReasons[0], "2 zombie") {
		t.Errorf("warning = %q, want zombie count prefix", out.WarningReasons[0])
	}
}

func TestExecuteZombieReapedByInitIsNotWarned(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t, []fakeProc{
		{pid: 1, statComm: "systemd", comm: "systemd", state: "S", ppid: 0},
		{pid: 50, statComm: "orphan", comm: "orphan", state: "Z", ppid: 1},
	})

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Errorf("Status = %q, want ok (init will reap)", res.Status)
	}

	out := decodeOutput(t, res)
	if len(out.ParentsWithZombies) != 1 || !out.ParentsWithZombies[0].IsInit {
		t.Errorf("parents_with_zombies = %+v, want single entry with is_init=true", out.ParentsWithZombies)
	}
	if len(out.WarningReasons) != 0 {
		t.Errorf("warning_reasons = %v, want empty (parent is PID 1)", out.WarningReasons)
	}
}

func TestExecuteZombieWithGoneParent(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t, []fakeProc{
		{pid: 1, statComm: "systemd", comm: "systemd", state: "S", ppid: 0},
		{pid: 70, statComm: "lost", comm: "lost", state: "Z", ppid: 9999},
	})

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Errorf("Status = %q, want ok (parent not alive, nothing to warn about)", res.Status)
	}

	out := decodeOutput(t, res)
	if len(out.Zombies) != 1 {
		t.Fatalf("len(zombies) = %d, want 1", len(out.Zombies))
	}
	if out.Zombies[0].ParentComm != "?" || out.Zombies[0].ParentState != "gone" {
		t.Errorf("zombies[0] = %+v, want parent_comm=? parent_state=gone", out.Zombies[0])
	}
	if len(out.WarningReasons) != 0 {
		t.Errorf("warning_reasons = %v, want empty", out.WarningReasons)
	}
}

func TestExecuteDStateWarningAndCap(t *testing.T) {
	t.Parallel()
	procs := []fakeProc{{pid: 1, statComm: "systemd", comm: "systemd", state: "S", ppid: 0}}
	for i := 0; i < 25; i++ {
		procs = append(procs, fakeProc{
			pid: 1000 + i, statComm: "stuck", comm: "stuck", state: "D", ppid: 1,
			wchan: "io_schedule", cmdline: "stuck\x00--flag\x00",
		})
	}
	tool := newFixtureTool(t, procs)

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if res.Status != registry.StatusWarning {
		t.Errorf("Status = %q, want warning (D-state storm)", res.Status)
	}

	out := decodeOutput(t, res)
	if out.Counts.DiskSleep != 25 {
		t.Errorf("counts.disk_sleep = %d, want 25", out.Counts.DiskSleep)
	}
	if len(out.DState) != 20 {
		t.Errorf("len(d_state) = %d, want capped at 20", len(out.DState))
	}
	found := false
	for _, w := range out.WarningReasons {
		if strings.Contains(w, "uninterruptible") {
			found = true
		}
	}
	if !found {
		t.Errorf("warning_reasons = %v, want a D-state warning mentioning 'uninterruptible'", out.WarningReasons)
	}
}

func TestExecuteFiveDStateProcessesIsNotWarned(t *testing.T) {
	t.Parallel()
	procs := []fakeProc{{pid: 1, statComm: "systemd", comm: "systemd", state: "S", ppid: 0}}
	for i := 0; i < 5; i++ {
		procs = append(procs, fakeProc{
			pid: 2000 + i, statComm: "busyio", comm: "busyio", state: "D", ppid: 1, wchan: "io_schedule",
		})
	}
	tool := newFixtureTool(t, procs)

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Errorf("Status = %q, want ok (exactly 5 D-state is at threshold, not above)", res.Status)
	}
	out := decodeOutput(t, res)
	if len(out.WarningReasons) != 0 {
		t.Errorf("warning_reasons = %v, want empty", out.WarningReasons)
	}
}

func TestExecuteZombieListCap(t *testing.T) {
	t.Parallel()
	procs := []fakeProc{
		{pid: 1, statComm: "systemd", comm: "systemd", state: "S", ppid: 0},
		{pid: 2, statComm: "reaper", comm: "reaper", state: "S", ppid: 1},
	}
	for i := 0; i < 30; i++ {
		procs = append(procs, fakeProc{
			pid: 3000 + i, statComm: "zom", comm: "zom", state: "Z", ppid: 2,
		})
	}
	tool := newFixtureTool(t, procs)

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	out := decodeOutput(t, res)
	if out.Counts.Zombie != 30 {
		t.Errorf("counts.zombie = %d, want 30 (counts are never truncated)", out.Counts.Zombie)
	}
	if len(out.Zombies) != 25 {
		t.Errorf("len(zombies) = %d, want capped at 25", len(out.Zombies))
	}
	if len(out.ParentsWithZombies) != 1 || out.ParentsWithZombies[0].ZombieCount != 30 {
		t.Errorf("parents_with_zombies = %+v, want reaper with zombie_count=30", out.ParentsWithZombies)
	}
	if res.Status != registry.StatusWarning {
		t.Errorf("Status = %q, want warning", res.Status)
	}
}

func TestExecuteSkipsVanishedAndNonNumericEntries(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t, []fakeProc{
		{pid: 1, statComm: "systemd", comm: "systemd", state: "S", ppid: 0},
		{pid: 42, statComm: "worker", comm: "worker", state: "R", ppid: 1},
		{pid: 43, noStat: true}, // vanished between readdir and stat read
	})

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	out := decodeOutput(t, res)
	if out.Counts.Total != 2 {
		t.Errorf("counts.total = %d, want 2 (vanished pid and noise entries skipped)", out.Counts.Total)
	}
}

func TestExecuteCommFallsBackToStatComm(t *testing.T) {
	t.Parallel()
	// No /comm file: the comm parsed from stat (with spaces/parens) is used.
	tool := newFixtureTool(t, []fakeProc{
		{pid: 1, statComm: "systemd", comm: "systemd", state: "S", ppid: 0},
		{pid: 77, statComm: "tricky) name", state: "Z", ppid: 1},
	})

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	out := decodeOutput(t, res)
	if len(out.Zombies) != 1 || out.Zombies[0].Comm != "tricky) name" {
		t.Errorf("zombies = %+v, want comm 'tricky) name' parsed from stat", out.Zombies)
	}
}

func TestExecuteWchanUnreadableFallsBackToQuestionMark(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t, []fakeProc{
		{pid: 1, statComm: "systemd", comm: "systemd", state: "S", ppid: 0},
		{pid: 88, statComm: "blocked", comm: "blocked", state: "D", ppid: 1}, // no wchan file
	})

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	out := decodeOutput(t, res)
	if len(out.DState) != 1 || out.DState[0].Wchan != "?" {
		t.Errorf("d_state = %+v, want wchan '?'", out.DState)
	}
}

func TestExecuteContextCancellation(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t, []fakeProc{
		{pid: 1, statComm: "systemd", comm: "systemd", state: "S", ppid: 0},
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := tool.Execute(ctx, nil)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil (errors must be encapsulated)", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %q, want error for canceled context", res.Status)
	}
	if !strings.Contains(strings.ToLower(res.Summary), "context") {
		t.Errorf("Summary = %q, want mention of context cancellation", res.Summary)
	}
}

func TestExecuteUnreadableProcRootIsEncapsulatedError(t *testing.T) {
	t.Parallel()
	tool := New()
	tool.procfsRoot = filepath.Join(t.TempDir(), "does-not-exist")

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil (errors must be encapsulated)", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %q, want error", res.Status)
	}
}

func TestParseStat(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		in       string
		wantComm string
		wantSt   byte
		wantPPID int
		wantErr  bool
	}{
		{
			name:     "simple",
			in:       "42 (bash) S 1 42 42 0 -1 4194560 0 0 0 0 0 0 0 0 20 0 1 0 100 1000 100",
			wantComm: "bash", wantSt: 'S', wantPPID: 1,
		},
		{
			name:     "comm with spaces",
			in:       "43 (my app) R 42 43 43 0 -1 4194560 0 0 0 0 0 0 0 0 20 0 1 0 100 1000 100",
			wantComm: "my app", wantSt: 'R', wantPPID: 42,
		},
		{
			name:     "comm with closing paren",
			in:       "44 (a) b) Z 1 44 44 0 -1 4194560 0 0 0 0 0 0 0 0 20 0 1 0 100 1000 100",
			wantComm: "a) b", wantSt: 'Z', wantPPID: 1,
		},
		{
			name:     "traced lowercase t",
			in:       "45 (dbg) t 7 45 45 0 -1 4194560 0",
			wantComm: "dbg", wantSt: 't', wantPPID: 7,
		},
		{name: "no parens", in: "garbage without parens", wantErr: true},
		{name: "nothing after comm", in: "46 (x)", wantErr: true},
		{name: "missing ppid", in: "46 (x) S", wantErr: true},
		{name: "non-numeric ppid", in: "47 (x) S abc", wantErr: true},
		{name: "empty", in: "", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseStat([]byte(tc.in))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseStat(%q) error = nil, want error", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseStat(%q) error = %v", tc.in, err)
			}
			if got.comm != tc.wantComm || got.state != tc.wantSt || got.ppid != tc.wantPPID {
				t.Errorf("parseStat(%q) = {comm:%q state:%c ppid:%d}, want {comm:%q state:%c ppid:%d}",
					tc.in, got.comm, got.state, got.ppid, tc.wantComm, tc.wantSt, tc.wantPPID)
			}
		})
	}
}

func TestListPids(t *testing.T) {
	t.Parallel()
	root := writeProcFixture(t, []fakeProc{
		{pid: 300, statComm: "c", comm: "c", state: "S", ppid: 1},
		{pid: 1, statComm: "a", comm: "a", state: "S", ppid: 0},
		{pid: 42, statComm: "b", comm: "b", state: "S", ppid: 1},
	})

	pids, err := listPids(root)
	if err != nil {
		t.Fatalf("listPids() error = %v", err)
	}
	want := []int{1, 42, 300}
	if len(pids) != len(want) {
		t.Fatalf("listPids() = %v, want %v", pids, want)
	}
	for i := range want {
		if pids[i] != want[i] {
			t.Fatalf("listPids() = %v, want sorted %v", pids, want)
		}
	}

	t.Run("missing root returns error", func(t *testing.T) {
		t.Parallel()
		if _, err := listPids(filepath.Join(t.TempDir(), "nope")); err == nil {
			t.Error("listPids() error = nil, want error for missing root")
		}
	})
}

func TestReadComm(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "comm"), []byte("nginx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readComm(dir); got != "nginx" {
		t.Errorf("readComm() = %q, want %q (newline trimmed)", got, "nginx")
	}
	if got := readComm(filepath.Join(dir, "missing")); got != "" {
		t.Errorf("readComm(missing) = %q, want empty", got)
	}
}

func TestReadCmdline(t *testing.T) {
	t.Parallel()

	t.Run("nul separated args joined with spaces", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte("dd\x00if=/dev/zero\x00of=/tmp/x\x00"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := readCmdline(dir); got != "dd if=/dev/zero of=/tmp/x" {
			t.Errorf("readCmdline() = %q, want %q", got, "dd if=/dev/zero of=/tmp/x")
		}
	})

	t.Run("missing file yields empty", func(t *testing.T) {
		t.Parallel()
		if got := readCmdline(t.TempDir()); got != "" {
			t.Errorf("readCmdline() = %q, want empty", got)
		}
	})

	t.Run("truncated to 100 chars", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		long := strings.Repeat("a", 150)
		if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(long+"\x00"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := readCmdline(dir)
		if len(got) != 100 {
			t.Errorf("len(readCmdline()) = %d, want 100", len(got))
		}
		if !strings.HasPrefix(long, got) {
			t.Errorf("readCmdline() = %q, want a prefix of the original", got)
		}
	})
}

func TestReadWchan(t *testing.T) {
	t.Parallel()

	t.Run("present", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "wchan"), []byte("io_schedule"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := readWchan(dir); got != "io_schedule" {
			t.Errorf("readWchan() = %q, want io_schedule", got)
		}
	})

	t.Run("missing falls back to question mark", func(t *testing.T) {
		t.Parallel()
		if got := readWchan(t.TempDir()); got != "?" {
			t.Errorf("readWchan() = %q, want ?", got)
		}
	})

	t.Run("empty file falls back to question mark", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "wchan"), []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := readWchan(dir); got != "?" {
			t.Errorf("readWchan() = %q, want ?", got)
		}
	})
}

func TestStateName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   byte
		want string
	}{
		{'R', "running"},
		{'S', "sleeping"},
		{'D', "disk_sleep"},
		{'Z', "zombie"},
		{'T', "stopped"},
		{'t', "traced"},
		{'I', "idle"},
		{'X', "other"},
		{'?', "other"},
	}
	for _, tc := range cases {
		if got := stateName(tc.in); got != tc.want {
			t.Errorf("stateName(%c) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
