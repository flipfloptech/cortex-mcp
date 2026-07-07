package lustrejobstats

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
		_ = tool.resolvePath("obdfilter/testfs-OST0000/job_stats")
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
	dir := tool.sysfsRoot
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = listSubdirs(dir)
	}
}

func BenchmarkParseJobStats(b *testing.B) {
	content := []byte(ost0JobStats)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = parseJobStats(content)
	}
}

func BenchmarkUnquoteJobID(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = unquoteJobID(`"backup rsync.5678"`)
	}
}

func BenchmarkAggregateRecords(b *testing.B) {
	records, _ := parseJobStats([]byte(ost0JobStats))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		aggs := map[string]*jobAggregate{}
		aggregateRecords(aggs, records, "testfs-OST0000", false)
	}
}

func BenchmarkBuildIOTop(b *testing.B) {
	aggs := map[string]*jobAggregate{}
	records, _ := parseJobStats([]byte(ost0JobStats))
	aggregateRecords(aggs, records, "testfs-OST0000", false)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buildIOTop(aggs, 15, true)
	}
}

func BenchmarkBuildMetaTop(b *testing.B) {
	aggs := map[string]*jobAggregate{}
	records, _ := parseJobStats([]byte(mdtJobStats))
	aggregateRecords(aggs, records, "testfs-MDT0000", true)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buildMetaTop(aggs, 15)
	}
}

func BenchmarkTopOp(b *testing.B) {
	ops := map[string]uint64{"open": 500, "close": 500, "getattr": 1200, "unlink": 250}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = topOp(ops)
	}
}

func BenchmarkMbFromBytes(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = mbFromBytes(838860800)
	}
}

func BenchmarkSortedTargets(b *testing.B) {
	set := map[string]struct{}{"testfs-OST0000": {}, "testfs-OST0001": {}, "testfs-MDT0000": {}}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = sortedTargets(set)
	}
}
