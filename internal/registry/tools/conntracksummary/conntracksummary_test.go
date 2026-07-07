package conntracksummary

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// Modern kernel column layout (>= 4.9-ish): clashres instead of searched.
const statModernFixture = `entries  clashres found new invalid ignore delete chainlength insert insert_failed drop early_drop icmp_error  expect_new expect_create expect_delete search_restart
0000000c  00000000 00000001 00000000 00000002 00000004 00000000 00000000 00000000 00000001 00000002 00000001 00000000  00000000 00000000 00000000 00000003
0000000c  00000000 00000000 00000000 00000003 00000000 00000000 00000000 00000000 00000000 00000001 00000000 00000000  00000000 00000000 00000000 00000002
`

// Older kernel column layout: searched/delete_list columns present.
const statLegacyFixture = `entries  searched found new invalid ignore delete delete_list insert insert_failed drop early_drop icmp_error  expect_new expect_create expect_delete search_restart
00000005  0000000a 00000001 00000002 00000001 00000000 00000000 00000000 00000002 00000000 00000000 00000000 00000000  00000000 00000000 00000000 00000000
`

// writeConntrackProc builds a fake procfs tree. Empty strings skip the file.
func writeConntrackProc(t testing.TB, count, max, stat string) string {
	t.Helper()
	root := t.TempDir()
	nfDir := filepath.Join(root, "sys", "net", "netfilter")
	statDir := filepath.Join(root, "net", "stat")
	for _, d := range []string{nfDir, statDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, content string) {
		if content == "" {
			return
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(nfDir, "nf_conntrack_count"), count)
	write(filepath.Join(nfDir, "nf_conntrack_max"), max)
	write(filepath.Join(statDir, "nf_conntrack"), stat)
	return root
}

func TestContractCompliance(t *testing.T) {
	t.Parallel()

	tool := New()
	var _ registry.Tool = tool

	if tool.Name() != "get_conntrack_summary" {
		t.Errorf("expected Name() == 'get_conntrack_summary', got %q", tool.Name())
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
	for _, src := range []string{"nf_conntrack_count", "nf_conntrack_max", "/proc/net/stat/nf_conntrack"} {
		if !strings.Contains(help, src) {
			t.Errorf("Help() must reference data source %q", src)
		}
	}
}

func TestReadUintFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	valid := filepath.Join(dir, "valid")
	if err := os.WriteFile(valid, []byte("  262144\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	garbage := filepath.Join(dir, "garbage")
	if err := os.WriteFile(garbage, []byte("not-a-number"), 0o644); err != nil {
		t.Fatal(err)
	}

	if v, err := readUintFile(valid); err != nil || v != 262144 {
		t.Errorf("readUintFile(valid) = %d, %v; want 262144, nil", v, err)
	}
	if _, err := readUintFile(garbage); err == nil {
		t.Error("expected error for non-numeric content")
	}
	if _, err := readUintFile(filepath.Join(dir, "missing")); err == nil {
		t.Error("expected error for missing file")
	}
}

func TestParseConntrackStat(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name              string
		input             string
		wantCPUs          int
		wantInvalid       uint64
		wantInsertFailed  uint64
		wantDrop          uint64
		wantEarlyDrop     uint64
		wantSearchRestart uint64
		wantErr           bool
	}{
		{
			name:              "modern header, sums across 2 CPUs",
			input:             statModernFixture,
			wantCPUs:          2,
			wantInvalid:       5, // 0x2 + 0x3
			wantInsertFailed:  1,
			wantDrop:          3, // 0x2 + 0x1
			wantEarlyDrop:     1,
			wantSearchRestart: 5, // 0x3 + 0x2
		},
		{
			name:     "legacy header parsed by column names",
			input:    statLegacyFixture,
			wantCPUs: 1, wantInvalid: 1,
		},
		{
			name:    "header only",
			input:   "entries searched found\n",
			wantErr: true,
		},
		{
			name:    "empty",
			input:   "",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			counters, cpus, err := parseConntrackStat([]byte(tc.input))
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cpus != tc.wantCPUs {
				t.Errorf("expected %d CPUs, got %d", tc.wantCPUs, cpus)
			}
			if counters.Invalid != tc.wantInvalid {
				t.Errorf("invalid = %d, want %d", counters.Invalid, tc.wantInvalid)
			}
			if counters.InsertFailed != tc.wantInsertFailed {
				t.Errorf("insert_failed = %d, want %d", counters.InsertFailed, tc.wantInsertFailed)
			}
			if counters.Drop != tc.wantDrop {
				t.Errorf("drop = %d, want %d", counters.Drop, tc.wantDrop)
			}
			if counters.EarlyDrop != tc.wantEarlyDrop {
				t.Errorf("early_drop = %d, want %d", counters.EarlyDrop, tc.wantEarlyDrop)
			}
			if counters.SearchRestart != tc.wantSearchRestart {
				t.Errorf("search_restart = %d, want %d", counters.SearchRestart, tc.wantSearchRestart)
			}
		})
	}
}

func TestComputeUsagePct(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		count uint64
		max   uint64
		want  float64
	}{
		{"zero max guards division", 100, 0, 0},
		{"half", 131072, 262144, 50},
		{"rounds to 2dp", 1, 3, 33.33},
		{"rounds up", 2, 3, 66.67},
		{"full", 262144, 262144, 100},
		{"empty table", 0, 262144, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := computeUsagePct(tc.count, tc.max); got != tc.want {
				t.Errorf("computeUsagePct(%d, %d) = %v, want %v", tc.count, tc.max, got, tc.want)
			}
		})
	}
}

func TestCollectWarnings(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		usagePct    float64
		counters    *Counters
		wantCount   int
		wantContain []string
	}{
		{"healthy", 10, &Counters{}, 0, nil},
		{"nil counters tolerated", 10, nil, 0, nil},
		{"table nearly full", 85.5, &Counters{}, 1, []string{"85.5"}},
		{"drops", 10, &Counters{Drop: 4}, 1, []string{"drop=4"}},
		{"early drops", 10, &Counters{EarlyDrop: 2}, 1, []string{"early_drop=2"}},
		{"insert failures", 10, &Counters{InsertFailed: 7}, 1, []string{"insert_failed=7"}},
		{
			"all at once", 90.01, &Counters{Drop: 1, EarlyDrop: 2, InsertFailed: 3},
			4, []string{"90.01", "drop=1", "early_drop=2", "insert_failed=3"},
		},
		{"exactly 80 pct is not a warning", 80, &Counters{}, 0, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := collectWarnings(tc.usagePct, tc.counters)
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

func TestIsSupported(t *testing.T) {
	t.Parallel()

	t.Run("count file present", func(t *testing.T) {
		t.Parallel()
		tool := New()
		tool.procfsRoot = writeConntrackProc(t, "12\n", "262144\n", statModernFixture)
		if ok, reason := tool.IsSupported(); !ok {
			t.Errorf("expected supported, got reason %q", reason)
		}
	})

	t.Run("count file missing", func(t *testing.T) {
		t.Parallel()
		tool := New()
		tool.procfsRoot = t.TempDir()
		ok, reason := tool.IsSupported()
		if ok {
			t.Fatal("expected unsupported")
		}
		if reason != "conntrack not loaded" {
			t.Errorf("unexpected reason: %q", reason)
		}
	})
}

func TestExecuteHappyPath(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.procfsRoot = writeConntrackProc(t, "12\n", "262144\n", statModernFixture)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if res.Status != registry.StatusWarning {
		// The modern fixture contains drop/early_drop/insert_failed counters > 0.
		t.Fatalf("expected StatusWarning from fixture counters, got %s (summary: %s)", res.Status, res.Summary)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("failed to unmarshal data: %v", err)
	}
	if out.Count != 12 || out.Max != 262144 {
		t.Errorf("expected count=12 max=262144, got %d/%d", out.Count, out.Max)
	}
	if out.UsagePct != 0 {
		t.Errorf("expected usage_pct 0 (12/262144 rounds to 0.00), got %v", out.UsagePct)
	}
	if !out.CountersAvailable || out.Counters == nil {
		t.Fatalf("expected counters available, got %+v", out)
	}
	if out.CPUCount != 2 {
		t.Errorf("expected cpu_count 2, got %d", out.CPUCount)
	}
	if out.Counters.Invalid != 5 || out.Counters.Drop != 3 || out.Counters.EarlyDrop != 1 ||
		out.Counters.InsertFailed != 1 || out.Counters.SearchRestart != 5 {
		t.Errorf("unexpected counters: %+v", out.Counters)
	}
	if len(out.WarningReasons) != 3 {
		t.Errorf("expected 3 warning reasons (drop, early_drop, insert_failed), got %v", out.WarningReasons)
	}
}

func TestExecuteCleanTable(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.procfsRoot = writeConntrackProc(t, "100\n", "1000\n", statLegacyFixture)

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("expected StatusOK, got %s (summary: %s)", res.Status, res.Summary)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatal(err)
	}
	if out.UsagePct != 10 {
		t.Errorf("expected usage_pct 10, got %v", out.UsagePct)
	}
	if len(out.WarningReasons) != 0 {
		t.Errorf("expected no warnings, got %v", out.WarningReasons)
	}
}

func TestExecuteUsageWarning(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.procfsRoot = writeConntrackProc(t, "900\n", "1000\n", statLegacyFixture)

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if res.Status != registry.StatusWarning {
		t.Fatalf("expected StatusWarning at 90%% usage, got %s", res.Status)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatal(err)
	}
	if out.UsagePct != 90 {
		t.Errorf("expected usage_pct 90, got %v", out.UsagePct)
	}
	if len(out.WarningReasons) != 1 || !strings.Contains(out.WarningReasons[0], "90") {
		t.Errorf("expected usage warning, got %v", out.WarningReasons)
	}
}

func TestExecuteMissingStatFileDegrades(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.procfsRoot = writeConntrackProc(t, "100\n", "1000\n", "")

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("expected StatusOK when only counts are available, got %s (summary: %s)", res.Status, res.Summary)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatal(err)
	}
	if out.CountersAvailable {
		t.Error("expected counters_available == false")
	}
	if out.Counters != nil {
		t.Errorf("expected nil counters, got %+v", out.Counters)
	}
	if out.Count != 100 || out.Max != 1000 || out.UsagePct != 10 {
		t.Errorf("unexpected counts: %+v", out)
	}
}

func TestExecuteMissingCountFile(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.procfsRoot = t.TempDir() // nothing exists

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected encapsulated error result, got hard error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Fatalf("expected StatusError, got %s", res.Status)
	}
	if !strings.Contains(res.Summary, "nf_conntrack_count") {
		t.Errorf("expected summary to mention nf_conntrack_count, got %q", res.Summary)
	}
}

func TestExecuteMissingMaxFileDegrades(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.procfsRoot = writeConntrackProc(t, "100\n", "", statLegacyFixture)

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("expected StatusOK, got %s (summary: %s)", res.Status, res.Summary)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Max != 0 || out.UsagePct != 0 {
		t.Errorf("expected max=0 usage_pct=0 when max unreadable, got %+v", out)
	}
}

func TestExecuteContextCancelled(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.procfsRoot = writeConntrackProc(t, "12\n", "262144\n", statModernFixture)

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
