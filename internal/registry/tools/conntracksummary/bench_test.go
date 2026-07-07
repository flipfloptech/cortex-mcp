package conntracksummary

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// benchTool returns a tool wired to a fake procfs tree so benchmarks never
// touch real host state.
func benchTool(b *testing.B) *Tool {
	b.Helper()
	tool := New()
	tool.procfsRoot = writeConntrackProc(b, "12\n", "262144\n", statModernFixture)
	return tool
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
	tool := benchTool(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	tool := benchTool(b)
	ctx := context.Background()
	args := json.RawMessage(`{}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, args)
	}
}

func BenchmarkReadUintFile(b *testing.B) {
	path := filepath.Join(b.TempDir(), "count")
	if err := os.WriteFile(path, []byte("262144\n"), 0o644); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = readUintFile(path)
	}
}

func BenchmarkParseConntrackStat(b *testing.B) {
	data := []byte(statModernFixture)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = parseConntrackStat(data)
	}
}

func BenchmarkComputeUsagePct(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = computeUsagePct(131072, 262144)
	}
}

func BenchmarkCollectWarnings(b *testing.B) {
	counters := &Counters{Invalid: 5, InsertFailed: 1, Drop: 3, EarlyDrop: 1, SearchRestart: 5}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = collectWarnings(90.01, counters)
	}
}
