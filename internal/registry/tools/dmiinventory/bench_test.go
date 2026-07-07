package dmiinventory

import (
	"context"
	"path/filepath"
	"testing"
)

func BenchmarkNew(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = New()
	}
}

func BenchmarkName(b *testing.B) {
	t := New()
	for i := 0; i < b.N; i++ {
		_ = t.Name()
	}
}

func BenchmarkDescription(b *testing.B) {
	t := New()
	for i := 0; i < b.N; i++ {
		_ = t.Description()
	}
}

func BenchmarkHelp(b *testing.B) {
	t := New()
	for i := 0; i < b.N; i++ {
		_ = t.Help()
	}
}

func BenchmarkCategory(b *testing.B) {
	t := New()
	for i := 0; i < b.N; i++ {
		_ = t.Category()
	}
}

func BenchmarkParameters(b *testing.B) {
	t := New()
	for i := 0; i < b.N; i++ {
		_ = t.Parameters()
	}
}

func BenchmarkHidden(b *testing.B) {
	t := New()
	for i := 0; i < b.N; i++ {
		_ = t.Hidden()
	}
}

func BenchmarkIsSupported(b *testing.B) {
	t := New()
	t.sysfsRoot = writeDMI(b, fullDMIFiles())
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = t.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	root := writeDMI(b, fullDMIFiles())
	t, _, _ := mockTool(root, 0, true, true, dmidecodeMemoryFixture, nil)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = t.Execute(ctx, nil)
	}
}

func BenchmarkReadID(b *testing.B) {
	root := writeDMI(b, fullDMIFiles())
	dir := filepath.Join(root, "class", "dmi", "id")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = readID(dir, "sys_vendor")
	}
}

func BenchmarkDecodeChassisType(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = decodeChassisType("23")
	}
}

func BenchmarkParseDMIMemory(b *testing.B) {
	payload := []byte(dmidecodeMemoryFixture)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = parseDMIMemory(payload)
	}
}

func BenchmarkRunDMIDecode(b *testing.B) {
	root := writeDMI(b, fullDMIFiles())
	t, _, _ := mockTool(root, 1000, true, true, dmidecodeMemoryFixture, nil)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = t.runDMIDecode(ctx)
	}
}
