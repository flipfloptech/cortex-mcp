package infinibandstatus

import (
	"context"
	"path/filepath"
	"testing"
)

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

func BenchmarkHidden(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Hidden()
	}
}

func BenchmarkParameters(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Parameters()
	}
}

func BenchmarkIsSupported(b *testing.B) {
	tool := newWithRoot(buildFakeIBTree(b))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	tool := newWithRoot(buildFakeIBTree(b))
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, nil)
	}
}

func BenchmarkCollectPort(b *testing.B) {
	root := buildFakeIBTree(b)
	tool := newWithRoot(root)
	portDir := filepath.Join(root, "class", "infiniband", "mlx5_0", "ports", "1")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.collectPort("mlx5_0", 1, portDir)
	}
}

func BenchmarkParseStateLine(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = parseStateLine("4: ACTIVE")
	}
}

func BenchmarkParseLID(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = parseLID("0x3")
	}
}

func BenchmarkParseRateGbps(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = parseRateGbps("100 Gb/sec (4X EDR)")
	}
}

func BenchmarkReadCounters(b *testing.B) {
	root := buildFakeIBTree(b)
	dir := filepath.Join(root, "class", "infiniband", "mlx5_0", "ports", "1", "counters")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = readCounters(dir)
	}
}

func BenchmarkReadFileTrim(b *testing.B) {
	root := buildFakeIBTree(b)
	path := filepath.Join(root, "class", "infiniband", "mlx5_0", "ports", "1", "state")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = readFileTrim(path)
	}
}
