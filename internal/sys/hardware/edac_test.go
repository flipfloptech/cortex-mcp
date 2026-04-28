package hardware

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGetEDACErrorsInfo_MissingDriver(t *testing.T) {
	dir := t.TempDir()

	// Empty directory simulates no EDAC driver loaded
	edacDir := filepath.Join(dir, "edac", "mc")

	// We pass the fake edac directory path to a testable version of GetEDACErrorsInfo
	info, err := getEDACErrorsInfoFromPath(edacDir, 100.0) // 100 seconds uptime
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if info.SystemSummary.EDACSubsystemActive {
		t.Error("expected edac_subsystem_active to be false")
	}
	if info.SystemSummary.HealthStatus != "degraded" {
		t.Errorf("expected health_status 'degraded', got '%s'", info.SystemSummary.HealthStatus)
	}
}

func TestGetEDACErrorsInfo_Healthy(t *testing.T) {
	dir := t.TempDir()
	edacDir := filepath.Join(dir, "edac", "mc")

	// Create mc0 and mc1
	mc0 := filepath.Join(edacDir, "mc0")
	mc1 := filepath.Join(edacDir, "mc1")
	if err := os.MkdirAll(mc0, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(mc1, 0755); err != nil {
		t.Fatal(err)
	}

	_ = os.WriteFile(filepath.Join(mc0, "ce_count"), []byte("0\n"), 0644)
	_ = os.WriteFile(filepath.Join(mc0, "ue_count"), []byte("0\n"), 0644)
	_ = os.WriteFile(filepath.Join(mc1, "ce_count"), []byte("0\n"), 0644)
	_ = os.WriteFile(filepath.Join(mc1, "ue_count"), []byte("0\n"), 0644)

	// Create dimms with zero errors
	dimm0 := filepath.Join(mc0, "dimm0")
	if err := os.MkdirAll(dimm0, 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dimm0, "dimm_ce_count"), []byte("0\n"), 0644)

	info, err := getEDACErrorsInfoFromPath(edacDir, 100.0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !info.SystemSummary.EDACSubsystemActive {
		t.Error("expected edac_subsystem_active to be true")
	}
	if info.SystemSummary.HealthStatus != "healthy" {
		t.Errorf("expected health_status 'healthy', got '%s'", info.SystemSummary.HealthStatus)
	}
	if info.SystemSummary.TotalCorrectableErrors != 0 {
		t.Errorf("expected 0 CE, got %d", info.SystemSummary.TotalCorrectableErrors)
	}

	if len(info.Controllers) != 2 {
		t.Fatalf("expected 2 controllers, got %d", len(info.Controllers))
	}
	for _, ctrl := range info.Controllers {
		if len(ctrl.FailingDIMMs) > 0 {
			t.Errorf("expected failing_dimms to be empty/nil for controller %d, got %+v", ctrl.ControllerID, ctrl.FailingDIMMs)
		}
	}
}

func TestGetEDACErrorsInfo_ErrorsAndNamingVariants(t *testing.T) {
	dir := t.TempDir()
	edacDir := filepath.Join(dir, "edac", "mc")

	// mc0: dimm naming
	mc0 := filepath.Join(edacDir, "mc0")
	if err := os.MkdirAll(mc0, 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(mc0, "ce_count"), []byte("42\n"), 0644)
	_ = os.WriteFile(filepath.Join(mc0, "ue_count"), []byte("0\n"), 0644)

	dimm0 := filepath.Join(mc0, "dimm0")
	if err := os.MkdirAll(dimm0, 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dimm0, "dimm_ce_count"), []byte("42\n"), 0644)
	_ = os.WriteFile(filepath.Join(dimm0, "dimm_label"), []byte("DIMM_A1\n"), 0644)
	_ = os.WriteFile(filepath.Join(dimm0, "dimm_dev_type"), []byte("x8\n"), 0644)

	// mc1: csrow naming
	mc1 := filepath.Join(edacDir, "mc1")
	if err := os.MkdirAll(mc1, 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(mc1, "ce_count"), []byte("0\n"), 0644)
	_ = os.WriteFile(filepath.Join(mc1, "ue_count"), []byte("1\n"), 0644) // UE! Critical!

	csrow1 := filepath.Join(mc1, "csrow1")
	if err := os.MkdirAll(csrow1, 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(csrow1, "ce_count"), []byte("0\n"), 0644) // Fallback for csrow?
	_ = os.WriteFile(filepath.Join(csrow1, "ue_count"), []byte("1\n"), 0644)
	_ = os.WriteFile(filepath.Join(csrow1, "dimm_label"), []byte("Node1_DIMM0\n"), 0644)

	info, err := getEDACErrorsInfoFromPath(edacDir, 100.0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if info.SystemSummary.HealthStatus != "critical" {
		t.Errorf("expected health_status 'critical', got '%s'", info.SystemSummary.HealthStatus)
	}
	if info.SystemSummary.TotalCorrectableErrors != 42 {
		t.Errorf("expected 42 CE, got %d", info.SystemSummary.TotalCorrectableErrors)
	}
	if info.SystemSummary.TotalUncorrectableErrors != 1 {
		t.Errorf("expected 1 UE, got %d", info.SystemSummary.TotalUncorrectableErrors)
	}

	var mc0Ctrl *EDACController
	for _, ctrl := range info.Controllers {
		if ctrl.ControllerID == 0 {
			mc0Ctrl = &ctrl
			break
		}
	}
	if mc0Ctrl == nil {
		t.Fatal("mc0 not found")
	}

	if len(mc0Ctrl.FailingDIMMs) != 1 {
		t.Fatalf("expected 1 failing DIMM in mc0, got %d", len(mc0Ctrl.FailingDIMMs))
	}
	if mc0Ctrl.FailingDIMMs[0].Label != "DIMM_A1" {
		t.Errorf("expected DIMM_A1, got %s", mc0Ctrl.FailingDIMMs[0].Label)
	}
}

func BenchmarkGetEDACErrorsInfoFromPath(b *testing.B) {
	dir := b.TempDir()
	edacDir := filepath.Join(dir, "edac", "mc")
	mc0 := filepath.Join(edacDir, "mc0")
	_ = os.MkdirAll(mc0, 0755)
	_ = os.WriteFile(filepath.Join(mc0, "ce_count"), []byte("42\n"), 0644)
	_ = os.WriteFile(filepath.Join(mc0, "ue_count"), []byte("0\n"), 0644)
	dimm0 := filepath.Join(mc0, "dimm0")
	_ = os.MkdirAll(dimm0, 0755)
	_ = os.WriteFile(filepath.Join(dimm0, "dimm_ce_count"), []byte("42\n"), 0644)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = getEDACErrorsInfoFromPath(edacDir, 100.0)
	}
}

func BenchmarkGetEDACErrorsInfo(b *testing.B) {
	b.Skip("skipping to avoid real filesystem IO in benchmark")
}

func BenchmarkParseFailingDIMMs(b *testing.B) {
	dir := b.TempDir()
	dimm0 := filepath.Join(dir, "dimm0")
	_ = os.MkdirAll(dimm0, 0755)
	_ = os.WriteFile(filepath.Join(dimm0, "dimm_ce_count"), []byte("42\n"), 0644)
	_ = os.WriteFile(filepath.Join(dimm0, "dimm_label"), []byte("DIMM_A1\n"), 0644)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseFailingDIMMs(dir)
	}
}

func BenchmarkReadUint64Safe(b *testing.B) {
	dir := b.TempDir()
	path := filepath.Join(dir, "val")
	_ = os.WriteFile(path, []byte("42\n"), 0644)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = readUint64Safe(path)
	}
}

func BenchmarkReadStringSafe(b *testing.B) {
	dir := b.TempDir()
	path := filepath.Join(dir, "val")
	_ = os.WriteFile(path, []byte("DIMM_A1\n"), 0644)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = readStringSafe(path)
	}
}
