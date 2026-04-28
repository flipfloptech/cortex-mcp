package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGetHugePageInfo_HappyPath(t *testing.T) {
	// Create mock filesystem
	dir := t.TempDir()

	// Mock /proc/meminfo
	meminfoPath := filepath.Join(dir, "meminfo")
	meminfoData := []byte(`MemTotal:       32768000 kB
HugePages_Total:    1024
HugePages_Free:      124
HugePages_Rsvd:        0
Hugepagesize:       2048 kB
`)
	if err := os.WriteFile(meminfoPath, meminfoData, 0644); err != nil {
		t.Fatal(err)
	}

	// Mock THP enabled
	thpDir := filepath.Join(dir, "transparent_hugepage")
	if err := os.Mkdir(thpDir, 0755); err != nil {
		t.Fatal(err)
	}
	enabledPath := filepath.Join(thpDir, "enabled")
	if err := os.WriteFile(enabledPath, []byte("always [madvise] never\n"), 0644); err != nil {
		t.Fatal(err)
	}
	defragPath := filepath.Join(thpDir, "defrag")
	if err := os.WriteFile(defragPath, []byte("always defer [defer+madvise] madvise never\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Mock /proc/vmstat
	vmstatPath := filepath.Join(dir, "vmstat")
	vmstatData := []byte(`nr_free_pages 12345
thp_fault_alloc 45021
thp_fault_fallback 12
thp_collapse_alloc 890
`)
	if err := os.WriteFile(vmstatPath, vmstatData, 0644); err != nil {
		t.Fatal(err)
	}

	// Call the function
	info, err := getHugePageInfoFromPaths(meminfoPath, enabledPath, defragPath, vmstatPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Assertions for StaticHugepages
	if info.StaticHugepages.TotalPages != 1024 {
		t.Errorf("expected 1024 total pages, got %d", info.StaticHugepages.TotalPages)
	}
	if info.StaticHugepages.FreePages != 124 {
		t.Errorf("expected 124 free pages, got %d", info.StaticHugepages.FreePages)
	}
	if info.StaticHugepages.ReservedPages != 0 {
		t.Errorf("expected 0 reserved pages, got %d", info.StaticHugepages.ReservedPages)
	}
	if info.StaticHugepages.PageSizeKB != 2048 {
		t.Errorf("expected 2048 page size, got %d", info.StaticHugepages.PageSizeKB)
	}
	expectedTotalMB := 2048.0 // 1024 * 2048 / 1024
	if info.StaticHugepages.TotalSizeMB != expectedTotalMB {
		t.Errorf("expected %.1f TotalSizeMB, got %.1f", expectedTotalMB, info.StaticHugepages.TotalSizeMB)
	}

	// ((1024 - 124) / 1024) * 100 = 87.890625
	expectedUsage := 87.890625
	if info.StaticHugepages.UsagePct != expectedUsage {
		t.Errorf("expected usage_pct %f, got %f", expectedUsage, info.StaticHugepages.UsagePct)
	}

	// Assertions for SystemSummary
	if !info.SystemSummary.StaticHugepagesSupported {
		t.Error("expected StaticHugepagesSupported to be true")
	}
	if info.SystemSummary.THPEnabledMode != "madvise" {
		t.Errorf("expected THPEnabledMode 'madvise', got '%s'", info.SystemSummary.THPEnabledMode)
	}
	if info.SystemSummary.THPDefragPolicy != "defer+madvise" {
		t.Errorf("expected THPDefragPolicy 'defer+madvise', got '%s'", info.SystemSummary.THPDefragPolicy)
	}

	// Assertions for THPStats
	if info.THPStats == nil {
		t.Fatal("expected THPStats to not be nil")
	}
	if info.THPStats.FaultAllocations != 45021 {
		t.Errorf("expected 45021 fault allocations, got %d", info.THPStats.FaultAllocations)
	}
	if info.THPStats.FaultFallbacks != 12 {
		t.Errorf("expected 12 fault fallbacks, got %d", info.THPStats.FaultFallbacks)
	}
	if info.THPStats.CollapseAllocations != 890 {
		t.Errorf("expected 890 collapse allocations, got %d", info.THPStats.CollapseAllocations)
	}

	// is_stalling_risk should be true because thp_fault_fallback > 0
	if !info.THPStats.IsStallingRisk {
		t.Error("expected IsStallingRisk to be true due to fallbacks")
	}
}

func TestGetHugePageInfo_NoTHP(t *testing.T) {
	dir := t.TempDir()

	meminfoPath := filepath.Join(dir, "meminfo")
	_ = os.WriteFile(meminfoPath, []byte("HugePages_Total: 10\nHugePages_Free: 5\nHugepagesize: 2048 kB\n"), 0644)

	// Missing THP files
	enabledPath := filepath.Join(dir, "missing_enabled")
	defragPath := filepath.Join(dir, "missing_defrag")
	vmstatPath := filepath.Join(dir, "missing_vmstat")

	info, err := getHugePageInfoFromPaths(meminfoPath, enabledPath, defragPath, vmstatPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if info.SystemSummary.THPEnabledMode != "unsupported" {
		t.Errorf("expected THPEnabledMode to be 'unsupported', got '%s'", info.SystemSummary.THPEnabledMode)
	}
	if info.SystemSummary.THPDefragPolicy != "unsupported" {
		t.Errorf("expected THPDefragPolicy to be 'unsupported', got '%s'", info.SystemSummary.THPDefragPolicy)
	}
	if info.THPStats != nil {
		t.Errorf("expected THPStats to be nil, got %+v", info.THPStats)
	}
}

func TestGetHugePageInfo_ZeroTotalPages(t *testing.T) {
	dir := t.TempDir()

	meminfoPath := filepath.Join(dir, "meminfo")
	_ = os.WriteFile(meminfoPath, []byte("HugePages_Total: 0\nHugePages_Free: 0\nHugepagesize: 2048 kB\n"), 0644)

	info, err := getHugePageInfoFromPaths(meminfoPath, "a", "b", "c") // THP files missing
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if info.StaticHugepages.UsagePct != 0.0 {
		t.Errorf("expected UsagePct 0.0, got %f", info.StaticHugepages.UsagePct)
	}
}

func TestGetHugePageInfo_StallingRisk_AlwaysAlways(t *testing.T) {
	dir := t.TempDir()

	meminfoPath := filepath.Join(dir, "meminfo")
	_ = os.WriteFile(meminfoPath, []byte("HugePages_Total: 0\n"), 0644)

	enabledPath := filepath.Join(dir, "enabled")
	_ = os.WriteFile(enabledPath, []byte("[always] madvise never\n"), 0644)

	defragPath := filepath.Join(dir, "defrag")
	_ = os.WriteFile(defragPath, []byte("[always] defer defer+madvise madvise never\n"), 0644)

	vmstatPath := filepath.Join(dir, "vmstat")
	// 0 fallbacks, but mode is always/always
	_ = os.WriteFile(vmstatPath, []byte("thp_fault_fallback 0\n"), 0644)

	info, err := getHugePageInfoFromPaths(meminfoPath, enabledPath, defragPath, vmstatPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !info.THPStats.IsStallingRisk {
		t.Error("expected IsStallingRisk to be true due to always/always policy")
	}
}

func BenchmarkGetHugePageInfoFromPaths(b *testing.B) {
	dir := b.TempDir()

	meminfoPath := filepath.Join(dir, "meminfo")
	_ = os.WriteFile(meminfoPath, []byte("HugePages_Total: 1024\nHugePages_Free: 124\nHugepagesize: 2048 kB\n"), 0644)

	enabledPath := filepath.Join(dir, "enabled")
	_ = os.WriteFile(enabledPath, []byte("always [madvise] never\n"), 0644)

	defragPath := filepath.Join(dir, "defrag")
	_ = os.WriteFile(defragPath, []byte("always defer [defer+madvise] madvise never\n"), 0644)

	vmstatPath := filepath.Join(dir, "vmstat")
	_ = os.WriteFile(vmstatPath, []byte("thp_fault_alloc 45021\nthp_fault_fallback 12\nthp_collapse_alloc 890\n"), 0644)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = getHugePageInfoFromPaths(meminfoPath, enabledPath, defragPath, vmstatPath)
	}
}

func BenchmarkGetHugePageInfo(b *testing.B) {
	b.Skip("skipping to avoid real filesystem IO in benchmark")
}

func BenchmarkParseTHPMode(b *testing.B) {
	dir := b.TempDir()
	enabledPath := filepath.Join(dir, "enabled")
	_ = os.WriteFile(enabledPath, []byte("always [madvise] never\n"), 0644)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = parseTHPMode(enabledPath)
	}
}
