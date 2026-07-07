package processmemorydetail

import (
	"context"
	"encoding/json"
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

const statusContent = `Name:	testproc
Umask:	0022
State:	S (sleeping)
Pid:	4242
Threads:	7
VmSwap:	     512 kB
`

const smapsRollupContent = `00400000-7fff03c58000 ---p 00000000 00:00 0                          [rollup]
Rss:              204800 kB
Pss:              153600 kB
Shared_Clean:      10240 kB
Shared_Dirty:       5120 kB
Private_Clean:     51200 kB
Private_Dirty:    102400 kB
Referenced:       190000 kB
Anonymous:        150000 kB
AnonHugePages:     40960 kB
Swap:              20480 kB
SwapPss:           10240 kB
`

// Two mappings; the parser must sum keys across both.
const smapsContent = `00400000-00452000 r-xp 00000000 08:02 173521 /usr/bin/testproc
Rss:              102400 kB
Pss:               51200 kB
Shared_Clean:       1024 kB
Shared_Dirty:       1024 kB
Private_Clean:      2048 kB
Private_Dirty:      2048 kB
AnonHugePages:      2048 kB
Swap:                  0 kB
SwapPss:               0 kB
VmFlags: rd ex mr mw me dw
7f0000000000-7f0000021000 rw-p 00000000 00:00 0
Rss:              102400 kB
Pss:               51200 kB
Shared_Clean:       1024 kB
Shared_Dirty:       1024 kB
Private_Clean:      2048 kB
Private_Dirty:      2048 kB
AnonHugePages:         0 kB
Swap:               1024 kB
SwapPss:             512 kB
VmFlags: rd wr mr mw me ac
`

const limitsFinite = `Limit                     Soft Limit           Hard Limit           Units     
Max cpu time              unlimited            unlimited            seconds   
Max file size             unlimited            unlimited            bytes     
Max address space         4294967296           4294967296           bytes     
Max locked memory         8388608              8388608              bytes     
Max open files            1024                 524288               files     
`

const limitsUnlimited = `Limit                     Soft Limit           Hard Limit           Units     
Max address space         unlimited            unlimited            bytes     
Max locked memory         unlimited            unlimited            bytes     
`

// setupProcTree builds a fake procfs root with a self/ entry (IsSupported)
// and one target pid populated from the given file map.
func setupProcTree(t testing.TB, pid int, files map[string]string) string {
	t.Helper()
	root := t.TempDir()

	selfDir := filepath.Join(root, "self")
	if err := os.MkdirAll(selfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(selfDir, "status"), []byte("Name:\tself\nThreads:\t1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	pidDir := filepath.Join(root, fmt.Sprintf("%d", pid))
	if err := os.MkdirAll(pidDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(pidDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// ---------------------------------------------------------------------------
// Contract compliance
// ---------------------------------------------------------------------------

func TestProcessMemoryDetailTool_ContractCompliance(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "get_process_memory_detail" {
		t.Errorf("expected name get_process_memory_detail, got %q", tool.Name())
	}
	if tool.Category() != registry.CategoryMemory {
		t.Errorf("expected category memory, got %q", tool.Category())
	}
	if tool.Description() == "" {
		t.Error("expected non-empty description")
	}
	if tool.Hidden() {
		t.Error("expected Hidden() == false")
	}

	params := tool.Parameters()
	if len(params) != 1 || params[0].Name != "target_pid" || !params[0].Required || params[0].Type != "integer" {
		t.Errorf("expected single required integer target_pid parameter, got %+v", params)
	}

	help := tool.Help()
	for _, src := range []string{"smaps_rollup", "smaps", "status", "limits"} {
		if !strings.Contains(help, src) {
			t.Errorf("Help() must reference data source %q", src)
		}
	}
}

// ---------------------------------------------------------------------------
// IsSupported
// ---------------------------------------------------------------------------

func TestProcessMemoryDetailTool_IsSupported(t *testing.T) {
	t.Parallel()

	t.Run("self status readable", func(t *testing.T) {
		t.Parallel()
		root := setupProcTree(t, 1, map[string]string{"status": statusContent})
		tool := &Tool{procfsRoot: root}
		if ok, _ := tool.IsSupported(); !ok {
			t.Error("expected supported when <procfsRoot>/self/status is readable")
		}
	})

	t.Run("missing procfs", func(t *testing.T) {
		t.Parallel()
		tool := &Tool{procfsRoot: filepath.Join(t.TempDir(), "nope")}
		ok, reason := tool.IsSupported()
		if ok {
			t.Error("expected unsupported without procfs")
		}
		if reason == "" {
			t.Error("expected reason when unsupported")
		}
	})
}

// ---------------------------------------------------------------------------
// Execute: happy path via smaps_rollup
// ---------------------------------------------------------------------------

func TestProcessMemoryDetailTool_Execute_Rollup(t *testing.T) {
	t.Parallel()
	root := setupProcTree(t, 4242, map[string]string{
		"status":       statusContent,
		"smaps_rollup": smapsRollupContent,
		"smaps":        "should not be read when rollup exists",
		"limits":       limitsFinite,
	})
	tool := &Tool{procfsRoot: root}

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"target_pid": 4242}`))
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

	if out.PID != 4242 || out.Comm != "testproc" || out.Threads != 7 {
		t.Errorf("unexpected identity fields: %+v", out)
	}
	if out.RssMB != 200.0 {
		t.Errorf("expected rss_mb 200.0, got %v", out.RssMB)
	}
	if out.PssMB != 150.0 {
		t.Errorf("expected pss_mb 150.0, got %v", out.PssMB)
	}
	if out.SharedMB != 15.0 { // 10240 + 5120 kB
		t.Errorf("expected shared_mb 15.0 (clean+dirty), got %v", out.SharedMB)
	}
	if out.PrivateMB != 150.0 { // 51200 + 102400 kB
		t.Errorf("expected private_mb 150.0 (clean+dirty), got %v", out.PrivateMB)
	}
	if out.SwapMB != 20.0 {
		t.Errorf("expected swap_mb 20.0, got %v", out.SwapMB)
	}
	if out.ThpMB != 40.0 {
		t.Errorf("expected thp_mb 40.0 (AnonHugePages), got %v", out.ThpMB)
	}

	addr, ok := out.Limits.AddressSpace.(float64)
	if !ok || addr != 4294967296 {
		t.Errorf("expected finite address_space limit 4294967296 bytes, got %v", out.Limits.AddressSpace)
	}
	locked, ok := out.Limits.LockedMemory.(float64)
	if !ok || locked != 8388608 {
		t.Errorf("expected locked_memory 8388608 bytes, got %v", out.Limits.LockedMemory)
	}

	if out.RssPctOfAddressLimit == nil {
		t.Fatal("expected rss_pct_of_address_limit for finite address space limit")
	}
	// 204800 kB = 209715200 bytes; 209715200 / 4294967296 * 100 = 4.88...% -> 4.9
	if *out.RssPctOfAddressLimit != 4.9 {
		t.Errorf("expected rss_pct_of_address_limit 4.9, got %v", *out.RssPctOfAddressLimit)
	}
}

// ---------------------------------------------------------------------------
// Execute: smaps sum fallback (old kernels)
// ---------------------------------------------------------------------------

func TestProcessMemoryDetailTool_Execute_SmapsFallback(t *testing.T) {
	t.Parallel()
	root := setupProcTree(t, 5555, map[string]string{
		"status": statusContent,
		"smaps":  smapsContent, // no smaps_rollup: pre-4.14 kernel
		"limits": limitsUnlimited,
	})
	tool := &Tool{procfsRoot: root}

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"target_pid": 5555}`))
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("expected ok via smaps fallback, got %q (%s)", res.Status, res.Summary)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatal(err)
	}

	if out.RssMB != 200.0 { // 102400 + 102400 kB
		t.Errorf("expected summed rss_mb 200.0, got %v", out.RssMB)
	}
	if out.PssMB != 100.0 { // 51200 + 51200 kB
		t.Errorf("expected summed pss_mb 100.0, got %v", out.PssMB)
	}
	if out.SharedMB != 4.0 { // (1024+1024)*2 kB
		t.Errorf("expected shared_mb 4.0, got %v", out.SharedMB)
	}
	if out.PrivateMB != 8.0 { // (2048+2048)*2 kB
		t.Errorf("expected private_mb 8.0, got %v", out.PrivateMB)
	}
	if out.SwapMB != 1.0 {
		t.Errorf("expected swap_mb 1.0, got %v", out.SwapMB)
	}
	if out.ThpMB != 2.0 {
		t.Errorf("expected thp_mb 2.0, got %v", out.ThpMB)
	}

	if s, ok := out.Limits.AddressSpace.(string); !ok || s != "unlimited" {
		t.Errorf("expected address_space \"unlimited\", got %v", out.Limits.AddressSpace)
	}
	if out.RssPctOfAddressLimit != nil {
		t.Errorf("rss_pct_of_address_limit must be omitted for unlimited address space, got %v", *out.RssPctOfAddressLimit)
	}
}

// ---------------------------------------------------------------------------
// Execute: error paths
// ---------------------------------------------------------------------------

func TestProcessMemoryDetailTool_Execute_ArgErrors(t *testing.T) {
	t.Parallel()
	root := setupProcTree(t, 4242, map[string]string{"status": statusContent})

	tests := []struct {
		name string
		args json.RawMessage
	}{
		{"nil args", nil},
		{"empty object", json.RawMessage(`{}`)},
		{"zero pid", json.RawMessage(`{"target_pid": 0}`)},
		{"negative pid", json.RawMessage(`{"target_pid": -5}`)},
		{"wrong type", json.RawMessage(`{"target_pid": "abc"}`)},
		{"invalid json", json.RawMessage(`{oops`)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tool := &Tool{procfsRoot: root}
			res, err := tool.Execute(context.Background(), tc.args)
			if err != nil {
				t.Fatalf("errors must be encapsulated, got hard error: %v", err)
			}
			if res.Status != registry.StatusError {
				t.Errorf("expected error status, got %q", res.Status)
			}
		})
	}
}

func TestProcessMemoryDetailTool_Execute_DeadPID(t *testing.T) {
	t.Parallel()
	root := setupProcTree(t, 4242, map[string]string{"status": statusContent})
	tool := &Tool{procfsRoot: root}

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"target_pid": 99999}`))
	if err != nil {
		t.Fatalf("errors must be encapsulated, got hard error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Fatalf("expected error status for dead pid, got %q", res.Status)
	}
	if !strings.Contains(res.Summary, "process not found") {
		t.Errorf("expected 'process not found' in summary, got %q", res.Summary)
	}
}

func TestProcessMemoryDetailTool_Execute_PermissionDenied(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("running as root: file modes are not enforced")
	}

	root := setupProcTree(t, 4242, map[string]string{
		"status": statusContent,
		"limits": limitsFinite,
	})
	pidDir := filepath.Join(root, "4242")
	for _, f := range []string{"smaps_rollup", "smaps"} {
		if err := os.WriteFile(filepath.Join(pidDir, f), []byte("secret"), 0o000); err != nil {
			t.Fatal(err)
		}
	}

	tool := &Tool{procfsRoot: root}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"target_pid": 4242}`))
	if err != nil {
		t.Fatalf("errors must be encapsulated, got hard error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Fatalf("expected error status for permission denied, got %q", res.Status)
	}
	lower := strings.ToLower(res.Summary)
	if !strings.Contains(lower, "privilege") && !strings.Contains(lower, "root") {
		t.Errorf("error must name the privilege requirement, got %q", res.Summary)
	}
}

func TestProcessMemoryDetailTool_Execute_ContextCancelled(t *testing.T) {
	t.Parallel()
	root := setupProcTree(t, 4242, map[string]string{"status": statusContent})
	tool := &Tool{procfsRoot: root}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := tool.Execute(ctx, json.RawMessage(`{"target_pid": 4242}`))
	if err != nil {
		t.Fatalf("cancellation must be encapsulated, got hard error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("expected error status for cancelled context, got %q", res.Status)
	}
}

// ---------------------------------------------------------------------------
// Parser tables
// ---------------------------------------------------------------------------

func TestParseMemKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want map[string]int64
	}{
		{
			name: "rollup single occurrence",
			in:   "Rss:  100 kB\nPss:   50 kB\n",
			want: map[string]int64{"Rss": 100, "Pss": 50},
		},
		{
			name: "smaps sums duplicates",
			in:   "Rss: 100 kB\nheader line\nRss: 150 kB\n",
			want: map[string]int64{"Rss": 250},
		},
		{
			name: "ignores non-kB and malformed lines",
			in:   "VmFlags: rd ex\nRss: abc kB\nRss: 10 kB\nLocked: 5 kB\n",
			want: map[string]int64{"Rss": 10, "Locked": 5},
		},
		{
			name: "empty input",
			in:   "",
			want: map[string]int64{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := parseMemKeys([]byte(tc.in))
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("key %s: expected %d, got %d", k, v, got[k])
				}
			}
			if len(got) != len(tc.want) {
				t.Errorf("expected %d keys, got %d: %v", len(tc.want), len(got), got)
			}
		})
	}
}

func TestParseLimitsFile(t *testing.T) {
	t.Parallel()

	t.Run("finite", func(t *testing.T) {
		t.Parallel()
		addr, locked := parseLimitsFile([]byte(limitsFinite))
		if !addr.known || addr.unlimited || addr.bytes != 4294967296 {
			t.Errorf("unexpected address space limit: %+v", addr)
		}
		if !locked.known || locked.unlimited || locked.bytes != 8388608 {
			t.Errorf("unexpected locked memory limit: %+v", locked)
		}
	})

	t.Run("unlimited", func(t *testing.T) {
		t.Parallel()
		addr, locked := parseLimitsFile([]byte(limitsUnlimited))
		if !addr.known || !addr.unlimited {
			t.Errorf("expected unlimited address space, got %+v", addr)
		}
		if !locked.known || !locked.unlimited {
			t.Errorf("expected unlimited locked memory, got %+v", locked)
		}
	})

	t.Run("empty file", func(t *testing.T) {
		t.Parallel()
		addr, locked := parseLimitsFile(nil)
		if addr.known || locked.known {
			t.Errorf("expected unknown limits for empty file, got %+v / %+v", addr, locked)
		}
	})
}

func TestParseStatus(t *testing.T) {
	t.Parallel()

	t.Run("full status", func(t *testing.T) {
		t.Parallel()
		comm, threads, vmSwapKB := parseStatus([]byte(statusContent))
		if comm != "testproc" || threads != 7 || vmSwapKB != 512 {
			t.Errorf("got (%q, %d, %d), want (testproc, 7, 512)", comm, threads, vmSwapKB)
		}
	})

	t.Run("empty", func(t *testing.T) {
		t.Parallel()
		comm, threads, vmSwapKB := parseStatus(nil)
		if comm != "" || threads != 0 || vmSwapKB != 0 {
			t.Errorf("expected zero values, got (%q, %d, %d)", comm, threads, vmSwapKB)
		}
	})
}

func TestKbToMB(t *testing.T) {
	t.Parallel()
	tests := []struct {
		kb   int64
		want float64
	}{
		{0, 0},
		{1024, 1.0},
		{1536, 1.5},
		{204800, 200.0},
		{100, 0.1},
		{51, 0.0}, // 0.0498 MB rounds to 0.0
	}
	for _, tc := range tests {
		if got := kbToMB(tc.kb); got != tc.want {
			t.Errorf("kbToMB(%d): expected %v, got %v", tc.kb, tc.want, got)
		}
	}
}

// ---------------------------------------------------------------------------
// Benchmarks (benchcov: one per non-test function)
// ---------------------------------------------------------------------------

func benchMemTool(b *testing.B) *Tool {
	b.Helper()
	root := setupProcTree(b, 4242, map[string]string{
		"status":       statusContent,
		"smaps_rollup": smapsRollupContent,
		"limits":       limitsFinite,
	})
	return &Tool{procfsRoot: root}
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
	tool := benchMemTool(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	tool := benchMemTool(b)
	args := json.RawMessage(`{"target_pid": 4242}`)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, args)
	}
}

func BenchmarkParseMemKeys(b *testing.B) {
	data := []byte(smapsRollupContent)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseMemKeys(data)
	}
}

func BenchmarkParseLimitsFile(b *testing.B) {
	data := []byte(limitsFinite)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = parseLimitsFile(data)
	}
}

func BenchmarkParseStatus(b *testing.B) {
	data := []byte(statusContent)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = parseStatus(data)
	}
}

func BenchmarkKbToMB(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = kbToMB(204800)
	}
}
