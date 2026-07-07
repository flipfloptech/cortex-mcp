package lnetstatus

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// benchTool returns a tool wired to a fake debugfs tree so benchmarks never
// touch real host state.
func benchTool(b *testing.B) *Tool {
	b.Helper()
	tool := New()
	tool.debugfsRoot = writeLnetDebugfs(b, nisFixture, peersFixture, statsFixture)
	tool.sysfsRoot = b.TempDir()
	tool.execLookPath = lookPathMissing
	tool.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, errors.New("not available in benchmark")
	}
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

func BenchmarkParseNIS(b *testing.B) {
	data := []byte(nisFixture)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseNIS(data)
	}
}

func BenchmarkParsePeerCount(b *testing.B) {
	data := []byte(peersFixture)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parsePeerCount(data)
	}
}

func BenchmarkParseStats(b *testing.B) {
	data := []byte(statsFixture)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = parseStats(data)
	}
}

func BenchmarkParseLnetctlNet(b *testing.B) {
	data := []byte(lnetctlNetFixture)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseLnetctlNet(data)
	}
}

func BenchmarkParseLnetctlStats(b *testing.B) {
	data := []byte(lnetctlStatsFixture)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = parseLnetctlStats(data)
	}
}

func BenchmarkCollectWarnings(b *testing.B) {
	neg := int64(-3)
	nis := []NI{
		{NID: "0@lo", Status: "up"},
		{NID: "1@tcp", Status: "down", MinCredits: &neg},
	}
	stats := &Stats{DropCount: 9}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = collectWarnings(nis, stats)
	}
}

func BenchmarkIsPermissionError(b *testing.B) {
	out := []byte("cannot open /dev/lnet: Operation not permitted")
	err := errors.New("exit status 1")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = isPermissionError(out, err)
	}
}

func BenchmarkRunLnetctl(b *testing.B) {
	tool := New()
	tool.execLookPath = lookPathMissing
	tool.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte(lnetctlNetFixture), nil
	}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.runLnetctl(ctx, "net", "show")
	}
}
