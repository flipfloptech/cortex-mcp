package multipathstatus

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
	addAllPathsDownMap(b, tool)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, nil)
	}
}

func BenchmarkCollectMap(b *testing.B) {
	tool := newFixtureTool(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = collectMap(tool.sysfsRoot, "dm-0")
	}
}

func BenchmarkHasMpathDevices(b *testing.B) {
	tool := newFixtureTool(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = hasMpathDevices(tool.sysfsRoot)
	}
}

func BenchmarkParseMultipathdMaps(b *testing.B) {
	out := []byte("mpatha 36005 4 active\nmpathb 36006 2 suspend\n")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseMultipathdMaps(out)
	}
}

func BenchmarkReadFileTrim(b *testing.B) {
	tool := newFixtureTool(b)
	path := filepath.Join(tool.sysfsRoot, "block", "dm-0", "dm", "name")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = readFileTrim(path)
	}
}

func BenchmarkMultipathdDMStates(b *testing.B) {
	tool := newFixtureTool(b)
	tool.lookPath = func(string) (string, error) { return "/mock/sbin/multipathd", nil }
	tool.execCommand = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("mpatha 36005 2 active\n"), nil
	}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.multipathdDMStates(ctx)
	}
}
