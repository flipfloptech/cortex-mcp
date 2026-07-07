package slabinfo

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func TestSlabInfoTool_Contract(t *testing.T) {
	tool := &SlabInfoTool{}
	if tool.Name() != "get_slab_info" {
		t.Errorf("expected get_slab_info, got %s", tool.Name())
	}
	if tool.Category() != registry.CategoryMemory {
		t.Errorf("expected memory, got %s", tool.Category())
	}
	if tool.Description() == "" {
		t.Error("description should not be empty")
	}
	if tool.Help() == "" {
		t.Error("help should not be empty")
	}
	if tool.Parameters() != nil {
		t.Error("parameters should be nil")
	}
	if tool.Hidden() {
		t.Error("tool should not be hidden")
	}
}

func TestSlabInfoTool_IsSupported(t *testing.T) {
	tool := &SlabInfoTool{}
	if registry.IsLinux() {
		// Just creating a dummy file
		dir := t.TempDir()
		slabInfoPath = filepath.Join(dir, "slabinfo")

		ok, msg := tool.IsSupported()
		if ok {
			t.Errorf("expected false because file is missing, got %v: %s", ok, msg)
		}

		_ = os.WriteFile(slabInfoPath, []byte(""), 0644)
		ok, _ = tool.IsSupported()
		if !ok {
			t.Errorf("expected true when file exists")
		}

	} else {
		ok, msg := tool.IsSupported()
		if ok {
			t.Errorf("expected false on non-Linux, got %v", ok)
		}
		if msg == "" {
			t.Errorf("expected error message on non-Linux")
		}
	}
}

func TestParseSlabInfo(t *testing.T) {
	content := `slabinfo - version: 2.1
# name            <active_objs> <num_objs> <objsize> <objperslab> <pagesperslab> : tunables <limit> <batchcount> <sharedfactor> : slabdata <active_slabs> <num_slabs> <sharedavail>
dentry            45000000 45100000    192   21    1 : tunables    0    0    0 : slabdata 2147619 2147619      0
xfs_inode         18000000 18500000    960   34    8 : tunables    0    0    0 : slabdata 544117 544117      0
kmalloc-8192      1200 85000   8192    4    8 : tunables    0    0    0 : slabdata  21250  21250      0
buffer_head       20000 20000     104   39    1 : tunables    0    0    0 : slabdata    512    512      0
radix_tree_node   30000 30000     584   14    2 : tunables    0    0    0 : slabdata   2142   2142      0
anon_vma          10000 10000      88   46    1 : tunables    0    0    0 : slabdata    217    217      0
kmalloc-256       15000 15000     256   16    1 : tunables    0    0    0 : slabdata    937    937      0
kmalloc-512       8000 8000     512   16    2 : tunables    0    0    0 : slabdata    500    500      0
task_struct       500 500    3968    8    8 : tunables    0    0    0 : slabdata     62     62      0
filp              4000 4000     256   16    1 : tunables    0    0    0 : slabdata    250    250      0
shmem_inode_cache 1000 1000     712   22    4 : tunables    0    0    0 : slabdata     45     45      0
signal_cache      300 300    1088   15    4 : tunables    0    0    0 : slabdata     20     20      0
sighand_cache     300 300    2112   15    8 : tunables    0    0    0 : slabdata     20     20      0
pid               2000 2000     128   32    1 : tunables    0    0    0 : slabdata     62     62      0
fs_cache          1000 1000      64   64    1 : tunables    0    0    0 : slabdata     15     15      0
files_cache       1000 1000     704   23    4 : tunables    0    0    0 : slabdata     43     43      0
invalid_line      too short
`

	reader := bytes.NewReader([]byte(content))
	data, err := ParseSlabInfo(reader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if data.SystemSummary.TotalSlabCachesDetected != 16 { // 16 valid lines
		t.Errorf("expected 16 caches detected, got %d", data.SystemSummary.TotalSlabCachesDetected)
	}

	if len(data.TopSlabs) != 15 { // Truncated
		t.Errorf("expected 15 top slabs, got %d", len(data.TopSlabs))
	}

	// Calculate dentry: 45100000 * 192 = 8659200000 bytes = 8258.1 MB
	var dentry *SlabEntry
	for i := range data.TopSlabs {
		if data.TopSlabs[i].Name == "dentry" {
			dentry = &data.TopSlabs[i]
			break
		}
	}
	if dentry == nil {
		t.Fatalf("expected dentry in top slabs")
	}
	if dentry.TotalSizeMB != 8258.1 {
		t.Errorf("expected dentry 8258.1 MB, got %f", dentry.TotalSizeMB)
	}
	if dentry.FragmentationPct != 0.22 {
		t.Errorf("expected dentry fragmentation 0.22, got %f", dentry.FragmentationPct)
	}

	// Calculate kmalloc-8192 fragmentation: 85000 - 1200 = 83800. 83800 / 85000 * 100 = 98.588... -> 98.59
	// Wait, math.Round(98.588 * 100) / 100 = 98.59.
	// Find kmalloc-8192
	var kmalloc8192 *SlabEntry
	for i := range data.TopSlabs {
		if data.TopSlabs[i].Name == "kmalloc-8192" {
			kmalloc8192 = &data.TopSlabs[i]
			break
		}
	}
	if kmalloc8192 == nil {
		t.Fatalf("expected to find kmalloc-8192 in top slabs")
	}
	if kmalloc8192.FragmentationPct != 98.59 {
		t.Errorf("expected kmalloc-8192 fragmentation 98.59, got %f", kmalloc8192.FragmentationPct)
	}
}

func TestSlabInfoTool_Execute(t *testing.T) {
	tool := &SlabInfoTool{}
	dir := t.TempDir()
	slabInfoPath = filepath.Join(dir, "slabinfo")

	// Test missing file
	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute should not return a raw Go error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("expected error status for missing file, got %s", res.Status)
	}

	// Test success
	content := "dentry 100 100 192 1 1\n"
	_ = os.WriteFile(slabInfoPath, []byte(content), 0644)

	res, err = tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Errorf("expected OK status, got %s", res.Status)
	}

	var data SlabInfoData
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}
	if len(data.TopSlabs) != 1 {
		t.Errorf("expected 1 slab, got %d", len(data.TopSlabs))
	}
	if data.TopSlabs[0].Name != "dentry" {
		t.Errorf("expected dentry, got %s", data.TopSlabs[0].Name)
	}
}

func TestSlabInfoTool_Execute_PermissionDenied(t *testing.T) {
	tool := &SlabInfoTool{}
	dir := t.TempDir()
	slabInfoPath = filepath.Join(dir, "slabinfo")

	// Create a file without read permissions
	_ = os.WriteFile(slabInfoPath, []byte("dentry 100 100 192 1 1\n"), 0200)

	// In some environments, root runs tests, which bypasses 0200 perms.
	// We'll skip if we can still read it.
	f, err := os.Open(slabInfoPath)
	if err == nil {
		_ = f.Close()
		t.Skip("skipping permission denied test because process runs as root")
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected go error: %v", err)
	}

	if res.Status != registry.StatusError {
		t.Errorf("expected error status, got %s", res.Status)
	}
	if res.Summary != "Unauthorized: Root privileges required to read /proc/slabinfo" {
		t.Errorf("unexpected summary: %s", res.Summary)
	}
	if string(res.Data) != "null" {
		t.Errorf("expected null data array, got %s", string(res.Data))
	}
}

func BenchmarkParseSlabInfo(b *testing.B) {
	content := `slabinfo - version: 2.1
# name            <active_objs> <num_objs> <objsize> <objperslab> <pagesperslab> : tunables <limit> <batchcount> <sharedfactor> : slabdata <active_slabs> <num_slabs> <sharedavail>
dentry            45000000 45100000    192   21    1 : tunables    0    0    0 : slabdata 2147619 2147619      0
xfs_inode         18000000 18500000    960   34    8 : tunables    0    0    0 : slabdata 544117 544117      0
kmalloc-8192      1200 85000   8192    4    8 : tunables    0    0    0 : slabdata  21250  21250      0
buffer_head       20000 20000     104   39    1 : tunables    0    0    0 : slabdata    512    512      0
radix_tree_node   30000 30000     584   14    2 : tunables    0    0    0 : slabdata   2142   2142      0
anon_vma          10000 10000      88   46    1 : tunables    0    0    0 : slabdata    217    217      0
kmalloc-256       15000 15000     256   16    1 : tunables    0    0    0 : slabdata    937    937      0
`

	payload := []byte(content)
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		reader := bytes.NewReader(payload)
		_, _ = ParseSlabInfo(reader)
	}
}

func BenchmarkName(b *testing.B) {
	tool := &SlabInfoTool{}
	for i := 0; i < b.N; i++ {
		_ = tool.Name()
	}
}

func BenchmarkDescription(b *testing.B) {
	tool := &SlabInfoTool{}
	for i := 0; i < b.N; i++ {
		_ = tool.Description()
	}
}

func BenchmarkHelp(b *testing.B) {
	tool := &SlabInfoTool{}
	for i := 0; i < b.N; i++ {
		_ = tool.Help()
	}
}

func BenchmarkCategory(b *testing.B) {
	tool := &SlabInfoTool{}
	for i := 0; i < b.N; i++ {
		_ = tool.Category()
	}
}

func BenchmarkParameters(b *testing.B) {
	tool := &SlabInfoTool{}
	for i := 0; i < b.N; i++ {
		_ = tool.Parameters()
	}
}

func BenchmarkHidden(b *testing.B) {
	tool := &SlabInfoTool{}
	for i := 0; i < b.N; i++ {
		_ = tool.Hidden()
	}
}

func BenchmarkIsSupported(b *testing.B) {
	tool := &SlabInfoTool{}
	dir := b.TempDir()
	slabInfoPath = filepath.Join(dir, "slabinfo")
	_ = os.WriteFile(slabInfoPath, []byte(""), 0644)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = tool.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	tool := &SlabInfoTool{}
	dir := b.TempDir()
	slabInfoPath = filepath.Join(dir, "slabinfo")
	_ = os.WriteFile(slabInfoPath, []byte("dentry 100 100 192 1 1\n"), 0644)
	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, nil)
	}
}
