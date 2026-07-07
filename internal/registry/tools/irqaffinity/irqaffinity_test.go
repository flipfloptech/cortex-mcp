package irqaffinity

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func TestIrqAffinityTool_Contract(t *testing.T) {
	tool := New()

	if tool.Name() != "get_irq_affinity" {
		t.Errorf("expected name 'get_irq_affinity', got %q", tool.Name())
	}
	if tool.Category() != registry.CategoryCompute {
		t.Errorf("expected category 'compute', got %q", tool.Category())
	}
	if tool.Description() == "" {
		t.Error("expected non-empty description")
	}
	if tool.Help() == "" {
		t.Error("expected non-empty help")
	}
	if tool.Parameters() != nil {
		t.Errorf("expected nil parameters, got %v", tool.Parameters())
	}
}

func TestIrqAffinityTool_IsSupported(t *testing.T) {
	tool := New()
	supported, msg := tool.IsSupported()
	if registry.IsLinux() {
		if !supported {
			t.Errorf("expected supported on Linux, got msg: %s", msg)
		}
	} else {
		if supported {
			t.Error("expected not supported on non-Linux")
		}
	}
}

func TestIrqAffinityTool_Execute_Success(t *testing.T) {
	// Create a temporary mock /proc/interrupts file
	tempDir := t.TempDir()
	procInterrupts = filepath.Join(tempDir, "interrupts")

	mockData := `           CPU0       CPU1       CPU2       CPU3       
  0:         26          0          0          0   IO-APIC   2-edge      timer
  1:          0          0          0          0   IO-APIC   1-edge      i8042
 42:   20000000   15000000    1000000     500000   IR-PCI-MSI-edge      nvme0q1
134:  900000000          0          0          0   IR-PCI-MSI-edge      mlx5_comp0
135:          0  900000000          0          0   IR-PCI-MSI-edge      mlx5_comp1
NMI:          0          0          0          0   Non-maskable interrupts
LOC:   15000000   15000000   15000000   15000000   Local timer interrupts
IPI:     200000     200000     200000     200000   Inter-processor interrupts
`
	err := os.WriteFile(procInterrupts, []byte(mockData), 0644)
	if err != nil {
		t.Fatalf("failed to write mock data: %v", err)
	}

	tool := New()
	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.Status == registry.StatusError {
		t.Fatalf("tool execution returned error: %s", res.Summary)
	}

	var data IrqAffinityData
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("failed to unmarshal JSON data: %v", err)
	}

	// Validate System Summary
	if data.SystemSummary.TotalHardwareInterrupts == 0 {
		t.Error("expected non-zero total hardware interrupts")
	}

	// Calculate expected total for CPU0: 26 + 20000000 + 900000000 + 0 + 15000000 + 200000 = 935,200,026
	if len(data.SystemSummary.MostBurdenedCpus) == 0 {
		t.Fatalf("expected at least one burdened CPU")
	}

	topCpu := data.SystemSummary.MostBurdenedCpus[0]
	if topCpu.CpuId != 0 && topCpu.CpuId != 1 { // CPU0 or CPU1 have 900M+
		t.Errorf("expected CPU0 or CPU1 as most burdened, got %d", topCpu.CpuId)
	}

	// Validate Top IRQ Sources
	if len(data.TopIrqSources) == 0 {
		t.Fatalf("expected top IRQ sources")
	}

	mlx5, ok := data.TopIrqSources["mlx5_comp0"]
	if !ok {
		t.Fatalf("missing mlx5_comp0 in top irq sources")
	}

	if mlx5.IrqNumber != "134" {
		t.Errorf("expected irq number '134', got %q", mlx5.IrqNumber)
	}
	if mlx5.TotalCount != 900000000 {
		t.Errorf("expected total count 900000000, got %d", mlx5.TotalCount)
	}
	if len(mlx5.PrimaryCpus) != 1 || mlx5.PrimaryCpus[0] != 0 {
		t.Errorf("expected primary_cpus [0], got %v", mlx5.PrimaryCpus)
	}

	nvme, ok := data.TopIrqSources["nvme0q1"]
	if !ok {
		t.Fatalf("missing nvme0q1 in top irq sources")
	}
	// nvme0q1 total = 20M + 15M + 1M + 0.5M = 36.5M.
	// 1% is 365,000.
	// CPU0=20M (yes), CPU1=15M (yes), CPU2=1M (yes), CPU3=500k (yes). All > 1%.
	if len(nvme.PrimaryCpus) != 4 {
		t.Errorf("expected 4 primary cpus for nvme0q1, got %v", nvme.PrimaryCpus)
	}

	loc, ok := data.TopIrqSources["Local timer interrupts"]
	if !ok {
		// Maybe it's keyed by 'LOC'? Wait, the spec says "LOC": { ... }
		loc, ok = data.TopIrqSources["LOC"]
		if !ok {
			t.Fatalf("missing LOC in top irq sources")
		}
	}
	if loc.Type != "Local timer interrupts" {
		t.Errorf("expected type 'Local timer interrupts', got %q", loc.Type)
	}
	if loc.IrqNumber != "LOC" {
		t.Errorf("expected irq number 'LOC', got %q", loc.IrqNumber)
	}
	if len(loc.PrimaryCpus) != 4 {
		t.Errorf("expected 4 primary cpus for LOC, got %v", loc.PrimaryCpus)
	}
}

func TestIrqAffinityTool_Execute_MissingFile(t *testing.T) {
	tempDir := t.TempDir()
	procInterrupts = filepath.Join(tempDir, "does-not-exist")

	tool := New()
	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.Status != registry.StatusError {
		t.Fatalf("expected error result when file is missing")
	}
}

func BenchmarkExecute(b *testing.B) {
	tempDir := b.TempDir()
	procInterrupts = filepath.Join(tempDir, "interrupts")

	mockData := `           CPU0       CPU1       
  0:         26          0   IO-APIC   2-edge      timer
 42:  200000000  150000000   IR-PCI-MSI-edge      nvme0q1
134:  900000000          0   IR-PCI-MSI-edge      mlx5_comp0
NMI:          0          0   Non-maskable interrupts
LOC:   15000000   15000000   Local timer interrupts
`
	err := os.WriteFile(procInterrupts, []byte(mockData), 0644)
	if err != nil {
		b.Fatalf("failed to write mock data: %v", err)
	}

	tool := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(context.Background(), nil)
	}
}

func BenchmarkNew(b *testing.B) {
	for i := 0; i < b.N; i++ {
		New()
	}
}

func BenchmarkName(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		tool.Name()
	}
}

func BenchmarkDescription(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		tool.Description()
	}
}

func BenchmarkHelp(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		tool.Help()
	}
}

func BenchmarkCategory(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		tool.Category()
	}
}

func BenchmarkParameters(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		tool.Parameters()
	}
}

func BenchmarkHidden(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		tool.Hidden()
	}
}

func BenchmarkIsSupported(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		tool.IsSupported()
	}
}
