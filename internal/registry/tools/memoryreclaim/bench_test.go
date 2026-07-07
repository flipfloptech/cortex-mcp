package memoryreclaim

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// benchTool returns a tool wired to two-phase fake samples and a fake clock
// so benchmark iterations never sleep for real.
func benchTool(b *testing.B) *Tool {
	b.Helper()
	tool := New()
	fakeSampler(tool, busyBefore, busyAfter, 500*time.Millisecond)
	return tool
}

func BenchmarkNew(b *testing.B) {
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
	tool.procfsRoot = b.TempDir()
	if err := os.WriteFile(filepath.Join(tool.procfsRoot, "vmstat"), []byte(quietVmstat), 0o644); err != nil {
		b.Fatalf("write vmstat: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	tool := benchTool(b)
	ctx := context.Background()
	args := json.RawMessage(`{"sample_duration_ms": 10}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, args)
	}
}

func BenchmarkParseVmstat(b *testing.B) {
	data := []byte(busyAfter)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseVmstat(data)
	}
}

func BenchmarkResolveCounter(b *testing.B) {
	m := parseVmstat([]byte(busyAfter))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = resolveCounter(m, "allocstall")
	}
}

func BenchmarkResolveWindow(b *testing.B) {
	v := 750
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = resolveWindow(&v)
	}
}

func BenchmarkComputeOutput(b *testing.B) {
	before := parseVmstat([]byte(busyBefore))
	after := parseVmstat([]byte(busyAfter))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = computeOutput(before, after, 500*time.Millisecond)
	}
}

func BenchmarkBuildWarnings(b *testing.B) {
	r := Rates{SwapIn: 1, SwapOut: 2, DirectScan: 3, Allocstall: 4, CompactStall: 5}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buildWarnings(r)
	}
}

func BenchmarkRound2(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = round2(87.142857)
	}
}
