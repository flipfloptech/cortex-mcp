package storage

import (
	"os"
	"path/filepath"
	"testing"
)

// --- parseSchedulerString tests ---

func TestParseSchedulerString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		raw           string
		wantActive    string
		wantAvailable []string
	}{
		{
			name:          "standard_multi",
			raw:           "[mq-deadline] kyber bfq none",
			wantActive:    "mq-deadline",
			wantAvailable: []string{"mq-deadline", "kyber", "bfq", "none"},
		},
		{
			name:          "none_active",
			raw:           "mq-deadline kyber bfq [none]",
			wantActive:    "none",
			wantAvailable: []string{"mq-deadline", "kyber", "bfq", "none"},
		},
		{
			name:          "kyber_active",
			raw:           "mq-deadline [kyber] bfq none",
			wantActive:    "kyber",
			wantAvailable: []string{"mq-deadline", "kyber", "bfq", "none"},
		},
		{
			name:          "single_none_bracketed",
			raw:           "[none]",
			wantActive:    "none",
			wantAvailable: []string{"none"},
		},
		{
			name:          "empty_string",
			raw:           "",
			wantActive:    "none",
			wantAvailable: []string{"none"},
		},
		{
			name:          "whitespace_only",
			raw:           "   \t  ",
			wantActive:    "none",
			wantAvailable: []string{"none"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			active, available := parseSchedulerString(tt.raw)
			if active != tt.wantActive {
				t.Errorf("active = %q, want %q", active, tt.wantActive)
			}
			if len(available) != len(tt.wantAvailable) {
				t.Fatalf("available length = %d, want %d: %v", len(available), len(tt.wantAvailable), available)
			}
			for i, v := range available {
				if v != tt.wantAvailable[i] {
					t.Errorf("available[%d] = %q, want %q", i, v, tt.wantAvailable[i])
				}
			}
		})
	}
}

func TestParseSchedulerString_NoBrackets(t *testing.T) {
	t.Parallel()
	// Some DM/virtual devices return "none" without brackets.
	active, available := parseSchedulerString("none")
	if active != "none" {
		t.Errorf("active = %q, want %q", active, "none")
	}
	if len(available) != 1 || available[0] != "none" {
		t.Errorf("available = %v, want [none]", available)
	}
}

// --- readScheduler tests ---

func TestReadScheduler(t *testing.T) {
	t.Parallel()
	sysfs := t.TempDir()
	queueDir := filepath.Join(sysfs, "block/nvme0n1/queue")
	mkdirAll(t, queueDir)
	writeFile(t, filepath.Join(queueDir, "scheduler"), "[mq-deadline] kyber bfq none\n")

	got := readScheduler(sysfs, "nvme0n1")
	if got != "[mq-deadline] kyber bfq none" {
		t.Errorf("readScheduler = %q, want %q", got, "[mq-deadline] kyber bfq none")
	}
}

func TestReadScheduler_Missing(t *testing.T) {
	t.Parallel()
	got := readScheduler(t.TempDir(), "nonexistent")
	if got != "" {
		t.Errorf("readScheduler missing = %q, want empty", got)
	}
}

// --- readReadAheadKB tests ---

func TestReadReadAheadKB(t *testing.T) {
	t.Parallel()
	sysfs := t.TempDir()
	queueDir := filepath.Join(sysfs, "block/sda/queue")
	mkdirAll(t, queueDir)
	writeFile(t, filepath.Join(queueDir, "read_ahead_kb"), "128\n")

	got := readReadAheadKB(sysfs, "sda")
	if got != 128 {
		t.Errorf("readReadAheadKB = %d, want 128", got)
	}
}

func TestReadReadAheadKB_Missing(t *testing.T) {
	t.Parallel()
	got := readReadAheadKB(t.TempDir(), "nonexistent")
	if got != 0 {
		t.Errorf("readReadAheadKB missing = %d, want 0", got)
	}
}

func TestReadReadAheadKB_NonNumeric(t *testing.T) {
	t.Parallel()
	sysfs := t.TempDir()
	queueDir := filepath.Join(sysfs, "block/sda/queue")
	mkdirAll(t, queueDir)
	writeFile(t, filepath.Join(queueDir, "read_ahead_kb"), "garbage\n")

	got := readReadAheadKB(sysfs, "sda")
	if got != 0 {
		t.Errorf("readReadAheadKB garbage = %d, want 0", got)
	}
}

// --- isSchedulerDevice tests ---

func TestIsSchedulerDevice(t *testing.T) {
	t.Parallel()

	// Create a sysfs root with a mix of devices.
	sysfs := t.TempDir()
	blockDir := filepath.Join(sysfs, "block")

	// Physical disks (no partition file).
	for _, dev := range []string{"nvme0n1", "sda", "vda", "xvda", "dm-0", "dm-10", "md0", "md127"} {
		mkdirAll(t, filepath.Join(blockDir, dev))
	}

	// Partitions (have a partition file).
	for _, dev := range []string{"nvme0n1p1", "nvme0n1p2", "sda1", "sda2"} {
		devDir := filepath.Join(blockDir, dev)
		mkdirAll(t, devDir)
		writeFile(t, filepath.Join(devDir, "partition"), "1\n")
	}

	// Virtual devices to exclude.
	for _, dev := range []string{"loop0", "loop1", "ram0", "zram0", "nbd0"} {
		mkdirAll(t, filepath.Join(blockDir, dev))
	}

	tests := []struct {
		name string
		dev  string
		want bool
	}{
		{"nvme_disk", "nvme0n1", true},
		{"sata_disk", "sda", true},
		{"virtio_disk", "vda", true},
		{"xen_disk", "xvda", true},
		{"dm_device", "dm-0", true},
		{"dm_device_high", "dm-10", true},
		{"md_device", "md0", true},
		{"md_device_high", "md127", true},
		{"nvme_partition", "nvme0n1p1", false},
		{"nvme_partition2", "nvme0n1p2", false},
		{"sata_partition", "sda1", false},
		{"sata_partition2", "sda2", false},
		{"loop_device", "loop0", false},
		{"loop_device1", "loop1", false},
		{"ram_device", "ram0", false},
		{"zram_device", "zram0", false},
		{"nbd_device", "nbd0", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := isSchedulerDevice(sysfs, tt.dev)
			if got != tt.want {
				t.Errorf("isSchedulerDevice(%q) = %v, want %v", tt.dev, got, tt.want)
			}
		})
	}
}

// --- evaluateWarnings tests ---

func TestEvaluateWarnings_NVMeSchedulerTrap(t *testing.T) {
	t.Parallel()
	info := SchedulerInfo{
		DeviceName:      "nvme0n1",
		ActiveScheduler: "mq-deadline",
		ReadAheadKB:     128,
	}
	evaluateWarnings(&info)
	if !info.TuningWarning {
		t.Error("expected TuningWarning=true for NVMe with mq-deadline")
	}
	if len(info.WarningReasons) != 1 {
		t.Fatalf("expected 1 warning reason, got %d: %v", len(info.WarningReasons), info.WarningReasons)
	}
}

func TestEvaluateWarnings_NVMeNone(t *testing.T) {
	t.Parallel()
	info := SchedulerInfo{
		DeviceName:      "nvme0n1",
		ActiveScheduler: "none",
		ReadAheadKB:     128,
	}
	evaluateWarnings(&info)
	if info.TuningWarning {
		t.Error("expected TuningWarning=false for NVMe with none scheduler")
	}
	if len(info.WarningReasons) != 0 {
		t.Errorf("expected 0 warning reasons, got %d", len(info.WarningReasons))
	}
}

func TestEvaluateWarnings_HighReadAhead(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		readAheadKB int
		wantWarning bool
	}{
		{"below_threshold", 128, false},
		{"at_threshold", 4096, true},
		{"above_threshold", 8192, true},
		{"just_below", 4095, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			info := SchedulerInfo{
				DeviceName:      "sda",
				ActiveScheduler: "mq-deadline",
				ReadAheadKB:     tt.readAheadKB,
			}
			evaluateWarnings(&info)
			if info.TuningWarning != tt.wantWarning {
				t.Errorf("TuningWarning = %v, want %v (read_ahead_kb=%d)", info.TuningWarning, tt.wantWarning, tt.readAheadKB)
			}
		})
	}
}

func TestEvaluateWarnings_MultipleWarnings(t *testing.T) {
	t.Parallel()
	info := SchedulerInfo{
		DeviceName:      "nvme0n1",
		ActiveScheduler: "bfq",
		ReadAheadKB:     8192,
	}
	evaluateWarnings(&info)
	if !info.TuningWarning {
		t.Error("expected TuningWarning=true for NVMe with bfq + high read-ahead")
	}
	if len(info.WarningReasons) != 2 {
		t.Fatalf("expected 2 warning reasons, got %d: %v", len(info.WarningReasons), info.WarningReasons)
	}
}

// --- GetSchedulerInfo integration test ---

func TestGetSchedulerInfo_Integration(t *testing.T) {
	t.Parallel()
	sysfs := t.TempDir()
	blockDir := filepath.Join(sysfs, "block")

	// NVMe device with none scheduler (optimal).
	setupSchedulerDevice(t, blockDir, "nvme0n1", "[none] mq-deadline kyber bfq", "128")

	// SATA device with mq-deadline and high read-ahead.
	setupSchedulerDevice(t, blockDir, "sda", "[mq-deadline] kyber bfq none", "4096")

	// DM device with none scheduler (no brackets).
	setupSchedulerDevice(t, blockDir, "dm-0", "none", "128")

	// Partition — should be filtered out.
	partDir := filepath.Join(blockDir, "nvme0n1p1")
	mkdirAll(t, filepath.Join(partDir, "queue"))
	writeFile(t, filepath.Join(partDir, "partition"), "1\n")
	writeFile(t, filepath.Join(partDir, "queue/scheduler"), "[none] mq-deadline kyber bfq\n")
	writeFile(t, filepath.Join(partDir, "queue/read_ahead_kb"), "128\n")

	// Loop device — should be filtered out.
	loopDir := filepath.Join(blockDir, "loop0")
	mkdirAll(t, filepath.Join(loopDir, "queue"))
	writeFile(t, filepath.Join(loopDir, "queue/scheduler"), "[none]\n")
	writeFile(t, filepath.Join(loopDir, "queue/read_ahead_kb"), "128\n")

	payload, err := GetSchedulerInfo(sysfs)
	if err != nil {
		t.Fatalf("GetSchedulerInfo error: %v", err)
	}

	// Should have exactly 3 devices: nvme0n1, sda, dm-0.
	if payload.SystemSummary.DevicesAudited != 3 {
		t.Errorf("DevicesAudited = %d, want 3", payload.SystemSummary.DevicesAudited)
	}
	if len(payload.Devices) != 3 {
		t.Fatalf("Devices count = %d, want 3", len(payload.Devices))
	}

	// Verify device-level data (devices should be sorted).
	devMap := make(map[string]SchedulerInfo)
	for _, d := range payload.Devices {
		devMap[d.DeviceName] = d
	}

	// NVMe with none — no warnings.
	nvme, ok := devMap["nvme0n1"]
	if !ok {
		t.Fatal("missing nvme0n1 in results")
	}
	if nvme.ActiveScheduler != "none" {
		t.Errorf("nvme0n1 ActiveScheduler = %q, want %q", nvme.ActiveScheduler, "none")
	}
	if nvme.ReadAheadKB != 128 {
		t.Errorf("nvme0n1 ReadAheadKB = %d, want 128", nvme.ReadAheadKB)
	}
	if nvme.TuningWarning {
		t.Error("nvme0n1 should have no tuning warning")
	}

	// SATA with high read-ahead — 1 warning.
	sda, ok := devMap["sda"]
	if !ok {
		t.Fatal("missing sda in results")
	}
	if sda.ActiveScheduler != "mq-deadline" {
		t.Errorf("sda ActiveScheduler = %q, want %q", sda.ActiveScheduler, "mq-deadline")
	}
	if sda.ReadAheadKB != 4096 {
		t.Errorf("sda ReadAheadKB = %d, want 4096", sda.ReadAheadKB)
	}
	if !sda.TuningWarning {
		t.Error("sda should have tuning warning for high read-ahead")
	}
	if len(sda.WarningReasons) != 1 {
		t.Errorf("sda WarningReasons count = %d, want 1", len(sda.WarningReasons))
	}

	// DM with none (no brackets) — no warnings.
	dm, ok := devMap["dm-0"]
	if !ok {
		t.Fatal("missing dm-0 in results")
	}
	if dm.ActiveScheduler != "none" {
		t.Errorf("dm-0 ActiveScheduler = %q, want %q", dm.ActiveScheduler, "none")
	}
	if dm.TuningWarning {
		t.Error("dm-0 should have no tuning warning")
	}

	// Summary warnings count: sda has 1 warning.
	if payload.SystemSummary.TuningWarningsDetected != 1 {
		t.Errorf("TuningWarningsDetected = %d, want 1", payload.SystemSummary.TuningWarningsDetected)
	}
}

func TestGetSchedulerInfo_EmptyBlockDir(t *testing.T) {
	t.Parallel()
	sysfs := t.TempDir()
	mkdirAll(t, filepath.Join(sysfs, "block"))

	payload, err := GetSchedulerInfo(sysfs)
	if err != nil {
		t.Fatalf("GetSchedulerInfo error: %v", err)
	}
	if payload.SystemSummary.DevicesAudited != 0 {
		t.Errorf("DevicesAudited = %d, want 0", payload.SystemSummary.DevicesAudited)
	}
	if len(payload.Devices) != 0 {
		t.Errorf("Devices count = %d, want 0", len(payload.Devices))
	}
}

func TestGetSchedulerInfo_MissingBlockDir(t *testing.T) {
	t.Parallel()
	sysfs := t.TempDir()

	_, err := GetSchedulerInfo(sysfs)
	if err == nil {
		t.Error("expected error when /sys/block is missing")
	}
}

// --- test helper ---

func setupSchedulerDevice(t *testing.T, blockDir, name, scheduler, readAhead string) {
	t.Helper()
	devDir := filepath.Join(blockDir, name)
	queueDir := filepath.Join(devDir, "queue")
	mkdirAll(t, queueDir)
	writeFile(t, filepath.Join(queueDir, "scheduler"), scheduler+"\n")
	writeFile(t, filepath.Join(queueDir, "read_ahead_kb"), readAhead+"\n")
}

// --- Benchmarks ---

func BenchmarkParseSchedulerString(b *testing.B) {
	raw := "[mq-deadline] kyber bfq none"
	for i := 0; i < b.N; i++ {
		_, _ = parseSchedulerString(raw)
	}
}

func BenchmarkReadScheduler(b *testing.B) {
	sysfs := b.TempDir()
	queueDir := filepath.Join(sysfs, "block/nvme0n1/queue")
	mkdirAllB(b, queueDir)
	writeFileB(b, filepath.Join(queueDir, "scheduler"), "[mq-deadline] kyber bfq none\n")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = readScheduler(sysfs, "nvme0n1")
	}
}

func BenchmarkReadReadAheadKB(b *testing.B) {
	sysfs := b.TempDir()
	queueDir := filepath.Join(sysfs, "block/sda/queue")
	mkdirAllB(b, queueDir)
	writeFileB(b, filepath.Join(queueDir, "read_ahead_kb"), "128\n")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = readReadAheadKB(sysfs, "sda")
	}
}

func BenchmarkIsSchedulerDevice(b *testing.B) {
	sysfs := b.TempDir()
	mkdirAllB(b, filepath.Join(sysfs, "block/nvme0n1"))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = isSchedulerDevice(sysfs, "nvme0n1")
	}
}

func BenchmarkEvaluateWarnings(b *testing.B) {
	for i := 0; i < b.N; i++ {
		info := SchedulerInfo{
			DeviceName:      "nvme0n1",
			ActiveScheduler: "bfq",
			ReadAheadKB:     8192,
		}
		evaluateWarnings(&info)
	}
}

func BenchmarkGetSchedulerInfo(b *testing.B) {
	sysfs := b.TempDir()
	blockDir := filepath.Join(sysfs, "block")
	setupSchedulerDeviceB(b, blockDir, "nvme0n1", "[none] mq-deadline kyber bfq", "128")
	setupSchedulerDeviceB(b, blockDir, "sda", "[mq-deadline] kyber bfq none", "4096")
	setupSchedulerDeviceB(b, blockDir, "dm-0", "none", "128")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = GetSchedulerInfo(sysfs)
	}
}

func setupSchedulerDeviceB(b *testing.B, blockDir, name, scheduler, readAhead string) {
	b.Helper()
	devDir := filepath.Join(blockDir, name)
	queueDir := filepath.Join(devDir, "queue")
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		b.Fatalf("mkdirAll %s: %v", queueDir, err)
	}
	if err := os.WriteFile(filepath.Join(queueDir, "scheduler"), []byte(scheduler+"\n"), 0o644); err != nil {
		b.Fatalf("writeFile scheduler: %v", err)
	}
	if err := os.WriteFile(filepath.Join(queueDir, "read_ahead_kb"), []byte(readAhead+"\n"), 0o644); err != nil {
		b.Fatalf("writeFile read_ahead_kb: %v", err)
	}
}
