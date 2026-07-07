package cpupowerstate

import (
	"context"
	"encoding/json"
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

// CpuPowerStateData is the JSON root struct.
type CpuPowerStateData struct {
	SystemSummary     SystemSummary               `json:"system_summary"`
	FrequencyProfiles map[string]FrequencyProfile `json:"frequency_profiles,omitempty"`
	CStateLimits      *CStateLimits               `json:"c_state_limits,omitempty"`
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

	// 1. Check for cpufreq management via cpu0
	cpu0Freq := filepath.Join(sysDevicesSystemCpuPath, "cpu0", "cpufreq")
	if _, err := os.Stat(cpu0Freq); err != nil {
		data.SystemSummary.PowerManagementManagedByOS = false
		data.SystemSummary.CStatesVisible = false
		return registry.NewResult(t.Name(), registry.StatusOK, "Power management managed by BIOS/Hypervisor", data), nil
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

	return registry.NewResult(t.Name(), registry.StatusOK, "CPU Power State Data", data), nil
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
