package cpu

import (
	"context"
	"os"
	"testing"
)

func BenchmarkParseCPUList(b *testing.B) {
	input := "0-7,16-23,32,34,36"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ParseCPUList(input)
	}
}

func BenchmarkGetTopology(b *testing.B) {
	files := map[string]string{
		"devices/system/node/node0/cpulist": "0,16",
		"devices/system/node/node1/cpulist": "1,17",

		"devices/system/cpu/cpu0/topology/physical_package_id":  "0",
		"devices/system/cpu/cpu0/topology/core_id":              "0",
		"devices/system/cpu/cpu0/topology/thread_siblings_list": "0,16",
		"devices/system/cpu/cpu0/cache/index3/shared_cpu_list":  "0,16",

		"devices/system/cpu/cpu16/topology/physical_package_id":  "0",
		"devices/system/cpu/cpu16/topology/core_id":              "0",
		"devices/system/cpu/cpu16/topology/thread_siblings_list": "0,16",
		"devices/system/cpu/cpu16/cache/index3/shared_cpu_list":  "0,16",

		"devices/system/cpu/cpu1/topology/physical_package_id":  "1",
		"devices/system/cpu/cpu1/topology/core_id":              "1",
		"devices/system/cpu/cpu1/topology/thread_siblings_list": "1,17",
		"devices/system/cpu/cpu1/cache/index3/shared_cpu_list":  "1,17",

		"devices/system/cpu/cpu17/topology/physical_package_id":  "1",
		"devices/system/cpu/cpu17/topology/core_id":              "1",
		"devices/system/cpu/cpu17/topology/thread_siblings_list": "1,17",
		"devices/system/cpu/cpu17/cache/index3/shared_cpu_list":  "1,17",
	}

	base := createMockSysfs(nil, files) // nil T because we just want the base path
	defer func() {
		// Cleanup since we used nil T
		_ = os.RemoveAll(base)
	}()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = GetTopology(context.Background(), base)
	}
}

func BenchmarkReadFileWithContext(b *testing.B) {
	base := createMockSysfs(nil, map[string]string{
		"testfile": "hello world",
	})
	defer func() { _ = os.RemoveAll(base) }()
	path := base + "/testfile"
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = readFileWithContext(ctx, path)
	}
}
