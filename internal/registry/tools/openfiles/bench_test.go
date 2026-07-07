package openfiles

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func BenchmarkNew(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = New()
	}
}

func BenchmarkName(b *testing.B) {
	t := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Name()
	}
}

func BenchmarkDescription(b *testing.B) {
	t := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Description()
	}
}

func BenchmarkHelp(b *testing.B) {
	t := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Help()
	}
}

func BenchmarkCategory(b *testing.B) {
	t := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Category()
	}
}

func BenchmarkParameters(b *testing.B) {
	t := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Parameters()
	}
}

func BenchmarkHidden(b *testing.B) {
	t := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Hidden()
	}
}

func BenchmarkIsSupported(b *testing.B) {
	tool := writeFDFixture(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	tool := writeFDFixture(b)
	ctx := context.Background()
	args := json.RawMessage(`{"path":"/mnt/lustre"}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, args)
	}
}

func BenchmarkParseArgs(b *testing.B) {
	args := json.RawMessage(`{"path":"/mnt/lustre","limit":50}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = parseArgs(args)
	}
}

func BenchmarkScanFDDir(b *testing.B) {
	tool := writeFDFixture(b)
	fdDir := filepath.Join(tool.procfsRoot, "1234", "fd")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = scanFDDir(fdDir, "/mnt/lustre")
	}
}

func BenchmarkReadComm(b *testing.B) {
	tool := writeFDFixture(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.readComm("1234")
	}
}
