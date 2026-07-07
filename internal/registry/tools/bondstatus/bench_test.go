package bondstatus

import (
	"context"
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
	root := buildFakeBondingTree(b, map[string]string{"bond0": bond8023adContent})
	tool := newWithRoot(root)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	root := buildFakeBondingTree(b, map[string]string{
		"bond0": bond8023adContent,
		"bond1": bondActiveBackupContent,
	})
	tool := newWithRoot(root)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, nil)
	}
}

func BenchmarkParseBondFile(b *testing.B) {
	content := []byte(bond8023adContent)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseBondFile("bond0", content)
	}
}

func BenchmarkParseSpeedMbps(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = parseSpeedMbps("25000 Mbps")
	}
}
