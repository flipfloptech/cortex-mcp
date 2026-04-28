package storage

import "encoding/json"

// BlockTopology is the root container for the storage stack map.
type BlockTopology struct {
	PhysicalDevices []PhysicalDevice `json:"physical_devices"`
	VirtualLayers   VirtualLayers    `json:"virtual_layers"`
	SwapDevices     []SwapDevice     `json:"swap_devices"`
}

// PhysicalDevice represents a real hardware block device.
type PhysicalDevice struct {
	DeviceName string      `json:"device_name"`
	MajorMinor string      `json:"major_minor"`
	Model      string      `json:"model,omitempty"`
	SizeGiB    float64     `json:"size_gib"`
	Transport  string      `json:"transport"`
	Partitions []Partition `json:"partitions"`
}

// Partition represents a child partition of a physical device.
type Partition struct {
	Name    string   `json:"name"`
	SizeGiB float64  `json:"size_gib"`
	Holders []string `json:"holders"`
}

// VirtualLayers groups all virtual block device abstractions.
type VirtualLayers struct {
	MDArrays     []MDArray      `json:"md_arrays"`
	DeviceMapper []DeviceMapper `json:"device_mapper"`
}

// MDArray represents a Linux software RAID array from /proc/mdstat.
type MDArray struct {
	ArrayName  string   `json:"array_name"`
	KernelName string   `json:"kernel_name"`
	Level      string   `json:"level"`
	State      string   `json:"state"`
	Slaves     []string `json:"slaves"`
	MountPoint string   `json:"mount_point,omitempty"`
	Filesystem string   `json:"filesystem,omitempty"`
}

// DeviceMapper represents a Device Mapper target (LVM, DM-Crypt, etc.).
type DeviceMapper struct {
	DMName     string   `json:"dm_name"`
	KernelName string   `json:"kernel_name"`
	Slaves     []string `json:"slaves"`
	MountPoint string   `json:"mount_point,omitempty"`
	Filesystem string   `json:"filesystem,omitempty"`
}

// SwapDevice represents a swap area from /proc/swaps.
type SwapDevice struct {
	Filename string `json:"filename"`
	Type     string `json:"type"`
	SizeKiB  int64  `json:"size_kib"`
	UsedKiB  int64  `json:"used_kib"`
	Priority int    `json:"priority"`
}

// MountEntry represents a parsed line from /proc/self/mountinfo.
type MountEntry struct {
	MajorMinor string
	MountPoint string
	Filesystem string
}

// GetBlockTopology is the top-level orchestrator. Not yet implemented.
func GetBlockTopology(sysfsBase, procBase string) (*BlockTopology, error) {
	_ = json.RawMessage{} // suppress unused import until implementation
	return nil, nil
}
