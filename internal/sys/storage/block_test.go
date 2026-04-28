package storage

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

// --- Helpers for synthetic sysfs ---

func mkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdirAll %s: %v", path, err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	mkdirAll(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeFile %s: %v", path, err)
	}
}

// setupNVMeDevice creates a synthetic NVMe device in sysfs.
func setupNVMeDevice(t *testing.T, sysfs string) {
	t.Helper()
	dev := filepath.Join(sysfs, "class/block/nvme0n1")
	mkdirAll(t, filepath.Join(dev, "device"))
	writeFile(t, filepath.Join(dev, "dev"), "259:0\n")
	writeFile(t, filepath.Join(dev, "size"), "1953525168\n") // ~931.5 GiB
	writeFile(t, filepath.Join(dev, "device/model"), "Samsung SSD 980 PRO  \n")

	// Partition
	part := filepath.Join(sysfs, "class/block/nvme0n1p1")
	mkdirAll(t, filepath.Join(part, "device"))
	writeFile(t, filepath.Join(part, "dev"), "259:1\n")
	writeFile(t, filepath.Join(part, "size"), "1953523712\n")
	writeFile(t, filepath.Join(part, "partition"), "1\n")
	mkdirAll(t, filepath.Join(part, "holders/dm-0"))

	// Link partition back to parent
	mkdirAll(t, filepath.Join(dev, "nvme0n1p1"))
	writeFile(t, filepath.Join(dev, "nvme0n1p1/partition"), "1\n")
}

// setupSATADevice creates a synthetic SATA device.
func setupSATADevice(t *testing.T, sysfs string) {
	t.Helper()
	dev := filepath.Join(sysfs, "class/block/sda")
	mkdirAll(t, filepath.Join(dev, "device"))
	writeFile(t, filepath.Join(dev, "dev"), "8:0\n")
	writeFile(t, filepath.Join(dev, "size"), "976773168\n") // ~465.8 GiB
	writeFile(t, filepath.Join(dev, "device/model"), "WDC WD5000AAKX\n")
}

// setupVirtualDevices creates dm and loop devices that should be filtered.
func setupVirtualDevices(t *testing.T, sysfs string) {
	t.Helper()
	for _, name := range []string{"dm-0", "loop0", "loop1"} {
		dev := filepath.Join(sysfs, "class/block", name)
		mkdirAll(t, dev)
		writeFile(t, filepath.Join(dev, "dev"), "253:0\n")
		writeFile(t, filepath.Join(dev, "size"), "1000000\n")
	}
	// dm-0 gets a dm/name
	writeFile(t, filepath.Join(sysfs, "class/block/dm-0/dm/name"), "vg_sys-lv_root\n")
	// dm-0 has slaves
	mkdirAll(t, filepath.Join(sysfs, "class/block/dm-0/slaves/nvme0n1p1"))
}

func TestReadMajorMinor(t *testing.T) {
	t.Parallel()
	sysfs := t.TempDir()
	writeFile(t, filepath.Join(sysfs, "class/block/sda/dev"), "8:0\n")

	got := readMajorMinor(sysfs, "sda")
	if got != "8:0" {
		t.Errorf("readMajorMinor = %q, want %q", got, "8:0")
	}
}

func TestReadMajorMinor_Missing(t *testing.T) {
	t.Parallel()
	got := readMajorMinor(t.TempDir(), "nonexistent")
	if got != "" {
		t.Errorf("readMajorMinor missing = %q, want %q", got, "")
	}
}

func TestSizeCalculation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		sectors int64
		want    float64
	}{
		{"standard_nvme", 1953525168, 931.5},
		{"zero_sectors", 0, 0.0},
		{"small_partition", 2048, 0.0}, // 1 MiB rounds to 0.0 at 1 decimal
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := sectorsToGiB(tt.sectors)
			if math.Abs(got-tt.want) > 0.15 {
				t.Errorf("sectorsToGiB(%d) = %.1f, want ~%.1f", tt.sectors, got, tt.want)
			}
		})
	}
}

func TestDetectTransport(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		devName string
		want    string
	}{
		{"nvme_device", "nvme0n1", "pcie"},
		{"nvme_partition", "nvme0n1p1", "pcie"},
		{"unknown_device", "xvda", "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := detectTransport(t.TempDir(), tt.devName)
			if got != tt.want {
				t.Errorf("detectTransport(%q) = %q, want %q", tt.devName, got, tt.want)
			}
		})
	}
}

func TestDiscoverPhysicalDevices_NVMe(t *testing.T) {
	t.Parallel()
	sysfs := t.TempDir()
	setupNVMeDevice(t, sysfs)
	setupVirtualDevices(t, sysfs)

	devs := discoverPhysicalDevices(sysfs)
	if len(devs) != 1 {
		t.Fatalf("expected 1 physical device, got %d", len(devs))
	}
	d := devs[0]
	if d.DeviceName != "nvme0n1" {
		t.Errorf("DeviceName = %q, want %q", d.DeviceName, "nvme0n1")
	}
	if d.MajorMinor != "259:0" {
		t.Errorf("MajorMinor = %q, want %q", d.MajorMinor, "259:0")
	}
	if d.Transport != "pcie" {
		t.Errorf("Transport = %q, want %q", d.Transport, "pcie")
	}
	if d.Model != "Samsung SSD 980 PRO" {
		t.Errorf("Model = %q, want %q", d.Model, "Samsung SSD 980 PRO")
	}
	if len(d.Partitions) != 1 {
		t.Fatalf("expected 1 partition, got %d", len(d.Partitions))
	}
	if d.Partitions[0].Name != "nvme0n1p1" {
		t.Errorf("Partition Name = %q, want %q", d.Partitions[0].Name, "nvme0n1p1")
	}
	if len(d.Partitions[0].Holders) != 1 || d.Partitions[0].Holders[0] != "dm-0" {
		t.Errorf("Partition Holders = %v, want [dm-0]", d.Partitions[0].Holders)
	}
}

func TestDiscoverPhysicalDevices_SATA(t *testing.T) {
	t.Parallel()
	sysfs := t.TempDir()
	setupSATADevice(t, sysfs)

	devs := discoverPhysicalDevices(sysfs)
	if len(devs) != 1 {
		t.Fatalf("expected 1 physical device, got %d", len(devs))
	}
	if devs[0].DeviceName != "sda" {
		t.Errorf("DeviceName = %q, want %q", devs[0].DeviceName, "sda")
	}
}

func TestDiscoverPhysicalDevices_FiltersVirtual(t *testing.T) {
	t.Parallel()
	sysfs := t.TempDir()
	setupVirtualDevices(t, sysfs)

	devs := discoverPhysicalDevices(sysfs)
	for _, d := range devs {
		t.Errorf("virtual device %q leaked into physical devices", d.DeviceName)
	}
}

func TestParseMountInfo(t *testing.T) {
	t.Parallel()
	proc := t.TempDir()
	content := `22 1 259:2 / / rw,relatime shared:1 - ext4 /dev/nvme0n1p2 rw
30 22 0:26 / /sys rw,nosuid,nodev,noexec,relatime shared:7 - sysfs sysfs rw
31 22 253:0 / /home rw,relatime shared:15 - xfs /dev/mapper/vg-home rw
`
	writeFile(t, filepath.Join(proc, "self/mountinfo"), content)

	mounts := parseMountInfo(proc)
	if len(mounts) == 0 {
		t.Fatal("expected mount entries, got 0")
	}
	entry, ok := mounts["259:2"]
	if !ok {
		t.Fatal("missing mount entry for 259:2")
	}
	if entry.MountPoint != "/" {
		t.Errorf("MountPoint = %q, want %q", entry.MountPoint, "/")
	}
	if entry.Filesystem != "ext4" {
		t.Errorf("Filesystem = %q, want %q", entry.Filesystem, "ext4")
	}
	entry2, ok := mounts["253:0"]
	if !ok {
		t.Fatal("missing mount entry for 253:0")
	}
	if entry2.MountPoint != "/home" {
		t.Errorf("MountPoint = %q, want %q", entry2.MountPoint, "/home")
	}
}

func TestParseMountInfo_Missing(t *testing.T) {
	t.Parallel()
	mounts := parseMountInfo(t.TempDir())
	if len(mounts) != 0 {
		t.Errorf("expected empty map for missing mountinfo, got %d entries", len(mounts))
	}
}

func TestParseSwaps(t *testing.T) {
	t.Parallel()
	proc := t.TempDir()
	content := `Filename				Type		Size		Used		Priority
/dev/dm-1                               partition	8388604		0		-2
`
	writeFile(t, filepath.Join(proc, "swaps"), content)

	swaps := parseSwaps(proc)
	if len(swaps) != 1 {
		t.Fatalf("expected 1 swap, got %d", len(swaps))
	}
	s := swaps[0]
	if s.Filename != "/dev/dm-1" {
		t.Errorf("Filename = %q, want %q", s.Filename, "/dev/dm-1")
	}
	if s.SizeKiB != 8388604 {
		t.Errorf("SizeKiB = %d, want %d", s.SizeKiB, 8388604)
	}
	if s.Priority != -2 {
		t.Errorf("Priority = %d, want %d", s.Priority, -2)
	}
}

func TestParseSwaps_Missing(t *testing.T) {
	t.Parallel()
	swaps := parseSwaps(t.TempDir())
	if len(swaps) != 0 {
		t.Errorf("expected empty swaps for missing file, got %d", len(swaps))
	}
}

func TestParseMDStat_ActiveArray(t *testing.T) {
	t.Parallel()
	proc := t.TempDir()
	content := `Personalities : [raid1]
md0 : active raid1 sdb1[1] sda1[0]
      976630464 blocks super 1.2 [2/2] [UU]

unused devices: <none>
`
	writeFile(t, filepath.Join(proc, "mdstat"), content)

	arrays := parseMDStat(proc)
	if len(arrays) != 1 {
		t.Fatalf("expected 1 array, got %d", len(arrays))
	}
	a := arrays[0]
	if a.ArrayName != "md0" {
		t.Errorf("ArrayName = %q, want %q", a.ArrayName, "md0")
	}
	if a.Level != "raid1" {
		t.Errorf("Level = %q, want %q", a.Level, "raid1")
	}
	if a.State != "active" {
		t.Errorf("State = %q, want %q", a.State, "active")
	}
	if len(a.Slaves) != 2 {
		t.Errorf("Slaves = %v, want 2 entries", a.Slaves)
	}
}

func TestParseMDStat_DegradedArray(t *testing.T) {
	t.Parallel()
	proc := t.TempDir()
	content := `Personalities : [raid1]
md0 : active raid1 sda1[0]
      976630464 blocks super 1.2 [2/1] [U_]

unused devices: <none>
`
	writeFile(t, filepath.Join(proc, "mdstat"), content)

	arrays := parseMDStat(proc)
	if len(arrays) != 1 {
		t.Fatalf("expected 1 array, got %d", len(arrays))
	}
	if arrays[0].State != "degraded" {
		t.Errorf("State = %q, want %q", arrays[0].State, "degraded")
	}
}

func TestParseMDStat_Empty(t *testing.T) {
	t.Parallel()
	proc := t.TempDir()
	content := `Personalities :
unused devices: <none>
`
	writeFile(t, filepath.Join(proc, "mdstat"), content)

	arrays := parseMDStat(proc)
	if len(arrays) != 0 {
		t.Errorf("expected 0 arrays, got %d", len(arrays))
	}
}

func TestParseMDStat_Missing(t *testing.T) {
	t.Parallel()
	arrays := parseMDStat(t.TempDir())
	if len(arrays) != 0 {
		t.Errorf("expected 0 arrays for missing file, got %d", len(arrays))
	}
}

func TestDiscoverDeviceMapper(t *testing.T) {
	t.Parallel()
	sysfs := t.TempDir()
	setupNVMeDevice(t, sysfs)
	setupVirtualDevices(t, sysfs)

	mounts := map[string]MountEntry{
		"253:0": {MajorMinor: "253:0", MountPoint: "/", Filesystem: "ext4"},
	}

	dms := discoverDeviceMapper(sysfs, mounts)
	if len(dms) != 1 {
		t.Fatalf("expected 1 dm device, got %d", len(dms))
	}
	dm := dms[0]
	if dm.DMName != "vg_sys-lv_root" {
		t.Errorf("DMName = %q, want %q", dm.DMName, "vg_sys-lv_root")
	}
	if dm.KernelName != "dm-0" {
		t.Errorf("KernelName = %q, want %q", dm.KernelName, "dm-0")
	}
	if dm.MountPoint != "/" {
		t.Errorf("MountPoint = %q, want %q", dm.MountPoint, "/")
	}
	if len(dm.Slaves) != 1 || dm.Slaves[0] != "nvme0n1p1" {
		t.Errorf("Slaves = %v, want [nvme0n1p1]", dm.Slaves)
	}
}

func TestGetBlockTopology_Integration(t *testing.T) {
	t.Parallel()
	sysfs := t.TempDir()
	proc := t.TempDir()

	setupNVMeDevice(t, sysfs)
	setupVirtualDevices(t, sysfs)

	writeFile(t, filepath.Join(proc, "self/mountinfo"),
		"22 1 253:0 / / rw,relatime shared:1 - ext4 /dev/mapper/vg_sys-lv_root rw\n")
	writeFile(t, filepath.Join(proc, "swaps"),
		"Filename\t\t\t\tType\t\tSize\t\tUsed\t\tPriority\n")
	writeFile(t, filepath.Join(proc, "mdstat"),
		"Personalities :\nunused devices: <none>\n")

	topo, err := GetBlockTopology(sysfs, proc)
	if err != nil {
		t.Fatalf("GetBlockTopology error: %v", err)
	}
	if topo == nil {
		t.Fatal("GetBlockTopology returned nil")
	}
	if len(topo.PhysicalDevices) != 1 {
		t.Errorf("PhysicalDevices count = %d, want 1", len(topo.PhysicalDevices))
	}
	if len(topo.VirtualLayers.DeviceMapper) != 1 {
		t.Errorf("DeviceMapper count = %d, want 1", len(topo.VirtualLayers.DeviceMapper))
	}
	if len(topo.VirtualLayers.MDArrays) != 0 {
		t.Errorf("MDArrays count = %d, want 0", len(topo.VirtualLayers.MDArrays))
	}
	if len(topo.SwapDevices) != 0 {
		t.Errorf("SwapDevices count = %d, want 0", len(topo.SwapDevices))
	}
}

func TestGetBlockTopology_EmptySystem(t *testing.T) {
	t.Parallel()
	sysfs := t.TempDir()
	proc := t.TempDir()
	mkdirAll(t, filepath.Join(sysfs, "class/block"))

	topo, err := GetBlockTopology(sysfs, proc)
	if err != nil {
		t.Fatalf("GetBlockTopology error: %v", err)
	}
	if topo == nil {
		t.Fatal("GetBlockTopology returned nil")
	}
	if len(topo.PhysicalDevices) != 0 {
		t.Errorf("PhysicalDevices = %d, want 0", len(topo.PhysicalDevices))
	}
}

func TestReadHolders(t *testing.T) {
	t.Parallel()
	sysfs := t.TempDir()
	mkdirAll(t, filepath.Join(sysfs, "class/block/sda1/holders/md0"))
	mkdirAll(t, filepath.Join(sysfs, "class/block/sda1/holders/dm-0"))

	holders := readHolders(sysfs, "sda1")
	if len(holders) != 2 {
		t.Errorf("holders count = %d, want 2", len(holders))
	}
}

func TestReadSlaves(t *testing.T) {
	t.Parallel()
	sysfs := t.TempDir()
	mkdirAll(t, filepath.Join(sysfs, "class/block/dm-0/slaves/sda1"))

	slaves := readSlaves(sysfs, "dm-0")
	if len(slaves) != 1 || slaves[0] != "sda1" {
		t.Errorf("slaves = %v, want [sda1]", slaves)
	}
}
