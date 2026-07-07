package zfsstatus

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
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, nil)
	}
}

func BenchmarkParseARCStats(b *testing.B) {
	data := []byte(arcstatsFixture)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseARCStats(data)
	}
}

func BenchmarkParseZpoolList(b *testing.B) {
	data := []byte(zpoolListFixture)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseZpoolList(data)
	}
}

func BenchmarkParseZpoolStatus(b *testing.B) {
	data := []byte(zpoolStatusBackup)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = parseZpoolStatus(data)
	}
}

func BenchmarkParseZfsCount(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseZfsCount("1.05K")
	}
}

func BenchmarkApplyPoolWarnings(b *testing.B) {
	base := Pool{
		Name:        "bad",
		Health:      "DEGRADED",
		CapacityPct: 95,
		Errors:      PoolErrors{Read: 1},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p := base
		p.WarningReasons = []string{}
		applyPoolWarnings(&p)
	}
}

func BenchmarkReadFileTrim(b *testing.B) {
	tool := newFixtureTool(b)
	path := filepath.Join(tool.sysfsRoot, "module", "zfs", "version")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = readFileTrim(path)
	}
}
