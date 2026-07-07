package kernelsecuritystate

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// benchTool builds a fully-populated fake tree for benchmarking.
func benchTool(b *testing.B) *Tool {
	b.Helper()
	procRoot := b.TempDir()
	sysRoot := b.TempDir()

	write := func(path, content string) {
		b.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			b.Fatal(err)
		}
	}

	write(filepath.Join(procRoot, "sys", "kernel", "tainted"), "4097\n")
	write(filepath.Join(sysRoot, "kernel", "security", "lockdown"), "none [integrity] confidentiality\n")
	write(filepath.Join(sysRoot, "fs", "selinux", "enforce"), "1\n")
	write(filepath.Join(sysRoot, "module", "apparmor", "parameters", "enabled"), "Y\n")
	write(filepath.Join(sysRoot, "devices", "system", "cpu", "vulnerabilities", "meltdown"), "Mitigation: PTI\n")
	write(filepath.Join(sysRoot, "devices", "system", "cpu", "vulnerabilities", "spectre_v2"), "Vulnerable\n")

	tool := New()
	tool.procfsRoot = procRoot
	tool.sysfsRoot = sysRoot
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

func BenchmarkDecodeTaint(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = decodeTaint(4097)
	}
}

func BenchmarkParseLockdown(b *testing.B) {
	data := []byte("none [integrity] confidentiality\n")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseLockdown(data)
	}
}

func BenchmarkClassifyVulnerability(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = classifyVulnerability("Mitigation: Clear CPU buffers; SMT disabled")
	}
}

func BenchmarkReadTrimmedFile(b *testing.B) {
	tool := benchTool(b)
	path := filepath.Join(tool.sysfsRoot, "fs", "selinux", "enforce")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = readTrimmedFile(path)
	}
}

func BenchmarkCollectVulnerabilities(b *testing.B) {
	tool := benchTool(b)
	dir := filepath.Join(tool.sysfsRoot, "devices", "system", "cpu", "vulnerabilities")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = collectVulnerabilities(dir)
	}
}
