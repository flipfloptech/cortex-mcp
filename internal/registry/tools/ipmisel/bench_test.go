package ipmisel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubBenchEnv points every external seam at deterministic in-process fakes so
// benchmarks never execute real binaries or probe the real /dev tree.
func stubBenchEnv(b *testing.B) {
	b.Helper()
	oldLook, oldCmd, oldEuid, oldDev := execLookPath, execCommand, geteuid, devRoot
	b.Cleanup(func() {
		execLookPath, execCommand, geteuid, devRoot = oldLook, oldCmd, oldEuid, oldDev
	})

	execLookPath = func(file string) (string, error) {
		if file == "ipmitool" {
			return "/mock/bin/ipmitool", nil
		}
		return "", errors.New("not found")
	}
	geteuid = func() int { return 0 }
	devRoot = b.TempDir()
	if err := os.WriteFile(filepath.Join(devRoot, "ipmi0"), nil, 0o644); err != nil {
		b.Fatal(err)
	}
	execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "info") {
			return []byte(selInfoFixture), nil
		}
		return []byte(selElistFixture), nil
	}
}

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
	stubBenchEnv(b)
	tool := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	stubBenchEnv(b)
	tool := New()
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, nil)
	}
}

func BenchmarkBmcDevicePath(b *testing.B) {
	stubBenchEnv(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = bmcDevicePath()
	}
}

func BenchmarkBuildIpmitoolCommand(b *testing.B) {
	stubBenchEnv(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = buildIpmitoolCommand("sel", "elist")
	}
}

func BenchmarkParseSELList(b *testing.B) {
	in := []byte(selElistFixture)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseSELList(in)
	}
}

func BenchmarkParseSELRecord(b *testing.B) {
	line := "   1 | 05/01/2026 | 13:02:11 | Temperature #0x30 | Upper Critical going high | Asserted"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = parseSELRecord(line)
	}
}

func BenchmarkParseSELInfo(b *testing.B) {
	in := []byte(selInfoFixture)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseSELInfo(in)
	}
}

func BenchmarkParseTimestamp(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseTimestamp("05/01/2026", "13:02:11")
	}
}

func BenchmarkSensorType(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = sensorType("Temperature #0x30")
	}
}

func BenchmarkFirstInt(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = firstInt("8144 bytes")
	}
}

func BenchmarkIsPermissionError(b *testing.B) {
	out := []byte("sudo: a password is required")
	err := errors.New("exit status 1")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = isPermissionError(out, err)
	}
}

func BenchmarkAnalyzeRecords(b *testing.B) {
	records := parseSELList([]byte(selElistFixture))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = analyzeRecords(records)
	}
}
