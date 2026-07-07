package lustreserverstats

import (
	"context"
	"encoding/json"
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
	t := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = t.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	tool := newFixtureTool(b)
	ctx := context.Background()
	args := json.RawMessage(`{}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, args)
	}
}

func BenchmarkResolvePath(b *testing.B) {
	tool := newFixtureTool(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.resolvePath("mgs/MGS/num_exports")
	}
}

func BenchmarkGetSubdirs(b *testing.B) {
	tool := newFixtureTool(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.getSubdirs("obdfilter")
	}
}

func BenchmarkListSubdirs(b *testing.B) {
	tool := newFixtureTool(b)
	dir := tool.sysfsPath
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = listSubdirs(dir)
	}
}

func BenchmarkCollectTarget(b *testing.B) {
	tool := newFixtureTool(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.collectTarget("obdfilter", "testfs-OST0000")
	}
}

func BenchmarkReadUintFile(b *testing.B) {
	tool := newFixtureTool(b)
	path := tool.resolvePath("mgs/MGS/num_exports")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = readUintFile(path)
	}
}

func BenchmarkParseStatsCounters(b *testing.B) {
	content := []byte(ossStats)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseStatsCounters(content)
	}
}

func BenchmarkTopCounters(b *testing.B) {
	counters := parseStatsCounters([]byte(ossStats))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = topCounters(counters, 10)
	}
}

func BenchmarkParseBrwIOSizes(b *testing.B) {
	content := []byte(ossBrwStats)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseBrwIOSizes(content, 5)
	}
}

func BenchmarkUsedPct(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = usedPct(1000000, 250000)
	}
}
