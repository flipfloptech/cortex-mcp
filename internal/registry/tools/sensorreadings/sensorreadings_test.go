package sensorreadings

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// writeChip creates a fake hwmon chip directory <root>/class/hwmon/<name>
// with the given attribute files.
func writeChip(tb testing.TB, root, hwmonName string, files map[string]string) {
	tb.Helper()
	dir := filepath.Join(root, "class", "hwmon", hwmonName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		tb.Fatal(err)
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			tb.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			tb.Fatal(err)
		}
	}
}

// newFakeSysfs builds a representative hwmon tree:
//   - hwmon0 (coretemp): three temps — ok, warning (>=max), critical (>=crit)
//   - hwmon1 (nct6779): attributes nested under device/ — fan + voltage
//   - hwmon2 (power_meter): power1_average in microwatts, no label
//   - hwmon3 (badchip): unparseable sensor only — chip skipped entirely
func newFakeSysfs(tb testing.TB) string {
	tb.Helper()
	root := tb.TempDir()

	writeChip(tb, root, "hwmon0", map[string]string{
		"name":        "coretemp\n",
		"temp1_input": "45000\n",
		"temp1_label": "Package id 0\n",
		"temp1_max":   "85000\n",
		"temp1_crit":  "100000\n",
		"temp2_input": "95000\n",
		"temp2_label": "Core 0\n",
		"temp2_max":   "85000\n",
		"temp2_crit":  "100000\n",
		"temp3_input": "101000\n",
		"temp3_label": "Core 1\n",
		"temp3_max":   "85000\n",
		"temp3_crit":  "100000\n",
	})

	// Attributes nested one level down in device/ (older kernel layout).
	writeChip(tb, root, "hwmon1", map[string]string{
		"device/name":       "nct6779\n",
		"device/fan1_input": "1200\n",
		"device/fan1_label": "CPU Fan\n",
		"device/in0_input":  "12250\n",
		"device/in0_label":  "+12V\n",
	})

	writeChip(tb, root, "hwmon2", map[string]string{
		"name":           "power_meter\n",
		"power1_average": "215500000\n",
	})

	writeChip(tb, root, "hwmon3", map[string]string{
		"name":        "badchip\n",
		"temp1_input": "garbage\n",
	})

	// Stray non-hwmon entry must be ignored.
	if err := os.WriteFile(filepath.Join(root, "class", "hwmon", "stray"), []byte("x"), 0o644); err != nil {
		tb.Fatal(err)
	}

	return root
}

func executeOutput(tb testing.TB, tool *Tool) (*registry.ToolResult, Output) {
	tb.Helper()
	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		tb.Fatalf("Execute returned hard error: %v", err)
	}
	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		tb.Fatalf("failed to unmarshal output: %v", err)
	}
	return res, out
}

func TestContract(t *testing.T) {
	t.Parallel()

	tool := New()
	if tool.Name() != "get_sensor_readings" {
		t.Errorf("Name = %q, want %q", tool.Name(), "get_sensor_readings")
	}
	if tool.Category() != registry.CategoryHardware {
		t.Errorf("Category = %q, want hardware", tool.Category())
	}
	if tool.Description() == "" {
		t.Error("Description must not be empty")
	}
	if tool.Hidden() {
		t.Error("tool must not be hidden")
	}
	if params := tool.Parameters(); params != nil {
		t.Errorf("Parameters = %v, want nil", params)
	}

	help := tool.Help()
	if help == "" {
		t.Fatal("Help must not be empty")
	}
	// Help must reference its data sources per workflow contract.
	for _, source := range []string{
		"/sys/class/hwmon",
		"temp",
		"_input",
		"fan",
		"power",
		"in",
	} {
		if !strings.Contains(help, source) {
			t.Errorf("Help missing data source reference: %q", source)
		}
	}
}

func TestIsSupported(t *testing.T) {
	t.Parallel()

	t.Run("missing_hwmon_class", func(t *testing.T) {
		t.Parallel()
		tool := New()
		tool.sysfsRoot = t.TempDir()
		supported, reason := tool.IsSupported()
		if supported {
			t.Error("expected unsupported when class/hwmon is missing")
		}
		if !strings.Contains(reason, "no hwmon sensors exposed") {
			t.Errorf("reason = %q, want mention of 'no hwmon sensors exposed'", reason)
		}
	})

	t.Run("empty_hwmon_class", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "class", "hwmon"), 0o755); err != nil {
			t.Fatal(err)
		}
		tool := New()
		tool.sysfsRoot = root
		supported, reason := tool.IsSupported()
		if supported {
			t.Error("expected unsupported when class/hwmon has no hwmon dirs")
		}
		if !strings.Contains(reason, "common in VMs") {
			t.Errorf("reason = %q, want mention of VMs", reason)
		}
	})

	t.Run("populated_hwmon_class", func(t *testing.T) {
		t.Parallel()
		tool := New()
		tool.sysfsRoot = newFakeSysfs(t)
		supported, reason := tool.IsSupported()
		if !supported {
			t.Errorf("expected supported, got reason: %q", reason)
		}
	})
}

func TestExecuteHappyPath(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.sysfsRoot = newFakeSysfs(t)

	res, out := executeOutput(t, tool)

	if res.Status != registry.StatusError {
		t.Errorf("Status = %q, want error (a sensor is over critical threshold)", res.Status)
	}

	if out.Summary.Chips != 3 {
		t.Errorf("summary.chips = %d, want 3 (badchip skipped)", out.Summary.Chips)
	}
	if out.Summary.SensorsTotal != 6 {
		t.Errorf("summary.sensors_total = %d, want 6", out.Summary.SensorsTotal)
	}
	if out.Summary.SensorsWarning != 1 {
		t.Errorf("summary.sensors_warning = %d, want 1", out.Summary.SensorsWarning)
	}
	if out.Summary.SensorsCritical != 1 {
		t.Errorf("summary.sensors_critical = %d, want 1", out.Summary.SensorsCritical)
	}

	if len(out.Chips) != 3 {
		t.Fatalf("chips = %d, want 3", len(out.Chips))
	}

	core := out.Chips[0]
	if core.Name != "coretemp" {
		t.Errorf("chips[0].name = %q, want coretemp", core.Name)
	}
	if len(core.Temps) != 3 {
		t.Fatalf("coretemp temps = %d, want 3", len(core.Temps))
	}

	pkg := core.Temps[0]
	if pkg.Label != "Package id 0" || pkg.Celsius != 45.0 || pkg.Status != "ok" {
		t.Errorf("temp1 = %+v, want Package id 0 / 45.0 / ok", pkg)
	}
	if pkg.MaxC == nil || *pkg.MaxC != 85.0 || pkg.CritC == nil || *pkg.CritC != 100.0 {
		t.Errorf("temp1 thresholds = %v/%v, want 85.0/100.0", pkg.MaxC, pkg.CritC)
	}
	if core.Temps[1].Status != "warning" {
		t.Errorf("temp2 status = %q, want warning (95.0 >= max 85.0)", core.Temps[1].Status)
	}
	if core.Temps[2].Status != "critical" {
		t.Errorf("temp3 status = %q, want critical (101.0 >= crit 100.0)", core.Temps[2].Status)
	}

	nct := out.Chips[1]
	if nct.Name != "nct6779" {
		t.Errorf("chips[1].name = %q, want nct6779 (nested device/ layout)", nct.Name)
	}
	if len(nct.Fans) != 1 || nct.Fans[0].Label != "CPU Fan" || nct.Fans[0].RPM != 1200 {
		t.Errorf("nct fans = %+v, want CPU Fan @ 1200 RPM", nct.Fans)
	}
	if len(nct.Voltages) != 1 || nct.Voltages[0].Label != "+12V" || nct.Voltages[0].Volts != 12.25 {
		t.Errorf("nct voltages = %+v, want +12V @ 12.25V", nct.Voltages)
	}

	meter := out.Chips[2]
	if meter.Name != "power_meter" {
		t.Errorf("chips[2].name = %q, want power_meter", meter.Name)
	}
	if len(meter.Power) != 1 || meter.Power[0].Label != "power1" || meter.Power[0].Watts != 215.5 {
		t.Errorf("meter power = %+v, want power1 @ 215.5W", meter.Power)
	}

	if len(out.WarningReasons) != 2 {
		t.Fatalf("warning_reasons = %v, want 2 entries", out.WarningReasons)
	}
	joined := strings.Join(out.WarningReasons, " | ")
	if !strings.Contains(joined, "Core 0") || !strings.Contains(joined, "Core 1") {
		t.Errorf("warning_reasons %q must name the over-threshold sensors", joined)
	}
	if !strings.Contains(joined, "101") {
		t.Errorf("warning_reasons %q must include the offending reading", joined)
	}
}

func TestExecuteAllHealthyStatusOK(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeChip(t, root, "hwmon0", map[string]string{
		"name":        "coretemp\n",
		"temp1_input": "40000\n",
		"temp1_max":   "85000\n",
		"temp1_crit":  "100000\n",
	})
	tool := New()
	tool.sysfsRoot = root

	res, out := executeOutput(t, tool)
	if res.Status != registry.StatusOK {
		t.Errorf("Status = %q, want ok", res.Status)
	}
	if len(out.Chips) != 1 || len(out.Chips[0].Temps) != 1 {
		t.Fatalf("unexpected chips: %+v", out.Chips)
	}
	if out.Chips[0].Temps[0].Label != "temp1" {
		t.Errorf("label = %q, want fallback temp1", out.Chips[0].Temps[0].Label)
	}
	if len(out.WarningReasons) != 0 {
		t.Errorf("warning_reasons = %v, want none", out.WarningReasons)
	}
}

func TestExecuteMissingThresholds(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeChip(t, root, "hwmon0", map[string]string{
		"name":        "acpitz\n",
		"temp1_input": "120000\n", // hot, but no thresholds exposed
	})
	tool := New()
	tool.sysfsRoot = root

	res, out := executeOutput(t, tool)
	if res.Status != registry.StatusOK {
		t.Errorf("Status = %q, want ok (no thresholds => status ok)", res.Status)
	}
	temp := out.Chips[0].Temps[0]
	if temp.Status != "ok" {
		t.Errorf("status = %q, want ok when max/crit missing", temp.Status)
	}
	if temp.MaxC != nil || temp.CritC != nil {
		t.Errorf("thresholds = %v/%v, want both omitted", temp.MaxC, temp.CritC)
	}
	if temp.Celsius != 120.0 {
		t.Errorf("celsius = %v, want 120.0", temp.Celsius)
	}
}

func TestExecuteMissingTree(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.sysfsRoot = t.TempDir()

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected encapsulated error, got hard error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %q, want error when class/hwmon missing", res.Status)
	}
}

func TestExecuteContextCancelled(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.sysfsRoot = newFakeSysfs(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := tool.Execute(ctx, nil)
	if err != nil {
		t.Fatalf("expected encapsulated error, got hard error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %q, want error on cancelled context", res.Status)
	}
}

func TestSensorStatus(t *testing.T) {
	t.Parallel()

	f := func(v float64) *float64 { return &v }
	tests := []struct {
		name  string
		value float64
		max   *float64
		crit  *float64
		want  string
	}{
		{"below_all", 50, f(85), f(100), "ok"},
		{"at_max", 85, f(85), f(100), "warning"},
		{"between", 90, f(85), f(100), "warning"},
		{"at_crit", 100, f(85), f(100), "critical"},
		{"above_crit", 120, f(85), f(100), "critical"},
		{"crit_only_below", 90, nil, f(100), "ok"},
		{"crit_only_hit", 100, nil, f(100), "critical"},
		{"max_only_hit", 90, f(85), nil, "warning"},
		{"no_thresholds", 500, nil, nil, "ok"},
	}
	for _, tc := range tests {
		if got := sensorStatus(tc.value, tc.max, tc.crit); got != tc.want {
			t.Errorf("%s: sensorStatus(%v) = %q, want %q", tc.name, tc.value, got, tc.want)
		}
	}
}

func TestRound1(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   float64
		want float64
	}{
		{45.06, 45.1},
		{45.04, 45.0},
		{-1.25, -1.3}, // math.Round: half away from zero
		{0, 0},
	}
	for _, tc := range tests {
		if got := round1(tc.in); got != tc.want {
			t.Errorf("round1(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestRound3(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   float64
		want float64
	}{
		{12.2504, 12.25},
		{12.2506, 12.251},
		{0.0004, 0},
	}
	for _, tc := range tests {
		if got := round3(tc.in); got != tc.want {
			t.Errorf("round3(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestReadScaled(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "temp1_input")
	if err := os.WriteFile(path, []byte("45500\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok := readScaled(path, 1000)
	if !ok || got != 45.5 {
		t.Errorf("readScaled = (%v, %v), want (45.5, true)", got, ok)
	}

	bad := filepath.Join(dir, "bad")
	if err := os.WriteFile(bad, []byte("not-a-number\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := readScaled(bad, 1000); ok {
		t.Error("readScaled on garbage must return ok=false")
	}
	if _, ok := readScaled(filepath.Join(dir, "missing"), 1000); ok {
		t.Error("readScaled on missing file must return ok=false")
	}
}

func TestReadTrimmed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "name")
	if err := os.WriteFile(path, []byte("coretemp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := readTrimmed(path)
	if !ok || got != "coretemp" {
		t.Errorf("readTrimmed = (%q, %v), want (coretemp, true)", got, ok)
	}
	if _, ok := readTrimmed(filepath.Join(dir, "missing")); ok {
		t.Error("readTrimmed on missing file must return ok=false")
	}
}

func TestSensorFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeChip(t, root, "hwmon9", map[string]string{
		"name":               "base\n",
		"temp1_input":        "1000\n",
		"device/name":        "nested\n", // base dir must win
		"device/fan1_input":  "900\n",
		"device/fan1_label":  "Rear\n",
		"device/subdir/skip": "x\n", // only direct children considered
	})

	files := sensorFiles(filepath.Join(root, "class", "hwmon", "hwmon9"))

	if _, ok := files["temp1_input"]; !ok {
		t.Error("sensorFiles missing base temp1_input")
	}
	if _, ok := files["fan1_input"]; !ok {
		t.Error("sensorFiles missing nested device/fan1_input")
	}
	if got := files["name"]; !strings.HasSuffix(got, "hwmon9/name") {
		t.Errorf("name resolved to %q, want base dir to take priority", got)
	}
	if _, ok := files["skip"]; ok {
		t.Error("sensorFiles must not recurse below device/")
	}
}

func TestCollectChip(t *testing.T) {
	t.Parallel()

	root := newFakeSysfs(t)

	chip := collectChip(filepath.Join(root, "class", "hwmon", "hwmon0"))
	if chip.Name != "coretemp" || len(chip.Temps) != 3 {
		t.Errorf("collectChip(hwmon0) = %+v, want coretemp with 3 temps", chip)
	}

	empty := collectChip(filepath.Join(root, "class", "hwmon", "hwmon3"))
	if len(empty.Temps)+len(empty.Fans)+len(empty.Power)+len(empty.Voltages) != 0 {
		t.Errorf("collectChip(hwmon3) = %+v, want zero sensors (garbage skipped)", empty)
	}
}
