package cpupowerstate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func setupMockSysfs(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	write := func(path, content string) {
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Create mock paths
	// /sys/devices/system/cpu/cpu0/cpufreq
	cpu0Freq := filepath.Join(dir, "cpu0", "cpufreq")
	if err := os.MkdirAll(cpu0Freq, 0755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(cpu0Freq, "scaling_governor"), "powersave\n")
	write(filepath.Join(cpu0Freq, "energy_performance_preference"), "performance\n")
	write(filepath.Join(cpu0Freq, "scaling_cur_freq"), "4350000\n")
	write(filepath.Join(cpu0Freq, "scaling_max_freq"), "4500000\n")
	write(filepath.Join(cpu0Freq, "scaling_min_freq"), "1500000\n")
	write(filepath.Join(cpu0Freq, "scaling_driver"), "amd-pstate-epp\n")

	// /sys/devices/system/cpu/cpu1/cpufreq
	cpu1Freq := filepath.Join(dir, "cpu1", "cpufreq")
	if err := os.MkdirAll(cpu1Freq, 0755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(cpu1Freq, "scaling_governor"), "powersave\n")
	write(filepath.Join(cpu1Freq, "energy_performance_preference"), "performance\n")
	write(filepath.Join(cpu1Freq, "scaling_cur_freq"), "4350000\n")
	write(filepath.Join(cpu1Freq, "scaling_max_freq"), "4500000\n")
	write(filepath.Join(cpu1Freq, "scaling_min_freq"), "1500000\n")

	// /sys/devices/system/cpu/cpuidle
	cpuidle := filepath.Join(dir, "cpuidle")
	if err := os.MkdirAll(cpuidle, 0755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(cpuidle, "current_driver"), "acpi_idle\n")

	// /sys/devices/system/cpu/cpu0/cpuidle/stateX
	state0 := filepath.Join(dir, "cpu0", "cpuidle", "state0")
	if err := os.MkdirAll(state0, 0755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(state0, "name"), "POLL\n")
	write(filepath.Join(state0, "disable"), "0\n")

	state1 := filepath.Join(dir, "cpu0", "cpuidle", "state1")
	if err := os.MkdirAll(state1, 0755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(state1, "name"), "C1\n")
	write(filepath.Join(state1, "disable"), "0\n")

	state2 := filepath.Join(dir, "cpu0", "cpuidle", "state2")
	if err := os.MkdirAll(state2, 0755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(state2, "name"), "C6\n")
	write(filepath.Join(state2, "disable"), "1\n")

	return dir
}

func TestCpuPowerState_Contract(t *testing.T) {
	tool := &CpuPowerStateTool{}
	if tool.Name() != "get_cpu_power_state" {
		t.Errorf("expected name get_cpu_power_state, got %s", tool.Name())
	}
	if tool.Category() != registry.CategoryCompute {
		t.Errorf("expected category compute, got %s", tool.Category())
	}
	if tool.Description() == "" || tool.Help() == "" {
		t.Errorf("expected description and help to be populated")
	}
	if len(tool.Parameters()) != 0 {
		t.Errorf("expected 0 parameters, got %d", len(tool.Parameters()))
	}
}

func TestCpuPowerState_Execute_HappyPath(t *testing.T) {
	dir := setupMockSysfs(t)
	sysDevicesSystemCpuPath = dir

	tool := &CpuPowerStateTool{}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if res.Status == registry.StatusError {
		t.Fatalf("expected success result, got error: %s", string(res.Data))
	}

	var data CpuPowerStateData
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	if !data.SystemSummary.PowerManagementManagedByOS {
		t.Errorf("expected power management to be OS managed")
	}
	if data.SystemSummary.CpufreqDriver != "amd-pstate-epp" {
		t.Errorf("expected cpufreq_driver amd-pstate-epp, got %s", data.SystemSummary.CpufreqDriver)
	}
	if data.SystemSummary.CpuidleDriver != "acpi_idle" {
		t.Errorf("expected cpuidle_driver acpi_idle, got %s", data.SystemSummary.CpuidleDriver)
	}
	if !data.SystemSummary.CStatesVisible {
		t.Errorf("expected C-states to be visible")
	}

	if len(data.FrequencyProfiles) != 1 {
		t.Fatalf("expected 1 frequency profile, got %d", len(data.FrequencyProfiles))
	}

	prof, ok := data.FrequencyProfiles["powersave-performance"]
	if !ok {
		t.Fatalf("expected powersave-performance profile")
	}

	if len(prof.AffectedLogicalCpus) != 2 || prof.AffectedLogicalCpus[0] != 0 || prof.AffectedLogicalCpus[1] != 1 {
		t.Errorf("expected CPUs 0 and 1, got %v", prof.AffectedLogicalCpus)
	}
	if prof.AverageCurrentMhz != 4350 {
		t.Errorf("expected 4350 avg MHz, got %d", prof.AverageCurrentMhz)
	}
	if prof.HardwareMaxMhz != 4500 {
		t.Errorf("expected 4500 max MHz, got %d", prof.HardwareMaxMhz)
	}
	if prof.HardwareMinMhz != 1500 {
		t.Errorf("expected 1500 min MHz, got %d", prof.HardwareMinMhz)
	}

	if data.CStateLimits == nil {
		t.Fatalf("expected CStateLimits to be populated")
	}

	if len(data.CStateLimits.EnabledStates) != 2 {
		t.Errorf("expected 2 enabled states, got %d", len(data.CStateLimits.EnabledStates))
	}
	if len(data.CStateLimits.DisabledStates) != 1 || data.CStateLimits.DisabledStates[0] != "C6" {
		t.Errorf("expected 1 disabled state (C6), got %v", data.CStateLimits.DisabledStates)
	}
}

func TestCpuPowerState_MissingCpufreq(t *testing.T) {
	dir := t.TempDir()
	sysDevicesSystemCpuPath = dir

	tool := &CpuPowerStateTool{}
	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	var data CpuPowerStateData
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	if data.SystemSummary.PowerManagementManagedByOS {
		t.Errorf("expected power_management_managed_by_os to be false")
	}
	if data.SystemSummary.CpufreqDriver != "" {
		t.Errorf("expected empty cpufreq_driver")
	}
	if len(data.FrequencyProfiles) > 0 {
		t.Errorf("expected 0 frequency profiles")
	}
}

func TestCpuPowerState_MissingCpuidle(t *testing.T) {
	dir := t.TempDir()
	sysDevicesSystemCpuPath = dir

	// Only create cpufreq, skip cpuidle
	cpu0Freq := filepath.Join(dir, "cpu0", "cpufreq")
	if err := os.MkdirAll(cpu0Freq, 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(cpu0Freq, "scaling_governor"), []byte("performance\n"), 0644)

	tool := &CpuPowerStateTool{}
	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	var data CpuPowerStateData
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	if !data.SystemSummary.PowerManagementManagedByOS {
		t.Errorf("expected power_management_managed_by_os to be true")
	}
	if data.SystemSummary.CStatesVisible {
		t.Errorf("expected c_states_visible to be false")
	}
	if data.CStateLimits != nil {
		t.Errorf("expected c_state_limits to be nil")
	}
}

func BenchmarkExecute(b *testing.B) {
	dir := setupMockSysfs(&testing.T{})
	sysDevicesSystemCpuPath = dir

	tool := &CpuPowerStateTool{}
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(context.Background(), nil)
	}
}

func BenchmarkNew(b *testing.B) {
	for i := 0; i < b.N; i++ {
		New()
	}
}

func BenchmarkName(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		tool.Name()
	}
}

func BenchmarkDescription(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		tool.Description()
	}
}

func BenchmarkHelp(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		tool.Help()
	}
}

func BenchmarkCategory(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		tool.Category()
	}
}

func BenchmarkParameters(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		tool.Parameters()
	}
}

func BenchmarkHidden(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		tool.Hidden()
	}
}

func BenchmarkIsSupported(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		tool.IsSupported()
	}
}

func BenchmarkMaxSlice(b *testing.B) {
	for i := 0; i < b.N; i++ {
		maxSlice([]int{1, 2, 3})
	}
}

func BenchmarkMinSlice(b *testing.B) {
	for i := 0; i < b.N; i++ {
		minSlice([]int{1, 2, 3})
	}
}

func BenchmarkAvgSlice(b *testing.B) {
	for i := 0; i < b.N; i++ {
		avgSlice([]int{1, 2, 3})
	}
}

func BenchmarkReadKHz(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = readKHz("nonexistent")
	}
}

// --- Enhancement: thermal throttle counters ---

// throttleCPU describes a single fake CPU for setupThrottleSysfs.
type throttleCPU struct {
	id            int
	coreCount     string // core_throttle_count content; "" omits the file
	pkgCount      string // package_throttle_count content; "" omits the file
	pkgID         string // topology/physical_package_id content; "" omits topology
	noThrottleDir bool   // when true, the thermal_throttle dir is not created
}

// setupThrottleSysfs builds a fake sysfs cpu tree containing thermal_throttle
// counters. When withCpufreq is true a minimal cpu0 cpufreq node is created so
// Execute takes the OS-managed path.
func setupThrottleSysfs(t *testing.T, withCpufreq bool, cpus []throttleCPU) string {
	t.Helper()
	dir := t.TempDir()

	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	if withCpufreq {
		write(filepath.Join(dir, "cpu0", "cpufreq", "scaling_governor"), "performance\n")
		write(filepath.Join(dir, "cpu0", "cpufreq", "scaling_driver"), "intel_pstate\n")
	}

	for _, c := range cpus {
		cpuDir := filepath.Join(dir, "cpu"+strconv.Itoa(c.id))
		if err := os.MkdirAll(cpuDir, 0755); err != nil {
			t.Fatal(err)
		}
		if !c.noThrottleDir {
			ttDir := filepath.Join(cpuDir, "thermal_throttle")
			if err := os.MkdirAll(ttDir, 0755); err != nil {
				t.Fatal(err)
			}
			if c.coreCount != "" {
				write(filepath.Join(ttDir, "core_throttle_count"), c.coreCount)
			}
			if c.pkgCount != "" {
				write(filepath.Join(ttDir, "package_throttle_count"), c.pkgCount)
			}
		}
		if c.pkgID != "" {
			write(filepath.Join(cpuDir, "topology", "physical_package_id"), c.pkgID)
		}
	}

	return dir
}

// executeRaw runs the tool and unmarshals the data payload into a generic map
// so tests can assert on the exact wire-format keys.
func executeRaw(t *testing.T, tool *CpuPowerStateTool) (*registry.ToolResult, map[string]interface{}) {
	t.Helper()
	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(res.Data, &raw); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	return res, raw
}

// throttleBlock extracts the thermal_throttle block, failing the test when absent.
func throttleBlock(t *testing.T, raw map[string]interface{}) map[string]interface{} {
	t.Helper()
	blockRaw, ok := raw["thermal_throttle"]
	if !ok {
		t.Fatalf("expected thermal_throttle block in payload, got keys %v", rawKeys(raw))
	}
	block, ok := blockRaw.(map[string]interface{})
	if !ok {
		t.Fatalf("expected thermal_throttle to be an object, got %T", blockRaw)
	}
	return block
}

func rawKeys(raw map[string]interface{}) []string {
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func restoreSysfsRoot(t *testing.T) {
	t.Helper()
	old := sysDevicesSystemCpuPath
	t.Cleanup(func() { sysDevicesSystemCpuPath = old })
}

func TestCpuPowerState_ThermalThrottle_MultiPackage(t *testing.T) {
	restoreSysfsRoot(t)
	sysDevicesSystemCpuPath = setupThrottleSysfs(t, true, []throttleCPU{
		{id: 0, coreCount: "5\n", pkgCount: "2\n", pkgID: "0\n"},
		{id: 1, coreCount: "0\n", pkgCount: "2\n", pkgID: "0\n"},
		{id: 2, coreCount: "3\n", pkgCount: "7\n", pkgID: "1\n"},
		{id: 3, coreCount: "0\n", pkgCount: "7\n", pkgID: "1\n"},
	})

	res, raw := executeRaw(t, New())
	block := throttleBlock(t, raw)

	if got := block["core_events_total"]; got != float64(8) {
		t.Errorf("core_events_total = %v, want 8", got)
	}
	// Both cpus in package 0 report 2 and both cpus in package 1 report 7:
	// the distinct-package sum is 2 + 7 = 9, not 2+2+7+7 = 18.
	if got := block["package_events_total"]; got != float64(9) {
		t.Errorf("package_events_total = %v, want 9", got)
	}
	if got := block["cpus_with_core_events"]; got != float64(2) {
		t.Errorf("cpus_with_core_events = %v, want 2", got)
	}
	if _, ok := block["package_count_approximate"]; ok {
		t.Errorf("package_count_approximate should be omitted when topology is available")
	}

	if res.Status != registry.StatusWarning {
		t.Errorf("Status = %q, want warning (throttle events recorded)", res.Status)
	}
	want := "CPU has thermally throttled since boot (core events: 8, package events: 9)"
	warnings, ok := raw["warning_reasons"].([]interface{})
	if !ok || len(warnings) != 1 || warnings[0] != want {
		t.Errorf("warning_reasons = %v, want [%q]", raw["warning_reasons"], want)
	}
	if res.Summary != want {
		t.Errorf("Summary = %q, want %q", res.Summary, want)
	}
}

func TestCpuPowerState_ThermalThrottle_TopologyAbsentApproximates(t *testing.T) {
	restoreSysfsRoot(t)
	sysDevicesSystemCpuPath = setupThrottleSysfs(t, true, []throttleCPU{
		{id: 0, coreCount: "1\n", pkgCount: "4\n"},
		{id: 1, coreCount: "0\n", pkgCount: "4\n"},
	})

	_, raw := executeRaw(t, New())
	block := throttleBlock(t, raw)

	if got := block["package_events_total"]; got != float64(4) {
		t.Errorf("package_events_total = %v, want 4 (max across cpus)", got)
	}
	if got := block["package_count_approximate"]; got != true {
		t.Errorf("package_count_approximate = %v, want true when topology is absent", got)
	}
	if got := block["core_events_total"]; got != float64(1) {
		t.Errorf("core_events_total = %v, want 1", got)
	}
	if got := block["cpus_with_core_events"]; got != float64(1) {
		t.Errorf("cpus_with_core_events = %v, want 1", got)
	}
}

func TestCpuPowerState_ThermalThrottle_AbsentOmitsBlock(t *testing.T) {
	restoreSysfsRoot(t)
	// The pre-existing mock tree has no thermal_throttle dirs (AMD / VM case).
	sysDevicesSystemCpuPath = setupMockSysfs(t)

	res, raw := executeRaw(t, New())

	if _, ok := raw["thermal_throttle"]; ok {
		t.Errorf("expected thermal_throttle to be omitted entirely, got %v", raw["thermal_throttle"])
	}
	if _, ok := raw["warning_reasons"]; ok {
		t.Errorf("expected no warning_reasons, got %v", raw["warning_reasons"])
	}
	if res.Status != registry.StatusOK {
		t.Errorf("Status = %q, want ok", res.Status)
	}
	if res.Summary != "CPU Power State Data" {
		t.Errorf("Summary = %q, want unchanged legacy summary", res.Summary)
	}
}

func TestCpuPowerState_ThermalThrottle_ZeroEventsNoWarning(t *testing.T) {
	restoreSysfsRoot(t)
	sysDevicesSystemCpuPath = setupThrottleSysfs(t, true, []throttleCPU{
		{id: 0, coreCount: "0\n", pkgCount: "0\n", pkgID: "0\n"},
	})

	res, raw := executeRaw(t, New())
	block := throttleBlock(t, raw)

	if got := block["core_events_total"]; got != float64(0) {
		t.Errorf("core_events_total = %v, want 0", got)
	}
	if got := block["package_events_total"]; got != float64(0) {
		t.Errorf("package_events_total = %v, want 0", got)
	}
	if got := block["cpus_with_core_events"]; got != float64(0) {
		t.Errorf("cpus_with_core_events = %v, want 0", got)
	}
	if res.Status != registry.StatusOK {
		t.Errorf("Status = %q, want ok (zero events must not warn)", res.Status)
	}
	if res.Summary != "CPU Power State Data" {
		t.Errorf("Summary = %q, want unchanged legacy summary", res.Summary)
	}
	if _, ok := raw["warning_reasons"]; ok {
		t.Errorf("expected no warning_reasons, got %v", raw["warning_reasons"])
	}
}

func TestCpuPowerState_ThermalThrottle_UnreadableCountsTreatedAsZero(t *testing.T) {
	restoreSysfsRoot(t)
	sysDevicesSystemCpuPath = setupThrottleSysfs(t, true, []throttleCPU{
		// cpu0: core file missing, package file malformed -> both treated as 0.
		{id: 0, pkgCount: "garbage\n", pkgID: "0\n"},
		// cpu1: package file missing -> 0.
		{id: 1, coreCount: "2\n", pkgID: "0\n"},
	})

	res, raw := executeRaw(t, New())
	block := throttleBlock(t, raw)

	if got := block["core_events_total"]; got != float64(2) {
		t.Errorf("core_events_total = %v, want 2", got)
	}
	if got := block["package_events_total"]; got != float64(0) {
		t.Errorf("package_events_total = %v, want 0 (unreadable files count as 0)", got)
	}
	if got := block["cpus_with_core_events"]; got != float64(1) {
		t.Errorf("cpus_with_core_events = %v, want 1", got)
	}
	if res.Status != registry.StatusWarning {
		t.Errorf("Status = %q, want warning (core events recorded)", res.Status)
	}
}

func TestCpuPowerState_ThermalThrottle_BiosManagedIncludesBlock(t *testing.T) {
	restoreSysfsRoot(t)
	// No cpufreq anywhere: Execute takes the BIOS/Hypervisor-managed early
	// path, but throttle counters are still surfaced when present.
	sysDevicesSystemCpuPath = setupThrottleSysfs(t, false, []throttleCPU{
		{id: 0, coreCount: "0\n", pkgCount: "0\n", pkgID: "0\n"},
	})

	res, raw := executeRaw(t, New())
	block := throttleBlock(t, raw)

	if got := block["core_events_total"]; got != float64(0) {
		t.Errorf("core_events_total = %v, want 0", got)
	}
	if res.Status != registry.StatusOK {
		t.Errorf("Status = %q, want ok", res.Status)
	}
	if res.Summary != "Power management managed by BIOS/Hypervisor" {
		t.Errorf("Summary = %q, want unchanged BIOS-managed summary", res.Summary)
	}

	var data CpuPowerStateData
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	if data.SystemSummary.PowerManagementManagedByOS {
		t.Errorf("expected power_management_managed_by_os to remain false")
	}
}

// setupThrottleSysfsBench builds a minimal throttle tree for benchmarks.
func setupThrottleSysfsBench(b *testing.B) string {
	b.Helper()
	dir := b.TempDir()
	for cpu := 0; cpu < 2; cpu++ {
		ttDir := filepath.Join(dir, "cpu"+strconv.Itoa(cpu), "thermal_throttle")
		if err := os.MkdirAll(ttDir, 0755); err != nil {
			b.Fatal(err)
		}
		topoDir := filepath.Join(dir, "cpu"+strconv.Itoa(cpu), "topology")
		if err := os.MkdirAll(topoDir, 0755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ttDir, "core_throttle_count"), []byte("3\n"), 0644); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ttDir, "package_throttle_count"), []byte("7\n"), 0644); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(topoDir, "physical_package_id"), []byte("0\n"), 0644); err != nil {
			b.Fatal(err)
		}
	}
	return dir
}

func BenchmarkCollectThermalThrottle(b *testing.B) {
	old := sysDevicesSystemCpuPath
	sysDevicesSystemCpuPath = setupThrottleSysfsBench(b)
	defer func() { sysDevicesSystemCpuPath = old }()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		collectThermalThrottle()
	}
}

func BenchmarkReadThrottleCount(b *testing.B) {
	dir := b.TempDir()
	path := filepath.Join(dir, "core_throttle_count")
	if err := os.WriteFile(path, []byte("42\n"), 0644); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = readThrottleCount(path)
	}
}

func BenchmarkApplyThrottleWarning(b *testing.B) {
	for i := 0; i < b.N; i++ {
		data := CpuPowerStateData{ThermalThrottle: &ThermalThrottle{CoreEventsTotal: 5, PackageEventsTotal: 2}}
		_, _ = applyThrottleWarning(&data, "CPU Power State Data")
	}
}
