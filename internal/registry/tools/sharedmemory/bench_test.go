package sharedmemory

import (
	"context"
	"testing"
)

// benchExecTool wires a tool to fully fake procfs/devshm trees and a fake
// statfs so benchmark iterations never touch the real system.
func benchExecTool(b *testing.B) *Tool {
	b.Helper()
	tool := New()
	tool.procfsRoot = writeProcTree(b, modernShm, mountinfoFixture, "1234")
	tool.devShmRoot = writeDevShmTree(b, map[string]int{
		"pulse-shm-123": 1024 * 1024,
		"app/ring.shm":  512 * 1024,
	})
	tool.statfs = fakeStatfs(map[string][2]uint64{
		"/dev/shm": {100, 10},
		"/run":     {100, 90},
		"/tmp":     {200, 150},
	})
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
	tool.procfsRoot = writeProcTree(b, modernShm, mountinfoFixture)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	tool := benchExecTool(b)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, nil)
	}
}

func BenchmarkParseSysvShm(b *testing.B) {
	data := []byte(modernShm)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = parseSysvShm(data)
	}
}

func BenchmarkBuildSysv(b *testing.B) {
	procRoot := writeProcTree(b, "", "", "1234")
	segs, err := parseSysvShm([]byte(modernShm))
	if err != nil {
		b.Fatalf("parseSysvShm: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buildSysv(segs, procRoot)
	}
}

func BenchmarkListPosixShm(b *testing.B) {
	root := writeDevShmTree(b, map[string]int{
		"pulse-shm-123": 1024 * 1024,
		"sem.mysem":     10,
		"app/ring.shm":  512 * 1024,
	})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = listPosixShm(root)
	}
}

func BenchmarkParseTmpfsMounts(b *testing.B) {
	data := []byte(mountinfoFixture)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseTmpfsMounts(data)
	}
}

func BenchmarkStatfsWithTimeout(b *testing.B) {
	tool := New()
	tool.statfs = fakeStatfs(map[string][2]uint64{"/dev/shm": {100, 10}})
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = tool.statfsWithTimeout(ctx, "/dev/shm")
	}
}

func BenchmarkCollectTmpfs(b *testing.B) {
	tool := New()
	tool.statfs = fakeStatfs(map[string][2]uint64{
		"/dev/shm": {100, 10},
		"/run":     {100, 90},
		"/tmp":     {200, 150},
	})
	mounts := []string{"/dev/shm", "/run", "/tmp"}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = tool.collectTmpfs(ctx, mounts)
	}
}

func BenchmarkBuildWarnings(b *testing.B) {
	sysv := SysvInfo{OrphanedCount: 3, OrphanedMB: 24.5}
	tmpfs := []TmpfsMount{
		{Mount: "/dev/shm", UsedPct: 90.0, Status: "ok"},
		{Mount: "/run", UsedPct: 10.0, Status: "ok"},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buildWarnings(sysv, tmpfs)
	}
}

func BenchmarkToMB(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = toMB(8 * 1024 * 1024)
	}
}

func BenchmarkRound1(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = round1(90.05)
	}
}
