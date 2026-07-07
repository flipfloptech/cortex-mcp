package filelocks

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
	tool := writeLocksFixture(b, locksNoWaiters)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	tool := writeLocksFixture(b, locksWithWaiters)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, nil)
	}
}

func BenchmarkParseLocks(b *testing.B) {
	content := []byte(locksWithWaiters)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseLocks(content)
	}
}

func BenchmarkParseLockLine(b *testing.B) {
	line := "1: POSIX  ADVISORY  WRITE 1234 08:01:12345 0 EOF"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = parseLockLine(line)
	}
}

func BenchmarkResolveComm(b *testing.B) {
	tool := writeLocksFixture(b, locksNoWaiters)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.resolveComm(100)
	}
}

func BenchmarkBuildData(b *testing.B) {
	tool := writeLocksFixture(b, locksWithWaiters)
	entries := parseLocks([]byte(locksWithWaiters))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.buildData(entries)
	}
}
