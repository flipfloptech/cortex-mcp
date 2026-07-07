package sensorreadings

import (
	"context"
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
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = t.Execute(ctx, nil)
	}
}

func BenchmarkReadTrimmed(b *testing.B) {
	dir := b.TempDir()
	path := filepath.Join(dir, "name")
	if err := os.WriteFile(path, []byte("coretemp\n"), 0o644); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = readTrimmed(path)
	}
}

func BenchmarkReadScaled(b *testing.B) {
	dir := b.TempDir()
	path := filepath.Join(dir, "temp1_input")
	if err := os.WriteFile(path, []byte("45500\n"), 0o644); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = readScaled(path, 1000)
	}
}

func BenchmarkRound1(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = round1(45.06)
	}
}

func BenchmarkRound3(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = round3(12.2504)
	}
}

func BenchmarkSensorStatus(b *testing.B) {
	maxC := 85.0
	critC := 100.0
	for i := 0; i < b.N; i++ {
		_ = sensorStatus(90.0, &maxC, &critC)
	}
}

func BenchmarkSensorFiles(b *testing.B) {
	root := newFakeSysfs(b)
	dir := filepath.Join(root, "class", "hwmon", "hwmon1")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = sensorFiles(dir)
	}
}

func BenchmarkCollectChip(b *testing.B) {
	root := newFakeSysfs(b)
	dir := filepath.Join(root, "class", "hwmon", "hwmon0")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = collectChip(dir)
	}
}
