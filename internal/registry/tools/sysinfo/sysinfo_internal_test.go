package sysinfo

import (
	"os"
	"path/filepath"
	"testing"
)

// Helper to create a temporary file with content
func createTempFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDetectVirtualization(t *testing.T) {
	tmpDir := t.TempDir()

	// Save originals and defer restore
	origCgroup := procSelfCgroupPath
	origMount := procSelfMountinfoPath
	origDockerEnv := dockerEnvPath
	origRunEnv := runContainerEnvPath
	origSysVendor := sysVendorPath
	origProductName := productNamePath
	origHypervisor := hypervisorTypePath
	defer func() {
		procSelfCgroupPath = origCgroup
		procSelfMountinfoPath = origMount
		dockerEnvPath = origDockerEnv
		runContainerEnvPath = origRunEnv
		sysVendorPath = origSysVendor
		productNamePath = origProductName
		hypervisorTypePath = origHypervisor
	}()

	tests := []struct {
		name     string
		setup    func()
		wantVirt bool
		wantCont bool
		wantType string
	}{
		{
			name: "docker via cgroup",
			setup: func() {
				procSelfCgroupPath = createTempFile(t, tmpDir, "cgroup1", "1:name=systemd:/docker/1234\n")
			},
			wantVirt: true,
			wantCont: true,
			wantType: "docker",
		},
		{
			name: "kubernetes via cgroup",
			setup: func() {
				procSelfCgroupPath = createTempFile(t, tmpDir, "cgroup2", "1:name=systemd:/kubepods/burstable\n")
			},
			wantVirt: true,
			wantCont: true,
			wantType: "kubepods",
		},
		{
			name: "overlay via mountinfo",
			setup: func() {
				procSelfCgroupPath = createTempFile(t, tmpDir, "cgroup3", "0::/\n")
				procSelfMountinfoPath = createTempFile(t, tmpDir, "mountinfo1", "30 24 0:26 / / rw,relatime - overlay overlay rw\n")
			},
			wantVirt: true,
			wantCont: true,
			wantType: "overlay",
		},
		{
			name: "kvm via hypervisor type",
			setup: func() {
				procSelfCgroupPath = filepath.Join(tmpDir, "missing")
				procSelfMountinfoPath = filepath.Join(tmpDir, "missing")
				dockerEnvPath = filepath.Join(tmpDir, "missing")
				hypervisorTypePath = createTempFile(t, tmpDir, "hyper1", "kvm\n")
			},
			wantVirt: true,
			wantCont: false,
			wantType: "kvm",
		},
		{
			name: "qemu via sys_vendor",
			setup: func() {
				hypervisorTypePath = filepath.Join(tmpDir, "missing")
				sysVendorPath = createTempFile(t, tmpDir, "sysv1", "QEMU\n")
			},
			wantVirt: true,
			wantCont: false,
			wantType: "QEMU",
		},
		{
			name: "bare metal",
			setup: func() {
				procSelfCgroupPath = filepath.Join(tmpDir, "missing")
				procSelfMountinfoPath = filepath.Join(tmpDir, "missing")
				dockerEnvPath = filepath.Join(tmpDir, "missing")
				runContainerEnvPath = filepath.Join(tmpDir, "missing")
				hypervisorTypePath = filepath.Join(tmpDir, "missing")
				sysVendorPath = createTempFile(t, tmpDir, "sysv2", "Dell Inc.\n")
				productNamePath = createTempFile(t, tmpDir, "prod1", "PowerEdge R740\n")
			},
			wantVirt: false,
			wantCont: false,
			wantType: "bare-metal",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup()
			gotVirt, gotCont, gotType := detectVirtualization()
			if gotVirt != tc.wantVirt {
				t.Errorf("gotVirt = %v, want %v", gotVirt, tc.wantVirt)
			}
			if gotCont != tc.wantCont {
				t.Errorf("gotCont = %v, want %v", gotCont, tc.wantCont)
			}
			if gotType != tc.wantType {
				t.Errorf("gotType = %v, want %v", gotType, tc.wantType)
			}
		})
	}
}

func TestDetectMellanox(t *testing.T) {
	tmpDir := t.TempDir()
	origInfinibandClassPath := infinibandClassPath
	defer func() { infinibandClassPath = origInfinibandClassPath }()

	infinibandClassPath = filepath.Join(tmpDir, "infiniband")

	// Create structure for Eth and IB
	// /sys/class/infiniband/mlx5_0/ports/1/link_layer -> Ethernet
	// /sys/class/infiniband/mlx5_1/ports/1/link_layer -> InfiniBand

	createTempFile(t, tmpDir, "infiniband/mlx5_0/ports/1/link_layer", "Ethernet\n")
	createTempFile(t, tmpDir, "infiniband/mlx5_1/ports/1/link_layer", "InfiniBand\n")
	createTempFile(t, tmpDir, "infiniband/mlx5_2/ports/1/link_layer", "Unknown\n") // Should ignore

	gotEth, gotIB := detectMellanox()
	if !gotEth || !gotIB {
		t.Errorf("detectMellanox() = eth:%v, ib:%v; want true, true", gotEth, gotIB)
	}

	// Test with no directories
	infinibandClassPath = filepath.Join(tmpDir, "missing")
	gotEth, gotIB = detectMellanox()
	if gotEth || gotIB {
		t.Errorf("detectMellanox() = eth:%v, ib:%v; want false, false", gotEth, gotIB)
	}
}

func TestReadMellanoxVersion(t *testing.T) {
	tmpDir := t.TempDir()
	origMlx := mlx5CoreVersionPath
	defer func() { mlx5CoreVersionPath = origMlx }()

	tests := []struct {
		name        string
		kernelVer   string
		fileContent string
		want        string
	}{
		{
			name:        "is mofed",
			kernelVer:   "5.14.0-362.el9.x86_64",
			fileContent: "23.10.1\n",
			want:        "23.10.1 (MOFED)",
		},
		{
			name:        "is upstream kernel",
			kernelVer:   "5.14.0-362.el9.x86_64",
			fileContent: "5.14.0-362.el9.x86_64\n",
			want:        "5.14.0-362.el9.x86_64 (Upstream)",
		},
		{
			name:        "is identical but missing newline",
			kernelVer:   "5.14.0",
			fileContent: "5.14.0",
			want:        "5.14.0 (Upstream)",
		},
		{
			name:        "missing file",
			kernelVer:   "5.14",
			fileContent: "",
			want:        "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.fileContent != "" {
				mlx5CoreVersionPath = createTempFile(t, tmpDir, "mlx5_"+tc.name, tc.fileContent)
			} else {
				mlx5CoreVersionPath = filepath.Join(tmpDir, "missing")
			}
			got := readMellanoxVersion(tc.kernelVer)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReadLustreVersion(t *testing.T) {
	tmpDir := t.TempDir()
	orig := lustreVersionPath
	defer func() { lustreVersionPath = orig }()

	lustreVersionPath = createTempFile(t, tmpDir, "lustre_version", "lustre: 2.15.3\n")
	got := readLustreVersion()
	if got != "lustre: 2.15.3" {
		t.Errorf("got %q, want %q", got, "lustre: 2.15.3")
	}
}

func TestReadKernelCmdline(t *testing.T) {
	tmpDir := t.TempDir()
	orig := procCmdlinePath
	defer func() { procCmdlinePath = orig }()

	procCmdlinePath = createTempFile(t, tmpDir, "cmdline", "BOOT_IMAGE=/vmlinuz root=/dev/sda1 ro quiet\n")
	got := readKernelCmdline()
	if got != "BOOT_IMAGE=/vmlinuz root=/dev/sda1 ro quiet" {
		t.Errorf("got %q, want %q", got, "BOOT_IMAGE=/vmlinuz root=/dev/sda1 ro quiet")
	}
}

func TestReadNodeScale(t *testing.T) {
	tmpDir := t.TempDir()
	origStat := procStatPath
	origMem := procMeminfoPath
	defer func() {
		procStatPath = origStat
		procMeminfoPath = origMem
	}()

	procStatPath = createTempFile(t, tmpDir, "stat", "cpu  1 2 3\nbtime 1610000000\nprocesses 100\n")
	procMeminfoPath = createTempFile(t, tmpDir, "meminfo", "MemTotal:       263884700 kB\nMemFree:         2000000 kB\n")

	gotMem, gotBoot := readNodeScale()

	wantMem := int64(263884700 / 1024) // ~257700 MB
	if gotMem != wantMem {
		t.Errorf("got Mem = %d, want %d", gotMem, wantMem)
	}
	if gotBoot != 1610000000 {
		t.Errorf("got Boot = %d, want %d", gotBoot, 1610000000)
	}
}

func BenchmarkDetectVirtualization(b *testing.B) {
	// Let it run against the real system for benchmark
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		detectVirtualization()
	}
}

func BenchmarkDetectMellanox(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		detectMellanox()
	}
}

func BenchmarkReadMellanoxVersion(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		readMellanoxVersion("5.14.0")
	}
}

func BenchmarkReadLustreVersion(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		readLustreVersion()
	}
}

func BenchmarkReadKernelCmdline(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		readKernelCmdline()
	}
}

func BenchmarkReadNodeScale(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		readNodeScale()
	}
}
