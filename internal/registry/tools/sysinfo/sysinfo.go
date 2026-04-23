// Package sysinfo provides the system_info tool plugin.
//
// This is the REFERENCE PLUGIN — all future tools should follow
// this exact pattern:
//
//  1. Implement registry.Tool interface
//  2. Register via init()
//  3. Self-detect platform support via IsSupported()
//  4. Return standardized registry.ToolResult from Execute()
//  5. Document filtering methodology in Help()
package sysinfo

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

var (
	procSelfCgroupPath    = "/proc/self/cgroup"
	procSelfMountinfoPath = "/proc/self/mountinfo"
	dockerEnvPath         = "/.dockerenv"
	runContainerEnvPath   = "/run/.containerenv"
	sysVendorPath         = "/sys/class/dmi/id/sys_vendor"
	productNamePath       = "/sys/class/dmi/id/product_name"
	hypervisorTypePath    = "/sys/hypervisor/type"
	infinibandClassPath   = "/sys/class/infiniband"
	mlx5CoreVersionPath   = "/sys/module/mlx5_core/version"
	lustreVersionPath     = "/sys/fs/lustre/version"
	procCmdlinePath       = "/proc/cmdline"
	procStatPath          = "/proc/stat"
	procMeminfoPath       = "/proc/meminfo"
)

func init() {
	registry.Register(registry.WithCache(5*time.Minute, &SystemInfoTool{}))
}

// SystemInfoTool gathers detailed system information from the local node.
type SystemInfoTool struct{}

// Name returns the unique tool identifier.
func (t *SystemInfoTool) Name() string { return "get_system_info" }

// Description returns a short summary for get_tool_list output.
func (t *SystemInfoTool) Description() string {
	return "Get detailed system information for this node"
}

// Help returns the full tool help text.
func (t *SystemInfoTool) Help() string {
	return `system_info — Get Detailed System Information

Returns structured system information for this node including hostname,
OS, architecture, CPU count, kernel version, distro identification,
and detected storage/cluster roles (SFA, MGS, MDS, OSS, Client).

Filtering: Deterministic — all values are read directly from the system.
No heuristics or estimation involved. Values are sourced from:
  - runtime.GOOS, runtime.GOARCH, runtime.NumCPU()
  - os.Hostname()
  - /proc/sys/kernel/osrelease (Linux kernel version)
  - /etc/os-release (Linux distro identification)
  - /sys/module/jsysdd, /sys/class/jsys, etc. (SFA detection)
  - /sys/fs/lustre/mgs/, mdt/, obdfilter/, llite/ (Lustre role detection)

Graceful degradation: if a data source is unavailable (e.g., minimal
container without /etc/os-release), the field is returned as "unknown"
or empty rather than failing. If no storage roles are detected, the
roles field returns ["generic"]. This tool always loads on Linux — it
is a core diagnostic tool that should be available even on stripped systems.

Output format:
  {
    "hostname": "oss1",
    "os": "linux",
    "arch": "amd64",
    "cpus": 64,
    "kernel": "5.14.0-362.el9.x86_64",
    "distro": "Rocky Linux 9.3",
    "roles": ["oss", "sfa"],
    "role_info": { ... }
  }

Parameters: None
Supported on: Linux`
}

// Category returns the tool category.
func (t *SystemInfoTool) Category() string { return "system" }

// Parameters returns the parameter schema (none for system_info).
func (t *SystemInfoTool) Parameters() []registry.ToolParam { return nil }

// Hidden returns false; system_info is a public tool.
func (t *SystemInfoTool) Hidden() bool { return false }

// IsSupported checks if this tool can operate on the current node.
// Only gates on Linux — individual data sources (kernel version, distro)
// degrade gracefully to "unknown" or empty when unavailable.
// This is intentional: system_info is a core diagnostic tool that should
// always be available, even on minimal or containerized Linux systems.
func (t *SystemInfoTool) IsSupported() (bool, string) {
	if !registry.IsLinux() {
		return false, "requires Linux (running on " + runtime.GOOS + ")"
	}
	return true, ""
}

// systemInfoData is the structured output for system_info.
type systemInfoData struct {
	Hostname                string                 `json:"hostname"`
	OS                      string                 `json:"os"`
	Arch                    string                 `json:"arch"`
	CPUs                    int                    `json:"cpus"`
	Kernel                  string                 `json:"kernel"`
	Distro                  string                 `json:"distro,omitempty"`
	Roles                   []string               `json:"roles"`
	RoleInfo                *registry.NodeRoleInfo `json:"role_info"`
	TotalMemoryMB           int64                  `json:"total_memory_mb"`
	BootTime                int64                  `json:"boot_time"`
	KernelCmdline           string                 `json:"kernel_cmdline,omitempty"`
	IsVirtualized           bool                   `json:"is_virtualized"`
	IsContainerized         bool                   `json:"is_containerized"`
	VirtContext             string                 `json:"virt_context,omitempty"`
	HasMellanox             bool                   `json:"has_mellanox"`
	UsingMellanoxEthernet   bool                   `json:"using_mellanox_ethernet"`
	UsingMellanoxInfiniband bool                   `json:"using_mellanox_infiniband"`
	MellanoxVersion         string                 `json:"mellanox_version,omitempty"`
	LustreVersion           string                 `json:"lustre_version,omitempty"`
}

// Execute gathers system information and returns a standardized result.
func (t *SystemInfoTool) Execute(_ context.Context, _ json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	hostname, _ := os.Hostname()
	kernel := readKernel()
	distro := readDistro()
	roleInfo := registry.DetectNodeRoles()

	isVirt, isCont, virtType := detectVirtualization()
	hasEth, hasIB := detectMellanox()
	mlxVersion := readMellanoxVersion(kernel)
	lustreVer := readLustreVersion()
	cmdline := readKernelCmdline()
	totalMem, btime := readNodeScale()

	data := systemInfoData{
		Hostname:                hostname,
		OS:                      runtime.GOOS,
		Arch:                    runtime.GOARCH,
		CPUs:                    runtime.NumCPU(),
		Kernel:                  kernel,
		Distro:                  distro,
		Roles:                   roleInfo.Roles(),
		RoleInfo:                roleInfo,
		TotalMemoryMB:           totalMem,
		BootTime:                btime,
		KernelCmdline:           cmdline,
		IsVirtualized:           isVirt,
		IsContainerized:         isCont,
		VirtContext:             virtType,
		HasMellanox:             mlxVersion != "",
		UsingMellanoxEthernet:   hasEth,
		UsingMellanoxInfiniband: hasIB,
		MellanoxVersion:         mlxVersion,
		LustreVersion:           lustreVer,
	}

	result := registry.NewResult(
		t.Name(),
		hostname, // nodeID is the hostname for local execution
		registry.StatusOK,
		hostname+" — "+distro+" ("+kernel+")",
		data,
	)
	result.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	result.Metadata.FilteringMethod = "deterministic"

	return result, nil
}

// readKernel reads the kernel version from /proc/sys/kernel/osrelease.
func readKernel() string {
	data, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(data))
}

// readDistro reads the distro identification from /etc/os-release.
func readDistro() string {
	release := registry.ReadOSRelease()
	if name, ok := release["PRETTY_NAME"]; ok {
		return name
	}
	if id, ok := release["ID"]; ok {
		return id
	}
	return ""
}

// detectVirtualization implements the bulletproof container/VM detection waterfall.
func detectVirtualization() (isVirt bool, isCont bool, virtType string) {
	// Layer 1: /proc/self/cgroup
	if data, err := os.ReadFile(procSelfCgroupPath); err == nil {
		content := string(data)
		if strings.Contains(content, "docker") {
			return true, true, "docker"
		}
		if strings.Contains(content, "kubepods") {
			return true, true, "kubepods"
		}
		if strings.Contains(content, "lxc") {
			return true, true, "lxc"
		}
		if strings.Contains(content, "containerd") {
			return true, true, "containerd"
		}
	}

	// Layer 2: /proc/self/mountinfo
	if data, err := os.ReadFile(procSelfMountinfoPath); err == nil {
		scanner := bufio.NewScanner(strings.NewReader(string(data)))
		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, " / ") {
				if strings.Contains(line, "overlay") || strings.Contains(line, "aufs") || strings.Contains(line, "shiftfs") {
					return true, true, "overlay"
				}
			}
		}
	}

	// Layer 3: Environment Breadcrumbs
	if _, err := os.Stat(dockerEnvPath); err == nil {
		return true, true, "docker"
	}
	if _, err := os.Stat(runContainerEnvPath); err == nil {
		return true, true, "podman"
	}

	// Virtual Machine Waterfall
	// Layer 1: DMI Strings
	if data, err := os.ReadFile(sysVendorPath); err == nil {
		vendor := strings.TrimSpace(string(data))
		vendorLower := strings.ToLower(vendor)
		if strings.Contains(vendorLower, "qemu") || strings.Contains(vendorLower, "vmware") || strings.Contains(vendorLower, "microsoft") || strings.Contains(vendorLower, "xen") {
			return true, false, vendor
		}
	}

	if data, err := os.ReadFile(productNamePath); err == nil {
		product := strings.TrimSpace(string(data))
		prodLower := strings.ToLower(product)
		if strings.Contains(prodLower, "virtualbox") || strings.Contains(prodLower, "kvm") || strings.Contains(prodLower, "amazon ec2") {
			return true, false, product
		}
	}

	// Layer 2: Hypervisor Sysfs
	if data, err := os.ReadFile(hypervisorTypePath); err == nil {
		htype := strings.TrimSpace(string(data))
		if htype != "" {
			return true, false, htype
		}
	}

	return false, false, "bare-metal"
}

// detectMellanox checks for the presence of Ethernet or Infiniband link layers on mlx5 devices.
func detectMellanox() (hasEth bool, hasIB bool) {
	devices, err := os.ReadDir(infinibandClassPath)
	if err != nil {
		return false, false
	}
	for _, dev := range devices {
		if !strings.HasPrefix(dev.Name(), "mlx5_") {
			continue
		}
		portsDir := filepath.Join(infinibandClassPath, dev.Name(), "ports")
		ports, err := os.ReadDir(portsDir)
		if err != nil {
			continue
		}
		for _, port := range ports {
			linkLayerPath := filepath.Join(portsDir, port.Name(), "link_layer")
			data, err := os.ReadFile(linkLayerPath)
			if err != nil {
				continue
			}
			layer := strings.TrimSpace(string(data))
			if strings.EqualFold(layer, "Ethernet") {
				hasEth = true
			} else if strings.EqualFold(layer, "InfiniBand") {
				hasIB = true
			}
		}
	}
	return hasEth, hasIB
}

// readMellanoxVersion reads the MOFED/upstream driver version and formats it appropriately.
func readMellanoxVersion(kernelVersion string) string {
	data, err := os.ReadFile(mlx5CoreVersionPath)
	if err != nil {
		return ""
	}
	ver := strings.TrimSpace(string(data))
	if ver == "" {
		return ""
	}
	if ver == kernelVersion {
		return ver + " (Upstream)"
	}
	return ver + " (MOFED)"
}

// readLustreVersion reads the installed Lustre/Exascaler version.
func readLustreVersion() string {
	data, err := os.ReadFile(lustreVersionPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// readKernelCmdline returns the kernel boot parameters.
func readKernelCmdline() string {
	data, err := os.ReadFile(procCmdlinePath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// readNodeScale returns the total memory in MB and boot time in seconds since epoch.
func readNodeScale() (totalMemMB int64, bootTime int64) {
	if memData, err := os.ReadFile(procMeminfoPath); err == nil {
		scanner := bufio.NewScanner(strings.NewReader(string(memData)))
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "MemTotal:") {
				parts := strings.Fields(line)
				if len(parts) >= 2 {
					if kb, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
						totalMemMB = kb / 1024
					}
				}
				break
			}
		}
	}

	if statData, err := os.ReadFile(procStatPath); err == nil {
		scanner := bufio.NewScanner(strings.NewReader(string(statData)))
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "btime ") {
				parts := strings.Fields(line)
				if len(parts) >= 2 {
					if bt, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
						bootTime = bt
					}
				}
				break
			}
		}
	}

	return totalMemMB, bootTime
}
