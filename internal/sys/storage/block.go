package storage

import (
	"bufio"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

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

// GetBlockTopology constructs the full storage stack topology by
// aggregating data from sysfs and procfs.
func GetBlockTopology(sysfsBase, procBase string) (*BlockTopology, error) {
	mounts := parseMountInfo(procBase)

	topo := &BlockTopology{
		PhysicalDevices: discoverPhysicalDevices(sysfsBase),
		VirtualLayers: VirtualLayers{
			MDArrays:     enrichMDArrays(sysfsBase, procBase, mounts),
			DeviceMapper: discoverDeviceMapper(sysfsBase, mounts),
		},
		SwapDevices: parseSwaps(procBase),
	}

	return topo, nil
}

// isPhysicalDevice returns true if the block device represents real hardware.
// A device is physical if /sys/class/block/<dev>/device exists and it is not
// a known virtual device type.
func isPhysicalDevice(sysfsBase, name string) bool {
	// Exclude known virtual prefixes.
	for _, prefix := range []string{"dm-", "md", "loop", "ram", "zram", "nbd"} {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}
	// A device is physical if the "device" symlink exists.
	devicePath := filepath.Join(sysfsBase, "class/block", name, "device")
	info, err := os.Stat(devicePath)
	if err != nil {
		return false
	}
	return info.IsDir()
}

// isPartition returns true if the block device entry is a partition.
func isPartition(sysfsBase, name string) bool {
	partFile := filepath.Join(sysfsBase, "class/block", name, "partition")
	_, err := os.Stat(partFile)
	return err == nil
}

// discoverPhysicalDevices walks /sys/class/block/ and returns only
// real hardware devices with their partitions.
func discoverPhysicalDevices(sysfsBase string) []PhysicalDevice {
	blockDir := filepath.Join(sysfsBase, "class/block")
	entries, err := os.ReadDir(blockDir)
	if err != nil {
		return nil
	}

	var devices []PhysicalDevice
	for _, entry := range entries {
		name := entry.Name()
		if !isPhysicalDevice(sysfsBase, name) {
			continue
		}
		if isPartition(sysfsBase, name) {
			continue
		}

		dev := PhysicalDevice{
			DeviceName: name,
			MajorMinor: readMajorMinor(sysfsBase, name),
			Model:      readModel(sysfsBase, name),
			SizeGiB:    readSizeGiB(sysfsBase, name),
			Transport:  detectTransport(sysfsBase, name),
			Partitions: discoverPartitions(sysfsBase, name),
		}
		devices = append(devices, dev)
	}

	sort.Slice(devices, func(i, j int) bool {
		return devices[i].DeviceName < devices[j].DeviceName
	})

	return devices
}

// discoverPartitions finds child partitions of a parent device.
func discoverPartitions(sysfsBase, parentDev string) []Partition {
	blockDir := filepath.Join(sysfsBase, "class/block")
	entries, err := os.ReadDir(blockDir)
	if err != nil {
		return nil
	}

	var parts []Partition
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, parentDev) || name == parentDev {
			continue
		}
		if !isPartition(sysfsBase, name) {
			continue
		}

		parts = append(parts, Partition{
			Name:    name,
			SizeGiB: readSizeGiB(sysfsBase, name),
			Holders: readHolders(sysfsBase, name),
		})
	}

	sort.Slice(parts, func(i, j int) bool {
		return parts[i].Name < parts[j].Name
	})

	return parts
}

// readMajorMinor reads the dev file containing "major:minor".
func readMajorMinor(sysfsBase, devName string) string {
	path := filepath.Join(sysfsBase, "class/block", devName, "dev")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// readModel reads the device model string.
func readModel(sysfsBase, devName string) string {
	path := filepath.Join(sysfsBase, "class/block", devName, "device/model")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// readSizeGiB reads the size file (in 512-byte sectors) and converts to GiB.
func readSizeGiB(sysfsBase, devName string) float64 {
	path := filepath.Join(sysfsBase, "class/block", devName, "size")
	data, err := os.ReadFile(path)
	if err != nil {
		return 0.0
	}
	sectors, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0.0
	}
	return sectorsToGiB(sectors)
}

// sectorsToGiB converts 512-byte sector count to GiB, rounded to 1 decimal.
func sectorsToGiB(sectors int64) float64 {
	gib := float64(sectors) * 512.0 / (1024.0 * 1024.0 * 1024.0)
	return math.Round(gib*10) / 10
}

// detectTransport determines the bus type for a block device.
func detectTransport(sysfsBase, devName string) string {
	// NVMe devices are always PCIe.
	if strings.HasPrefix(devName, "nvme") {
		return "pcie"
	}

	// Follow the device symlink to inspect the bus path.
	deviceLink := filepath.Join(sysfsBase, "class/block", devName, "device")
	target, err := os.Readlink(deviceLink)
	if err != nil {
		return "unknown"
	}

	switch {
	case strings.Contains(target, "virtio"):
		return "virtio"
	case strings.Contains(target, "usb"):
		return "usb"
	case strings.Contains(target, "/ata"):
		return "sata"
	case strings.Contains(target, "scsi"):
		return "sas"
	}

	return "unknown"
}

// readHolders lists the holder devices of a block device.
func readHolders(sysfsBase, devName string) []string {
	path := filepath.Join(sysfsBase, "class/block", devName, "holders")
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil
	}
	var holders []string
	for _, e := range entries {
		holders = append(holders, e.Name())
	}
	sort.Strings(holders)
	return holders
}

// readSlaves lists the slave (backing) devices of a virtual block device.
func readSlaves(sysfsBase, devName string) []string {
	path := filepath.Join(sysfsBase, "class/block", devName, "slaves")
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil
	}
	var slaves []string
	for _, e := range entries {
		slaves = append(slaves, e.Name())
	}
	sort.Strings(slaves)
	return slaves
}

// parseMountInfo parses /proc/self/mountinfo and returns a map keyed by
// "major:minor" to MountEntry.
func parseMountInfo(procBase string) map[string]MountEntry {
	path := filepath.Join(procBase, "self/mountinfo")
	f, err := os.Open(path)
	if err != nil {
		return make(map[string]MountEntry)
	}
	defer f.Close()

	mounts := make(map[string]MountEntry)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// Format: mountID parentID major:minor root mountPoint options... - fsType source superOpts
		fields := strings.Fields(line)
		if len(fields) < 7 {
			continue
		}
		majorMinor := fields[2]
		mountPoint := fields[4]

		// Find the separator "-" to get filesystem type.
		sepIdx := -1
		for i, f := range fields {
			if f == "-" {
				sepIdx = i
				break
			}
		}
		fsType := ""
		if sepIdx >= 0 && sepIdx+1 < len(fields) {
			fsType = fields[sepIdx+1]
		}

		mounts[majorMinor] = MountEntry{
			MajorMinor: majorMinor,
			MountPoint: mountPoint,
			Filesystem: fsType,
		}
	}

	return mounts
}

// parseSwaps parses /proc/swaps into a slice of SwapDevice.
func parseSwaps(procBase string) []SwapDevice {
	path := filepath.Join(procBase, "swaps")
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var swaps []SwapDevice
	scanner := bufio.NewScanner(f)
	first := true
	for scanner.Scan() {
		if first {
			first = false
			continue // skip header
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) < 5 {
			continue
		}
		size, _ := strconv.ParseInt(fields[2], 10, 64)
		used, _ := strconv.ParseInt(fields[3], 10, 64)
		prio, _ := strconv.Atoi(fields[4])

		swaps = append(swaps, SwapDevice{
			Filename: fields[0],
			Type:     fields[1],
			SizeKiB:  size,
			UsedKiB:  used,
			Priority: prio,
		})
	}

	return swaps
}

// mdLineRegex matches mdstat array lines like: md0 : active raid1 sdb1[1] sda1[0]
var mdLineRegex = regexp.MustCompile(`^(md\d+)\s*:\s*(\w+)\s+(\w+)\s+(.*)$`)

// mdDriveRegex extracts drive names from mdstat component notation: sda1[0]
var mdDriveRegex = regexp.MustCompile(`(\w+)\[\d+\]`)

// mdStatusRegex matches the bitmap status line: [2/1] [U_]
var mdStatusRegex = regexp.MustCompile(`\[(\d+)/(\d+)\]\s+\[([U_]+)\]`)

// parseMDStat parses /proc/mdstat to extract MD array information.
func parseMDStat(procBase string) []MDArray {
	path := filepath.Join(procBase, "mdstat")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	lines := strings.Split(string(data), "\n")
	var arrays []MDArray

	for i := 0; i < len(lines); i++ {
		matches := mdLineRegex.FindStringSubmatch(lines[i])
		if matches == nil {
			continue
		}

		arrayName := matches[1]
		level := matches[3]

		// Extract slave drives from the component list.
		componentStr := matches[4]
		driveMatches := mdDriveRegex.FindAllStringSubmatch(componentStr, -1)
		var slaves []string
		for _, dm := range driveMatches {
			slaves = append(slaves, dm[1])
		}
		sort.Strings(slaves)

		// Determine state from the next line's bitmap.
		state := "active"
		if i+1 < len(lines) {
			statusMatches := mdStatusRegex.FindStringSubmatch(lines[i+1])
			if statusMatches != nil {
				total, _ := strconv.Atoi(statusMatches[1])
				active, _ := strconv.Atoi(statusMatches[2])
				if active < total {
					state = "degraded"
				}
			}
		}

		arrays = append(arrays, MDArray{
			ArrayName:  arrayName,
			KernelName: arrayName,
			Level:      level,
			State:      state,
			Slaves:     slaves,
		})
	}

	return arrays
}

// enrichMDArrays combines parseMDStat with sysfs slave data and mount info.
func enrichMDArrays(sysfsBase, procBase string, mounts map[string]MountEntry) []MDArray {
	arrays := parseMDStat(procBase)
	for i, a := range arrays {
		// Enrich with sysfs slaves if available.
		sysSlaves := readSlaves(sysfsBase, a.KernelName)
		if len(sysSlaves) > 0 {
			arrays[i].Slaves = sysSlaves
		}

		// Enrich with mount info.
		mm := readMajorMinor(sysfsBase, a.KernelName)
		if entry, ok := mounts[mm]; ok {
			arrays[i].MountPoint = entry.MountPoint
			arrays[i].Filesystem = entry.Filesystem
		}
	}
	return arrays
}

// discoverDeviceMapper finds all dm-* devices and resolves their
// names, slaves, and mount points.
func discoverDeviceMapper(sysfsBase string, mounts map[string]MountEntry) []DeviceMapper {
	blockDir := filepath.Join(sysfsBase, "class/block")
	entries, err := os.ReadDir(blockDir)
	if err != nil {
		return nil
	}

	var dms []DeviceMapper
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "dm-") {
			continue
		}

		dmNamePath := filepath.Join(blockDir, name, "dm/name")
		dmNameData, err := os.ReadFile(dmNamePath)
		dmName := name
		if err == nil {
			dmName = strings.TrimSpace(string(dmNameData))
		}

		dm := DeviceMapper{
			DMName:     dmName,
			KernelName: name,
			Slaves:     readSlaves(sysfsBase, name),
		}

		// Stitch mount info via major:minor.
		mm := readMajorMinor(sysfsBase, name)
		if entry, ok := mounts[mm]; ok {
			dm.MountPoint = entry.MountPoint
			dm.Filesystem = entry.Filesystem
		}

		dms = append(dms, dm)
	}

	sort.Slice(dms, func(i, j int) bool {
		return dms[i].KernelName < dms[j].KernelName
	})

	return dms
}
