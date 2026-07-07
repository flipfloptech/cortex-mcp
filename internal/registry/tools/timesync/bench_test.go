package timesync

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// benchTool returns a tool with hermetic fakes for benchmarking.
func benchTool(b *testing.B) *Tool {
	b.Helper()
	root := b.TempDir()
	dir := filepath.Join(root, "devices", "system", "clocksource", "clocksource0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "current_clocksource"), []byte("tsc\n"), 0o644); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "available_clocksource"), []byte("tsc hpet acpi_pm\n"), 0o644); err != nil {
		b.Fatal(err)
	}
	tool := New()
	tool.sysfsRoot = root
	tool.goarch = "amd64"
	tool.adjtimex = func(tx *syscall.Timex) (int, error) {
		tx.Offset = 1200
		tx.Status = 0
		tx.Maxerror = 16000
		tx.Esterror = 100
		return 0, nil
	}
	tool.execLookPath = func(file string) (string, error) {
		return "", fmt.Errorf("%s: not found", file)
	}
	tool.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, fmt.Errorf("no exec in benchmarks")
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
	tool := New()
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

func BenchmarkCollectKernel(b *testing.B) {
	tool := benchTool(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var out Output
		tool.collectKernel(&out)
	}
}

func BenchmarkReadClocksource(b *testing.B) {
	tool := benchTool(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.readClocksource()
	}
}

func BenchmarkCollectDaemon(b *testing.B) {
	tool := benchTool(b)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.collectDaemon(ctx)
	}
}

func BenchmarkParseChronyTracking(b *testing.B) {
	data := []byte("A29FC87B,203.0.113.5,2,1723805518.224495338,0.000145339,-0.000004312,0.000035612,-0.696,0.021,3.7,0.000206396,0.000572906,64.2,Normal\n")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = parseChronyTracking(data)
	}
}

func BenchmarkParseTimedatectlShow(b *testing.B) {
	data := []byte("Timezone=Etc/UTC\nLocalRTC=no\nCanNTP=yes\nNTP=yes\nNTPSynchronized=yes\n")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseTimedatectlShow(data)
	}
}

func BenchmarkRound2(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = round2(145.339)
	}
}
