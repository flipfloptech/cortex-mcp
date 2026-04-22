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
	"context"
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func init() {
	registry.Register(registry.WithCache(5*time.Minute, &SystemInfoTool{}))
}

// SystemInfoTool gathers detailed system information from the local node.
type SystemInfoTool struct{}

// Name returns the unique tool identifier.
func (t *SystemInfoTool) Name() string { return "system_info" }

// Description returns a short summary for list_tools output.
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
	Hostname string                 `json:"hostname"`
	OS       string                 `json:"os"`
	Arch     string                 `json:"arch"`
	CPUs     int                    `json:"cpus"`
	Kernel   string                 `json:"kernel"`
	Distro   string                 `json:"distro,omitempty"`
	Roles    []string               `json:"roles"`
	RoleInfo *registry.NodeRoleInfo `json:"role_info"`
}

// Execute gathers system information and returns a standardized result.
func (t *SystemInfoTool) Execute(_ context.Context, _ json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	hostname, _ := os.Hostname()
	kernel := readKernel()
	distro := readDistro()
	roleInfo := registry.DetectNodeRoles()

	data := systemInfoData{
		Hostname: hostname,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		CPUs:     runtime.NumCPU(),
		Kernel:   kernel,
		Distro:   distro,
		Roles:    roleInfo.Roles(),
		RoleInfo: roleInfo,
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
