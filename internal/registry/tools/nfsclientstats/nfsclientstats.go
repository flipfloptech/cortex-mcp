package nfsclientstats

import (
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

type NFSServer struct {
	Version  string `json:"version"`
	Server   string `json:"server"`
	Port     int    `json:"port"`
	Use      int    `json:"use"`
	Hostname string `json:"hostname"`
}

type NFSVolume struct {
	Version string `json:"version"`
	Device  string `json:"device"`
	FSID    string `json:"fsid"`
	FSCache string `json:"fscache"`
}

type NFSMountStats struct {
	Device      string `json:"device"`
	MountPoint  string `json:"mount_point"`
	FSType      string `json:"fstype"`
	Age         uint64 `json:"age_seconds"`
	Protocol    string `json:"protocol"`
	Sends       uint64 `json:"rpc_sends"`
	Receives    uint64 `json:"rpc_receives"`
	BadXIDs     uint64 `json:"bad_xids"`
	ActiveReqs  uint64 `json:"active_requests"`
	Backlog     uint64 `json:"backlog"`
	MaxSlots    uint64 `json:"max_slots"`
	PendingReqs uint64 `json:"pending_requests"`
}

type NFSClientStatsSummary struct {
	ActiveServers int `json:"active_servers"`
	ActiveVolumes int `json:"active_volumes"`
	ActiveMounts  int `json:"active_mounts"`
	TotalBacklog  int `json:"total_backlog"`
}

type NFSClientStatsData struct {
	Servers []NFSServer           `json:"servers,omitempty"`
	Volumes []NFSVolume           `json:"volumes,omitempty"`
	Mounts  []NFSMountStats       `json:"mounts,omitempty"`
	Summary NFSClientStatsSummary `json:"summary"`
}

type NFSClientStatsTool struct {
	procfsRoot string
}

func New() *NFSClientStatsTool {
	return &NFSClientStatsTool{
		procfsRoot: "/proc",
	}
}

func init() {
	registry.Register(registry.WithCache(10*time.Second, New()))
}

// Name returns the unique tool identifier.
func (t *NFSClientStatsTool) Name() string {
	return "get_nfs_client_stats"
}

// Category returns the tool category.
func (t *NFSClientStatsTool) Category() registry.Category {
	return registry.CategoryStorage
}

// Help returns usage instructions and documentation.
func (t *NFSClientStatsTool) Help() string {
	return `Collects NFS client statistics, including server connections, active volumes, and per-mount RPC backlog/performance metrics.

Exposes version, port, device mapping, and low-level transport latency indicators to diagnose NFS client-side queue congestion.

Data Sources:
- /proc/fs/nfsfs/servers
- /proc/fs/nfsfs/volumes
- /proc/self/mountstats (or /proc/mountstats)`
}

// Description returns a brief one-line summary.
func (t *NFSClientStatsTool) Description() string {
	return "Collect NFS client connection states and RPC backlog metrics"
}

// Parameters returns the parameter schema (none).
func (t *NFSClientStatsTool) Parameters() []registry.ToolParam {
	return nil
}

// Hidden returns false.
func (t *NFSClientStatsTool) Hidden() bool { return false }

// IsSupported checks if NFS client paths or mountstats are available.
func (t *NFSClientStatsTool) IsSupported() (bool, string) {
	if !registry.PathExists(filepath.Join(t.procfsRoot, "self", "mountstats")) &&
		!registry.PathExists(filepath.Join(t.procfsRoot, "mountstats")) {
		return false, "NFS statistics not supported (missing /proc/self/mountstats)"
	}
	return true, ""
}

// Execute performs the tool's operation.
func (t *NFSClientStatsTool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	var data NFSClientStatsData

	// 1. Read /proc/fs/nfsfs/servers if available
	serversPath := filepath.Join(t.procfsRoot, "fs", "nfsfs", "servers")
	if registry.PathExists(serversPath) {
		content, err := os.ReadFile(serversPath)
		if err == nil {
			data.Servers = parseNFSServers(content)
		}
	}

	// 2. Read /proc/fs/nfsfs/volumes if available
	volumesPath := filepath.Join(t.procfsRoot, "fs", "nfsfs", "volumes")
	if registry.PathExists(volumesPath) {
		content, err := os.ReadFile(volumesPath)
		if err == nil {
			data.Volumes = parseNFSVolumes(content)
		}
	}

	// 3. Read /proc/self/mountstats (or /proc/mountstats as fallback)
	mountstatsPath := filepath.Join(t.procfsRoot, "self", "mountstats")
	if !registry.PathExists(mountstatsPath) {
		mountstatsPath = filepath.Join(t.procfsRoot, "mountstats")
	}
	if registry.PathExists(mountstatsPath) {
		content, err := os.ReadFile(mountstatsPath)
		if err == nil {
			data.Mounts = parseNFSMountstats(content)
		}
	}

	// Calculate Summary
	data.Summary.ActiveServers = len(data.Servers)
	data.Summary.ActiveVolumes = len(data.Volumes)
	data.Summary.ActiveMounts = len(data.Mounts)
	for _, m := range data.Mounts {
		data.Summary.TotalBacklog += int(m.Backlog)
	}

	summaryStr := fmt.Sprintf("NFS Connection Summary: Servers: %d, Volumes: %d, Active Mounts: %d, Total Backlog Queue: %d",
		data.Summary.ActiveServers,
		data.Summary.ActiveVolumes,
		data.Summary.ActiveMounts,
		data.Summary.TotalBacklog)

	res := registry.NewResult(t.Name(), registry.StatusOK, summaryStr, data)
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()

	return res, nil
}

func parseNFSServers(data []byte) []NFSServer {
	var list []NFSServer
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "NV") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		port, _ := strconv.Atoi(fields[2])
		use := 0
		hostname := ""
		if len(fields) >= 4 {
			use, _ = strconv.Atoi(fields[3])
		}
		if len(fields) >= 5 {
			hostname = fields[4]
		}
		list = append(list, NFSServer{
			Version:  fields[0],
			Server:   fields[1],
			Port:     port,
			Use:      use,
			Hostname: hostname,
		})
	}
	return list
}

func parseNFSVolumes(data []byte) []NFSVolume {
	var list []NFSVolume
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "NV") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		version := fields[0]
		device := fields[1]
		fsid := ""
		fscache := ""
		if len(fields) >= 6 {
			fsid = fields[4]
			fscache = fields[5]
		} else if len(fields) >= 4 {
			fsid = fields[2]
			fscache = fields[3]
		}
		list = append(list, NFSVolume{
			Version: version,
			Device:  device,
			FSID:    fsid,
			FSCache: fscache,
		})
	}
	return list
}

func parseNFSMountstats(data []byte) []NFSMountStats {
	var list []NFSMountStats
	lines := strings.Split(string(data), "\n")
	var current *NFSMountStats

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "device") {
			if current != nil {
				list = append(list, *current)
				current = nil
			}
			fields := strings.Fields(line)
			if len(fields) >= 8 && (fields[7] == "nfs" || fields[7] == "nfs4") {
				current = &NFSMountStats{
					Device:     fields[1],
					MountPoint: fields[4],
					FSType:     fields[7],
				}
			}
			continue
		}

		if current == nil {
			continue
		}

		if strings.HasPrefix(line, "age:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				age, _ := strconv.ParseUint(fields[1], 10, 64)
				current.Age = age
			}
		} else if strings.HasPrefix(line, "xprt:") {
			fields := strings.Fields(line)
			if len(fields) >= 12 {
				current.Protocol = fields[1]
				sends, _ := strconv.ParseUint(fields[7], 10, 64)
				receives, _ := strconv.ParseUint(fields[8], 10, 64)
				badXIDs, _ := strconv.ParseUint(fields[9], 10, 64)
				activeReqs, _ := strconv.ParseUint(fields[10], 10, 64)
				backlog, _ := strconv.ParseUint(fields[11], 10, 64)
				
				current.Sends = sends
				current.Receives = receives
				current.BadXIDs = badXIDs
				current.ActiveReqs = activeReqs
				current.Backlog = backlog

				if len(fields) >= 14 {
					maxSlots, _ := strconv.ParseUint(fields[12], 10, 64)
					pendingReqs, _ := strconv.ParseUint(fields[13], 10, 64)
					current.MaxSlots = maxSlots
					current.PendingReqs = pendingReqs
				}
			}
		}
	}

	if current != nil {
		list = append(list, *current)
	}

	return list
}
