package pressurestall

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// benchTool returns a tool wired to a fake procfs tree for benchmarking.
func benchTool(b *testing.B) *Tool {
	b.Helper()
	root := b.TempDir()
	dir := filepath.Join(root, "pressure")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		b.Fatal(err)
	}
	files := map[string]string{
		"cpu":    "some avg10=1.23 avg60=0.50 avg300=0.10 total=1000\n",
		"memory": "some avg10=0.00 avg60=0.00 avg300=0.00 total=10\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=5\n",
		"io":     "some avg10=2.00 avg60=1.00 avg300=0.50 total=999\nfull avg10=1.50 avg60=0.75 avg300=0.25 total=500\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	tool := New()
	tool.procfsRoot = root
	return tool
}

func BenchmarkNew(b *testing.B) {
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
	tool := benchTool(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	tool := benchTool(b)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, nil)
	}
}

func BenchmarkParsePSILine(b *testing.B) {
	line := "some avg10=0.12 avg60=1.50 avg300=0.03 total=123456"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = parsePSILine(line)
	}
}

func BenchmarkParsePressureFile(b *testing.B) {
	content := []byte("some avg10=1.00 avg60=2.00 avg300=3.00 total=100\nfull avg10=4.00 avg60=5.00 avg300=6.00 total=200\n")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = parsePressureFile(content)
	}
}

func BenchmarkEvaluateWarnings(b *testing.B) {
	resources := map[string]*ResourcePressure{
		"cpu":    {Some: &PSILine{Avg10: 55.0}},
		"memory": {Some: &PSILine{Avg10: 1.0}, Full: &PSILine{Avg10: 12.0}},
		"io":     {Some: &PSILine{Avg10: 1.0}, Full: &PSILine{Avg10: 25.0}},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = evaluateWarnings(resources)
	}
}
