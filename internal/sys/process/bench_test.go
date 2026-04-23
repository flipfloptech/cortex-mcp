package process

import (
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkGetBuf(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf := getBuf()
		putBuf(buf)
	}
}

func BenchmarkPutBuf(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf := getBuf()
		putBuf(buf)
	}
}

func BenchmarkReadFileBuffered(b *testing.B) {
	tmpFile := filepath.Join(b.TempDir(), "test_readfile")
	_ = os.WriteFile(tmpFile, []byte("test data"), 0644)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, bPtr, _ := readFileBuffered(tmpFile)
		if bPtr != nil {
			putBuf(bPtr)
		}
	}
}

func BenchmarkReadUptime(b *testing.B) {
	tmpFile := filepath.Join(b.TempDir(), "uptime")
	_ = os.WriteFile(tmpFile, []byte("123456.78 98765.43\n"), 0644)

	// Temporarily swap the procfs root if process package allows,
	// otherwise just bench the parsing if we can mock it.
	// We'll write the file and if the package hardcodes /proc/uptime it will read that.
	// Either way it's a file read.

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = readUptime()
	}
}

func BenchmarkGetPIDs(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = getPIDs()
	}
}

func BenchmarkReadStat(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Just test on our own PID to guarantee it exists
		_, _ = readStat(os.Getpid())
	}
}

func BenchmarkReadStatus(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = readStatus(os.Getpid())
	}
}

func BenchmarkReadCmdline(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = readCmdline(os.Getpid())
	}
}

func BenchmarkIsSupported(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = IsSupported()
	}
}

func BenchmarkGetUsername(b *testing.B) {
	// We pass a known UID to avoid lookup overhead variations
	cache := make(map[int]string)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = getUsername(1000, cache)
	}
}

func BenchmarkParseStat(b *testing.B) {
	// Let's add a benchmark for the underlying parsing logic if there is one
	// or just let the main benchmark cover it.
}
