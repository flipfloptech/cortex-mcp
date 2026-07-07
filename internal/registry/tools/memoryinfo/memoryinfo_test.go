package memoryinfo

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func TestMemoryInfoTool_ContractCompliance(t *testing.T) {
	t.Parallel()

	var tool registry.Tool = &MemoryInfoTool{}

	if tool.Name() == "" {
		t.Error("Name() must not be empty")
	}
	if tool.Description() == "" {
		t.Error("Description() must not be empty")
	}
	if tool.Help() == "" {
		t.Error("Help() must not be empty")
	}
	if tool.Category() == registry.CategoryUnknown {
		t.Error("Category() must not be empty")
	}
	if len(tool.Parameters()) != 0 {
		t.Error("Parameters() should be empty")
	}
}

func TestMemoryInfoTool_IsSupported(t *testing.T) {
	// Not t.Parallel() because it modifies global meminfoPath temporarily,
	// but actually we can just leave it as is if we don't modify it here.
	tool := &MemoryInfoTool{}
	supported, reason := tool.IsSupported()
	if registry.IsLinux() {
		// Just in case meminfo path exists or doesn't in CI.
		if _, err := os.Stat(meminfoPath); err == nil {
			if !supported {
				t.Errorf("Should be supported on Linux with %s: %s", meminfoPath, reason)
			}
		} else {
			if supported {
				t.Errorf("Should not be supported without %s", meminfoPath)
			}
		}
	} else {
		if supported {
			t.Error("Should not be supported on non-Linux")
		}
	}
}

func TestParseMemInfo_Standard(t *testing.T) {
	t.Parallel()

	input := `
MemTotal:       263930432 kB
MemFree:        74415516 kB
MemAvailable:   188743120 kB
Buffers:          341508 kB
Cached:         110298080 kB
SwapTotal:       8388604 kB
SwapFree:        7340028 kB
`
	data, err := ParseMemInfo(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseMemInfo returned error: %v", err)
	}

	if data.EstimationMode != "standard" {
		t.Errorf("expected standard mode, got %q", data.EstimationMode)
	}

	// total = 263930432 / 1024 = 257744 MB
	if data.TotalMB != 257744 {
		t.Errorf("expected TotalMB=257744, got %d", data.TotalMB)
	}

	// true_used = (263930432 - 188743120) / 1024 = 75187312 / 1024 = 73425 MB
	if data.TrueUsedMB != 73425 {
		t.Errorf("expected TrueUsedMB=73425, got %d", data.TrueUsedMB)
	}

	// swap_ratio = (8388604 - 7340028) / 8388604 = 1048576 / 8388604 = 0.125000...
	// rounded to 2 decimals = 12.5
	if data.SwapRatioPct != 12.5 {
		t.Errorf("expected SwapRatioPct=12.5, got %f", data.SwapRatioPct)
	}

	if !data.SwapActive {
		t.Error("SwapActive should be true")
	}
}

func TestParseMemInfo_Legacy(t *testing.T) {
	t.Parallel()

	// Missing MemAvailable
	input := `
MemTotal:       263930432 kB
MemFree:        74415516 kB
Buffers:          341508 kB
Cached:         110298080 kB
SwapTotal:       8388604 kB
SwapFree:        8388604 kB
`
	data, err := ParseMemInfo(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseMemInfo returned error: %v", err)
	}

	if data.EstimationMode != "legacy" {
		t.Errorf("expected legacy mode, got %q", data.EstimationMode)
	}

	// available = 74415516 + 341508 + 110298080 = 185055104
	// available MB = 185055104 / 1024 = 180717
	if data.AvailableMB != 180717 {
		t.Errorf("expected AvailableMB=180717, got %d", data.AvailableMB)
	}

	// swap_ratio = 0
	if data.SwapRatioPct != 0 {
		t.Errorf("expected SwapRatioPct=0, got %f", data.SwapRatioPct)
	}

	if data.SwapActive {
		t.Error("SwapActive should be false")
	}
}

func TestParseMemInfo_NoSwap(t *testing.T) {
	t.Parallel()

	input := `
MemTotal:       263930432 kB
MemAvailable:   188743120 kB
SwapTotal:             0 kB
SwapFree:              0 kB
`
	data, err := ParseMemInfo(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseMemInfo returned error: %v", err)
	}

	if data.SwapTotalMB != 0 {
		t.Errorf("expected SwapTotalMB=0, got %d", data.SwapTotalMB)
	}
	if data.SwapRatioPct != 0 {
		t.Errorf("expected SwapRatioPct=0, got %f", data.SwapRatioPct)
	}
	if data.SwapActive {
		t.Error("SwapActive should be false")
	}
}

func TestMemoryInfoTool_ExecuteSchema(t *testing.T) {
	// Not t.Parallel() because we mock meminfoPath
	originalPath := meminfoPath
	defer func() { meminfoPath = originalPath }()

	tmpFile, err := os.CreateTemp("", "meminfo-*")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()

	_, err = tmpFile.WriteString("MemTotal: 1024000 kB\nMemAvailable: 512000 kB\nSwapTotal: 102400 kB\nSwapFree: 51200 kB\n")
	if err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	_ = tmpFile.Close()

	meminfoPath = tmpFile.Name()

	tool := &MemoryInfoTool{}
	result, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.Status != registry.StatusOK {
		t.Errorf("Status = %q, want %q", result.Status, registry.StatusOK)
	}

	var data map[string]interface{}
	if err := json.Unmarshal(result.Data, &data); err != nil {
		t.Fatalf("Data is not valid JSON: %v", err)
	}

	requiredFields := []string{
		"total_mb", "true_used_mb", "available_mb",
		"swap_total_mb", "swap_ratio_pct", "swap_active", "estimation_mode",
	}

	for _, field := range requiredFields {
		if _, ok := data[field]; !ok {
			t.Errorf("Data missing required field: %s", field)
		}
	}

	if data["estimation_mode"] != "standard" {
		t.Errorf("expected estimation_mode=standard, got %v", data["estimation_mode"])
	}
	if data["swap_active"] != true {
		t.Errorf("expected swap_active=true, got %v", data["swap_active"])
	}

	val, ok := data["swap_ratio_pct"].(float64)
	if !ok || val != 50.0 {
		t.Errorf("expected swap_ratio_pct=50.0, got %v", data["swap_ratio_pct"])
	}
}

func TestMemoryInfoTool_ExecuteError(t *testing.T) {
	// Not t.Parallel() because we mock meminfoPath
	originalPath := meminfoPath
	defer func() { meminfoPath = originalPath }()
	meminfoPath = "/does/not/exist/meminfo/invalid"

	tool := &MemoryInfoTool{}
	result, err := tool.Execute(context.Background(), nil)
	// Go err should be nil to adhere to standard MCP error encapsulation
	if err != nil {
		t.Fatalf("Execute should not return a hard error, got: %v", err)
	}

	if result.Status != registry.StatusError {
		t.Errorf("Status = %q, want %q", result.Status, registry.StatusError)
	}

	var data map[string]string
	if err := json.Unmarshal(result.Data, &data); err != nil {
		t.Fatalf("Data is not valid JSON: %v", err)
	}

	if _, ok := data["error"]; !ok {
		t.Errorf("Data missing error message")
	}
}

func BenchmarkParseMemInfo(b *testing.B) {
	input := []byte(`
MemTotal:       263930432 kB
MemFree:        74415516 kB
MemAvailable:   188743120 kB
Buffers:          341508 kB
Cached:         110298080 kB
SwapCached:            0 kB
Active:         63412356 kB
Inactive:       113647164 kB
Active(anon):   38435132 kB
Inactive(anon): 24204212 kB
Active(file):   24977224 kB
Inactive(file): 89442952 kB
Unevictable:           0 kB
Mlocked:               0 kB
SwapTotal:       8388604 kB
SwapFree:        7340028 kB
`)
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, _ = ParseMemInfo(strings.NewReader(string(input)))
	}
}

func BenchmarkName(b *testing.B) {
	tool := &MemoryInfoTool{}
	for i := 0; i < b.N; i++ {
		_ = tool.Name()
	}
}

func BenchmarkDescription(b *testing.B) {
	tool := &MemoryInfoTool{}
	for i := 0; i < b.N; i++ {
		_ = tool.Description()
	}
}

func BenchmarkHelp(b *testing.B) {
	tool := &MemoryInfoTool{}
	for i := 0; i < b.N; i++ {
		_ = tool.Help()
	}
}

func BenchmarkCategory(b *testing.B) {
	tool := &MemoryInfoTool{}
	for i := 0; i < b.N; i++ {
		_ = tool.Category()
	}
}

func BenchmarkParameters(b *testing.B) {
	tool := &MemoryInfoTool{}
	for i := 0; i < b.N; i++ {
		_ = tool.Parameters()
	}
}

func BenchmarkHidden(b *testing.B) {
	tool := &MemoryInfoTool{}
	for i := 0; i < b.N; i++ {
		_ = tool.Hidden()
	}
}

func BenchmarkIsSupported(b *testing.B) {
	tool := &MemoryInfoTool{}
	for i := 0; i < b.N; i++ {
		_, _ = tool.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	originalPath := meminfoPath
	defer func() { meminfoPath = originalPath }()

	tmpFile, _ := os.CreateTemp("", "meminfo-*")
	defer func() { _ = os.Remove(tmpFile.Name()) }()
	_, _ = tmpFile.WriteString("MemTotal: 1024000 kB\nMemAvailable: 512000 kB\nSwapTotal: 102400 kB\nSwapFree: 51200 kB\n")
	_ = tmpFile.Close()

	meminfoPath = tmpFile.Name()
	tool := &MemoryInfoTool{}
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, nil)
	}
}
