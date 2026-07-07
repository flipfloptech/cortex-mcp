package containerinventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// ---------------------------------------------------------------------------
// Test fixtures
// ---------------------------------------------------------------------------

const (
	dockerID     = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	podmanID     = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	containerdID = "aaaabbbbccccddddeeeeffff0000111122223333444455556666777788889999"
	crioID       = "9999888877776666555544443333222211110000ffffeeeeddddccccbbbbaaaa"
)

// writeContainerCgroup populates one container scope dir with metric files.
func writeContainerCgroup(t testing.TB, dir string, procs int, memBytes, cpuUsec int64) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var pids []string
	for i := 0; i < procs; i++ {
		pids = append(pids, fmt.Sprintf("%d", 1000+i))
	}
	content := ""
	if len(pids) > 0 {
		content = strings.Join(pids, "\n") + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.current"), []byte(fmt.Sprintf("%d\n", memBytes)), 0o644); err != nil {
		t.Fatal(err)
	}
	cpuStat := fmt.Sprintf("usage_usec %d\nuser_usec 1\nsystem_usec 2\n", cpuUsec)
	if err := os.WriteFile(filepath.Join(dir, "cpu.stat"), []byte(cpuStat), 0o644); err != nil {
		t.Fatal(err)
	}
}

// setupCgroupV2Tree creates a fake cgroup v2 root with one container per
// runtime and returns the root.
func setupCgroupV2Tree(t testing.TB) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "cgroup.controllers"), []byte("cpu memory pids\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	writeContainerCgroup(t,
		filepath.Join(root, "system.slice", "docker-"+dockerID+".scope"),
		3, 157286400, 12345678) // 150.0 MB, 12.3 s
	writeContainerCgroup(t,
		filepath.Join(root, "machine.slice", "libpod-"+podmanID+".scope"),
		1, 52428800, 500000) // 50.0 MB, 0.5 s
	writeContainerCgroup(t,
		filepath.Join(root, "kubepods.slice", "kubepods-burstable.slice",
			"kubepods-burstable-podxyz.slice", "cri-containerd-"+containerdID+".scope"),
		5, 1073741824, 90000000) // 1024.0 MB, 90.0 s
	writeContainerCgroup(t,
		filepath.Join(root, "kubepods.slice", "kubepods-besteffort.slice",
			"kubepods-besteffort-podabc.slice", "crio-"+crioID+".scope"),
		2, 10485760, 100000) // 10.0 MB, 0.1 s

	// Non-container noise that must be ignored.
	writeContainerCgroup(t, filepath.Join(root, "system.slice", "sshd.service"), 1, 1024, 10)
	return root
}

func noLookPath(string) (string, error) { return "", errors.New("not found") }

func findByRuntime(out Output, runtime string) (Container, bool) {
	for _, c := range out.Containers {
		if c.Runtime == runtime {
			return c, true
		}
	}
	return Container{}, false
}

// ---------------------------------------------------------------------------
// Contract compliance
// ---------------------------------------------------------------------------

func TestContainerInventoryTool_ContractCompliance(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "get_container_inventory" {
		t.Errorf("expected name get_container_inventory, got %q", tool.Name())
	}
	if tool.Category() != registry.CategoryCompute {
		t.Errorf("expected category compute, got %q", tool.Category())
	}
	if tool.Description() == "" {
		t.Error("expected non-empty description")
	}
	if tool.Hidden() {
		t.Error("expected Hidden() == false")
	}
	if len(tool.Parameters()) != 0 {
		t.Errorf("expected no parameters, got %d", len(tool.Parameters()))
	}

	help := tool.Help()
	for _, src := range []string{"/sys/fs/cgroup", "docker ps", "podman ps"} {
		if !strings.Contains(help, src) {
			t.Errorf("Help() must reference data source %q", src)
		}
	}
}

// ---------------------------------------------------------------------------
// IsSupported
// ---------------------------------------------------------------------------

func TestContainerInventoryTool_IsSupported(t *testing.T) {
	t.Parallel()

	t.Run("cgroup root exists", func(t *testing.T) {
		t.Parallel()
		tool := &Tool{cgroupRoot: t.TempDir(), execLookPath: noLookPath}
		if ok, _ := tool.IsSupported(); !ok {
			t.Error("expected supported when cgroup root exists")
		}
	})

	t.Run("cgroup root missing", func(t *testing.T) {
		t.Parallel()
		tool := &Tool{cgroupRoot: filepath.Join(t.TempDir(), "nope"), execLookPath: noLookPath}
		ok, reason := tool.IsSupported()
		if ok {
			t.Error("expected unsupported when cgroup root missing")
		}
		if reason == "" {
			t.Error("expected reason when unsupported")
		}
	})
}

// ---------------------------------------------------------------------------
// Execute: happy path (native, no enrichment binaries)
// ---------------------------------------------------------------------------

func TestContainerInventoryTool_Execute_Native(t *testing.T) {
	t.Parallel()
	root := setupCgroupV2Tree(t)
	tool := &Tool{
		cgroupRoot:   root,
		execLookPath: noLookPath,
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			t.Errorf("execCommand must not run when no runtime binaries are in PATH")
			return nil, errors.New("unexpected")
		},
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("expected ok, got %q (%s)", res.Status, res.Summary)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatal(err)
	}

	if out.Total != 4 || len(out.Containers) != 4 {
		t.Fatalf("expected 4 containers, got total=%d len=%d: %+v", out.Total, len(out.Containers), out.Containers)
	}

	docker, ok := findByRuntime(out, "docker")
	if !ok {
		t.Fatal("missing docker container")
	}
	if docker.IDShort != dockerID[:12] {
		t.Errorf("expected id_short %q, got %q", dockerID[:12], docker.IDShort)
	}
	if docker.Procs != 3 {
		t.Errorf("expected 3 procs, got %d", docker.Procs)
	}
	if docker.MemoryMB != 150.0 {
		t.Errorf("expected 150.0 memory_mb, got %v", docker.MemoryMB)
	}
	if docker.CPUUsageSeconds != 12.3 {
		t.Errorf("expected 12.3 cpu_usage_seconds, got %v", docker.CPUUsageSeconds)
	}
	if docker.Name != "" || docker.Image != "" {
		t.Errorf("no enrichment available: name/image must be empty, got %+v", docker)
	}

	podman, ok := findByRuntime(out, "podman")
	if !ok || podman.IDShort != podmanID[:12] || podman.MemoryMB != 50.0 || podman.CPUUsageSeconds != 0.5 {
		t.Errorf("unexpected podman container: %+v (found=%v)", podman, ok)
	}

	containerd, ok := findByRuntime(out, "containerd")
	if !ok || containerd.IDShort != containerdID[:12] || containerd.Procs != 5 || containerd.MemoryMB != 1024.0 {
		t.Errorf("unexpected containerd container: %+v (found=%v)", containerd, ok)
	}

	crio, ok := findByRuntime(out, "crio")
	if !ok || crio.IDShort != crioID[:12] || crio.MemoryMB != 10.0 || crio.CPUUsageSeconds != 0.1 {
		t.Errorf("unexpected crio container: %+v (found=%v)", crio, ok)
	}

	want := map[string]int{"docker": 1, "podman": 1, "containerd": 1, "crio": 1}
	for k, v := range want {
		if out.CountsByRuntime[k] != v {
			t.Errorf("counts_by_runtime[%s]: expected %d, got %d", k, v, out.CountsByRuntime[k])
		}
	}
}

func TestContainerInventoryTool_Execute_EmptyTreeIsValid(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "cgroup.controllers"), []byte("cpu\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &Tool{cgroupRoot: root, execLookPath: noLookPath}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("no container slices must be a valid empty result, got %q", res.Status)
	}
	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Total != 0 || len(out.Containers) != 0 {
		t.Errorf("expected empty inventory, got %+v", out)
	}
}

// ---------------------------------------------------------------------------
// Execute: cgroup v1 degraded fallback
// ---------------------------------------------------------------------------

func TestContainerInventoryTool_Execute_CgroupV1Fallback(t *testing.T) {
	t.Parallel()
	tool := &Tool{cgroupRoot: t.TempDir(), execLookPath: noLookPath} // no cgroup.controllers

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if res.Status != registry.StatusDegraded {
		t.Fatalf("expected degraded status for cgroup v1, got %q", res.Status)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(res.Data, &payload); err != nil {
		t.Fatal(err)
	}
	if sup, ok := payload["is_supported"].(bool); !ok || sup {
		t.Errorf("expected is_supported=false in payload, got %v", payload)
	}
	msg, _ := payload["message"].(string)
	if !strings.Contains(msg, "cgroup v1") {
		t.Errorf("expected message mentioning cgroup v1, got %q", msg)
	}
}

// ---------------------------------------------------------------------------
// Execute: enrichment
// ---------------------------------------------------------------------------

func TestContainerInventoryTool_Execute_Enrichment(t *testing.T) {
	t.Parallel()
	root := setupCgroupV2Tree(t)
	tool := &Tool{
		cgroupRoot: root,
		execLookPath: func(file string) (string, error) {
			if file == "docker" || file == "podman" {
				return "/mock/bin/" + file, nil
			}
			return "", errors.New("not found")
		},
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			switch name {
			case "docker":
				return []byte(`{"ID":"` + dockerID[:12] + `","Names":"web-frontend","Image":"nginx:1.25"}` + "\n"), nil
			case "podman":
				return []byte(`[{"Id":"` + podmanID + `","Names":["db"],"Image":"docker.io/library/postgres:16"}]`), nil
			}
			return nil, fmt.Errorf("unexpected binary %q", name)
		},
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatal(err)
	}

	docker, _ := findByRuntime(out, "docker")
	if docker.Name != "web-frontend" || docker.Image != "nginx:1.25" {
		t.Errorf("expected docker enrichment, got %+v", docker)
	}

	podman, _ := findByRuntime(out, "podman")
	if podman.Name != "db" || podman.Image != "docker.io/library/postgres:16" {
		t.Errorf("expected podman enrichment, got %+v", podman)
	}

	// containerd/crio have no enrichment source: ids only.
	containerd, _ := findByRuntime(out, "containerd")
	if containerd.Name != "" || containerd.Image != "" {
		t.Errorf("containerd must not be enriched, got %+v", containerd)
	}
}

func TestContainerInventoryTool_Execute_EnrichmentFailureIsSilent(t *testing.T) {
	t.Parallel()
	root := setupCgroupV2Tree(t)
	tool := &Tool{
		cgroupRoot: root,
		execLookPath: func(file string) (string, error) {
			return "/mock/bin/" + file, nil
		},
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return nil, errors.New("daemon not running")
		},
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("enrichment failure must be silent, got %q (%s)", res.Status, res.Summary)
	}
	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Total != 4 {
		t.Fatalf("expected 4 containers despite enrichment failure, got %d", out.Total)
	}
	for _, c := range out.Containers {
		if c.Name != "" || c.Image != "" {
			t.Errorf("expected bare ids on enrichment failure, got %+v", c)
		}
	}
}

// ---------------------------------------------------------------------------
// Execute: cap and cancellation
// ---------------------------------------------------------------------------

func TestContainerInventoryTool_Execute_CapsAt100(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "cgroup.controllers"), []byte("cpu\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 120; i++ {
		id := fmt.Sprintf("%064d", i)
		writeContainerCgroup(t, filepath.Join(root, "system.slice", "docker-"+id+".scope"), 1, 1048576, 1000)
	}
	tool := &Tool{cgroupRoot: root, execLookPath: noLookPath}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Containers) != 100 {
		t.Errorf("expected containers list capped at 100, got %d", len(out.Containers))
	}
	if out.Total != 120 {
		t.Errorf("expected total 120, got %d", out.Total)
	}
	if out.CountsByRuntime["docker"] != 120 {
		t.Errorf("counts_by_runtime must reflect all found, got %v", out.CountsByRuntime)
	}
}

func TestContainerInventoryTool_Execute_ContextCancelled(t *testing.T) {
	t.Parallel()
	root := setupCgroupV2Tree(t)
	tool := &Tool{cgroupRoot: root, execLookPath: noLookPath}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := tool.Execute(ctx, nil)
	if err != nil {
		t.Fatalf("cancellation must be encapsulated, got hard error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("expected error status for cancelled context, got %q", res.Status)
	}
}

// ---------------------------------------------------------------------------
// Unit tables
// ---------------------------------------------------------------------------

func TestClassifyScope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		dir         string
		wantRuntime string
		wantID      string
		wantOK      bool
	}{
		{"docker scope", "docker-" + dockerID + ".scope", "docker", dockerID, true},
		{"podman scope", "libpod-" + podmanID + ".scope", "podman", podmanID, true},
		{"containerd scope", "cri-containerd-" + containerdID + ".scope", "containerd", containerdID, true},
		{"crio scope", "crio-" + crioID + ".scope", "crio", crioID, true},
		{"systemd service", "sshd.service", "", "", false},
		{"plain slice", "kubepods.slice", "", "", false},
		{"empty id", "docker-.scope", "", "", false},
		{"no scope suffix", "docker-" + dockerID, "", "", false},
		{"conmon noise", "libpod-conmon-" + podmanID + ".scope", "", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runtime, id, ok := classifyScope(tc.dir)
			if ok != tc.wantOK || runtime != tc.wantRuntime || id != tc.wantID {
				t.Errorf("classifyScope(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.dir, runtime, id, ok, tc.wantRuntime, tc.wantID, tc.wantOK)
			}
		})
	}
}

func TestReadContainerMetrics(t *testing.T) {
	t.Parallel()

	t.Run("full metrics", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "c.scope")
		writeContainerCgroup(t, dir, 4, 314572800, 5500000) // 300.0 MB, 5.5 s
		procs, memMB, cpuSec := readContainerMetrics(dir)
		if procs != 4 || memMB != 300.0 || cpuSec != 5.5 {
			t.Errorf("got (%d, %v, %v), want (4, 300.0, 5.5)", procs, memMB, cpuSec)
		}
	})

	t.Run("missing files degrade to zero", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		procs, memMB, cpuSec := readContainerMetrics(dir)
		if procs != 0 || memMB != 0 || cpuSec != 0 {
			t.Errorf("expected zeros for missing metric files, got (%d, %v, %v)", procs, memMB, cpuSec)
		}
	})

	t.Run("empty procs file", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
		procs, _, _ := readContainerMetrics(dir)
		if procs != 0 {
			t.Errorf("expected 0 procs for empty file, got %d", procs)
		}
	})
}

func TestParseDockerPS(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{"two lines", `{"ID":"aaaaaaaaaaaa","Names":"c1","Image":"i1"}` + "\n" + `{"ID":"bbbbbbbbbbbb","Names":"c2","Image":"i2"}` + "\n", 2},
		{"garbage line skipped", "{\"ID\":\"aaaaaaaaaaaa\",\"Names\":\"c1\",\"Image\":\"i1\"}\nnot json\n", 1},
		{"empty", "", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := parseDockerPS([]byte(tc.input))
			if len(m) != tc.want {
				t.Errorf("expected %d entries, got %d: %v", tc.want, len(m), m)
			}
		})
	}

	m := parseDockerPS([]byte(`{"ID":"aaaaaaaaaaaa","Names":"c1","Image":"i1"}`))
	if e, ok := m["aaaaaaaaaaaa"]; !ok || e.Name != "c1" || e.Image != "i1" {
		t.Errorf("unexpected map entry: %v", m)
	}
}

func TestParsePodmanPS(t *testing.T) {
	t.Parallel()
	longID := strings.Repeat("ab", 32)

	t.Run("valid", func(t *testing.T) {
		t.Parallel()
		m := parsePodmanPS([]byte(`[{"Id":"` + longID + `","Names":["db","alias"],"Image":"postgres:16"}]`))
		e, ok := m[longID[:12]]
		if !ok || e.Name != "db" || e.Image != "postgres:16" {
			t.Errorf("unexpected map: %v", m)
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		t.Parallel()
		if m := parsePodmanPS([]byte("nope")); len(m) != 0 {
			t.Errorf("expected empty map for invalid JSON, got %v", m)
		}
	})

	t.Run("no names", func(t *testing.T) {
		t.Parallel()
		m := parsePodmanPS([]byte(`[{"Id":"` + longID + `","Names":[],"Image":"x"}]`))
		if e := m[longID[:12]]; e.Name != "" || e.Image != "x" {
			t.Errorf("unexpected entry: %+v", e)
		}
	})
}

func TestRound1dp(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   float64
		want float64
	}{
		{0, 0},
		{1.04, 1.0},
		{1.05, 1.1},
		{149.999, 150.0},
		{12.345678, 12.3},
	}
	for _, tc := range tests {
		if got := round1dp(tc.in); got != tc.want {
			t.Errorf("round1dp(%v): expected %v, got %v", tc.in, tc.want, got)
		}
	}
}

// ---------------------------------------------------------------------------
// Benchmarks (benchcov: one per non-test function)
// ---------------------------------------------------------------------------

func benchInventoryTool(b *testing.B) *Tool {
	b.Helper()
	return &Tool{cgroupRoot: setupCgroupV2Tree(b), execLookPath: noLookPath}
}

func BenchmarkNew(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = New()
	}
}

func BenchmarkName(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Name()
	}
}

func BenchmarkDescription(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Description()
	}
}

func BenchmarkHelp(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Help()
	}
}

func BenchmarkCategory(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Category()
	}
}

func BenchmarkParameters(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Parameters()
	}
}

func BenchmarkHidden(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Hidden()
	}
}

func BenchmarkIsSupported(b *testing.B) {
	tool := benchInventoryTool(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	tool := benchInventoryTool(b)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, nil)
	}
}

func BenchmarkScanContainers(b *testing.B) {
	root := setupCgroupV2Tree(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = scanContainers(root)
	}
}

func BenchmarkClassifyScope(b *testing.B) {
	name := "docker-" + dockerID + ".scope"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = classifyScope(name)
	}
}

func BenchmarkReadContainerMetrics(b *testing.B) {
	dir := filepath.Join(b.TempDir(), "c.scope")
	writeContainerCgroup(b, dir, 4, 314572800, 5500000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = readContainerMetrics(dir)
	}
}

func BenchmarkEnrichmentMap(b *testing.B) {
	tool := &Tool{
		cgroupRoot: b.TempDir(),
		execLookPath: func(file string) (string, error) {
			return "/mock/bin/" + file, nil
		},
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "docker" {
				return []byte(`{"ID":"aaaaaaaaaaaa","Names":"c1","Image":"i1"}` + "\n"), nil
			}
			return []byte(`[]`), nil
		},
	}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.enrichmentMap(ctx, true, true)
	}
}

func BenchmarkParseDockerPS(b *testing.B) {
	data := []byte(`{"ID":"aaaaaaaaaaaa","Names":"c1","Image":"i1"}` + "\n")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseDockerPS(data)
	}
}

func BenchmarkParsePodmanPS(b *testing.B) {
	data := []byte(`[{"Id":"` + podmanID + `","Names":["db"],"Image":"postgres:16"}]`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parsePodmanPS(data)
	}
}

func BenchmarkRound1dp(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = round1dp(12.345678)
	}
}
