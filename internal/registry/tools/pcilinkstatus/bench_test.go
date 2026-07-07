package pcilinkstatus

import (
	"context"
	"encoding/json"
	"os"
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
	t.sysfsRoot = newFakeSysfs(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = t.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	t := New()
	t.sysfsRoot = newFakeSysfs(b)
	ctx := context.Background()
	args := json.RawMessage(`{"all": true}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = t.Execute(ctx, args)
	}
}

func BenchmarkReadTrimmed(b *testing.B) {
	dir := b.TempDir()
	path := filepath.Join(dir, "value")
	if err := os.WriteFile(path, []byte("8.0 GT/s PCIe\n"), 0o644); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = readTrimmed(path)
	}
}

func BenchmarkParseGTs(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = parseGTs("16.0 GT/s PCIe")
	}
}

func BenchmarkDecodeClassName(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = decodeClassName("0x010802")
	}
}

func BenchmarkIsInterestingClass(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = isInterestingClass("0x020700")
	}
}

func BenchmarkReadAERSum(b *testing.B) {
	dir := b.TempDir()
	path := filepath.Join(dir, "aer_dev_correctable")
	if err := os.WriteFile(path, []byte("RxErr 3\nBadTLP 2\nTOTAL_ERR_COR 5\n"), 0o644); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = readAERSum(path)
	}
}

func BenchmarkCollectDevice(b *testing.B) {
	root := newFakeSysfs(b)
	devPath := filepath.Join(root, "bus", "pci", "devices", "0000:01:00.0")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = collectDevice(devPath, "0000:01:00.0")
	}
}
