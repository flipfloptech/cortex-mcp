package lnetstatus

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

const nisFixture = `nid                      status alive refs peer  rtr   max    tx   min
0@lo                         up     0    2    0    0     0     0     0
192.168.10.1@tcp             up    -1    9    8    0   256   256   251
`

const nisDegradedFixture = `nid                      status alive refs peer  rtr   max    tx   min
0@lo                         up     0    2    0    0     0     0     0
192.168.10.5@o2ib          down    -1    1    8    0   256   248    -3
`

const peersFixture = `nid                      refs state  last   max   rtr   min    tx   min queue
192.168.10.2@tcp            1    NA    -1     8     8     8     8     8 0
192.168.10.3@tcp            1    NA    -1     8     8     8     8     8 0
`

const statsFixture = "2 25 0 447 446 1 0 2202799996 1291335213 0 0\n"

const statsDegradedFixture = "2 25 4 447 447 0 9 100 100 0 0\n"

const lnetctlNetFixture = `net:
    - net type: lo
      local NI(s):
        - nid: 0@lo
          status: up
    - net type: tcp
      local NI(s):
        - nid: 192.168.10.1@tcp
          status: up
`

const lnetctlStatsFixture = `statistics:
    msgs_alloc: 1
    msgs_max: 25
    rst_alloc: 25
    errors: 0
    send_count: 100
    resend_count: 0
    recv_count: 101
    route_count: 0
    drop_count: 5
    send_length: 3000
    recv_length: 4000
    route_length: 0
    drop_length: 0
`

// writeLnetDebugfs builds a fake debugfs tree in a temp dir and returns its root.
func writeLnetDebugfs(t testing.TB, nis, peers, stats string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "lnet")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"nis": nis, "peers": peers, "stats": stats}
	for name, content := range files {
		if content == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func lookPathMissing(string) (string, error) {
	return "", errors.New("executable file not found in $PATH")
}

func TestContractCompliance(t *testing.T) {
	t.Parallel()

	tool := New()
	var _ registry.Tool = tool

	if tool.Name() != "get_lnet_status" {
		t.Errorf("expected Name() == 'get_lnet_status', got %q", tool.Name())
	}
	if tool.Category() != registry.CategoryNetwork {
		t.Errorf("expected Category() == network, got %q", tool.Category())
	}
	if tool.Hidden() {
		t.Error("expected Hidden() == false")
	}
	if tool.Parameters() != nil {
		t.Error("expected Parameters() == nil (tool takes no parameters)")
	}
	if tool.Description() == "" {
		t.Error("expected non-empty Description()")
	}
	help := tool.Help()
	for _, src := range []string{"/sys/kernel/debug/lnet/nis", "/sys/kernel/debug/lnet/peers", "/sys/kernel/debug/lnet/stats", "lnetctl"} {
		if !strings.Contains(help, src) {
			t.Errorf("Help() must reference data source %q", src)
		}
	}
}

func TestParseNIS(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		input    string
		wantLen  int
		wantNID  string
		wantStat string
		wantRefs int64
		wantTx   int64
		wantMin  int64
	}{
		{
			name:     "healthy fixture",
			input:    nisFixture,
			wantLen:  2,
			wantNID:  "192.168.10.1@tcp",
			wantStat: "up",
			wantRefs: 9,
			wantTx:   256,
			wantMin:  251,
		},
		{
			name:     "down NI with negative min credits",
			input:    nisDegradedFixture,
			wantLen:  2,
			wantNID:  "192.168.10.5@o2ib",
			wantStat: "down",
			wantRefs: 1,
			wantTx:   248,
			wantMin:  -3,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			nis := parseNIS([]byte(tc.input))
			if len(nis) != tc.wantLen {
				t.Fatalf("expected %d NIs, got %d: %+v", tc.wantLen, len(nis), nis)
			}
			last := nis[len(nis)-1]
			if last.NID != tc.wantNID {
				t.Errorf("expected nid %q, got %q", tc.wantNID, last.NID)
			}
			if last.Status != tc.wantStat {
				t.Errorf("expected status %q, got %q", tc.wantStat, last.Status)
			}
			if last.Refs != tc.wantRefs {
				t.Errorf("expected refs %d, got %d", tc.wantRefs, last.Refs)
			}
			if last.TxCredits == nil || *last.TxCredits != tc.wantTx {
				t.Errorf("expected tx credits %d, got %v", tc.wantTx, last.TxCredits)
			}
			if last.MinCredits == nil || *last.MinCredits != tc.wantMin {
				t.Errorf("expected min credits %d, got %v", tc.wantMin, last.MinCredits)
			}
		})
	}

	t.Run("garbage and short lines are skipped", func(t *testing.T) {
		t.Parallel()
		nis := parseNIS([]byte("nid status\nnot-a-row\n0@lo up\n\n"))
		if len(nis) != 0 {
			t.Errorf("expected 0 NIs from garbage input, got %d", len(nis))
		}
	})
}

func TestParsePeerCount(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  int
	}{
		{"two peers", peersFixture, 2},
		{"header only", "nid                      refs state  last   max   rtr   min    tx   min queue\n", 0},
		{"empty", "", 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := parsePeerCount([]byte(tc.input)); got != tc.want {
				t.Errorf("parsePeerCount(%q) = %d, want %d", tc.input, got, tc.want)
			}
		})
	}
}

func TestParseStats(t *testing.T) {
	t.Parallel()

	t.Run("full 11-field line", func(t *testing.T) {
		t.Parallel()
		st, err := parseStats([]byte(statsFixture))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if st.MsgsAlloc != 2 || st.MsgsMax != 25 || st.Errors != 0 {
			t.Errorf("unexpected msgs/errors: %+v", st)
		}
		if st.SendCount != 447 || st.RecvCount != 446 || st.RouteCount != 1 || st.DropCount != 0 {
			t.Errorf("unexpected counters: %+v", st)
		}
		if st.SendLength != 2202799996 || st.RecvLength != 1291335213 {
			t.Errorf("unexpected lengths: %+v", st)
		}
	})

	t.Run("errors", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name  string
			input string
		}{
			{"too few fields", "1 2 3"},
			{"non-numeric", "a b c d e f g"},
			{"empty", ""},
		}
		for _, tc := range cases {
			if _, err := parseStats([]byte(tc.input)); err == nil {
				t.Errorf("%s: expected error, got nil", tc.name)
			}
		}
	})
}

func TestParseLnetctlNet(t *testing.T) {
	t.Parallel()

	nis := parseLnetctlNet([]byte(lnetctlNetFixture))
	if len(nis) != 2 {
		t.Fatalf("expected 2 NIs, got %d: %+v", len(nis), nis)
	}
	if nis[0].NID != "0@lo" || nis[0].Status != "up" {
		t.Errorf("unexpected NI 0: %+v", nis[0])
	}
	if nis[1].NID != "192.168.10.1@tcp" || nis[1].Status != "up" {
		t.Errorf("unexpected NI 1: %+v", nis[1])
	}
	if nis[1].TxCredits != nil {
		t.Errorf("expected credits to be unavailable from lnetctl, got %v", *nis[1].TxCredits)
	}

	if got := parseLnetctlNet([]byte("no yaml here")); len(got) != 0 {
		t.Errorf("expected 0 NIs from garbage, got %d", len(got))
	}
}

func TestParseLnetctlStats(t *testing.T) {
	t.Parallel()

	st, err := parseLnetctlStats([]byte(lnetctlStatsFixture))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if st.SendCount != 100 || st.RecvCount != 101 || st.DropCount != 5 || st.Errors != 0 {
		t.Errorf("unexpected stats: %+v", st)
	}
	if st.MsgsAlloc != 1 || st.MsgsMax != 25 || st.SendLength != 3000 {
		t.Errorf("unexpected stats: %+v", st)
	}

	if _, err := parseLnetctlStats([]byte("garbage with no counters")); err == nil {
		t.Error("expected error for output without counters")
	}
}

func TestCollectWarnings(t *testing.T) {
	t.Parallel()

	neg := int64(-3)
	pos := int64(251)

	cases := []struct {
		name        string
		nis         []NI
		stats       *Stats
		wantCount   int
		wantContain []string
	}{
		{
			name:      "all healthy",
			nis:       []NI{{NID: "0@lo", Status: "up", MinCredits: &pos}},
			stats:     &Stats{SendCount: 10},
			wantCount: 0,
		},
		{
			name:        "NI down",
			nis:         []NI{{NID: "1@tcp", Status: "down"}},
			stats:       &Stats{},
			wantCount:   1,
			wantContain: []string{"not up", "1@tcp"},
		},
		{
			name:        "credit starvation",
			nis:         []NI{{NID: "1@tcp", Status: "up", MinCredits: &neg}},
			stats:       &Stats{},
			wantCount:   1,
			wantContain: []string{"credit starvation", "min=-3"},
		},
		{
			name:        "drops",
			nis:         nil,
			stats:       &Stats{DropCount: 9},
			wantCount:   1,
			wantContain: []string{"drop_count=9"},
		},
		{
			name:        "everything wrong",
			nis:         []NI{{NID: "1@tcp", Status: "down", MinCredits: &neg}},
			stats:       &Stats{DropCount: 9},
			wantCount:   3,
			wantContain: []string{"not up", "credit starvation", "drop_count=9"},
		},
		{
			name:      "nil stats tolerated",
			nis:       []NI{{NID: "0@lo", Status: "up"}},
			stats:     nil,
			wantCount: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := collectWarnings(tc.nis, tc.stats)
			if len(got) != tc.wantCount {
				t.Fatalf("expected %d warnings, got %d: %v", tc.wantCount, len(got), got)
			}
			joined := strings.Join(got, " | ")
			for _, want := range tc.wantContain {
				if !strings.Contains(joined, want) {
					t.Errorf("expected warnings to contain %q, got %q", want, joined)
				}
			}
		})
	}
}

func TestIsPermissionError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		out  string
		err  error
		want bool
	}{
		{"permission denied in output", "cannot open: Permission denied", errors.New("exit status 1"), true},
		{"operation not permitted in error", "", errors.New("operation not permitted"), true},
		{"sudo password prompt", "sudo: a password is required", errors.New("exit status 1"), true},
		{"generic failure", "some other error", errors.New("exit status 2"), false},
		{"no error", "", nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isPermissionError([]byte(tc.out), tc.err); got != tc.want {
				t.Errorf("isPermissionError(%q, %v) = %v, want %v", tc.out, tc.err, got, tc.want)
			}
		})
	}
}

func TestIsSupported(t *testing.T) {
	t.Parallel()

	t.Run("lnet module loaded", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "module", "lnet"), 0o755); err != nil {
			t.Fatal(err)
		}
		tool := New()
		tool.sysfsRoot = root
		tool.execLookPath = lookPathMissing
		ok, reason := tool.IsSupported()
		if !ok {
			t.Errorf("expected supported, got reason %q", reason)
		}
	})

	t.Run("no module but lnetctl present", func(t *testing.T) {
		t.Parallel()
		tool := New()
		tool.sysfsRoot = t.TempDir()
		tool.execLookPath = func(file string) (string, error) {
			if file == "lnetctl" {
				return "/usr/sbin/lnetctl", nil
			}
			return "", errors.New("not found")
		}
		if ok, reason := tool.IsSupported(); !ok {
			t.Errorf("expected supported via lnetctl, got reason %q", reason)
		}
	})

	t.Run("neither module nor lnetctl", func(t *testing.T) {
		t.Parallel()
		tool := New()
		tool.sysfsRoot = t.TempDir()
		tool.execLookPath = lookPathMissing
		ok, reason := tool.IsSupported()
		if ok {
			t.Fatal("expected unsupported")
		}
		if reason != "LNet not loaded and lnetctl not found" {
			t.Errorf("unexpected reason: %q", reason)
		}
	})
}

func TestExecuteDebugfsHappyPath(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.debugfsRoot = writeLnetDebugfs(t, nisFixture, peersFixture, statsFixture)
	tool.execLookPath = lookPathMissing
	tool.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		t.Fatalf("execCommand must not be called on the debugfs path (called with %s %v)", name, args)
		return nil, nil
	}

	res, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("expected StatusOK, got %s (summary: %s)", res.Status, res.Summary)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("failed to unmarshal data: %v", err)
	}
	if out.Source != "debugfs" {
		t.Errorf("expected source debugfs, got %q", out.Source)
	}
	if out.NICount != 2 || out.NIsDown != 0 {
		t.Errorf("expected 2 NIs / 0 down, got %d / %d", out.NICount, out.NIsDown)
	}
	if !out.PeersAvailable || out.PeerCount != 2 {
		t.Errorf("expected 2 peers available, got available=%v count=%d", out.PeersAvailable, out.PeerCount)
	}
	if !out.StatsAvailable || out.Stats == nil || out.Stats.SendCount != 447 || out.Stats.DropCount != 0 {
		t.Errorf("unexpected stats: available=%v %+v", out.StatsAvailable, out.Stats)
	}
	if len(out.WarningReasons) != 0 {
		t.Errorf("expected no warnings, got %v", out.WarningReasons)
	}
	if len(out.NIs) != 2 || out.NIs[1].NID != "192.168.10.1@tcp" {
		t.Errorf("unexpected NIs: %+v", out.NIs)
	}
}

func TestExecuteDebugfsWarningPath(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.debugfsRoot = writeLnetDebugfs(t, nisDegradedFixture, peersFixture, statsDegradedFixture)
	tool.execLookPath = lookPathMissing

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if res.Status != registry.StatusWarning {
		t.Fatalf("expected StatusWarning, got %s (summary: %s)", res.Status, res.Summary)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("failed to unmarshal data: %v", err)
	}
	if out.NIsDown != 1 {
		t.Errorf("expected 1 NI down, got %d", out.NIsDown)
	}
	if len(out.WarningReasons) != 3 {
		t.Errorf("expected 3 warning reasons (down, starvation, drops), got %v", out.WarningReasons)
	}
}

func TestExecuteMissingStatsFileDegrades(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.debugfsRoot = writeLnetDebugfs(t, nisFixture, "", "")
	tool.execLookPath = lookPathMissing

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("expected StatusOK, got %s", res.Status)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatal(err)
	}
	if out.StatsAvailable || out.Stats != nil {
		t.Errorf("expected stats unavailable, got %+v", out.Stats)
	}
	if out.PeersAvailable {
		t.Error("expected peers unavailable")
	}
}

func TestExecuteLnetctlFallback(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.debugfsRoot = filepath.Join(t.TempDir(), "does-not-exist")
	tool.execLookPath = func(file string) (string, error) {
		if file == "lnetctl" {
			return "/usr/sbin/lnetctl", nil
		}
		return "", errors.New("not found")
	}
	tool.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "lnetctl" {
			t.Errorf("expected lnetctl invocation, got %s %v", name, args)
		}
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "net show"):
			return []byte(lnetctlNetFixture), nil
		case strings.Contains(joined, "stats show"):
			return []byte(lnetctlStatsFixture), nil
		}
		return nil, errors.New("unexpected command")
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	// drop_count=5 in the lnetctl stats fixture → warning expected.
	if res.Status != registry.StatusWarning {
		t.Fatalf("expected StatusWarning (drops>0), got %s (summary: %s)", res.Status, res.Summary)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Source != "lnetctl" {
		t.Errorf("expected source lnetctl, got %q", out.Source)
	}
	if out.NICount != 2 {
		t.Errorf("expected 2 NIs, got %d", out.NICount)
	}
	if out.PeersAvailable {
		t.Error("peer count is debugfs-only; expected peers_available=false on lnetctl path")
	}
	if !out.StatsAvailable || out.Stats == nil || out.Stats.DropCount != 5 {
		t.Errorf("unexpected stats: %+v", out.Stats)
	}
}

func TestExecuteBothSourcesUnavailable(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.debugfsRoot = filepath.Join(t.TempDir(), "does-not-exist")
	tool.execLookPath = lookPathMissing

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected encapsulated error result, got hard error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Fatalf("expected StatusError, got %s", res.Status)
	}
	for _, want := range []string{"lnetctl", "root"} {
		if !strings.Contains(res.Summary, want) {
			t.Errorf("expected error summary to mention %q, got %q", want, res.Summary)
		}
	}
}

func TestExecutePermissionLockout(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.debugfsRoot = filepath.Join(t.TempDir(), "does-not-exist")
	tool.execLookPath = func(file string) (string, error) {
		if file == "lnetctl" {
			return "/usr/sbin/lnetctl", nil
		}
		return "", errors.New("not found")
	}
	tool.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("lnetctl: cannot open /dev/lnet: Operation not permitted"), errors.New("exit status 1")
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected encapsulated error result, got hard error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Fatalf("expected StatusError, got %s", res.Status)
	}
	if !strings.Contains(strings.ToLower(res.Summary), "root") {
		t.Errorf("expected permission lockout summary to mention root, got %q", res.Summary)
	}
}

func TestExecuteLnetctlCommandFailure(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.debugfsRoot = filepath.Join(t.TempDir(), "does-not-exist")
	tool.execLookPath = func(file string) (string, error) {
		if file == "lnetctl" {
			return "/usr/sbin/lnetctl", nil
		}
		return "", errors.New("not found")
	}
	tool.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("something exploded"), errors.New("exit status 2")
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected encapsulated error result, got hard error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Fatalf("expected StatusError, got %s", res.Status)
	}
}

func TestExecuteContextCancelled(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.debugfsRoot = writeLnetDebugfs(t, nisFixture, peersFixture, statsFixture)
	tool.execLookPath = lookPathMissing

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := tool.Execute(ctx, nil)
	if err != nil {
		t.Fatalf("expected encapsulated error result, got hard error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Fatalf("expected StatusError for cancelled context, got %s", res.Status)
	}
	if !strings.Contains(strings.ToLower(res.Summary), "cancel") {
		t.Errorf("expected summary to mention cancellation, got %q", res.Summary)
	}
}

func TestRunLnetctlInvocation(t *testing.T) {
	t.Parallel()

	tool := New()
	var gotName string
	var gotArgs []string
	tool.execLookPath = lookPathMissing // no sudo available
	tool.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		gotName = name
		gotArgs = args
		return []byte("ok"), nil
	}

	if _, err := tool.runLnetctl(context.Background(), "net", "show"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Regardless of sudo availability the lnetctl subcommand must be preserved.
	full := gotName + " " + strings.Join(gotArgs, " ")
	if !strings.Contains(full, "lnetctl") || !strings.Contains(full, "net show") {
		t.Errorf("unexpected invocation: %s", full)
	}
}
