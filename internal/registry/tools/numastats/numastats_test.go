package numastats

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func TestNumaStatsTool_ContractCompliance(t *testing.T) {
	t.Parallel()

	var tool registry.Tool = &NumaStatsTool{}

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

func TestNumaStatsTool_IsSupported(t *testing.T) {
	tool := &NumaStatsTool{}
	supported, reason := tool.IsSupported()
	if registry.IsLinux() {
		if _, err := os.Stat(sysfsNodePath); err == nil {
			if !supported {
				t.Errorf("Should be supported on Linux with %s: %s", sysfsNodePath, reason)
			}
		} else {
			if supported {
				t.Errorf("Should not be supported without %s", sysfsNodePath)
			}
		}
	} else {
		if supported {
			t.Error("Should not be supported on non-Linux")
		}
	}
}

func TestParseNumaStat(t *testing.T) {
	t.Parallel()

	input := `numa_hit 8500000000
numa_miss 1500000000
numa_foreign 200000
interleave_hit 12345
local_node 8450000000
other_node 50000000
`
	tmpFile, err := os.CreateTemp("", "numastat-*")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()

	_, err = tmpFile.WriteString(input)
	if err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	_ = tmpFile.Close()

	metrics, err := parseNumaStatFile(tmpFile.Name())
	if err != nil {
		t.Fatalf("parseNumaStatFile returned error: %v", err)
	}

	if metrics.NumaHit != 8500000000 {
		t.Errorf("expected NumaHit=8500000000, got %d", metrics.NumaHit)
	}
	if metrics.NumaMiss != 1500000000 {
		t.Errorf("expected NumaMiss=1500000000, got %d", metrics.NumaMiss)
	}
	if metrics.NumaForeign != 200000 {
		t.Errorf("expected NumaForeign=200000, got %d", metrics.NumaForeign)
	}
	if metrics.LocalNode != 8450000000 {
		t.Errorf("expected LocalNode=8450000000, got %d", metrics.LocalNode)
	}
	if metrics.OtherNode != 50000000 {
		t.Errorf("expected OtherNode=50000000, got %d", metrics.OtherNode)
	}

	// miss_ratio_pct = (1500000000 / (8500000000 + 1500000000)) * 100
	// 1500000000 / 10000000000 = 0.15 = 15.00
	if metrics.NodeMissRatioPct != 15.00 {
		t.Errorf("expected NodeMissRatioPct=15.00, got %f", metrics.NodeMissRatioPct)
	}
}

func TestGetNumaStats_SingleNode(t *testing.T) {
	originalPath := sysfsNodePath
	defer func() { sysfsNodePath = originalPath }()

	tmpDir, err := os.MkdirTemp("", "sysfs_node_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	sysfsNodePath = tmpDir

	node0Dir := filepath.Join(tmpDir, "node0")
	if err := os.Mkdir(node0Dir, 0755); err != nil {
		t.Fatalf("failed to create node0 dir: %v", err)
	}

	err = os.WriteFile(filepath.Join(node0Dir, "numastat"), []byte("numa_hit 1000\nnuma_miss 0\nlocal_node 1000\nother_node 0\n"), 0644)
	if err != nil {
		t.Fatalf("failed to write numastat: %v", err)
	}

	data, err := GetNumaStats()
	if err != nil {
		t.Fatalf("GetNumaStats returned error: %v", err)
	}

	if data.SystemSummary.TotalNumaNodes != 1 {
		t.Errorf("expected 1 node, got %d", data.SystemSummary.TotalNumaNodes)
	}
	if data.SystemSummary.IsNuma != false {
		t.Errorf("expected IsNuma=false, got %v", data.SystemSummary.IsNuma)
	}
	if data.SystemSummary.SystemMissRatioPct != 0.0 {
		t.Errorf("expected SystemMissRatioPct=0.0, got %f", data.SystemSummary.SystemMissRatioPct)
	}
	if data.SystemSummary.TotalSystemAllocations != 1000 {
		t.Errorf("expected TotalSystemAllocations=1000, got %d", data.SystemSummary.TotalSystemAllocations)
	}

	node0, ok := data.NodeMetrics["node_0"]
	if !ok {
		t.Fatalf("missing node_0 metrics")
	}
	if node0.NumaHit != 1000 {
		t.Errorf("expected node0 NumaHit=1000, got %d", node0.NumaHit)
	}
	if node0.NodeMissRatioPct != 0.0 {
		t.Errorf("expected node0 NodeMissRatioPct=0.0, got %f", node0.NodeMissRatioPct)
	}
}

func TestGetNumaStats_MultiNode(t *testing.T) {
	originalPath := sysfsNodePath
	defer func() { sysfsNodePath = originalPath }()

	tmpDir, err := os.MkdirTemp("", "sysfs_node_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	sysfsNodePath = tmpDir

	node0Dir := filepath.Join(tmpDir, "node0")
	_ = os.Mkdir(node0Dir, 0755)
	_ = os.WriteFile(filepath.Join(node0Dir, "numastat"), []byte("numa_hit 80\nnuma_miss 20\n"), 0644)

	node1Dir := filepath.Join(tmpDir, "node1")
	_ = os.Mkdir(node1Dir, 0755)
	_ = os.WriteFile(filepath.Join(node1Dir, "numastat"), []byte("numa_hit 90\nnuma_miss 10\n"), 0644)

	data, err := GetNumaStats()
	if err != nil {
		t.Fatalf("GetNumaStats returned error: %v", err)
	}

	if data.SystemSummary.TotalNumaNodes != 2 {
		t.Errorf("expected 2 nodes, got %d", data.SystemSummary.TotalNumaNodes)
	}
	if data.SystemSummary.IsNuma != true {
		t.Errorf("expected IsNuma=true, got %v", data.SystemSummary.IsNuma)
	}

	// Total hits = 80 + 90 = 170
	// Total misses = 20 + 10 = 30
	// System ratio = 30 / 200 = 0.15 = 15.00
	if data.SystemSummary.SystemMissRatioPct != 15.00 {
		t.Errorf("expected SystemMissRatioPct=15.00, got %f", data.SystemSummary.SystemMissRatioPct)
	}
	if data.SystemSummary.TotalSystemAllocations != 200 {
		t.Errorf("expected TotalSystemAllocations=200, got %d", data.SystemSummary.TotalSystemAllocations)
	}

	node0 := data.NodeMetrics["node_0"]
	if node0.NodeMissRatioPct != 20.00 {
		t.Errorf("expected node_0 miss ratio 20.00, got %f", node0.NodeMissRatioPct)
	}

	node1 := data.NodeMetrics["node_1"]
	if node1.NodeMissRatioPct != 10.00 {
		t.Errorf("expected node_1 miss ratio 10.00, got %f", node1.NodeMissRatioPct)
	}
}

func TestNumaStatsTool_ExecuteSchema(t *testing.T) {
	originalPath := sysfsNodePath
	defer func() { sysfsNodePath = originalPath }()

	tmpDir, _ := os.MkdirTemp("", "sysfs_node_*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	sysfsNodePath = tmpDir
	node0Dir := filepath.Join(tmpDir, "node0")
	_ = os.Mkdir(node0Dir, 0755)
	_ = os.WriteFile(filepath.Join(node0Dir, "numastat"), []byte("numa_hit 1000\nnuma_miss 0\n"), 0644)

	tool := &NumaStatsTool{}
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

	requiredFields := []string{"system_summary", "node_metrics"}
	for _, field := range requiredFields {
		if _, ok := data[field]; !ok {
			t.Errorf("Data missing required field: %s", field)
		}
	}
}

func TestNumaStatsTool_ExecuteError(t *testing.T) {
	originalPath := sysfsNodePath
	defer func() { sysfsNodePath = originalPath }()
	sysfsNodePath = "/does/not/exist/sys/devices/system/node"

	tool := &NumaStatsTool{}
	result, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute should not return a hard error, got: %v", err)
	}

	if result.Status != registry.StatusError {
		t.Errorf("Status = %q, want %q", result.Status, registry.StatusError)
	}
}

func BenchmarkParseNumaStatFile(b *testing.B) {
	tmpFile, _ := os.CreateTemp("", "numastat-*")
	defer func() { _ = os.Remove(tmpFile.Name()) }()
	_, _ = tmpFile.WriteString("numa_hit 8500000000\nnuma_miss 1500000000\nnuma_foreign 200000\nlocal_node 8450000000\nother_node 50000000\n")
	_ = tmpFile.Close()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, _ = parseNumaStatFile(tmpFile.Name())
	}
}

func BenchmarkGetNumaStats(b *testing.B) {
	originalPath := sysfsNodePath
	defer func() { sysfsNodePath = originalPath }()

	tmpDir, _ := os.MkdirTemp("", "sysfs_node_*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	sysfsNodePath = tmpDir
	node0Dir := filepath.Join(tmpDir, "node0")
	_ = os.Mkdir(node0Dir, 0755)
	_ = os.WriteFile(filepath.Join(node0Dir, "numastat"), []byte("numa_hit 1000\nnuma_miss 0\n"), 0644)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, _ = GetNumaStats()
	}
}

func BenchmarkName(b *testing.B) {
	tool := &NumaStatsTool{}
	for i := 0; i < b.N; i++ {
		_ = tool.Name()
	}
}

func BenchmarkDescription(b *testing.B) {
	tool := &NumaStatsTool{}
	for i := 0; i < b.N; i++ {
		_ = tool.Description()
	}
}

func BenchmarkHelp(b *testing.B) {
	tool := &NumaStatsTool{}
	for i := 0; i < b.N; i++ {
		_ = tool.Help()
	}
}

func BenchmarkCategory(b *testing.B) {
	tool := &NumaStatsTool{}
	for i := 0; i < b.N; i++ {
		_ = tool.Category()
	}
}

func BenchmarkParameters(b *testing.B) {
	tool := &NumaStatsTool{}
	for i := 0; i < b.N; i++ {
		_ = tool.Parameters()
	}
}

func BenchmarkHidden(b *testing.B) {
	tool := &NumaStatsTool{}
	for i := 0; i < b.N; i++ {
		_ = tool.Hidden()
	}
}

func BenchmarkIsSupported(b *testing.B) {
	tool := &NumaStatsTool{}
	for i := 0; i < b.N; i++ {
		_, _ = tool.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	originalPath := sysfsNodePath
	defer func() { sysfsNodePath = originalPath }()

	tmpDir, _ := os.MkdirTemp("", "sysfs_node_*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	sysfsNodePath = tmpDir
	node0Dir := filepath.Join(tmpDir, "node0")
	_ = os.Mkdir(node0Dir, 0755)
	_ = os.WriteFile(filepath.Join(node0Dir, "numastat"), []byte("numa_hit 1000\nnuma_miss 0\n"), 0644)

	tool := &NumaStatsTool{}
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, nil)
	}
}
