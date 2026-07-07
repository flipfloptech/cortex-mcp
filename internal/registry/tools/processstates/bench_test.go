package processstates

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// benchProcs is a representative synthetic process mix used by hermetic
// Execute/IsSupported benchmarks (never the real host /proc).
func benchProcs() []fakeProc {
	return []fakeProc{
		{pid: 1, statComm: "systemd", comm: "systemd", state: "S", ppid: 0},
		{pid: 100, statComm: "bash", comm: "bash", state: "S", ppid: 1},
		{pid: 101, statComm: "zombie", comm: "zombie", state: "Z", ppid: 100},
		{pid: 300, statComm: "dd", comm: "dd", state: "D", ppid: 100,
			wchan: "io_schedule", cmdline: "dd\x00if=/dev/zero\x00"},
		{pid: 400, statComm: "runner", comm: "runner", state: "R", ppid: 1},
		{pid: 500, statComm: "kworker", comm: "kworker", state: "I", ppid: 2},
	}
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

func BenchmarkHidden(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Hidden()
	}
}

func BenchmarkParameters(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Parameters()
	}
}

func BenchmarkIsSupported(b *testing.B) {
	tool := newFixtureTool(b, benchProcs())
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	tool := newFixtureTool(b, benchProcs())
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, nil)
	}
}

func BenchmarkParseStat(b *testing.B) {
	line := []byte("42 (my app) S 1 42 42 0 -1 4194560 0 0 0 0 0 0 0 0 20 0 1 0 100 1000 100")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = parseStat(line)
	}
}

func BenchmarkListPids(b *testing.B) {
	root := writeProcFixture(b, benchProcs())
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = listPids(root)
	}
}

func BenchmarkReadComm(b *testing.B) {
	dir := b.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "comm"), []byte("nginx\n"), 0o644); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = readComm(dir)
	}
}

func BenchmarkReadCmdline(b *testing.B) {
	dir := b.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte("dd\x00if=/dev/zero\x00of=/tmp/x\x00"), 0o644); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = readCmdline(dir)
	}
}

func BenchmarkReadWchan(b *testing.B) {
	dir := b.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "wchan"), []byte("io_schedule"), 0o644); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = readWchan(dir)
	}
}

func BenchmarkStateName(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = stateName('D')
	}
}
