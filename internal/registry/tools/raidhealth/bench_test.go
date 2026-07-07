package raidhealth

import (
	"context"
	"path/filepath"
	"testing"
)

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
	tool := newFixtureTool(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	tool := newFixtureTool(b)
	addDegradedArray(b, tool)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, nil)
	}
}

func BenchmarkReadAttr(b *testing.B) {
	tool := newFixtureTool(b)
	dir := filepath.Join(tool.sysfsRoot, "block", "md0", "md")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = readAttr(dir, "array_state")
	}
}

func BenchmarkParseSyncCompleted(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseSyncCompleted("12345 / 24690")
	}
}

func BenchmarkReadMembers(b *testing.B) {
	tool := newFixtureTool(b)
	mdDir := filepath.Join(tool.sysfsRoot, "block", "md0", "md")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = readMembers(mdDir)
	}
}

func BenchmarkCollectArray(b *testing.B) {
	tool := newFixtureTool(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = collectArray(tool.sysfsRoot, "md0")
	}
}
