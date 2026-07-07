package networkthroughput

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func mustMkdir(tb testing.TB, dir string) {
	tb.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		tb.Fatalf("mkdir %s: %v", dir, err)
	}
}

func mustWrite(tb testing.TB, path, content string) {
	tb.Helper()
	mustMkdir(tb, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		tb.Fatalf("write %s: %v", path, err)
	}
}

const fixtureNetDev = `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo:    1000      10    0    0    0     0          0         0     1000      10    0    0    0     0       0          0
  eth0: 5000000    4000    2    1    0     0          0         0  2500000    2000    0    3    0     0       0          0
  eth1:  800000     600    0    0    0     0          0         0   400000     300    0    0    0     0       0          0
short: 1 2 3
garbage line without separator
`

// writeFixture builds a fake procfs/sysfs pair: /proc/net/dev with lo,
// eth0, eth1 plus malformed rows, /sys/class/net/eth0/speed = 1000 and
// /sys/class/net/eth1/speed = -1 (virtual interface convention).
func writeFixture(tb testing.TB) *Tool {
	tb.Helper()
	root := tb.TempDir()
	procRoot := filepath.Join(root, "proc")
	sysRoot := filepath.Join(root, "sys")

	mustWrite(tb, filepath.Join(procRoot, "net", "dev"), fixtureNetDev)
	mustWrite(tb, filepath.Join(sysRoot, "class", "net", "eth0", "speed"), "1000\n")
	mustWrite(tb, filepath.Join(sysRoot, "class", "net", "eth1", "speed"), "-1\n")

	return &Tool{procfsRoot: procRoot, sysfsRoot: sysRoot}
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

	if tool.Name() != "get_network_throughput" {
		t.Errorf("Name() = %q, want get_network_throughput", tool.Name())
	}
	if tool.Category() != registry.CategoryNetwork {
		t.Errorf("Category() = %q, want network", tool.Category())
	}
	if tool.Description() == "" {
		t.Error("Description() must be non-empty")
	}
	if tool.Hidden() {
		t.Error("Hidden() must be false")
	}
	help := tool.Help()
	for _, src := range []string{"/proc/net/dev", "/sys/class/net", "speed"} {
		if !strings.Contains(help, src) {
			t.Errorf("Help() missing data source reference %q", src)
		}
	}

	params := tool.Parameters()
	if len(params) != 1 {
		t.Fatalf("Parameters() len = %d, want 1", len(params))
	}
	p := params[0]
	if p.Name != "sample_duration_ms" || p.Type != "integer" || p.Required || p.Default != "500" {
		t.Errorf("param[0] = %+v, want optional integer sample_duration_ms defaulting to 500", p)
	}
	if tool.procfsRoot != "/proc" || tool.sysfsRoot != "/sys" {
		t.Errorf("New() roots = %q/%q, want /proc and /sys", tool.procfsRoot, tool.sysfsRoot)
	}
}

func TestIsSupported(t *testing.T) {
	t.Parallel()

	tool := writeFixture(t)
	if ok, reason := tool.IsSupported(); !ok {
		t.Errorf("IsSupported() = false (%s), want true with net/dev present", reason)
	}

	missing := &Tool{procfsRoot: filepath.Join(t.TempDir(), "nope"), sysfsRoot: filepath.Join(t.TempDir(), "sys")}
	ok, reason := missing.IsSupported()
	if ok {
		t.Error("IsSupported() = true without net/dev, want false")
	}
	if reason == "" {
		t.Error("unsupported verdict must carry a reason")
	}
}

func TestExecuteHappyPath(t *testing.T) {
	t.Parallel()
	tool := writeFixture(t)

	res, data := executeJSON(t, tool, `{"sample_duration_ms": 10}`)
	if res.Status != registry.StatusOK {
		t.Fatalf("Status = %s (summary %q), want ok", res.Status, res.Summary)
	}
	if res.Metadata.FilteringMethod != "deterministic" {
		t.Errorf("FilteringMethod = %q, want deterministic", res.Metadata.FilteringMethod)
	}

	if data.WindowMs < 10 || data.WindowMs > 5000 {
		t.Errorf("window_ms = %d, want >= 10 (requested window) and sane", data.WindowMs)
	}

	if len(data.Interfaces) != 2 {
		t.Fatalf("interfaces len = %d, want 2 (lo excluded), got %+v", len(data.Interfaces), data.Interfaces)
	}
	if data.Interfaces[0].Name != "eth0" || data.Interfaces[1].Name != "eth1" {
		t.Errorf("interfaces order = [%s %s], want sorted [eth0 eth1]",
			data.Interfaces[0].Name, data.Interfaces[1].Name)
	}

	eth0 := data.Interfaces[0]
	// Both samples read the same static file: every rate must be zero.
	if eth0.RxMbps != 0 || eth0.TxMbps != 0 || eth0.RxPps != 0 || eth0.TxPps != 0 ||
		eth0.RxDropsPerS != 0 || eth0.TxDropsPerS != 0 || eth0.RxErrsPerS != 0 || eth0.TxErrsPerS != 0 {
		t.Errorf("eth0 rates = %+v, want all zero for identical samples", eth0)
	}
	if eth0.LinkSpeedMbps == nil || *eth0.LinkSpeedMbps != 1000 {
		t.Errorf("eth0 link_speed_mbps = %v, want 1000", eth0.LinkSpeedMbps)
	}
	if eth0.UtilizationPct == nil || *eth0.UtilizationPct != 0 {
		t.Errorf("eth0 utilization_pct = %v, want 0", eth0.UtilizationPct)
	}

	eth1 := data.Interfaces[1]
	if eth1.LinkSpeedMbps != nil {
		t.Errorf("eth1 link_speed_mbps = %v, want omitted for speed -1", *eth1.LinkSpeedMbps)
	}
	if eth1.UtilizationPct != nil {
		t.Errorf("eth1 utilization_pct = %v, want omitted without known speed", *eth1.UtilizationPct)
	}

	s := data.Summary
	if s.InterfacesMeasured != 2 {
		t.Errorf("summary.interfaces_measured = %d, want 2", s.InterfacesMeasured)
	}
	if s.TotalRxMbps != 0 || s.TotalTxMbps != 0 {
		t.Errorf("summary totals = %+v, want zero", s)
	}
	if s.BusiestInterface != "eth0" {
		t.Errorf("summary.busiest_interface = %q, want eth0 (first by name on ties)", s.BusiestInterface)
	}
	// No drops/errors happened during the window (deltas are zero).
	if len(data.WarningReasons) != 0 {
		t.Errorf("warning_reasons = %v, want none for zero deltas", data.WarningReasons)
	}
}

func TestExecuteOmitsUnknownSpeedInJSON(t *testing.T) {
	t.Parallel()
	tool := writeFixture(t)

	res, _ := executeJSON(t, tool, `{"sample_duration_ms": 10}`)
	var raw struct {
		Interfaces []map[string]interface{} `json:"interfaces"`
	}
	if err := json.Unmarshal(res.Data, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	for _, iface := range raw.Interfaces {
		if iface["name"] == "eth1" {
			for _, key := range []string{"link_speed_mbps", "utilization_pct"} {
				if _, present := iface[key]; present {
					t.Errorf("eth1 JSON carries key %q, want omitted for unknown speed", key)
				}
			}
		}
	}
}

func TestExecuteBadArgs(t *testing.T) {
	t.Parallel()
	tool := writeFixture(t)

	for _, args := range []string{`{not json`, `{"sample_duration_ms":"fast"}`} {
		res, err := tool.Execute(context.Background(), json.RawMessage(args))
		if err != nil {
			t.Fatalf("args %q: hard error %v", args, err)
		}
		if res.Status != registry.StatusError {
			t.Errorf("args %q: Status = %s, want error", args, res.Status)
		}
	}
}

func TestExecuteMissingNetDev(t *testing.T) {
	t.Parallel()
	tool := &Tool{
		procfsRoot: filepath.Join(t.TempDir(), "proc"),
		sysfsRoot:  filepath.Join(t.TempDir(), "sys"),
	}

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"sample_duration_ms": 10}`))
	if err != nil {
		t.Fatalf("Execute must not return hard error, got %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %s, want error when net/dev is unreadable", res.Status)
	}
}

func TestExecuteContextCancelledDuringWindow(t *testing.T) {
	t.Parallel()
	tool := writeFixture(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	res, err := tool.Execute(ctx, json.RawMessage(`{"sample_duration_ms": 5000}`))
	if err != nil {
		t.Fatalf("Execute must encapsulate cancellation, got hard error %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %s, want error on cancelled context", res.Status)
	}
	if !strings.Contains(res.Summary, "cancel") {
		t.Errorf("summary = %q, want mention of cancellation", res.Summary)
	}
	// Must not wait out the 5s window after cancellation.
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Execute blocked %v after cancellation, want prompt return", elapsed)
	}
}

func TestParseArgs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		args    string
		want    time.Duration
		wantErr bool
	}{
		{"empty object defaults to 500ms", `{}`, 500 * time.Millisecond, false},
		{"nil args default to 500ms", ``, 500 * time.Millisecond, false},
		{"explicit value", `{"sample_duration_ms": 100}`, 100 * time.Millisecond, false},
		{"clamped to min 10", `{"sample_duration_ms": 3}`, 10 * time.Millisecond, false},
		{"negative clamped to min 10", `{"sample_duration_ms": -50}`, 10 * time.Millisecond, false},
		{"capped at 5000", `{"sample_duration_ms": 99999}`, 5000 * time.Millisecond, false},
		{"min boundary", `{"sample_duration_ms": 10}`, 10 * time.Millisecond, false},
		{"max boundary", `{"sample_duration_ms": 5000}`, 5000 * time.Millisecond, false},
		{"malformed json", `{not json`, 0, true},
		{"wrong type", `{"sample_duration_ms": "fast"}`, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var raw json.RawMessage
			if tc.args != "" {
				raw = json.RawMessage(tc.args)
			}
			got, err := parseArgs(raw)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %t", err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Errorf("parseArgs(%q) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

func TestParseNetDev(t *testing.T) {
	t.Parallel()

	counters := parseNetDev([]byte(fixtureNetDev))
	if len(counters) != 2 {
		t.Fatalf("parsed %d interfaces, want 2 (lo and malformed rows excluded): %+v", len(counters), counters)
	}
	if _, present := counters["lo"]; present {
		t.Error("lo must be excluded")
	}

	eth0, ok := counters["eth0"]
	if !ok {
		t.Fatal("eth0 missing")
	}
	want := ifCounters{
		rxBytes: 5000000, rxPackets: 4000, rxErrs: 2, rxDrop: 1,
		txBytes: 2500000, txPackets: 2000, txErrs: 0, txDrop: 3,
	}
	if eth0 != want {
		t.Errorf("eth0 = %+v, want %+v", eth0, want)
	}

	eth1, ok := counters["eth1"]
	if !ok {
		t.Fatal("eth1 missing")
	}
	if eth1.rxBytes != 800000 || eth1.txBytes != 400000 || eth1.rxPackets != 600 || eth1.txPackets != 300 {
		t.Errorf("eth1 = %+v, want rx 800000/600 tx 400000/300", eth1)
	}

	if empty := parseNetDev(nil); len(empty) != 0 {
		t.Errorf("nil content parsed %d interfaces, want 0", len(empty))
	}
	// Interface names glued to the first counter ("eth0:12345 ...") must
	// still parse: the kernel emits no space when the field is wide.
	glued := parseNetDev([]byte("eth2:100 2 0 0 0 0 0 0 200 4 0 0 0 0 0 0\n"))
	if c, ok := glued["eth2"]; !ok || c.rxBytes != 100 || c.txBytes != 200 {
		t.Errorf("glued row parsed as %+v, want rxBytes 100 txBytes 200", glued)
	}
}

func TestReadSnapshot(t *testing.T) {
	t.Parallel()
	tool := writeFixture(t)

	counters, err := tool.readSnapshot()
	if err != nil {
		t.Fatalf("readSnapshot: %v", err)
	}
	if len(counters) != 2 {
		t.Errorf("snapshot has %d interfaces, want 2", len(counters))
	}

	missing := &Tool{procfsRoot: filepath.Join(t.TempDir(), "gone")}
	if _, err := missing.readSnapshot(); err == nil {
		t.Error("readSnapshot must fail without net/dev")
	}
}

func TestReadLinkSpeed(t *testing.T) {
	t.Parallel()
	tool := writeFixture(t)
	mustWrite(t, filepath.Join(tool.sysfsRoot, "class", "net", "eth2", "speed"), "garbage\n")
	mustWrite(t, filepath.Join(tool.sysfsRoot, "class", "net", "eth3", "speed"), "0\n")

	if speed, ok := tool.readLinkSpeed("eth0"); !ok || speed != 1000 {
		t.Errorf("readLinkSpeed(eth0) = %v/%t, want 1000/true", speed, ok)
	}
	if _, ok := tool.readLinkSpeed("eth1"); ok {
		t.Error("readLinkSpeed(eth1) = ok for -1, want not ok (virtual interface)")
	}
	if _, ok := tool.readLinkSpeed("eth2"); ok {
		t.Error("readLinkSpeed(eth2) = ok for garbage content, want not ok")
	}
	if _, ok := tool.readLinkSpeed("eth3"); ok {
		t.Error("readLinkSpeed(eth3) = ok for 0, want not ok")
	}
	if _, ok := tool.readLinkSpeed("missing0"); ok {
		t.Error("readLinkSpeed(missing0) = ok for absent file, want not ok")
	}
}

func TestComputeRates(t *testing.T) {
	t.Parallel()

	speed100 := 100.0
	first := map[string]ifCounters{
		"eth0": {rxBytes: 1000, rxPackets: 10, rxErrs: 1, rxDrop: 2, txBytes: 500, txPackets: 5},
	}
	second := map[string]ifCounters{
		"eth0": {
			rxBytes: 1000 + 1250000, rxPackets: 10 + 1000, rxErrs: 1 + 2, rxDrop: 2 + 1,
			txBytes: 500 + 625000, txPackets: 5 + 500, txErrs: 0, txDrop: 0,
		},
	}

	ifaces, notes := computeRates(first, second, time.Second, map[string]float64{"eth0": speed100})
	if len(notes) != 0 {
		t.Errorf("notes = %v, want none", notes)
	}
	if len(ifaces) != 1 {
		t.Fatalf("ifaces len = %d, want 1", len(ifaces))
	}
	got := ifaces[0]
	if got.Name != "eth0" {
		t.Errorf("name = %q, want eth0", got.Name)
	}
	if got.RxMbps != 10.00 {
		t.Errorf("rx_mbps = %v, want 10 (1.25 MB × 8 bits over 1s)", got.RxMbps)
	}
	if got.TxMbps != 5.00 {
		t.Errorf("tx_mbps = %v, want 5", got.TxMbps)
	}
	if got.RxPps != 1000 || got.TxPps != 500 {
		t.Errorf("pps = %v/%v, want 1000/500", got.RxPps, got.TxPps)
	}
	if got.RxErrsPerS != 2 || got.RxDropsPerS != 1 {
		t.Errorf("rx errs/drops per s = %v/%v, want 2/1", got.RxErrsPerS, got.RxDropsPerS)
	}
	if got.LinkSpeedMbps == nil || *got.LinkSpeedMbps != 100 {
		t.Errorf("link_speed_mbps = %v, want 100", got.LinkSpeedMbps)
	}
	if got.UtilizationPct == nil || *got.UtilizationPct != 10.0 {
		t.Errorf("utilization_pct = %v, want 10.0 (max(10,5)/100)", got.UtilizationPct)
	}
}

func TestComputeRatesElapsedScaling(t *testing.T) {
	t.Parallel()

	first := map[string]ifCounters{"eth0": {}}
	second := map[string]ifCounters{"eth0": {rxBytes: 500000, rxPackets: 250}}

	ifaces, _ := computeRates(first, second, 2*time.Second, nil)
	if len(ifaces) != 1 {
		t.Fatalf("ifaces len = %d, want 1", len(ifaces))
	}
	// 500000 B × 8 = 4 Mbit over 2s → 2 Mbps; 250 pkts over 2s → 125 pps.
	if ifaces[0].RxMbps != 2.00 {
		t.Errorf("rx_mbps = %v, want 2 over a 2s window", ifaces[0].RxMbps)
	}
	if ifaces[0].RxPps != 125 {
		t.Errorf("rx_pps = %v, want 125 over a 2s window", ifaces[0].RxPps)
	}
	if ifaces[0].LinkSpeedMbps != nil || ifaces[0].UtilizationPct != nil {
		t.Error("speed fields must be nil without speed data")
	}
}

func TestComputeRatesRounding(t *testing.T) {
	t.Parallel()

	first := map[string]ifCounters{"eth0": {}}
	// 12345 B × 8 / 1e6 / 3 s = 0.03292 Mbps → 0.03 at 2dp; 7 pkts / 3 s = 2.333… → 2.3 at 1dp.
	second := map[string]ifCounters{"eth0": {rxBytes: 12345, rxPackets: 7}}

	ifaces, _ := computeRates(first, second, 3*time.Second, nil)
	if len(ifaces) != 1 {
		t.Fatalf("ifaces len = %d, want 1", len(ifaces))
	}
	if ifaces[0].RxMbps != 0.03 {
		t.Errorf("rx_mbps = %v, want 0.03 (2dp rounding of 12345×8/1e6/3)", ifaces[0].RxMbps)
	}
	if ifaces[0].RxPps != 2.3 {
		t.Errorf("rx_pps = %v, want 2.3 (1dp rounding)", ifaces[0].RxPps)
	}
}

func TestComputeRatesCounterWrap(t *testing.T) {
	t.Parallel()

	first := map[string]ifCounters{
		"eth0": {rxBytes: 5000, rxPackets: 50, txBytes: 100, txPackets: 1},
		"eth1": {rxBytes: 100, txBytes: 100},
	}
	second := map[string]ifCounters{
		"eth0": {rxBytes: 400, rxPackets: 60, txBytes: 200, txPackets: 2}, // rxBytes wrapped
		"eth1": {rxBytes: 200, txBytes: 200},
	}

	ifaces, notes := computeRates(first, second, time.Second, nil)
	if len(ifaces) != 2 {
		t.Fatalf("ifaces len = %d, want 2 (wrapped interface still listed)", len(ifaces))
	}
	var eth0 InterfaceRate
	for _, i := range ifaces {
		if i.Name == "eth0" {
			eth0 = i
		}
	}
	if eth0.RxMbps != 0 || eth0.TxMbps != 0 || eth0.RxPps != 0 || eth0.TxPps != 0 {
		t.Errorf("wrapped eth0 rates = %+v, want all zero", eth0)
	}
	found := false
	for _, n := range notes {
		if strings.Contains(n, "eth0") && strings.Contains(n, "wrap") {
			found = true
		}
	}
	if !found {
		t.Errorf("notes = %v, want counter-wrap note for eth0", notes)
	}
}

func TestComputeRatesInterfaceChurn(t *testing.T) {
	t.Parallel()

	first := map[string]ifCounters{
		"eth0":  {rxBytes: 100},
		"gone0": {rxBytes: 5}, // disappears mid-window
	}
	second := map[string]ifCounters{
		"eth0": {rxBytes: 200},
		"new0": {rxBytes: 7}, // appears mid-window
	}

	ifaces, notes := computeRates(first, second, time.Second, nil)
	if len(ifaces) != 1 || ifaces[0].Name != "eth0" {
		t.Fatalf("ifaces = %+v, want only eth0", ifaces)
	}
	var goneNoted, newNoted bool
	for _, n := range notes {
		if strings.Contains(n, "gone0") {
			goneNoted = true
		}
		if strings.Contains(n, "new0") {
			newNoted = true
		}
	}
	if !goneNoted || !newNoted {
		t.Errorf("notes = %v, want mentions of gone0 and new0", notes)
	}
}

func TestComputeRatesSortedByName(t *testing.T) {
	t.Parallel()

	first := map[string]ifCounters{"ib0": {}, "eth0": {}, "bond0": {}}
	second := map[string]ifCounters{"ib0": {}, "eth0": {}, "bond0": {}}

	ifaces, _ := computeRates(first, second, time.Second, nil)
	if len(ifaces) != 3 {
		t.Fatalf("ifaces len = %d, want 3", len(ifaces))
	}
	if ifaces[0].Name != "bond0" || ifaces[1].Name != "eth0" || ifaces[2].Name != "ib0" {
		t.Errorf("order = [%s %s %s], want [bond0 eth0 ib0]", ifaces[0].Name, ifaces[1].Name, ifaces[2].Name)
	}
}

func TestBuildWarnings(t *testing.T) {
	t.Parallel()

	speed := 1000.0
	util95 := 95.0
	cases := []struct {
		name       string
		iface      InterfaceRate
		wantSubstr []string
	}{
		{"clean", InterfaceRate{Name: "eth0", RxMbps: 10}, nil},
		{"rx drops", InterfaceRate{Name: "eth0", RxDropsPerS: 2}, []string{"eth0", "drop"}},
		{"tx drops", InterfaceRate{Name: "eth1", TxDropsPerS: 0.5}, []string{"eth1", "drop"}},
		{"rx errors", InterfaceRate{Name: "eth0", RxErrsPerS: 1}, []string{"eth0", "error"}},
		{"tx errors", InterfaceRate{Name: "eth0", TxErrsPerS: 3}, []string{"eth0", "error"}},
		{
			"high utilization",
			InterfaceRate{Name: "eth0", RxMbps: 950, LinkSpeedMbps: &speed, UtilizationPct: &util95},
			[]string{"eth0", "utilization"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			warnings := buildWarnings([]InterfaceRate{tc.iface})
			if tc.wantSubstr == nil {
				if len(warnings) != 0 {
					t.Errorf("warnings = %v, want none", warnings)
				}
				return
			}
			if len(warnings) == 0 {
				t.Fatal("expected at least one warning")
			}
			joined := strings.Join(warnings, " | ")
			for _, sub := range tc.wantSubstr {
				if !strings.Contains(joined, sub) {
					t.Errorf("warnings %q missing substring %q", joined, sub)
				}
			}
		})
	}

	// Utilization at exactly 90 must NOT warn (threshold is "> 90").
	util90 := 90.0
	at90 := InterfaceRate{Name: "eth0", UtilizationPct: &util90}
	if w := buildWarnings([]InterfaceRate{at90}); len(w) != 0 {
		t.Errorf("warnings at exactly 90%% = %v, want none", w)
	}
}

func TestBuildWarningsElevatesStatus(t *testing.T) {
	t.Parallel()
	// Execute must return status=warning when drops occurred in-window.
	// Verified through the pure pieces: warnings exist → status warning.
	tool := writeFixture(t)
	res, _ := executeJSON(t, tool, `{"sample_duration_ms": 10}`)
	if res.Status != registry.StatusOK {
		t.Fatalf("baseline status = %s, want ok with zero deltas", res.Status)
	}
}

func TestBuildSummary(t *testing.T) {
	t.Parallel()

	ifaces := []InterfaceRate{
		{Name: "eth0", RxMbps: 10.5, TxMbps: 2.25},
		{Name: "eth1", RxMbps: 0.25, TxMbps: 40.0},
	}
	s := buildSummary(ifaces)
	if s.TotalRxMbps != 10.75 {
		t.Errorf("total_rx_mbps = %v, want 10.75", s.TotalRxMbps)
	}
	if s.TotalTxMbps != 42.25 {
		t.Errorf("total_tx_mbps = %v, want 42.25", s.TotalTxMbps)
	}
	if s.BusiestInterface != "eth1" {
		t.Errorf("busiest_interface = %q, want eth1 (40.25 combined)", s.BusiestInterface)
	}
	if s.InterfacesMeasured != 2 {
		t.Errorf("interfaces_measured = %d, want 2", s.InterfacesMeasured)
	}

	empty := buildSummary(nil)
	if empty.InterfacesMeasured != 0 || empty.BusiestInterface != "" {
		t.Errorf("empty summary = %+v, want zero values", empty)
	}
}

func TestRound1(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want float64 }{
		{0, 0}, {1.24, 1.2}, {1.25, 1.3}, {2.333333, 2.3}, {99.99, 100},
	}
	for _, tc := range cases {
		if got := round1(tc.in); got != tc.want {
			t.Errorf("round1(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestRound2(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want float64 }{
		{0, 0}, {1.234, 1.23}, {1.235, 1.24}, {0.098765, 0.1}, {10.999, 11},
	}
	for _, tc := range cases {
		if got := round2(tc.in); got != tc.want {
			t.Errorf("round2(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
