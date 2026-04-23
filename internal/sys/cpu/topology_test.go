package cpu

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// createMockSysfs creates a temporary directory with the specified file structure
// and returns its path.
func createMockSysfs(t *testing.T, files map[string]string) string {
	t.Helper()
	base := t.TempDir()
	for path, content := range files {
		fullPath := filepath.Join(base, path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			t.Fatalf("failed to create dir: %v", err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write file: %v", err)
		}
	}
	return base
}

func TestParseCPUList(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input    string
		expected []int
	}{
		{"", nil},
		{"0", []int{0}},
		{"0-3", []int{0, 1, 2, 3}},
		{"0-3,16-19", []int{0, 1, 2, 3, 16, 17, 18, 19}},
		{"0,2,4-5", []int{0, 2, 4, 5}},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			res := ParseCPUList(tc.input)
			if len(res) == 0 && len(tc.expected) == 0 {
				return
			}
			if !reflect.DeepEqual(res, tc.expected) {
				t.Errorf("ParseCPUList(%q) = %v, expected %v", tc.input, res, tc.expected)
			}
		})
	}
}

func TestGetTopology_NUMA(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"devices/system/node/node0/cpulist": "0,16",
		"devices/system/node/node1/cpulist": "1,17",

		// Node 0
		"devices/system/cpu/cpu0/topology/physical_package_id":  "0",
		"devices/system/cpu/cpu0/topology/core_id":              "0",
		"devices/system/cpu/cpu0/topology/thread_siblings_list": "0,16",
		"devices/system/cpu/cpu0/cache/index3/shared_cpu_list":  "0,16",

		"devices/system/cpu/cpu16/topology/physical_package_id":  "0",
		"devices/system/cpu/cpu16/topology/core_id":              "0",
		"devices/system/cpu/cpu16/topology/thread_siblings_list": "0,16",
		"devices/system/cpu/cpu16/cache/index3/shared_cpu_list":  "0,16",

		// Node 1
		"devices/system/cpu/cpu1/topology/physical_package_id":  "1",
		"devices/system/cpu/cpu1/topology/core_id":              "1",
		"devices/system/cpu/cpu1/topology/thread_siblings_list": "1,17",
		"devices/system/cpu/cpu1/cache/index3/shared_cpu_list":  "1,17",

		"devices/system/cpu/cpu17/topology/physical_package_id":  "1",
		"devices/system/cpu/cpu17/topology/core_id":              "1",
		"devices/system/cpu/cpu17/topology/thread_siblings_list": "1,17",
		"devices/system/cpu/cpu17/cache/index3/shared_cpu_list":  "1,17",
	}

	base := createMockSysfs(t, files)
	topo, err := GetTopology(context.Background(), base)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if !topo.SystemSummary.IsNuma {
		t.Error("expected IsNuma to be true")
	}
	if !topo.SystemSummary.SMTEnabled {
		t.Error("expected SMTEnabled to be true")
	}
	if topo.SystemSummary.TotalLogicalCPUs != 4 {
		t.Errorf("expected 4 logical cpus, got %d", topo.SystemSummary.TotalLogicalCPUs)
	}
	if topo.SystemSummary.TotalNumaNodes != 2 {
		t.Errorf("expected 2 numa nodes, got %d", topo.SystemSummary.TotalNumaNodes)
	}

	if node0, ok := topo.Topology["numa_node_0"]; !ok {
		t.Error("expected numa_node_0")
	} else if len(node0.L3CacheDomains) != 1 {
		t.Errorf("expected 1 l3 cache domain in node 0, got %d", len(node0.L3CacheDomains))
	}
}

func TestGetTopology_UMA_Abstracted(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		// NO node entries -> UMA

		// Missing thread siblings and cache -> virtualized
		"devices/system/cpu/cpu0/topology/physical_package_id": "0",
		"devices/system/cpu/cpu0/topology/core_id":             "0",
		"devices/system/cpu/cpu1/topology/physical_package_id": "0",
		"devices/system/cpu/cpu1/topology/core_id":             "1",
	}

	base := createMockSysfs(t, files)
	topo, err := GetTopology(context.Background(), base)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if topo.SystemSummary.IsNuma {
		t.Error("expected IsNuma to be false")
	}
	if topo.SystemSummary.SMTEnabled {
		t.Error("expected SMTEnabled to be false")
	}
	if topo.SystemSummary.TotalLogicalCPUs != 2 {
		t.Errorf("expected 2 logical cpus, got %d", topo.SystemSummary.TotalLogicalCPUs)
	}
	if topo.SystemSummary.TotalNumaNodes != 1 {
		t.Errorf("expected 1 numa node, got %d", topo.SystemSummary.TotalNumaNodes)
	}
	if !topo.SystemSummary.L3TopologyAbstracted {
		t.Error("expected L3TopologyAbstracted to be true")
	}

	if node0, ok := topo.Topology["numa_node_0"]; !ok {
		t.Error("expected numa_node_0")
	} else if len(node0.L3CacheDomains) != 1 {
		t.Errorf("expected 1 l3 cache domain in node 0, got %d", len(node0.L3CacheDomains))
	}
}
