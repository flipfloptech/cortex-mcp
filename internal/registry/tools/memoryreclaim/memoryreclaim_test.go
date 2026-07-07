package memoryreclaim

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// quietVmstat is a modern-kernel vmstat snapshot with no reclaim activity.
const quietVmstat = `nr_free_pages 9447037
pgscan_kswapd 0
pgscan_direct 0
pgscan_direct_throttle 0
pgsteal_kswapd 0
pgsteal_direct 0
allocstall_dma 0
allocstall_dma32 0
allocstall_normal 0
allocstall_movable 0
compact_stall 0
compact_fail 0
compact_success 0
pswpin 0
pswpout 0
pgmajfault 0
thp_fault_fallback 0
oom_kill 0
`

// busyBefore/busyAfter are two samples of a modern-kernel vmstat under
// memory pressure. Deltas over a fake 500ms window give exact rates.
const busyBefore = `pswpin 100
pswpout 200
pgscan_kswapd 1000
pgscan_direct 500
pgscan_direct_throttle 9
pgsteal_kswapd 900
pgsteal_direct 400
allocstall_normal 10
allocstall_movable 5
compact_stall 3
compact_fail 1
compact_success 2
pgmajfault 50
thp_fault_fallback 7
oom_kill 0
`

const busyAfter = `pswpin 110
pswpout 250
pgscan_kswapd 1500
pgscan_direct 600
pgscan_direct_throttle 9
pgsteal_kswapd 1350
pgsteal_direct 480
allocstall_normal 12
allocstall_movable 6
compact_stall 4
compact_fail 1
compact_success 2
pgmajfault 60
thp_fault_fallback 7
oom_kill 1
`

// fakeSampler wires a Tool to a two-phase vmstat fake and a fake clock so
// tests never sleep for real and rates are exactly reproducible.
func fakeSampler(t *Tool, first, second string, elapsed time.Duration) {
	reads := 0
	t.readFile = func(string) ([]byte, error) {
		reads++
		if reads == 1 {
			return []byte(first), nil
		}
		return []byte(second), nil
	}
	base := time.Unix(1_700_000_000, 0)
	ticks := 0
	t.now = func() time.Time {
		ticks++
		if ticks == 1 {
			return base
		}
		return base.Add(elapsed)
	}
}

func TestMemoryReclaimTool_ContractCompliance(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "get_memory_reclaim_stats" {
		t.Errorf("Name() = %q, want get_memory_reclaim_stats", tool.Name())
	}
	if tool.Category() != registry.CategoryMemory {
		t.Errorf("Category() = %q, want memory", tool.Category())
	}
	if tool.Description() == "" {
		t.Error("Description() must not be empty")
	}
	if !strings.Contains(tool.Help(), "/proc/vmstat") {
		t.Errorf("Help() must reference /proc/vmstat data source, got: %s", tool.Help())
	}
	for _, want := range []string{"sample_duration_ms", "500", "5000"} {
		if !strings.Contains(tool.Help(), want) {
			t.Errorf("Help() must document %q, got: %s", want, tool.Help())
		}
	}
	if tool.Hidden() {
		t.Error("Hidden() must be false")
	}

	params := tool.Parameters()
	if len(params) != 1 {
		t.Fatalf("Parameters() = %d entries, want 1", len(params))
	}
	if params[0].Name != "sample_duration_ms" {
		t.Errorf("param name = %q, want sample_duration_ms", params[0].Name)
	}
	if params[0].Type != "integer" {
		t.Errorf("param type = %q, want integer", params[0].Type)
	}
	if params[0].Required {
		t.Error("sample_duration_ms must be optional")
	}
	if params[0].Default != "500" {
		t.Errorf("param default = %q, want 500", params[0].Default)
	}
}

func TestMemoryReclaimTool_IsSupported(t *testing.T) {
	t.Parallel()

	t.Run("unsupported when vmstat missing", func(t *testing.T) {
		t.Parallel()
		tool := New()
		tool.procfsRoot = t.TempDir()
		ok, reason := tool.IsSupported()
		if ok {
			t.Error("IsSupported() = true, want false when vmstat is missing")
		}
		if !strings.Contains(reason, "vmstat") {
			t.Errorf("reason = %q, want mention of vmstat", reason)
		}
	})

	t.Run("supported when vmstat exists", func(t *testing.T) {
		t.Parallel()
		tool := New()
		tool.procfsRoot = t.TempDir()
		if err := os.WriteFile(filepath.Join(tool.procfsRoot, "vmstat"), []byte(quietVmstat), 0o644); err != nil {
			t.Fatalf("write vmstat: %v", err)
		}
		ok, reason := tool.IsSupported()
		if !ok {
			t.Errorf("IsSupported() = false (%s), want true", reason)
		}
	})
}

func TestParseVmstat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data string
		want map[string]uint64
	}{
		{
			name: "valid lines",
			data: "nr_free_pages 9447037\npgscan_kswapd 12\n",
			want: map[string]uint64{"nr_free_pages": 9447037, "pgscan_kswapd": 12},
		},
		{
			name: "max uint64 value",
			data: "pswpin 18446744073709551615\n",
			want: map[string]uint64{"pswpin": 18446744073709551615},
		},
		{
			name: "malformed lines are skipped",
			data: "onlykey\npswpin notanumber\npswpin 3 extra\n\npgmajfault 5\n",
			want: map[string]uint64{"pgmajfault": 5},
		},
		{
			name: "negative values are skipped",
			data: "pswpin -3\npswpout 4\n",
			want: map[string]uint64{"pswpout": 4},
		},
		{
			name: "empty input",
			data: "",
			want: map[string]uint64{},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parseVmstat([]byte(tt.data))
			if len(got) != len(tt.want) {
				t.Fatalf("parseVmstat() = %v, want %v", got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("parseVmstat()[%q] = %d, want %d", k, got[k], v)
				}
			}
		})
	}
}

func TestResolveCounter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		m      map[string]uint64
		key    string
		want   uint64
		wantOK bool
	}{
		{
			name:   "exact key wins over suffixed variants",
			m:      map[string]uint64{"pgscan_direct": 5, "pgscan_direct_throttle": 3},
			key:    "pgscan_direct",
			want:   5,
			wantOK: true,
		},
		{
			name: "old kernel zone suffixes are summed",
			m: map[string]uint64{
				"pgscan_kswapd_dma":     1,
				"pgscan_kswapd_dma32":   2,
				"pgscan_kswapd_normal":  3,
				"pgscan_kswapd_movable": 4,
			},
			key:    "pgscan_kswapd",
			want:   10,
			wantOK: true,
		},
		{
			name: "throttle event counter excluded from fallback sum",
			m: map[string]uint64{
				"pgscan_direct_dma":      1,
				"pgscan_direct_normal":   2,
				"pgscan_direct_throttle": 100,
			},
			key:    "pgscan_direct",
			want:   3,
			wantOK: true,
		},
		{
			name:   "plain allocstall on old kernels",
			m:      map[string]uint64{"allocstall": 7},
			key:    "allocstall",
			want:   7,
			wantOK: true,
		},
		{
			name: "allocstall zone variants summed on modern kernels",
			m: map[string]uint64{
				"allocstall_dma":     1,
				"allocstall_dma32":   2,
				"allocstall_normal":  3,
				"allocstall_movable": 4,
			},
			key:    "allocstall",
			want:   10,
			wantOK: true,
		},
		{
			name:   "missing counter",
			m:      map[string]uint64{"nr_free_pages": 1},
			key:    "oom_kill",
			want:   0,
			wantOK: false,
		},
		{
			name:   "unrelated longer prefix does not match",
			m:      map[string]uint64{"pgscan_khugepaged": 42},
			key:    "pgscan_kswapd",
			want:   0,
			wantOK: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := resolveCounter(tt.m, tt.key)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("resolveCounter(%q) = (%d, %v), want (%d, %v)", tt.key, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestResolveWindow(t *testing.T) {
	t.Parallel()

	intPtr := func(v int) *int { return &v }

	tests := []struct {
		name string
		in   *int
		want time.Duration
	}{
		{name: "absent uses default", in: nil, want: 500 * time.Millisecond},
		{name: "below minimum clamps to 10ms", in: intPtr(1), want: 10 * time.Millisecond},
		{name: "negative clamps to 10ms", in: intPtr(-50), want: 10 * time.Millisecond},
		{name: "minimum accepted", in: intPtr(10), want: 10 * time.Millisecond},
		{name: "midrange passes through", in: intPtr(750), want: 750 * time.Millisecond},
		{name: "cap accepted", in: intPtr(5000), want: 5 * time.Second},
		{name: "above cap clamps to 5s", in: intPtr(60000), want: 5 * time.Second},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := resolveWindow(tt.in); got != tt.want {
				t.Errorf("resolveWindow(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestRound2(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   float64
		want float64
	}{
		{in: 87.142857, want: 87.14},
		{in: 0.005, want: 0.01},
		{in: 0, want: 0},
		{in: 199.999, want: 200},
	}
	for _, tt := range tests {
		if got := round2(tt.in); got != tt.want {
			t.Errorf("round2(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestComputeOutput(t *testing.T) {
	t.Parallel()

	t.Run("rich activity computes exact rates and totals", func(t *testing.T) {
		t.Parallel()
		before := parseVmstat([]byte(busyBefore))
		after := parseVmstat([]byte(busyAfter))

		out := computeOutput(before, after, 500*time.Millisecond)

		if out.WindowMs != 500 {
			t.Errorf("WindowMs = %d, want 500", out.WindowMs)
		}
		r := out.RatesPerSec
		if r.SwapIn != 20.00 {
			t.Errorf("swap_in = %v, want 20.00", r.SwapIn)
		}
		if r.SwapOut != 100.00 {
			t.Errorf("swap_out = %v, want 100.00", r.SwapOut)
		}
		if r.KswapdScan != 1000.00 {
			t.Errorf("kswapd_scan = %v, want 1000.00", r.KswapdScan)
		}
		if r.DirectScan != 200.00 {
			t.Errorf("direct_scan = %v, want 200.00", r.DirectScan)
		}
		if r.KswapdSteal != 900.00 {
			t.Errorf("kswapd_steal = %v, want 900.00", r.KswapdSteal)
		}
		if r.DirectSteal != 160.00 {
			t.Errorf("direct_steal = %v, want 160.00", r.DirectSteal)
		}
		if r.Allocstall != 6.00 {
			t.Errorf("allocstall = %v, want 6.00", r.Allocstall)
		}
		if r.MajorFaults != 20.00 {
			t.Errorf("major_faults = %v, want 20.00", r.MajorFaults)
		}
		if r.CompactStall != 2.00 {
			t.Errorf("compact_stall = %v, want 2.00", r.CompactStall)
		}

		wantTotals := map[string]uint64{
			"pswpin":             110,
			"pswpout":            250,
			"pgscan_direct":      600,
			"pgscan_kswapd":      1500,
			"allocstall":         18,
			"compact_stall":      4,
			"compact_fail":       1,
			"compact_success":    2,
			"thp_fault_fallback": 7,
			"oom_kill":           1,
		}
		if len(out.TotalsSinceBoot) != len(wantTotals) {
			t.Errorf("TotalsSinceBoot = %v, want %v", out.TotalsSinceBoot, wantTotals)
		}
		for k, v := range wantTotals {
			if out.TotalsSinceBoot[k] != v {
				t.Errorf("TotalsSinceBoot[%q] = %d, want %d", k, out.TotalsSinceBoot[k], v)
			}
		}

		// (1350+480)/(1500+600)*100 = 87.142857 → 87.14
		if out.ReclaimEfficiencyPct == nil {
			t.Fatal("ReclaimEfficiencyPct = nil, want 87.14")
		}
		if *out.ReclaimEfficiencyPct != 87.14 {
			t.Errorf("ReclaimEfficiencyPct = %v, want 87.14", *out.ReclaimEfficiencyPct)
		}
	})

	t.Run("counter regression yields zero rate", func(t *testing.T) {
		t.Parallel()
		before := map[string]uint64{"pswpin": 100}
		after := map[string]uint64{"pswpin": 40}
		out := computeOutput(before, after, 500*time.Millisecond)
		if out.RatesPerSec.SwapIn != 0 {
			t.Errorf("swap_in = %v, want 0 on counter regression", out.RatesPerSec.SwapIn)
		}
	})

	t.Run("missing keys are omitted from totals", func(t *testing.T) {
		t.Parallel()
		before := map[string]uint64{"pswpin": 1}
		after := map[string]uint64{"pswpin": 2}
		out := computeOutput(before, after, 500*time.Millisecond)
		if len(out.TotalsSinceBoot) != 1 {
			t.Fatalf("TotalsSinceBoot = %v, want only pswpin", out.TotalsSinceBoot)
		}
		if _, present := out.TotalsSinceBoot["oom_kill"]; present {
			t.Error("oom_kill must be omitted when absent from vmstat")
		}
	})

	t.Run("efficiency omitted when scan total is zero", func(t *testing.T) {
		t.Parallel()
		s := parseVmstat([]byte(quietVmstat))
		out := computeOutput(s, s, 500*time.Millisecond)
		if out.ReclaimEfficiencyPct != nil {
			t.Errorf("ReclaimEfficiencyPct = %v, want nil when pgscan total is 0", *out.ReclaimEfficiencyPct)
		}
	})

	t.Run("non positive elapsed yields zero rates but keeps totals", func(t *testing.T) {
		t.Parallel()
		before := map[string]uint64{"pswpin": 1}
		after := map[string]uint64{"pswpin": 100}
		out := computeOutput(before, after, 0)
		if out.RatesPerSec.SwapIn != 0 {
			t.Errorf("swap_in = %v, want 0 with zero elapsed", out.RatesPerSec.SwapIn)
		}
		if out.TotalsSinceBoot["pswpin"] != 100 {
			t.Errorf("TotalsSinceBoot[pswpin] = %d, want 100", out.TotalsSinceBoot["pswpin"])
		}
	})

	t.Run("old kernel zone suffixed sample aggregates", func(t *testing.T) {
		t.Parallel()
		oldKernel := `pgscan_kswapd_dma 1
pgscan_kswapd_normal 2
pgscan_direct_dma 3
pgscan_direct_normal 4
pgscan_direct_throttle 99
pgsteal_kswapd_dma 1
pgsteal_kswapd_normal 1
pgsteal_direct_dma 2
pgsteal_direct_normal 2
allocstall 6
pswpin 0
pswpout 0
pgmajfault 0
`
		s := parseVmstat([]byte(oldKernel))
		out := computeOutput(s, s, 500*time.Millisecond)
		if out.TotalsSinceBoot["pgscan_kswapd"] != 3 {
			t.Errorf("pgscan_kswapd total = %d, want 3", out.TotalsSinceBoot["pgscan_kswapd"])
		}
		if out.TotalsSinceBoot["pgscan_direct"] != 7 {
			t.Errorf("pgscan_direct total = %d, want 7 (throttle excluded)", out.TotalsSinceBoot["pgscan_direct"])
		}
		if out.TotalsSinceBoot["allocstall"] != 6 {
			t.Errorf("allocstall total = %d, want 6", out.TotalsSinceBoot["allocstall"])
		}
		// steal 6 / scan 10 * 100 = 60.00
		if out.ReclaimEfficiencyPct == nil || *out.ReclaimEfficiencyPct != 60.00 {
			t.Errorf("ReclaimEfficiencyPct = %v, want 60.00", out.ReclaimEfficiencyPct)
		}
	})
}

func TestBuildWarnings(t *testing.T) {
	t.Parallel()

	t.Run("quiet rates produce no warnings", func(t *testing.T) {
		t.Parallel()
		got := buildWarnings(Rates{})
		if len(got) != 0 {
			t.Errorf("buildWarnings() = %v, want none", got)
		}
	})

	t.Run("all activity classes warn", func(t *testing.T) {
		t.Parallel()
		got := buildWarnings(Rates{
			SwapIn:       1,
			SwapOut:      2,
			DirectScan:   3,
			Allocstall:   4,
			CompactStall: 5,
		})
		if len(got) != 4 {
			t.Fatalf("buildWarnings() = %v, want 4 warnings", got)
		}
		joined := strings.Join(got, " | ")
		for _, want := range []string{
			"direct-reclaiming — allocation latency impact",
			"allocstall",
			"swap",
			"compact_stall",
		} {
			if !strings.Contains(joined, want) {
				t.Errorf("warnings %q must mention %q", joined, want)
			}
		}
	})

	t.Run("swap in only still warns once", func(t *testing.T) {
		t.Parallel()
		got := buildWarnings(Rates{SwapIn: 0.5})
		if len(got) != 1 {
			t.Fatalf("buildWarnings() = %v, want 1 warning", got)
		}
		if !strings.Contains(got[0], "swap") {
			t.Errorf("warning %q must mention swap", got[0])
		}
	})
}

func TestMemoryReclaimTool_Execute_HappyPathQuiet(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.procfsRoot = "/fake-proc"
	fakeSampler(tool, quietVmstat, quietVmstat, 500*time.Millisecond)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"sample_duration_ms": 10}`))
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
	if out.WindowMs != 500 {
		t.Errorf("window_ms = %d, want 500 (fake clock)", out.WindowMs)
	}
	if out.RatesPerSec != (Rates{}) {
		t.Errorf("rates = %+v, want all zero", out.RatesPerSec)
	}
	if len(out.WarningReasons) != 0 {
		t.Errorf("warning_reasons = %v, want none", out.WarningReasons)
	}

	// Schema stability: the wire keys the catalog documents must exist.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(res.Data, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	for _, key := range []string{"window_ms", "rates_per_sec", "totals_since_boot", "warning_reasons"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("payload missing %q key", key)
		}
	}
	var rates map[string]float64
	if err := json.Unmarshal(raw["rates_per_sec"], &rates); err != nil {
		t.Fatalf("unmarshal rates: %v", err)
	}
	for _, key := range []string{
		"swap_in", "swap_out", "direct_scan", "kswapd_scan",
		"direct_steal", "kswapd_steal", "allocstall", "major_faults", "compact_stall",
	} {
		if _, ok := rates[key]; !ok {
			t.Errorf("rates_per_sec missing %q key", key)
		}
	}
}

func TestMemoryReclaimTool_Execute_HappyPathBusy(t *testing.T) {
	t.Parallel()

	tool := New()
	fakeSampler(tool, busyBefore, busyAfter, 500*time.Millisecond)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"sample_duration_ms": 10}`))
	if err != nil {
		t.Fatalf("Execute returned hard error: %v", err)
	}
	if res.Status != registry.StatusWarning {
		t.Fatalf("Status = %q, want warning (summary: %s)", res.Status, res.Summary)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if out.RatesPerSec.DirectScan != 200.00 {
		t.Errorf("direct_scan = %v, want 200.00", out.RatesPerSec.DirectScan)
	}
	if out.TotalsSinceBoot["oom_kill"] != 1 {
		t.Errorf("oom_kill total = %d, want 1", out.TotalsSinceBoot["oom_kill"])
	}
	if len(out.WarningReasons) != 4 {
		t.Errorf("warning_reasons = %v, want 4", out.WarningReasons)
	}
	if out.ReclaimEfficiencyPct == nil || *out.ReclaimEfficiencyPct != 87.14 {
		t.Errorf("reclaim_efficiency_pct = %v, want 87.14", out.ReclaimEfficiencyPct)
	}
}

func TestMemoryReclaimTool_Execute_ReadsVmstatFromProcfsRoot(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.procfsRoot = "/custom-proc"
	var gotPath string
	reads := 0
	tool.readFile = func(path string) ([]byte, error) {
		reads++
		gotPath = path
		return []byte(quietVmstat), nil
	}

	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"sample_duration_ms": 10}`)); err != nil {
		t.Fatalf("Execute returned hard error: %v", err)
	}
	want := filepath.Join("/custom-proc", "vmstat")
	if gotPath != want {
		t.Errorf("readFile path = %q, want %q", gotPath, want)
	}
	if reads != 2 {
		t.Errorf("readFile calls = %d, want 2 (two samples)", reads)
	}
}

func TestMemoryReclaimTool_Execute_DefaultReadFileUsesRealTree(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.procfsRoot = t.TempDir()
	if err := os.WriteFile(filepath.Join(tool.procfsRoot, "vmstat"), []byte(quietVmstat), 0o644); err != nil {
		t.Fatalf("write vmstat: %v", err)
	}

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"sample_duration_ms": 10}`))
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
	if out.WindowMs <= 0 {
		t.Errorf("window_ms = %d, want > 0 with the real clock", out.WindowMs)
	}
}

func TestMemoryReclaimTool_Execute_InvalidArgs(t *testing.T) {
	t.Parallel()

	tool := New()
	fakeSampler(tool, quietVmstat, quietVmstat, 500*time.Millisecond)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{not json`))
	if err != nil {
		t.Fatalf("Execute must encapsulate errors, got hard error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %q, want error on invalid args", res.Status)
	}
}

func TestMemoryReclaimTool_Execute_MissingVmstat(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.procfsRoot = t.TempDir() // no vmstat file

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"sample_duration_ms": 10}`))
	if err != nil {
		t.Fatalf("Execute must encapsulate errors, got hard error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %q, want error when vmstat is unreadable", res.Status)
	}
}

func TestMemoryReclaimTool_Execute_SecondSampleFails(t *testing.T) {
	t.Parallel()

	tool := New()
	reads := 0
	tool.readFile = func(string) ([]byte, error) {
		reads++
		if reads == 1 {
			return []byte(quietVmstat), nil
		}
		return nil, errors.New("boom")
	}

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"sample_duration_ms": 10}`))
	if err != nil {
		t.Fatalf("Execute must encapsulate errors, got hard error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %q, want error when second sample fails", res.Status)
	}
}

func TestMemoryReclaimTool_Execute_ContextAlreadyCanceled(t *testing.T) {
	t.Parallel()

	tool := New()
	fakeSampler(tool, quietVmstat, quietVmstat, 500*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := tool.Execute(ctx, json.RawMessage(`{"sample_duration_ms": 10}`))
	if err != nil {
		t.Fatalf("Execute must encapsulate cancellation, got hard error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %q, want error on canceled context", res.Status)
	}
}

func TestMemoryReclaimTool_Execute_ContextCanceledDuringWindow(t *testing.T) {
	t.Parallel()

	tool := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The first vmstat read fires cancel(), so the context dies while the
	// tool is inside its sampling wait. With the maximum 5s window, a
	// prompt return proves the select honors ctx.Done().
	tool.readFile = func(string) ([]byte, error) {
		cancel()
		return []byte(quietVmstat), nil
	}

	start := time.Now()
	res, err := tool.Execute(ctx, json.RawMessage(`{"sample_duration_ms": 5000}`))
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Execute must encapsulate cancellation, got hard error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %q, want error on mid-window cancellation", res.Status)
	}
	if !strings.Contains(strings.ToLower(res.Summary), "cancel") {
		t.Errorf("Summary = %q, want mention of cancellation", res.Summary)
	}
	if elapsed >= 2*time.Second {
		t.Errorf("Execute took %v, must return promptly on cancellation (not wait out the 5s window)", elapsed)
	}
}
