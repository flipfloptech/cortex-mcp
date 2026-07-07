package cpupowerstate

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

var (
	sysDevicesSystemCpuPath = "/sys/devices/system/cpu"
	cpuRegex                = regexp.MustCompile(`^cpu(\d+)$`)
)

func init() {
	registry.Register(New())
}

// New returns a new CpuPowerStateTool.
func New() *CpuPowerStateTool {
	return &CpuPowerStateTool{}
}

// CpuPowerStateTool implements the get_cpu_power_state diagnostic tool.
type CpuPowerStateTool struct{}

// Name returns the unique tool identifier.
func (t *CpuPowerStateTool) Name() string { return "get_cpu_power_state" }

// Description returns a short summary for get_tool_list output.
func (t *CpuPowerStateTool) Description() string {
	return "Get CPU frequency profiles and C-state limits"
}

// Help returns the full tool help text.
func (t *CpuPowerStateTool) Help() string {
	return `get_cpu_power_state — Get CPU Frequency and C-State profiles

Returns a highly aggregated map of CPU frequency profiles (P-states) and 
idle sleep limits (C-states). Groups logical CPUs by their governor and EPP.
Data is sourced purely from sysfs (/sys/devices/system/cpu/).

Outputs hardware limits, average active frequencies, and sleep states. 
Crucial for diagnosing latency spikes or thermal throttling.

Also reports cumulative thermal throttle event counters (Intel-specific,
from cpu*/thermal_throttle/) as a "thermal_throttle" block, warning when
the CPU has thermally throttled since boot. The block is omitted when the
interface is absent (AMD, most VMs).

Parameters: None`
}

// Category returns the tool category.
func (t *CpuPowerStateTool) Category() registry.Category { return registry.CategoryCompute }

// Parameters returns the parameter schema.
func (t *CpuPowerStateTool) Parameters() []registry.ToolParam { return nil }

// Hidden returns whether the tool is hidden from the MCP tool list.
func (t *CpuPowerStateTool) Hidden() bool { return false }

// IsSupported checks if this tool can operate on the current node.
func (t *CpuPowerStateTool) IsSupported() (bool, string) {
	if !registry.IsLinux() {
		return false, "requires Linux"
	}
	return true, ""
}

// SystemSummary holds OS-level power state configuration.
type SystemSummary struct {
	PowerManagementManagedByOS bool   `json:"power_management_managed_by_os"`
	CpufreqDriver              string `json:"cpufreq_driver,omitempty"`
	CpuidleDriver              string `json:"cpuidle_driver,omitempty"`
	CStatesVisible             bool   `json:"c_states_visible"`
}

// FrequencyProfile aggregates properties for a group of CPUs sharing a governor-epp configuration.
type FrequencyProfile struct {
	AffectedLogicalCpus []int  `json:"affected_logical_cpus"`
	EPPPreference       string `json:"epp_preference,omitempty"`
	HardwareMaxMhz      int    `json:"hardware_max_mhz"`
	HardwareMinMhz      int    `json:"hardware_min_mhz"`
	AverageCurrentMhz   int    `json:"average_current_mhz"`
}

// CStateLimits groups idle states globally.
type CStateLimits struct {
	EnabledStates  []string `json:"enabled_states"`
	DisabledStates []string `json:"disabled_states"`
}

// ThermalThrottle aggregates the cumulative thermal throttle event counters
// exposed by the x86 thermal interrupt driver (Intel-specific; absent on AMD
// and most virtual machines). Counters are cumulative since boot.
type ThermalThrottle struct {
	CoreEventsTotal    uint64 `json:"core_events_total"`
	PackageEventsTotal uint64 `json:"package_events_total"`
	CpusWithCoreEvents int    `json:"cpus_with_core_events"`
	// PackageCountApproximate is set when topology/physical_package_id is
	// unavailable and package_events_total is the max counter observed
	// across CPUs instead of a per-package distinct sum.
	PackageCountApproximate bool `json:"package_count_approximate,omitempty"`
}

// CpuPowerStateData is the JSON root struct.
type CpuPowerStateData struct {
	SystemSummary     SystemSummary               `json:"system_summary"`
	FrequencyProfiles map[string]FrequencyProfile `json:"frequency_profiles,omitempty"`
	CStateLimits      *CStateLimits               `json:"c_state_limits,omitempty"`
	ThermalThrottle   *ThermalThrottle            `json:"thermal_throttle,omitempty"`
	WarningReasons    []string                    `json:"warning_reasons,omitempty"`
}

// groupData tracks raw parsing state for calculating averages.
type groupData struct {
	cpus    []int
	epp     string
	curFreq []int
	maxFreq []int
	minFreq []int
}

// Execute gathers P-state and C-state data and returns a standardized result.
func (t *CpuPowerStateTool) Execute(_ context.Context, _ json.RawMessage) (*registry.ToolResult, error) {

	data := CpuPowerStateData{
		FrequencyProfiles: make(map[string]FrequencyProfile),
	}

	// 0. Collect thermal throttle counters (independent of cpufreq visibility;
	// nil when the sysfs interface is absent, e.g. AMD or most VMs).
	data.ThermalThrottle = collectThermalThrottle()

	// 1. Check for cpufreq management via cpu0
	cpu0Freq := filepath.Join(sysDevicesSystemCpuPath, "cpu0", "cpufreq")
	if _, err := os.Stat(cpu0Freq); err != nil {
		data.SystemSummary.PowerManagementManagedByOS = false
		data.SystemSummary.CStatesVisible = false
		status, summary := applyThrottleWarning(&data, "Power management managed by BIOS/Hypervisor")
		return registry.NewResult(t.Name(), status, summary, data), nil
	}
	data.SystemSummary.PowerManagementManagedByOS = true

	// Read cpufreq_driver
	if driver, err := os.ReadFile(filepath.Join(cpu0Freq, "scaling_driver")); err == nil {
		data.SystemSummary.CpufreqDriver = strings.TrimSpace(string(driver))
	}

	// 2. Scan cpufreq for all logical CPUs
	entries, err := os.ReadDir(sysDevicesSystemCpuPath)
	if err == nil {
		groups := make(map[string]*groupData)

		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			matches := cpuRegex.FindStringSubmatch(e.Name())
			if len(matches) != 2 {
				continue
			}
			cpuID, err := strconv.Atoi(matches[1])
			if err != nil {
				continue
			}

			freqDir := filepath.Join(sysDevicesSystemCpuPath, e.Name(), "cpufreq")
			if _, err := os.Stat(freqDir); err != nil {
				continue
			}

			govData, _ := os.ReadFile(filepath.Join(freqDir, "scaling_governor"))
			gov := strings.TrimSpace(string(govData))
			if gov == "" {
				continue
			}

			epp := ""
			if eppData, err := os.ReadFile(filepath.Join(freqDir, "energy_performance_preference")); err == nil {
				epp = strings.TrimSpace(string(eppData))
			}

			key := gov
			if epp != "" {
				key = gov + "-" + epp
			}

			if _, ok := groups[key]; !ok {
				groups[key] = &groupData{epp: epp}
			}
			gd := groups[key]
			gd.cpus = append(gd.cpus, cpuID)

			if cur, err := readKHz(filepath.Join(freqDir, "scaling_cur_freq")); err == nil {
				gd.curFreq = append(gd.curFreq, cur)
			}
			if maxF, err := readKHz(filepath.Join(freqDir, "scaling_max_freq")); err == nil {
				gd.maxFreq = append(gd.maxFreq, maxF)
			}
			if minF, err := readKHz(filepath.Join(freqDir, "scaling_min_freq")); err == nil {
				gd.minFreq = append(gd.minFreq, minF)
			}
		}

		// Calculate averages and bounds
		for key, gd := range groups {
			sort.Ints(gd.cpus)

			prof := FrequencyProfile{
				AffectedLogicalCpus: gd.cpus,
				EPPPreference:       gd.epp,
				HardwareMaxMhz:      maxSlice(gd.maxFreq),
				HardwareMinMhz:      minSlice(gd.minFreq),
				AverageCurrentMhz:   avgSlice(gd.curFreq),
			}
			data.FrequencyProfiles[key] = prof
		}
	}

	// 3. Scan cpuidle for C-state limits (using cpu0)
	cpu0Idle := filepath.Join(sysDevicesSystemCpuPath, "cpu0", "cpuidle")
	if _, err := os.Stat(cpu0Idle); err != nil {
		data.SystemSummary.CStatesVisible = false
	} else {
		data.SystemSummary.CStatesVisible = true

		if driver, err := os.ReadFile(filepath.Join(sysDevicesSystemCpuPath, "cpuidle", "current_driver")); err == nil {
			data.SystemSummary.CpuidleDriver = strings.TrimSpace(string(driver))
		}

		idleEntries, err := os.ReadDir(cpu0Idle)
		if err == nil {
			limits := &CStateLimits{
				EnabledStates:  []string{},
				DisabledStates: []string{},
			}
			for _, e := range idleEntries {
				if !e.IsDir() || !strings.HasPrefix(e.Name(), "state") {
					continue
				}
				stateDir := filepath.Join(cpu0Idle, e.Name())
				nameData, err := os.ReadFile(filepath.Join(stateDir, "name"))
				if err != nil {
					continue
				}
				name := strings.TrimSpace(string(nameData))
				if name == "" {
					continue
				}

				disabled := false
				if disData, err := os.ReadFile(filepath.Join(stateDir, "disable")); err == nil {
					dis := strings.TrimSpace(string(disData))
					disabled = (dis == "1")
				}

				if disabled {
					limits.DisabledStates = append(limits.DisabledStates, name)
				} else {
					limits.EnabledStates = append(limits.EnabledStates, name)
				}
			}
			data.CStateLimits = limits
		}
	}

	status, summary := applyThrottleWarning(&data, "CPU Power State Data")
	return registry.NewResult(t.Name(), status, summary, data), nil
}

// collectThermalThrottle scans <root>/cpu<N>/thermal_throttle counters and
// aggregates them. It returns nil (block omitted) when cpu0 does not expose a
// thermal_throttle directory. Package counters are identical for every CPU in
// a physical package, so they are summed once per distinct package via
// topology/physical_package_id; when topology is unavailable the maximum
// observed counter is reported and flagged as approximate.
func collectThermalThrottle() *ThermalThrottle {
	if _, err := os.Stat(filepath.Join(sysDevicesSystemCpuPath, "cpu0", "thermal_throttle")); err != nil {
		return nil
	}

	entries, err := os.ReadDir(sysDevicesSystemCpuPath)
	if err != nil {
		return nil
	}

	tt := &ThermalThrottle{}
	pkgCounts := make(map[int]uint64)
	topologyOK := true
	var maxPkgCount uint64

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if matches := cpuRegex.FindStringSubmatch(e.Name()); len(matches) != 2 {
			continue
		}
		ttDir := filepath.Join(sysDevicesSystemCpuPath, e.Name(), "thermal_throttle")
		if _, err := os.Stat(ttDir); err != nil {
			continue
		}

		core := readThrottleCount(filepath.Join(ttDir, "core_throttle_count"))
		tt.CoreEventsTotal += core
		if core > 0 {
			tt.CpusWithCoreEvents++
		}

		pkg := readThrottleCount(filepath.Join(ttDir, "package_throttle_count"))
		if pkg > maxPkgCount {
			maxPkgCount = pkg
		}

		pkgIDData, err := os.ReadFile(filepath.Join(sysDevicesSystemCpuPath, e.Name(), "topology", "physical_package_id"))
		if err != nil {
			topologyOK = false
			continue
		}
		pkgID, err := strconv.Atoi(strings.TrimSpace(string(pkgIDData)))
		if err != nil {
			topologyOK = false
			continue
		}
		if pkg > pkgCounts[pkgID] {
			pkgCounts[pkgID] = pkg
		}
	}

	if topologyOK {
		for _, v := range pkgCounts {
			tt.PackageEventsTotal += v
		}
	} else {
		tt.PackageEventsTotal = maxPkgCount
		tt.PackageCountApproximate = true
	}

	return tt
}

// readThrottleCount reads a cumulative throttle counter file. Unreadable or
// malformed files are treated as 0 events.
func readThrottleCount(path string) uint64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// applyThrottleWarning appends a thermal throttle warning to data when any
// throttle events were recorded and returns the result status and summary.
// With no events (or no throttle telemetry) the ok status and the provided
// summary are returned unchanged.
func applyThrottleWarning(data *CpuPowerStateData, okSummary string) (registry.ResultStatus, string) {
	tt := data.ThermalThrottle
	if tt == nil || (tt.CoreEventsTotal == 0 && tt.PackageEventsTotal == 0) {
		return registry.StatusOK, okSummary
	}
	warning := fmt.Sprintf("CPU has thermally throttled since boot (core events: %d, package events: %d)",
		tt.CoreEventsTotal, tt.PackageEventsTotal)
	data.WarningReasons = append(data.WarningReasons, warning)
	return registry.StatusWarning, warning
}

func readKHz(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	khz, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, err
	}
	return khz / 1000, nil // Convert KHz to MHz
}

func maxSlice(s []int) int {
	if len(s) == 0 {
		return 0
	}
	m := s[0]
	for _, v := range s[1:] {
		if v > m {
			m = v
		}
	}
	return m
}

func minSlice(s []int) int {
	if len(s) == 0 {
		return 0
	}
	m := s[0]
	for _, v := range s[1:] {
		if v < m {
			m = v
		}
	}
	return m
}

func avgSlice(s []int) int {
	if len(s) == 0 {
		return 0
	}
	sum := 0
	for _, v := range s {
		sum += v
	}
	return int(math.Round(float64(sum) / float64(len(s))))
}
