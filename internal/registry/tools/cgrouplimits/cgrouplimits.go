package cgrouplimits

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

type SystemSummary struct {
	CgroupVersion string `json:"cgroup_version"`
	IsSupported   bool   `json:"is_supported,omitempty"`
	Message       string `json:"message,omitempty"`
	TargetPID     int    `json:"target_pid,omitempty"`
	CgroupPath    string `json:"cgroup_path,omitempty"`
}

type CPUResourceLimits struct {
	AllowedLogicalCores float64 `json:"allowed_logical_cores"`
	IsUnlimited         bool    `json:"is_unlimited"`
	ThrottlingDetected  bool    `json:"throttling_detected"`
	ThrottledTimeMs     int64   `json:"throttled_time_ms"`
}

type CpusetResourceLimits struct {
	AllowedCPUs      []int `json:"allowed_cpus"`
	AllowedNUMANodes []int `json:"allowed_numa_nodes"`
	IsPinned         bool  `json:"is_pinned"`
}

type MemoryResourceLimits struct {
	LimitMB          int64 `json:"limit_mb"`
	CurrentUsageMB   int64 `json:"current_usage_mb,omitempty"`
	IsUnlimited      bool  `json:"is_unlimited"`
	OOMKillsDetected int64 `json:"oom_kills_detected"`
}

type ResourceLimits struct {
	CPU    CPUResourceLimits    `json:"cpu"`
	Cpuset CpusetResourceLimits `json:"cpuset"`
	Memory MemoryResourceLimits `json:"memory"`
}

type CgroupLimitsResult struct {
	SystemSummary    SystemSummary             `json:"system_summary"`
	ResourceLimits   *ResourceLimits           `json:"resource_limits,omitempty"`
	WorkloadManagers map[string]ResourceLimits `json:"workload_managers,omitempty"`
}

type CgroupLimitsTool struct {
	sysFsCgroupPath string
	procPath        string
}

func New() *CgroupLimitsTool {
	return &CgroupLimitsTool{
		sysFsCgroupPath: "/sys/fs/cgroup",
		procPath:        "/proc",
	}
}

func init() {
	registry.Register(registry.WithCache(10*time.Second, New()))
}

func (t *CgroupLimitsTool) Name() string {
	return "get_cgroup_limits"
}

func (t *CgroupLimitsTool) Category() string {
	return "compute"
}

func (t *CgroupLimitsTool) Help() string {
	return `Provides a targeted readout of Linux control group (cgroup) isolation policies for a specific workload.
Detects artificial CPU throttling, forced NUMA pinning, and cgroup-level Out-Of-Memory (OOM) events.

Targeted Mode (target_pid provided): Reads /proc/[target_pid]/cgroup to get the unified cgroup path and extracts the limits.
Global Mode (target_pid omitted): Scans for high-level workload manager parent slices (e.g., slurm, kubepods, docker) and reports their state.

Data Source: /sys/fs/cgroup/ and /proc/[pid]/cgroup`
}

func (t *CgroupLimitsTool) Description() string {
	return "Read Linux cgroup isolation policies, CPU throttling, and OOM events"
}

func (t *CgroupLimitsTool) Parameters() []registry.ToolParam {
	return []registry.ToolParam{
		{
			Name:        "target_pid",
			Type:        "integer",
			Description: "Specific process ID to inspect for cgroup limits",
			Required:    false,
		},
	}
}

func (t *CgroupLimitsTool) Hidden() bool { return false }

func (t *CgroupLimitsTool) IsSupported() (bool, string) {
	if !registry.IsLinux() {
		return false, "requires Linux"
	}
	if !registry.PathExists(t.sysFsCgroupPath) {
		return false, "/sys/fs/cgroup is missing"
	}
	return true, ""
}

func (t *CgroupLimitsTool) isV2() bool {
	return registry.PathExists(filepath.Join(t.sysFsCgroupPath, "cgroup.controllers"))
}

func (t *CgroupLimitsTool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()
	if !t.isV2() {
		return t.executeFallbackV1(start), nil
	}

	var req struct {
		TargetPID *int `json:"target_pid"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("invalid arguments: %v", err)), nil
		}
	}

	if req.TargetPID != nil {
		return t.executeTargetedMode(start, *req.TargetPID)
	}

	return t.executeGlobalMode(start)
}

func (t *CgroupLimitsTool) executeFallbackV1(start time.Time) *registry.ToolResult {
	out := CgroupLimitsResult{
		SystemSummary: SystemSummary{
			CgroupVersion: "v1",
			IsSupported:   false,
			Message:       "cgroup v1 is not supported by this tool. Only the v2 unified hierarchy is parsed.",
		},
	}

	result := registry.NewResult(
		t.Name(),
		registry.StatusOK,
		"cgroup v1 detected (unsupported)",
		out,
	)
	result.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	return result
}

func (t *CgroupLimitsTool) executeTargetedMode(start time.Time, pid int) (*registry.ToolResult, error) {
	cgroupFile := filepath.Join(t.procPath, strconv.Itoa(pid), "cgroup")
	data, err := os.ReadFile(cgroupFile)
	if err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("failed to read %s: %v", cgroupFile, err)), nil
	}

	cgroupPath := ""
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, ":", 3)
		if len(parts) == 3 && parts[0] == "0" && parts[1] == "" {
			cgroupPath = parts[2]
			break
		}
	}

	if cgroupPath == "" {
		return registry.NewErrorResult(t.Name(), "could not find unified cgroup path (0::) in proc file"), nil
	}

	fullPath := filepath.Join(t.sysFsCgroupPath, cgroupPath)
	limits, err := t.getEffectiveLimits(fullPath)
	if err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("failed to parse limits: %v", err)), nil
	}

	out := CgroupLimitsResult{
		SystemSummary: SystemSummary{
			CgroupVersion: "v2",
			TargetPID:     pid,
			CgroupPath:    cgroupPath,
		},
		ResourceLimits: &limits,
	}

	result := registry.NewResult(
		t.Name(),
		registry.StatusOK,
		fmt.Sprintf("Analyzed targeted cgroup limits for PID %d", pid),
		out,
	)
	result.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	return result, nil
}

func (t *CgroupLimitsTool) executeGlobalMode(start time.Time) (*registry.ToolResult, error) {
	managers := []string{"slurm", "kubepods.slice", "docker", "system.slice/docker.service"}

	out := CgroupLimitsResult{
		SystemSummary: SystemSummary{
			CgroupVersion: "v2",
		},
		WorkloadManagers: make(map[string]ResourceLimits),
	}

	for _, mgr := range managers {
		fullPath := filepath.Join(t.sysFsCgroupPath, mgr)
		if registry.PathExists(fullPath) {
			limits, err := t.getEffectiveLimits(fullPath)
			if err == nil {
				out.WorkloadManagers[mgr] = limits
			}
		}
	}

	msg := "Scanned global workload managers"
	if len(out.WorkloadManagers) == 0 {
		msg = "No workload managers detected"
	}

	result := registry.NewResult(
		t.Name(),
		registry.StatusOK,
		msg,
		out,
	)
	result.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	return result, nil
}

func (t *CgroupLimitsTool) getEffectiveLimits(fullPath string) (ResourceLimits, error) {
	effective := ResourceLimits{}
	effective.CPU.IsUnlimited = true
	effective.Memory.IsUnlimited = true

	current := fullPath
	isLeaf := true

	for registry.PathExists(current) {
		local, _ := t.parseLimits(current)

		// CPU Max
		if !local.CPU.IsUnlimited {
			if effective.CPU.IsUnlimited || local.CPU.AllowedLogicalCores < effective.CPU.AllowedLogicalCores {
				effective.CPU.AllowedLogicalCores = local.CPU.AllowedLogicalCores
				effective.CPU.IsUnlimited = false
			}
		}

		// Throttling
		if local.CPU.ThrottlingDetected {
			effective.CPU.ThrottlingDetected = true
		}
		if local.CPU.ThrottledTimeMs > effective.CPU.ThrottledTimeMs {
			effective.CPU.ThrottledTimeMs = local.CPU.ThrottledTimeMs
		}

		// Memory Max
		if !local.Memory.IsUnlimited {
			if effective.Memory.IsUnlimited || local.Memory.LimitMB < effective.Memory.LimitMB {
				effective.Memory.LimitMB = local.Memory.LimitMB
				effective.Memory.IsUnlimited = false
			}
		}

		if isLeaf {
			effective.Memory.CurrentUsageMB = local.Memory.CurrentUsageMB
		}

		// OOM Kills
		if local.Memory.OOMKillsDetected > effective.Memory.OOMKillsDetected {
			effective.Memory.OOMKillsDetected = local.Memory.OOMKillsDetected
		}

		// Cpuset
		if local.Cpuset.IsPinned && !effective.Cpuset.IsPinned {
			effective.Cpuset = local.Cpuset
		}

		isLeaf = false

		if current == t.sysFsCgroupPath || current == "/" {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		if !strings.HasPrefix(parent, t.sysFsCgroupPath) {
			break
		}
		current = parent
	}
	return effective, nil
}

func (t *CgroupLimitsTool) parseLimits(cgroupDir string) (ResourceLimits, error) {
	var limits ResourceLimits

	// CPU
	cpuMaxPath := filepath.Join(cgroupDir, "cpu.max")
	if data, err := os.ReadFile(cpuMaxPath); err == nil {
		val := strings.TrimSpace(string(data))
		if strings.HasPrefix(val, "max") {
			limits.CPU.IsUnlimited = true
		} else {
			fields := strings.Fields(val)
			if len(fields) >= 2 {
				max, _ := strconv.ParseFloat(fields[0], 64)
				period, _ := strconv.ParseFloat(fields[1], 64)
				if period > 0 {
					limits.CPU.AllowedLogicalCores = max / period
					limits.CPU.IsUnlimited = false
				} else {
					limits.CPU.IsUnlimited = true
				}
			} else {
				limits.CPU.IsUnlimited = true
			}
		}
	} else {
		limits.CPU.IsUnlimited = true
	}

	// CPU Stat
	cpuStatPath := filepath.Join(cgroupDir, "cpu.stat")
	if data, err := os.ReadFile(cpuStatPath); err == nil {
		scanner := bufio.NewScanner(bytes.NewReader(data))
		for scanner.Scan() {
			parts := strings.Fields(scanner.Text())
			if len(parts) == 2 {
				switch parts[0] {
				case "nr_throttled":
					nr, _ := strconv.ParseInt(parts[1], 10, 64)
					if nr > 0 {
						limits.CPU.ThrottlingDetected = true
					}
				case "throttled_usec":
					usec, _ := strconv.ParseInt(parts[1], 10, 64)
					limits.CPU.ThrottledTimeMs = usec / 1000
				}
			}
		}
	}

	// Cpuset
	limits.Cpuset.AllowedCPUs = []int{}
	limits.Cpuset.AllowedNUMANodes = []int{}

	cpusPath := filepath.Join(cgroupDir, "cpuset.cpus")
	if data, err := os.ReadFile(cpusPath); err == nil {
		limits.Cpuset.AllowedCPUs = parseCPUList(string(data))
	}

	memsPath := filepath.Join(cgroupDir, "cpuset.mems")
	if data, err := os.ReadFile(memsPath); err == nil {
		limits.Cpuset.AllowedNUMANodes = parseCPUList(string(data))
	}

	if len(limits.Cpuset.AllowedCPUs) > 0 || len(limits.Cpuset.AllowedNUMANodes) > 0 {
		limits.Cpuset.IsPinned = true
	}

	// Memory
	limits.Memory.IsUnlimited = true
	memMaxPath := filepath.Join(cgroupDir, "memory.max")
	if data, err := os.ReadFile(memMaxPath); err == nil {
		val := strings.TrimSpace(string(data))
		if val != "max" {
			bytes, _ := strconv.ParseInt(val, 10, 64)
			limits.Memory.LimitMB = bytes / (1024 * 1024)
			limits.Memory.IsUnlimited = false
		}
	}

	memCurrentPath := filepath.Join(cgroupDir, "memory.current")
	if data, err := os.ReadFile(memCurrentPath); err == nil {
		val := strings.TrimSpace(string(data))
		bytes, _ := strconv.ParseInt(val, 10, 64)
		limits.Memory.CurrentUsageMB = bytes / (1024 * 1024)
	}

	memEventsPath := filepath.Join(cgroupDir, "memory.events")
	if data, err := os.ReadFile(memEventsPath); err == nil {
		scanner := bufio.NewScanner(bytes.NewReader(data))
		for scanner.Scan() {
			parts := strings.Fields(scanner.Text())
			if len(parts) == 2 {
				if parts[0] == "oom_kill" {
					limits.Memory.OOMKillsDetected, _ = strconv.ParseInt(parts[1], 10, 64)
				}
			}
		}
	}

	return limits, nil
}

func parseCPUList(val string) []int {
	val = strings.TrimSpace(val)
	if val == "" || val == "max" {
		return []int{}
	}

	var result []int
	parts := strings.Split(val, ",")
	for _, part := range parts {
		if strings.Contains(part, "-") {
			rangeParts := strings.Split(part, "-")
			if len(rangeParts) == 2 {
				start, _ := strconv.Atoi(rangeParts[0])
				end, _ := strconv.Atoi(rangeParts[1])
				for i := start; i <= end; i++ {
					result = append(result, i)
				}
			}
		} else {
			num, _ := strconv.Atoi(part)
			result = append(result, num)
		}
	}
	return result
}
