package cgrouplimits

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func TestCgroupLimitsTool_Contract(t *testing.T) {
	t.Parallel()
	tool := New()

	if tool.Name() != "get_cgroup_limits" {
		t.Errorf("expected name get_cgroup_limits, got %s", tool.Name())
	}
	if tool.Category() != "compute" {
		t.Errorf("expected category compute, got %s", tool.Category())
	}
	if tool.Description() == "" {
		t.Error("expected non-empty description")
	}
	if tool.Help() == "" {
		t.Error("expected non-empty help")
	}

	params := tool.Parameters()
	foundPid := false
	for _, p := range params {
		if p.Name == "target_pid" {
			foundPid = true
		}
	}
	if !foundPid {
		t.Error("expected target_pid parameter")
	}
}

func setupMockCgroupFs(t *testing.T, v2 bool, workloadManagers bool, isTargeted bool) (string, string) {
	t.Helper()
	tempDir := t.TempDir()

	cgroupDir := filepath.Join(tempDir, "sys", "fs", "cgroup")
	procDir := filepath.Join(tempDir, "proc")

	err := os.MkdirAll(cgroupDir, 0755)
	if err != nil {
		t.Fatal(err)
	}
	err = os.MkdirAll(procDir, 0755)
	if err != nil {
		t.Fatal(err)
	}

	if v2 {
		err = os.WriteFile(filepath.Join(cgroupDir, "cgroup.controllers"), []byte("cpu cpuset memory"), 0644)
		if err != nil {
			t.Fatal(err)
		}
	}

	if workloadManagers && v2 {
		slurmDir := filepath.Join(cgroupDir, "slurm", "job_123")
		err = os.MkdirAll(slurmDir, 0755)
		if err != nil {
			t.Fatal(err)
		}
		files := map[string][]byte{
			"cpu.max":        []byte("400000 100000\n"),
			"cpu.stat":       []byte("nr_throttled 5\nthrottled_usec 1450000\n"),
			"cpuset.cpus":    []byte("0-3\n"),
			"cpuset.mems":    []byte("0\n"),
			"memory.max":     []byte("17179869184\n"),
			"memory.current": []byte("17091805184\n"),
			"memory.events":  []byte("oom 0\noom_kill 1\n"),
		}
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(slurmDir, name), content, 0644); err != nil {
				t.Fatal(err)
			}
		}
	}

	if isTargeted && v2 {
		pidDir := filepath.Join(procDir, "14502")
		err = os.MkdirAll(pidDir, 0755)
		if err != nil {
			t.Fatal(err)
		}
		err = os.WriteFile(filepath.Join(pidDir, "cgroup"), []byte("0::/slurm/job_123\n"), 0644)
		if err != nil {
			t.Fatal(err)
		}
	}

	return cgroupDir, procDir
}

func TestCgroupLimitsTool_Targeted_V2(t *testing.T) {
	t.Parallel()
	cgroupDir, procDir := setupMockCgroupFs(t, true, true, true)

	tool := &CgroupLimitsTool{
		sysFsCgroupPath: cgroupDir,
		procPath:        procDir,
	}

	args := json.RawMessage(`{"target_pid": 14502}`)
	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status == registry.StatusError {
		t.Fatalf("tool returned error result: %v", string(res.Data))
	}

	var data CgroupLimitsResult
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("failed to unmarshal result data: %v", err)
	}

	if data.SystemSummary.CgroupVersion != "v2" {
		t.Errorf("expected v2, got %v", data.SystemSummary.CgroupVersion)
	}
	if data.SystemSummary.CgroupPath != "/slurm/job_123" {
		t.Errorf("expected /slurm/job_123, got %v", data.SystemSummary.CgroupPath)
	}
	if data.ResourceLimits == nil {
		t.Fatal("expected resource_limits to be non-nil")
	}

	if data.ResourceLimits.CPU.AllowedLogicalCores != 4.0 {
		t.Errorf("expected 4.0 cores, got %v", data.ResourceLimits.CPU.AllowedLogicalCores)
	}
	if !data.ResourceLimits.CPU.ThrottlingDetected {
		t.Error("expected throttling to be detected")
	}
	if data.ResourceLimits.CPU.ThrottledTimeMs != 1450 {
		t.Errorf("expected 1450ms throttled time, got %v", data.ResourceLimits.CPU.ThrottledTimeMs)
	}

	if len(data.ResourceLimits.Cpuset.AllowedCPUs) != 4 || data.ResourceLimits.Cpuset.AllowedCPUs[0] != 0 {
		t.Errorf("expected cpus [0,1,2,3], got %v", data.ResourceLimits.Cpuset.AllowedCPUs)
	}
	if len(data.ResourceLimits.Cpuset.AllowedNUMANodes) != 1 || data.ResourceLimits.Cpuset.AllowedNUMANodes[0] != 0 {
		t.Errorf("expected numa nodes [0], got %v", data.ResourceLimits.Cpuset.AllowedNUMANodes)
	}
	if !data.ResourceLimits.Cpuset.IsPinned {
		t.Error("expected is_pinned to be true")
	}

	if data.ResourceLimits.Memory.LimitMB != 16384 {
		t.Errorf("expected limit_mb 16384, got %v", data.ResourceLimits.Memory.LimitMB)
	}
	if data.ResourceLimits.Memory.OOMKillsDetected != 1 {
		t.Errorf("expected 1 oom_kill, got %v", data.ResourceLimits.Memory.OOMKillsDetected)
	}
}

func TestCgroupLimitsTool_Fallback_V1(t *testing.T) {
	t.Parallel()
	cgroupDir, procDir := setupMockCgroupFs(t, false, false, false)

	tool := &CgroupLimitsTool{
		sysFsCgroupPath: cgroupDir,
		procPath:        procDir,
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status == registry.StatusError {
		t.Fatalf("tool returned error result: %v", string(res.Data))
	}

	var data CgroupLimitsResult
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("failed to unmarshal result data: %v", err)
	}

	if data.SystemSummary.CgroupVersion != "v1" {
		t.Errorf("expected v1, got %v", data.SystemSummary.CgroupVersion)
	}
	if data.SystemSummary.IsSupported {
		t.Error("expected is_supported to be false for v1")
	}
	if data.SystemSummary.Message == "" {
		t.Error("expected a message for v1 fallback")
	}
}

func TestParseCPUList(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input    string
		expected []int
	}{
		{"0-3", []int{0, 1, 2, 3}},
		{"1,3,5", []int{1, 3, 5}},
		{"0-2,5", []int{0, 1, 2, 5}},
		{"", []int{}},
		{"max", []int{}},
	}

	for _, tc := range tests {
		actual := parseCPUList(tc.input)
		if len(actual) != len(tc.expected) {
			t.Errorf("input %s: expected length %d, got %d", tc.input, len(tc.expected), len(actual))
			continue
		}
		for i, v := range actual {
			if v != tc.expected[i] {
				t.Errorf("input %s: expected index %d to be %d, got %d", tc.input, i, tc.expected[i], v)
			}
		}
	}
}

func TestParseLimits_SubCore(t *testing.T) {
	t.Parallel()
	cgroupDir, _ := setupMockCgroupFs(t, true, false, false)

	slurmDir := filepath.Join(cgroupDir, "slurm", "job_123")
	if err := os.MkdirAll(slurmDir, 0755); err != nil {
		t.Fatal(err)
	}

	// CPUQuota=50% -> max 50000, period 100000
	if err := os.WriteFile(filepath.Join(slurmDir, "cpu.max"), []byte("50000 100000\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &CgroupLimitsTool{
		sysFsCgroupPath: cgroupDir,
		procPath:        "/proc",
	}

	limits, err := tool.parseLimits(slurmDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if limits.CPU.IsUnlimited {
		t.Error("expected IsUnlimited to be false for 50000 100000")
	}
	if limits.CPU.AllowedLogicalCores != 0.5 {
		t.Errorf("expected 0.5 cores, got %v", limits.CPU.AllowedLogicalCores)
	}
}

func TestParseLimits_Hierarchy(t *testing.T) {
	tempDir := t.TempDir()
	sysFs := filepath.Join(tempDir, "sys", "fs", "cgroup")
	if err := os.MkdirAll(sysFs, 0755); err != nil {
		t.Fatal(err)
	}

	systemSlice := filepath.Join(sysFs, "system.slice")
	scope := filepath.Join(systemSlice, "run-123.scope")
	if err := os.MkdirAll(scope, 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(sysFs, "cpuset.cpus"), []byte("0-3\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(systemSlice, "cpu.max"), []byte("50000 100000\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(scope, "memory.max"), []byte("536870912\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scope, "memory.current"), []byte("10485760\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &CgroupLimitsTool{
		sysFsCgroupPath: sysFs,
		procPath:        filepath.Join(tempDir, "proc"),
	}

	limits, err := tool.getEffectiveLimits(scope)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if limits.CPU.IsUnlimited {
		t.Errorf("Expected CPU to be limited")
	}
	if limits.CPU.AllowedLogicalCores != 0.5 {
		t.Errorf("Expected AllowedLogicalCores=0.5, got %v", limits.CPU.AllowedLogicalCores)
	}

	if limits.Memory.IsUnlimited {
		t.Errorf("Expected Memory to be limited")
	}
	if limits.Memory.LimitMB != 512 {
		t.Errorf("Expected LimitMB=512, got %d", limits.Memory.LimitMB)
	}
	if limits.Memory.CurrentUsageMB != 10 {
		t.Errorf("Expected CurrentUsageMB=10, got %d", limits.Memory.CurrentUsageMB)
	}

	if !limits.Cpuset.IsPinned {
		t.Errorf("Expected Cpuset IsPinned=true")
	}
	if len(limits.Cpuset.AllowedCPUs) != 4 {
		t.Errorf("Expected 4 allowed CPUs, got %d", len(limits.Cpuset.AllowedCPUs))
	}
}

func BenchmarkCgroupLimitsTool_ExecuteTargeted(b *testing.B) {
	cgroupDir, procDir := setupMockCgroupFs(&testing.T{}, true, true, true)
	tool := &CgroupLimitsTool{
		sysFsCgroupPath: cgroupDir,
		procPath:        procDir,
	}
	args := json.RawMessage(`{"target_pid": 14502}`)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, args)
	}
}

func BenchmarkGetEffectiveLimits(b *testing.B) {
	cgroupDir, procDir := setupMockCgroupFs(&testing.T{}, true, true, true)
	tool := &CgroupLimitsTool{sysFsCgroupPath: cgroupDir, procPath: procDir}
	slurmDir := filepath.Join(cgroupDir, "slurm", "job_14502")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.getEffectiveLimits(slurmDir)
	}
}

func BenchmarkCategory(b *testing.B) {
	tool := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.Category()
	}
}

func BenchmarkExecuteTargetedMode(b *testing.B) {
	cgroupDir, procDir := setupMockCgroupFs(&testing.T{}, true, true, true)
	tool := &CgroupLimitsTool{sysFsCgroupPath: cgroupDir, procPath: procDir}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.executeTargetedMode("host", time.Now(), 14502)
	}
}

func BenchmarkName(b *testing.B) {
	tool := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.Name()
	}
}

func BenchmarkHelp(b *testing.B) {
	tool := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.Help()
	}
}

func BenchmarkIsSupported(b *testing.B) {
	cgroupDir, procDir := setupMockCgroupFs(&testing.T{}, true, true, true)
	tool := &CgroupLimitsTool{sysFsCgroupPath: cgroupDir, procPath: procDir}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.IsSupported()
	}
}

func BenchmarkExecuteGlobalMode(b *testing.B) {
	cgroupDir, procDir := setupMockCgroupFs(&testing.T{}, true, true, true)
	tool := &CgroupLimitsTool{sysFsCgroupPath: cgroupDir, procPath: procDir}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.executeGlobalMode("host", time.Now())
	}
}

func BenchmarkDescription(b *testing.B) {
	tool := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.Description()
	}
}

func BenchmarkHidden(b *testing.B) {
	tool := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.Hidden()
	}
}

func BenchmarkParseLimits(b *testing.B) {
	cgroupDir, procDir := setupMockCgroupFs(&testing.T{}, true, true, true)
	tool := &CgroupLimitsTool{sysFsCgroupPath: cgroupDir, procPath: procDir}
	slurmDir := filepath.Join(cgroupDir, "slurm", "job_123")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.parseLimits(slurmDir)
	}
}

func BenchmarkParameters(b *testing.B) {
	tool := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.Parameters()
	}
}

func BenchmarkIsV2(b *testing.B) {
	cgroupDir, procDir := setupMockCgroupFs(&testing.T{}, true, true, true)
	tool := &CgroupLimitsTool{sysFsCgroupPath: cgroupDir, procPath: procDir}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.isV2()
	}
}

func BenchmarkExecute(b *testing.B) {
	cgroupDir, procDir := setupMockCgroupFs(&testing.T{}, true, true, true)
	tool := &CgroupLimitsTool{sysFsCgroupPath: cgroupDir, procPath: procDir}
	args := json.RawMessage(`{"target_pid": 14502}`)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, args)
	}
}

func BenchmarkExecuteFallbackV1(b *testing.B) {
	cgroupDir, procDir := setupMockCgroupFs(&testing.T{}, false, false, false)
	tool := &CgroupLimitsTool{sysFsCgroupPath: cgroupDir, procPath: procDir}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.executeFallbackV1("host", time.Now())
	}
}

func BenchmarkParseCPUList(b *testing.B) {
	val := "0-3,5,7-9"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseCPUList(val)
	}
}

func BenchmarkNew(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = New()
	}
}
