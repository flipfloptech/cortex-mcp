package lustreclientstats

import (
	"context"
	"testing"
)

func BenchmarkNew(b *testing.B) {
	b.ResetTimer()
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
	t := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = t.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	t := New()
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = t.Execute(ctx, nil)
	}
}

func BenchmarkParseLliteStats(b *testing.B) {
	content := []byte(`
read_bytes                3340631 samples [bytes] 4096 1048576 14421705314304
write_bytes               3290112 samples [bytes] 4096 1048576 144217053
open                      12345 samples [reqs]
close                     12340 samples [reqs]
`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = parseLliteStats(content)
	}
}

func BenchmarkParseReadAheadStats(b *testing.B) {
	content := []byte(`
hits 3340631 samples [pages]
misses 32901120 samples [pages]
readpage_backwards 15 samples [pages]
`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseReadAheadStats(content)
	}
}

func BenchmarkParseImport(b *testing.B) {
	content := []byte(`
target: lustre-OST0000_UUID
state: FULL
connect_count: 5
inflight: 3
`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseImport("osc-OST0000", content)
	}
}

func BenchmarkParseAdaptiveTimeout(b *testing.B) {
	content := []byte("service : cur 1 worst 30 (at 1681257150, 85d23h58m54s ago) 1 1 1 1")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = parseAdaptiveTimeout(content)
	}
}

