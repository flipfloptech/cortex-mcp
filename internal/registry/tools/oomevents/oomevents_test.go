package oomevents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// ringFixture holds two complete OOM incidents in klogctl raw format:
// a memcg-constrained kill (paired oom-kill + cgroup Killed line) and a
// global kill (paired oom-kill with global_oom + global Killed line).
const ringFixture = `<6>[  100.000000] systemd[1]: Started regular service.
<3>[12345.678901] oom-kill:constraint=CONSTRAINT_MEMCG,nodemask=(null),cpuset=/,mems_allowed=0,oom_memcg=/system.slice/foo.service,task_memcg=/system.slice/foo.service,task=myapp,pid=4321,uid=1000
<3>[12345.678901] Memory cgroup out of memory: Killed process 4321 (myapp) total-vm:1234kB, anon-rss:567kB, file-rss:89kB, shmem-rss:0kB, UID:1000 pgtables:12kB oom_score_adj:0
<6>[13000.000000] eth0: link becomes ready
<3>[22222.000000] oom-kill:constraint=CONSTRAINT_NONE,nodemask=(null),cpuset=/,mems_allowed=0,global_oom,task_memcg=/user.slice,task=chrome,pid=9999,uid=1000
<3>[22222.000000] Out of memory: Killed process 9999 (chrome) total-vm:204800kB, anon-rss:102400kB, file-rss:2048kB, shmem-rss:1024kB, UID:1000 pgtables:512kB oom_score_adj:300
`

// procStatFixture pins boot time to 1700000000 (2023-11-14T22:13:20Z), so
// offset 12345.678901 => 2023-11-15T01:39:05Z and 22222 => 2023-11-15T04:23:42Z.
const procStatFixture = "cpu  1 2 3 4 5\nbtime 1700000000\nprocesses 4242\n"

// writeProcStat creates <root>/stat with the given content ("" = omit).
func writeProcStat(tb testing.TB, content string) string {
	tb.Helper()
	root := tb.TempDir()
	if content != "" {
		if err := os.WriteFile(filepath.Join(root, "stat"), []byte(content), 0o644); err != nil {
			tb.Fatalf("write proc stat: %v", err)
		}
	}
	return root
}

// newFakeTool returns a Tool with an injected ring buffer, a fake procfs
// carrying a fixed btime, and a dmesg fallback that must not be reached.
func newFakeTool(tb testing.TB, ring string) *Tool {
	tb.Helper()
	tool := New()
	tool.procfsRoot = writeProcStat(tb, procStatFixture)
	tool.klogRead = func() ([]byte, error) { return []byte(ring), nil }
	tool.execCommand = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("dmesg must not be called when klogctl succeeds")
	}
	tool.lookPath = func(string) (string, error) { return "", errors.New("not found") }
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

	if tool.Name() != "query_oom_events" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "query_oom_events")
	}
	if tool.Category() != registry.CategoryMemory {
		t.Errorf("Category() = %q, want memory", tool.Category())
	}
	if tool.Hidden() {
		t.Error("Hidden() = true, want false")
	}
	if tool.Description() == "" {
		t.Error("Description() must not be empty")
	}
	params := tool.Parameters()
	if len(params) != 1 {
		t.Fatalf("len(Parameters()) = %d, want 1", len(params))
	}
	if params[0].Name != "last_n" || params[0].Type != "integer" || params[0].Required {
		t.Errorf("Parameters()[0] = %+v, want optional integer last_n", params[0])
	}
	for _, ref := range []string{"klogctl", "dmesg", "btime", "/proc/stat"} {
		if !strings.Contains(tool.Help(), ref) {
			t.Errorf("Help() must reference data source %q", ref)
		}
	}
}

func TestIsSupported(t *testing.T) {
	t.Parallel()

	t.Run("supported when klogctl read succeeds", func(t *testing.T) {
		t.Parallel()
		tool := newFakeTool(t, ringFixture)
		if ok, reason := tool.IsSupported(); !ok {
			t.Errorf("IsSupported() = false (%q), want true", reason)
		}
	})

	t.Run("supported via dmesg binary when klogctl blocked", func(t *testing.T) {
		t.Parallel()
		tool := newFakeTool(t, "")
		tool.klogRead = func() ([]byte, error) { return nil, errors.New("operation not permitted") }
		tool.lookPath = func(file string) (string, error) {
			if file != "dmesg" {
				return "", errors.New("unexpected lookup: " + file)
			}
			return "/usr/bin/dmesg", nil
		}
		if ok, reason := tool.IsSupported(); !ok {
			t.Errorf("IsSupported() = false (%q), want true", reason)
		}
	})

	t.Run("unsupported when both sources blocked", func(t *testing.T) {
		t.Parallel()
		tool := newFakeTool(t, "")
		tool.klogRead = func() ([]byte, error) { return nil, errors.New("operation not permitted") }
		ok, reason := tool.IsSupported()
		if ok {
			t.Error("IsSupported() = true, want false")
		}
		if !strings.Contains(reason, "dmesg") || !strings.Contains(reason, "klogctl") {
			t.Errorf("reason = %q, want mention of both dmesg and klogctl", reason)
		}
	})
}

func TestExecuteHappyPathNative(t *testing.T) {
	t.Parallel()
	tool := newFakeTool(t, ringFixture)

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil (errors must be encapsulated)", err)
	}
	if res.Status != registry.StatusWarning {
		t.Errorf("Status = %q, want warning (OOM kills found)", res.Status)
	}

	out := decodeOutput(t, res)
	if out.TotalFound != 2 {
		t.Fatalf("total_found = %d, want 2", out.TotalFound)
	}
	if len(out.Events) != 2 {
		t.Fatalf("len(events) = %d, want 2", len(out.Events))
	}

	// Newest first: the chrome (global) kill at offset 22222 leads.
	newest := out.Events[0]
	if newest.VictimPID != 9999 || newest.VictimComm != "chrome" {
		t.Errorf("events[0] victim = %d/%q, want 9999/chrome", newest.VictimPID, newest.VictimComm)
	}
	if newest.Scope != "global" {
		t.Errorf("events[0].scope = %q, want global", newest.Scope)
	}
	if newest.Constraint != "CONSTRAINT_NONE" {
		t.Errorf("events[0].constraint = %q, want CONSTRAINT_NONE", newest.Constraint)
	}
	if newest.Memcg != "/user.slice" {
		t.Errorf("events[0].memcg = %q, want /user.slice (task_memcg fallback)", newest.Memcg)
	}
	if newest.Time == nil || *newest.Time != "2023-11-15T04:23:42Z" {
		t.Errorf("events[0].time = %v, want 2023-11-15T04:23:42Z", newest.Time)
	}
	if newest.TotalVMMB != 200.0 || newest.AnonRSSMB != 100.0 || newest.FileRSSMB != 2.0 || newest.ShmemRSSMB != 1.0 {
		t.Errorf("events[0] mem = %v/%v/%v/%v MB, want 200/100/2/1",
			newest.TotalVMMB, newest.AnonRSSMB, newest.FileRSSMB, newest.ShmemRSSMB)
	}

	older := out.Events[1]
	if older.VictimPID != 4321 || older.VictimComm != "myapp" {
		t.Errorf("events[1] victim = %d/%q, want 4321/myapp", older.VictimPID, older.VictimComm)
	}
	if older.Scope != "cgroup" {
		t.Errorf("events[1].scope = %q, want cgroup", older.Scope)
	}
	if older.Constraint != "CONSTRAINT_MEMCG" {
		t.Errorf("events[1].constraint = %q, want CONSTRAINT_MEMCG", older.Constraint)
	}
	if older.Memcg != "/system.slice/foo.service" {
		t.Errorf("events[1].memcg = %q, want /system.slice/foo.service", older.Memcg)
	}
	if older.Time == nil || *older.Time != "2023-11-15T01:39:05Z" {
		t.Errorf("events[1].time = %v, want 2023-11-15T01:39:05Z", older.Time)
	}
	if older.TotalVMMB != 1.2 || older.AnonRSSMB != 0.6 || older.FileRSSMB != 0.1 || older.ShmemRSSMB != 0.0 {
		t.Errorf("events[1] mem = %v/%v/%v/%v MB, want 1.2/0.6/0.1/0",
			older.TotalVMMB, older.AnonRSSMB, older.FileRSSMB, older.ShmemRSSMB)
	}

	if out.CountByComm["myapp"] != 1 || out.CountByComm["chrome"] != 1 {
		t.Errorf("count_by_comm = %v, want myapp=1 chrome=1", out.CountByComm)
	}
	if !strings.Contains(out.Note, "recent history only") {
		t.Errorf("note = %q, want ring buffer coverage caveat", out.Note)
	}
}

func TestExecuteFallbackToDmesg(t *testing.T) {
	t.Parallel()
	tool := newFakeTool(t, "")
	tool.klogRead = func() ([]byte, error) { return nil, errors.New("klogctl: operation not permitted") }
	tool.execCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "dmesg" || len(args) != 1 || args[0] != "-r" {
			return nil, fmt.Errorf("unexpected command: %s %v", name, args)
		}
		return []byte(ringFixture), nil
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	out := decodeOutput(t, res)
	if out.TotalFound != 2 {
		t.Errorf("total_found = %d, want 2 via dmesg fallback", out.TotalFound)
	}
}

func TestExecuteBothSourcesFailExplainsPrivilege(t *testing.T) {
	t.Parallel()
	tool := newFakeTool(t, "")
	tool.klogRead = func() ([]byte, error) { return nil, errors.New("operation not permitted") }
	tool.execCommand = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("exec: permission denied")
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil (errors must be encapsulated)", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %q, want error", res.Status)
	}
	if !strings.Contains(strings.ToLower(res.Summary), "privile") {
		t.Errorf("Summary = %q, want privilege explanation", res.Summary)
	}
}

func TestExecuteZeroEventsIsOKWithEmptyArray(t *testing.T) {
	t.Parallel()
	tool := newFakeTool(t, "<6>[  100.000000] systemd[1]: nothing bad here\n<6>[  101.000000] all quiet\n")

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Errorf("Status = %q, want ok for zero events", res.Status)
	}
	out := decodeOutput(t, res)
	if out.TotalFound != 0 || len(out.Events) != 0 {
		t.Errorf("total_found=%d len(events)=%d, want 0/0", out.TotalFound, len(out.Events))
	}
	if !strings.Contains(string(res.Data), `"events":[]`) {
		t.Errorf("data = %s, want events serialized as [] not null", res.Data)
	}
}

// manyEventsRing builds n standalone Killed-process records, oldest first.
func manyEventsRing(n int) string {
	var sb strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&sb,
			"<3>[%d.000000] Out of memory: Killed process %d (app%d) total-vm:1000kB, anon-rss:500kB, file-rss:100kB, shmem-rss:0kB, UID:0 pgtables:8kB oom_score_adj:0\n",
			1000+i, i, i)
	}
	return sb.String()
}

func TestExecuteLastN(t *testing.T) {
	t.Parallel()

	t.Run("defaults to 10 newest", func(t *testing.T) {
		t.Parallel()
		tool := newFakeTool(t, manyEventsRing(60))
		res, err := tool.Execute(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		out := decodeOutput(t, res)
		if out.TotalFound != 60 {
			t.Errorf("total_found = %d, want 60", out.TotalFound)
		}
		if len(out.Events) != 10 {
			t.Errorf("len(events) = %d, want default 10", len(out.Events))
		}
		if out.Events[0].VictimPID != 60 {
			t.Errorf("events[0].victim_pid = %d, want 60 (newest first)", out.Events[0].VictimPID)
		}
		if len(out.CountByComm) != 60 {
			t.Errorf("len(count_by_comm) = %d, want 60 (aggregated over all found)", len(out.CountByComm))
		}
	})

	t.Run("caps at 50", func(t *testing.T) {
		t.Parallel()
		tool := newFakeTool(t, manyEventsRing(60))
		args, _ := json.Marshal(map[string]any{"last_n": 100})
		res, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatal(err)
		}
		out := decodeOutput(t, res)
		if len(out.Events) != 50 {
			t.Errorf("len(events) = %d, want capped at 50", len(out.Events))
		}
	})

	t.Run("honors explicit small value", func(t *testing.T) {
		t.Parallel()
		tool := newFakeTool(t, manyEventsRing(60))
		args, _ := json.Marshal(map[string]any{"last_n": 3})
		res, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatal(err)
		}
		out := decodeOutput(t, res)
		if len(out.Events) != 3 || out.Events[0].VictimPID != 60 || out.Events[2].VictimPID != 58 {
			t.Errorf("events = %+v, want the 3 newest (pids 60,59,58)", out.Events)
		}
	})
}

func TestExecuteWithoutBtimeOmitsTime(t *testing.T) {
	t.Parallel()
	tool := newFakeTool(t, ringFixture)
	tool.procfsRoot = writeProcStat(t, "") // no stat file at all

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	out := decodeOutput(t, res)
	if out.TotalFound != 2 {
		t.Fatalf("total_found = %d, want 2 (events still parsed)", out.TotalFound)
	}
	for i, ev := range out.Events {
		if ev.Time != nil {
			t.Errorf("events[%d].time = %q, want null without btime", i, *ev.Time)
		}
	}
}

func TestExecuteLineWithoutTimestampPrefixOmitsTime(t *testing.T) {
	t.Parallel()
	// busybox-style dmesg output: no [offset] prefix at all.
	tool := newFakeTool(t, "Out of memory: Killed process 5 (bare) total-vm:100kB, anon-rss:50kB, file-rss:10kB, shmem-rss:0kB, UID:0 pgtables:4kB oom_score_adj:0\n")

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	out := decodeOutput(t, res)
	if out.TotalFound != 1 {
		t.Fatalf("total_found = %d, want 1", out.TotalFound)
	}
	if out.Events[0].Time != nil {
		t.Errorf("time = %q, want null when the [offset] prefix is absent", *out.Events[0].Time)
	}
	if out.Events[0].VictimPID != 5 || out.Events[0].VictimComm != "bare" {
		t.Errorf("event = %+v, want pid 5 comm bare", out.Events[0])
	}
}

func TestExecuteUnpairedOOMKillLineStillCounts(t *testing.T) {
	t.Parallel()
	// Ring rotation cut off the Killed line: the oom-kill record alone
	// must still surface the victim identity and cgroup context.
	tool := newFakeTool(t, "<3>[500.000000] oom-kill:constraint=CONSTRAINT_MEMCG,nodemask=(null),cpuset=/,mems_allowed=0,oom_memcg=/system.slice/bar.service,task_memcg=/system.slice/bar.service,task=barapp,pid=777,uid=99\n")

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	out := decodeOutput(t, res)
	if out.TotalFound != 1 {
		t.Fatalf("total_found = %d, want 1", out.TotalFound)
	}
	ev := out.Events[0]
	if ev.VictimPID != 777 || ev.VictimComm != "barapp" {
		t.Errorf("event victim = %d/%q, want 777/barapp", ev.VictimPID, ev.VictimComm)
	}
	if ev.Scope != "cgroup" || ev.Memcg != "/system.slice/bar.service" {
		t.Errorf("event = %+v, want cgroup scope with memcg", ev)
	}
}

func TestExecuteInvalidArgs(t *testing.T) {
	t.Parallel()
	tool := newFakeTool(t, ringFixture)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"last_n": "not-a-number"}`))
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil (errors must be encapsulated)", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %q, want error for invalid arguments", res.Status)
	}
}

func TestExecuteContextCancellation(t *testing.T) {
	t.Parallel()
	tool := newFakeTool(t, ringFixture)

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

func TestParseOOMEventsPairsAdjacentRecordsByPID(t *testing.T) {
	t.Parallel()

	t.Run("matching pids merge into one event", func(t *testing.T) {
		t.Parallel()
		events := parseOOMEvents([]byte(ringFixture), 1700000000, true)
		if len(events) != 2 {
			t.Fatalf("len(events) = %d, want 2 (pairs merged)", len(events))
		}
		// Oldest first at parser level.
		if events[0].VictimPID != 4321 || events[1].VictimPID != 9999 {
			t.Errorf("parser order = %d,%d, want 4321,9999 (ring order)", events[0].VictimPID, events[1].VictimPID)
		}
	})

	t.Run("mismatched pids produce two events", func(t *testing.T) {
		t.Parallel()
		ring := "<3>[100.000000] oom-kill:constraint=CONSTRAINT_NONE,nodemask=(null),cpuset=/,mems_allowed=0,global_oom,task_memcg=/a,task=alpha,pid=111,uid=0\n" +
			"<3>[101.000000] Out of memory: Killed process 222 (beta) total-vm:100kB, anon-rss:50kB, file-rss:10kB, shmem-rss:0kB, UID:0 pgtables:4kB oom_score_adj:0\n"
		events := parseOOMEvents([]byte(ring), 0, false)
		if len(events) != 2 {
			t.Fatalf("len(events) = %d, want 2", len(events))
		}
		if events[0].VictimPID != 111 || events[0].VictimComm != "alpha" {
			t.Errorf("events[0] = %+v, want unpaired oom-kill victim alpha/111 first", events[0])
		}
		if events[1].VictimPID != 222 || events[1].VictimComm != "beta" {
			t.Errorf("events[1] = %+v, want beta/222", events[1])
		}
	})

	t.Run("empty input yields no events", func(t *testing.T) {
		t.Parallel()
		if events := parseOOMEvents(nil, 0, false); len(events) != 0 {
			t.Errorf("events = %+v, want empty", events)
		}
	})
}

func TestParseLinePrefix(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in         string
		wantOffset float64
		wantHas    bool
		wantMsg    string
	}{
		{"<3>[  123.456789] hello world", 123.456789, true, "hello world"},
		{"[123.456] no priority", 123.456, true, "no priority"},
		{"plain message", 0, false, "plain message"},
		{"<3>prefixed but no timestamp", 0, false, "prefixed but no timestamp"},
		{"[UFW BLOCK] not a timestamp", 0, false, "[UFW BLOCK] not a timestamp"},
		{"", 0, false, ""},
	}
	for _, tc := range cases {
		offset, has, msg := parseLinePrefix(tc.in)
		if offset != tc.wantOffset || has != tc.wantHas || msg != tc.wantMsg {
			t.Errorf("parseLinePrefix(%q) = (%v, %v, %q), want (%v, %v, %q)",
				tc.in, offset, has, msg, tc.wantOffset, tc.wantHas, tc.wantMsg)
		}
	}
}

func TestParseKilledLine(t *testing.T) {
	t.Parallel()

	t.Run("global variant", func(t *testing.T) {
		t.Parallel()
		rec, ok := parseKilledLine("Out of memory: Killed process 4321 (myapp) total-vm:1234kB, anon-rss:567kB, file-rss:89kB, shmem-rss:0kB, UID:1000 pgtables:12kB oom_score_adj:0")
		if !ok {
			t.Fatal("parseKilledLine() ok = false, want true")
		}
		if rec.pid != 4321 || rec.comm != "myapp" || rec.cgroupScope {
			t.Errorf("rec = %+v, want pid 4321 comm myapp global", rec)
		}
		if rec.totalVMKB != 1234 || rec.anonRSSKB != 567 || rec.fileRSSKB != 89 || rec.shmemRSSKB != 0 {
			t.Errorf("rec mem = %d/%d/%d/%d, want 1234/567/89/0",
				rec.totalVMKB, rec.anonRSSKB, rec.fileRSSKB, rec.shmemRSSKB)
		}
	})

	t.Run("cgroup variant", func(t *testing.T) {
		t.Parallel()
		rec, ok := parseKilledLine("Memory cgroup out of memory: Killed process 7 (svc) total-vm:10kB, anon-rss:5kB, file-rss:1kB, shmem-rss:2kB, UID:0 pgtables:1kB oom_score_adj:0")
		if !ok || !rec.cgroupScope || rec.pid != 7 || rec.comm != "svc" || rec.shmemRSSKB != 2 {
			t.Errorf("rec = %+v ok=%v, want cgroup-scope pid 7", rec, ok)
		}
	})

	t.Run("comm with spaces", func(t *testing.T) {
		t.Parallel()
		rec, ok := parseKilledLine("Out of memory: Killed process 12 (my app) total-vm:10kB, anon-rss:5kB, file-rss:1kB, shmem-rss:0kB, UID:0 pgtables:1kB oom_score_adj:0")
		if !ok || rec.comm != "my app" {
			t.Errorf("rec = %+v ok=%v, want comm 'my app'", rec, ok)
		}
	})

	t.Run("non-matching line", func(t *testing.T) {
		t.Parallel()
		if _, ok := parseKilledLine("systemd[1]: Started something."); ok {
			t.Error("parseKilledLine() ok = true, want false")
		}
	})

	t.Run("non-numeric pid", func(t *testing.T) {
		t.Parallel()
		if _, ok := parseKilledLine("Out of memory: Killed process abc (x) total-vm:1kB"); ok {
			t.Error("parseKilledLine() ok = true, want false")
		}
	})
}

func TestParseOOMKillLine(t *testing.T) {
	t.Parallel()

	t.Run("memcg constrained", func(t *testing.T) {
		t.Parallel()
		rec, ok := parseOOMKillLine("oom-kill:constraint=CONSTRAINT_MEMCG,nodemask=(null),cpuset=/,mems_allowed=0,oom_memcg=/system.slice/foo,task_memcg=/system.slice/foo,task=myapp,pid=4321,uid=1000")
		if !ok {
			t.Fatal("parseOOMKillLine() ok = false, want true")
		}
		if rec.constraint != "CONSTRAINT_MEMCG" || rec.oomMemcg != "/system.slice/foo" ||
			rec.taskMemcg != "/system.slice/foo" || rec.task != "myapp" || rec.pid != 4321 {
			t.Errorf("rec = %+v, unexpected fields", rec)
		}
	})

	t.Run("global kill has no oom_memcg", func(t *testing.T) {
		t.Parallel()
		rec, ok := parseOOMKillLine("oom-kill:constraint=CONSTRAINT_NONE,nodemask=(null),cpuset=/,mems_allowed=0,global_oom,task_memcg=/user.slice,task=chrome,pid=9999,uid=1000")
		if !ok || rec.oomMemcg != "" || rec.taskMemcg != "/user.slice" || rec.constraint != "CONSTRAINT_NONE" {
			t.Errorf("rec = %+v ok=%v, want empty oom_memcg with task_memcg fallback", rec, ok)
		}
	})

	t.Run("non-matching line", func(t *testing.T) {
		t.Parallel()
		if _, ok := parseOOMKillLine("Out of memory: Killed process 1 (x)"); ok {
			t.Error("parseOOMKillLine() ok = true, want false")
		}
	})
}

func TestKbToMB(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   int64
		want float64
	}{
		{0, 0},
		{89, 0.1},
		{512, 0.5},
		{1024, 1.0},
		{1234, 1.2},
		{1536, 1.5},
		{204800, 200.0},
	}
	for _, tc := range cases {
		if got := kbToMB(tc.in); got != tc.want {
			t.Errorf("kbToMB(%d) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestReadBtime(t *testing.T) {
	t.Parallel()

	t.Run("valid", func(t *testing.T) {
		t.Parallel()
		root := writeProcStat(t, procStatFixture)
		btime, ok := readBtime(root)
		if !ok || btime != 1700000000 {
			t.Errorf("readBtime() = (%d, %v), want (1700000000, true)", btime, ok)
		}
	})

	t.Run("missing stat file", func(t *testing.T) {
		t.Parallel()
		if _, ok := readBtime(t.TempDir()); ok {
			t.Error("readBtime() ok = true, want false")
		}
	})

	t.Run("no btime line", func(t *testing.T) {
		t.Parallel()
		root := writeProcStat(t, "cpu  1 2 3\nprocesses 5\n")
		if _, ok := readBtime(root); ok {
			t.Error("readBtime() ok = true, want false")
		}
	})

	t.Run("malformed btime value", func(t *testing.T) {
		t.Parallel()
		root := writeProcStat(t, "btime notanumber\n")
		if _, ok := readBtime(root); ok {
			t.Error("readBtime() ok = true, want false")
		}
	})
}

func TestOffsetToRFC3339(t *testing.T) {
	t.Parallel()

	if got := offsetToRFC3339(1700000000, 12345.678901); got != "2023-11-15T01:39:05Z" {
		t.Errorf("offsetToRFC3339(1700000000, 12345.678901) = %q, want 2023-11-15T01:39:05Z", got)
	}
	if got := offsetToRFC3339(1700000000, 0); got != "2023-11-14T22:13:20Z" {
		t.Errorf("offsetToRFC3339(1700000000, 0) = %q, want 2023-11-14T22:13:20Z", got)
	}
}
