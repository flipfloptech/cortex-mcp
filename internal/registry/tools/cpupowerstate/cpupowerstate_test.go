package cpupowerstate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
	if tool.Category() != "compute" {
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

func BenchmarkCpuPowerState(b *testing.B) {
	dir := setupMockSysfs(&testing.T{})
	sysDevicesSystemCpuPath = dir

	tool := &CpuPowerStateTool{}
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(context.Background(), nil)
	}
}
