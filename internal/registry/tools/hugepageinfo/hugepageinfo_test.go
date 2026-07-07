package hugepageinfo

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func TestTool_Contract(t *testing.T) {
	tool := New()

	if tool.Name() != "get_hugepage_info" {
		t.Errorf("expected name 'get_hugepage_info', got '%s'", tool.Name())
	}
	if tool.Category() != registry.CategoryMemory {
		t.Errorf("expected category 'memory', got '%s'", tool.Category())
	}
	if tool.Description() == "" {
		t.Error("expected non-empty description")
	}
	if tool.Help() == "" {
		t.Error("expected non-empty help")
	}
	if params := tool.Parameters(); params != nil {
		t.Errorf("expected nil parameters, got %v", params)
	}
}

func TestTool_Execute(t *testing.T) {
	tool := New()

	// Check IsSupported first
	supported, reason := tool.IsSupported()
	if !supported {
		t.Skipf("skipping execute test: %s", reason)
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.Status == registry.StatusError {
		t.Fatalf("unexpected tool error: %s", string(res.Data))
	}

	// Validate JSON schema
	var data map[string]interface{}
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}

	// Check core schema elements
	if _, ok := data["system_summary"]; !ok {
		t.Error("missing system_summary in JSON")
	}
	if _, ok := data["static_hugepages"]; !ok {
		t.Error("missing static_hugepages in JSON")
	}
}

func BenchmarkNew(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = New()
	}
}

func BenchmarkName(b *testing.B) {
	tool := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.Name()
	}
}

func BenchmarkDescription(b *testing.B) {
	tool := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.Description()
	}
}

func BenchmarkHelp(b *testing.B) {
	tool := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.Help()
	}
}

func BenchmarkCategory(b *testing.B) {
	tool := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.Category()
	}
}

func BenchmarkParameters(b *testing.B) {
	tool := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.Parameters()
	}
}

func BenchmarkHidden(b *testing.B) {
	tool := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.Hidden()
	}
}

func BenchmarkIsSupported(b *testing.B) {
	tool := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	tool := New()
	supported, _ := tool.IsSupported()
	if !supported {
		b.Skip("skipping benchmark: tool unsupported")
	}

	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, nil)
	}
}

// --- Enhancement: per-NUMA-node hugepage breakdown ---

// nodePool describes one hugepage size pool on one fake NUMA node.
type nodePool struct {
	node    int
	sizeKB  string // directory suffix, e.g. "2048"
	total   string // nr_hugepages content; "" omits the file
	free    string // free_hugepages content; "" omits the file
	surplus string // surplus_hugepages content; "" omits the file
}

// setupNodeSysfs builds a fake /sys/devices/system/node tree with per-node
// hugepage pools and points the package at it for the duration of the test.
func setupNodeSysfs(t *testing.T, pools []nodePool, bareNodes ...int) string {
	t.Helper()
	dir := t.TempDir()

	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	for _, p := range pools {
		sizeDir := filepath.Join(dir, fmt.Sprintf("node%d", p.node), "hugepages", fmt.Sprintf("hugepages-%skB", p.sizeKB))
		if err := os.MkdirAll(sizeDir, 0755); err != nil {
			t.Fatal(err)
		}
		if p.total != "" {
			write(filepath.Join(sizeDir, "nr_hugepages"), p.total)
		}
		if p.free != "" {
			write(filepath.Join(sizeDir, "free_hugepages"), p.free)
		}
		if p.surplus != "" {
			write(filepath.Join(sizeDir, "surplus_hugepages"), p.surplus)
		}
	}

	// Nodes that exist but expose no hugepages directory (old kernels).
	for _, n := range bareNodes {
		if err := os.MkdirAll(filepath.Join(dir, fmt.Sprintf("node%d", n)), 0755); err != nil {
			t.Fatal(err)
		}
	}

	old := sysDevicesSystemNodePath
	sysDevicesSystemNodePath = dir
	t.Cleanup(func() { sysDevicesSystemNodePath = old })

	return dir
}

// executeRaw runs the tool (skipping when the host lacks /proc/meminfo, in
// line with TestTool_Execute) and returns the raw JSON payload.
func executeRaw(t *testing.T, tool *Tool) (*registry.ToolResult, map[string]interface{}) {
	t.Helper()

	supported, reason := tool.IsSupported()
	if !supported {
		t.Skipf("skipping execute test: %s", reason)
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(res.Data, &raw); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}
	return res, raw
}

// perNodeEntries extracts the per_node array, failing the test when absent.
func perNodeEntries(t *testing.T, raw map[string]interface{}) []map[string]interface{} {
	t.Helper()
	listRaw, ok := raw["per_node"]
	if !ok {
		t.Fatalf("expected per_node block in payload, got keys %v", payloadKeys(raw))
	}
	list, ok := listRaw.([]interface{})
	if !ok {
		t.Fatalf("expected per_node to be an array, got %T", listRaw)
	}
	entries := make([]map[string]interface{}, 0, len(list))
	for i, e := range list {
		entry, ok := e.(map[string]interface{})
		if !ok {
			t.Fatalf("per_node[%d] is %T, want object", i, e)
		}
		entries = append(entries, entry)
	}
	return entries
}

func payloadKeys(raw map[string]interface{}) []string {
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestTool_PerNode_Breakdown(t *testing.T) {
	setupNodeSysfs(t, []nodePool{
		// Deliberately created out of order to pin sorting by node then size.
		{node: 1, sizeKB: "2048", total: "512\n", free: "512\n", surplus: "0\n"},
		{node: 0, sizeKB: "1048576", total: "2\n", free: "1\n", surplus: "0\n"},
		{node: 0, sizeKB: "2048", total: "512\n", free: "128\n", surplus: "4\n"},
	})

	res, raw := executeRaw(t, New())
	entries := perNodeEntries(t, raw)

	if len(entries) != 3 {
		t.Fatalf("len(per_node) = %d, want 3", len(entries))
	}

	want := []struct {
		node, sizeKB, total, free, surplus float64
	}{
		// Sorted by node, then numerically by size (2048 < 1048576).
		{0, 2048, 512, 128, 4},
		{0, 1048576, 2, 1, 0},
		{1, 2048, 512, 512, 0},
	}
	for i, w := range want {
		e := entries[i]
		if e["node"] != w.node {
			t.Errorf("per_node[%d].node = %v, want %v", i, e["node"], w.node)
		}
		if e["size_kb"] != w.sizeKB {
			t.Errorf("per_node[%d].size_kb = %v, want %v", i, e["size_kb"], w.sizeKB)
		}
		if e["total"] != w.total {
			t.Errorf("per_node[%d].total = %v, want %v", i, e["total"], w.total)
		}
		if e["free"] != w.free {
			t.Errorf("per_node[%d].free = %v, want %v", i, e["free"], w.free)
		}
		if e["surplus"] != w.surplus {
			t.Errorf("per_node[%d].surplus = %v, want %v", i, e["surplus"], w.surplus)
		}
	}

	// Free pages remain on every node for every size: no imbalance warning.
	if res.Status != registry.StatusOK {
		t.Errorf("Status = %q, want ok", res.Status)
	}
	if _, ok := raw["warning_reasons"]; ok {
		t.Errorf("expected no warning_reasons, got %v", raw["warning_reasons"])
	}

	// Existing top-level blocks must still be present alongside per_node.
	if _, ok := raw["system_summary"]; !ok {
		t.Error("missing system_summary in JSON")
	}
	if _, ok := raw["static_hugepages"]; !ok {
		t.Error("missing static_hugepages in JSON")
	}
}

func TestTool_PerNode_ImbalanceWarning(t *testing.T) {
	setupNodeSysfs(t, []nodePool{
		{node: 0, sizeKB: "2048", total: "1024\n", free: "0\n", surplus: "0\n"},
		{node: 1, sizeKB: "2048", total: "1024\n", free: "768\n", surplus: "0\n"},
	})

	res, raw := executeRaw(t, New())

	if res.Status != registry.StatusWarning {
		t.Fatalf("Status = %q, want warning (node 0 exhausted while node 1 has free pages)", res.Status)
	}

	want := "HugePages exhausted on node 0 while node 1 has 768 free — NUMA-pinned allocations may fail"
	warnings, ok := raw["warning_reasons"].([]interface{})
	if !ok || len(warnings) != 1 || warnings[0] != want {
		t.Errorf("warning_reasons = %v, want [%q]", raw["warning_reasons"], want)
	}
	if res.Summary != want {
		t.Errorf("Summary = %q, want %q", res.Summary, want)
	}
}

func TestTool_PerNode_NoImbalanceWhenAllExhausted(t *testing.T) {
	setupNodeSysfs(t, []nodePool{
		// Every node exhausted: nowhere to steal from, so no imbalance warning.
		{node: 0, sizeKB: "2048", total: "1024\n", free: "0\n", surplus: "0\n"},
		{node: 1, sizeKB: "2048", total: "1024\n", free: "0\n", surplus: "0\n"},
	})

	res, raw := executeRaw(t, New())

	if res.Status != registry.StatusOK {
		t.Errorf("Status = %q, want ok", res.Status)
	}
	if _, ok := raw["warning_reasons"]; ok {
		t.Errorf("expected no warning_reasons, got %v", raw["warning_reasons"])
	}
}

func TestTool_PerNode_AbsentOmitsBlock(t *testing.T) {
	// No node directories at all (UMA / old kernels without per-node sysfs).
	setupNodeSysfs(t, nil)

	res, raw := executeRaw(t, New())

	if _, ok := raw["per_node"]; ok {
		t.Errorf("expected per_node to be omitted entirely, got %v", raw["per_node"])
	}
	if res.Status != registry.StatusOK {
		t.Errorf("Status = %q, want ok", res.Status)
	}
}

func TestTool_PerNode_NodesWithoutHugepagesOmitBlock(t *testing.T) {
	// Node dirs exist, but none exposes a hugepages directory (old kernels).
	setupNodeSysfs(t, nil, 0, 1)

	_, raw := executeRaw(t, New())

	if _, ok := raw["per_node"]; ok {
		t.Errorf("expected per_node to be omitted entirely, got %v", raw["per_node"])
	}
}

func TestTool_PerNode_PartialDataIncluded(t *testing.T) {
	dir := setupNodeSysfs(t, []nodePool{
		// Only nr_hugepages is readable; missing files are reported as 0.
		{node: 0, sizeKB: "2048", total: "42\n"},
	}, 1)

	// A malformed size directory must be ignored, not break the scan.
	if err := os.MkdirAll(filepath.Join(dir, "node0", "hugepages", "hugepages-bogus"), 0755); err != nil {
		t.Fatal(err)
	}

	res, raw := executeRaw(t, New())
	entries := perNodeEntries(t, raw)

	if len(entries) != 1 {
		t.Fatalf("len(per_node) = %d, want 1 (only readable pools included)", len(entries))
	}
	e := entries[0]
	if e["node"] != float64(0) || e["size_kb"] != float64(2048) {
		t.Errorf("per_node[0] identity = (%v, %v), want (0, 2048)", e["node"], e["size_kb"])
	}
	if e["total"] != float64(42) {
		t.Errorf("per_node[0].total = %v, want 42", e["total"])
	}
	if e["free"] != float64(0) || e["surplus"] != float64(0) {
		t.Errorf("per_node[0] free/surplus = (%v, %v), want (0, 0) for unreadable files", e["free"], e["surplus"])
	}

	// free==0 && total>0 on node 0, but no other node has free pages of that
	// size, so this must not trigger the imbalance warning.
	if res.Status != registry.StatusOK {
		t.Errorf("Status = %q, want ok", res.Status)
	}
}

// setupNodeSysfsBench builds a minimal two-node tree for benchmarks.
func setupNodeSysfsBench(b *testing.B) string {
	b.Helper()
	dir := b.TempDir()
	for node := 0; node < 2; node++ {
		sizeDir := filepath.Join(dir, fmt.Sprintf("node%d", node), "hugepages", "hugepages-2048kB")
		if err := os.MkdirAll(sizeDir, 0755); err != nil {
			b.Fatal(err)
		}
		for name, content := range map[string]string{
			"nr_hugepages":      "512\n",
			"free_hugepages":    "128\n",
			"surplus_hugepages": "0\n",
		} {
			if err := os.WriteFile(filepath.Join(sizeDir, name), []byte(content), 0644); err != nil {
				b.Fatal(err)
			}
		}
	}
	return dir
}

func BenchmarkCollectPerNodeHugePages(b *testing.B) {
	old := sysDevicesSystemNodePath
	sysDevicesSystemNodePath = setupNodeSysfsBench(b)
	defer func() { sysDevicesSystemNodePath = old }()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = collectPerNodeHugePages()
	}
}

func BenchmarkReadHugeCount(b *testing.B) {
	dir := b.TempDir()
	path := filepath.Join(dir, "nr_hugepages")
	if err := os.WriteFile(path, []byte("512\n"), 0644); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = readHugeCount(path)
	}
}

func BenchmarkEvaluateNodeImbalance(b *testing.B) {
	perNode := []NodeHugePages{
		{Node: 0, SizeKB: 2048, Total: 1024, Free: 0},
		{Node: 1, SizeKB: 2048, Total: 1024, Free: 768},
		{Node: 0, SizeKB: 1048576, Total: 2, Free: 1},
		{Node: 1, SizeKB: 1048576, Total: 2, Free: 2},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = evaluateNodeImbalance(perNode)
	}
}
