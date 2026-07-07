package gpustatus

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// stubBenchEnv points every external seam at deterministic in-process fakes so
// benchmarks never execute real binaries or scan the real sysfs tree.
func stubBenchEnv(b *testing.B, lookPaths map[string]bool, out []byte) {
	b.Helper()
	oldLook, oldCmd, oldSysfs := execLookPath, execCommand, sysfsRoot
	b.Cleanup(func() {
		execLookPath, execCommand, sysfsRoot = oldLook, oldCmd, oldSysfs
	})
	execLookPath = func(file string) (string, error) {
		if lookPaths[file] {
			return "/mock/bin/" + file, nil
		}
		return "", errors.New("not found")
	}
	execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return out, nil
	}
	sysfsRoot = b.TempDir()
}

func benchSysfsRoot(b *testing.B) string {
	b.Helper()
	root := b.TempDir()
	dev := filepath.Join(root, "class", "drm", "card0", "device")
	if err := os.MkdirAll(filepath.Join(dev, "hwmon", "hwmon0"), 0o755); err != nil {
		b.Fatal(err)
	}
	files := map[string]string{
		"gpu_busy_percent":              "42\n",
		"mem_info_vram_used":            "4294967296\n",
		"mem_info_vram_total":           "17179869184\n",
		"hwmon/hwmon0/temp1_input":      "45000\n",
		"hwmon/hwmon0/power1_average":   "35000000\n",
		"hwmon/hwmon0/unrelated_sensor": "1\n",
	}
	for rel, content := range files {
		if err := os.WriteFile(filepath.Join(dev, rel), []byte(content), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	return root
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
	stubBenchEnv(b, map[string]bool{"nvidia-smi": true}, nil)
	tool := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	stubBenchEnv(b, map[string]bool{"nvidia-smi": true}, []byte(nvidiaCSVFixture))
	tool := New()
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, nil)
	}
}

func BenchmarkBuildResult(b *testing.B) {
	temp := 90.0
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buildResult("get_gpu_status", "sysfs", []GPU{{Index: 0, TemperatureC: &temp}, {Index: 1}})
	}
}

func BenchmarkParseNvidiaSMI(b *testing.B) {
	in := []byte(nvidiaCSVFixture)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseNvidiaSMI(in)
	}
}

func BenchmarkParseNvidiaFloat(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseNvidiaFloat("312.45")
	}
}

func BenchmarkParseRocmSMI(b *testing.B) {
	in := []byte(rocmJSONFixture)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = parseRocmSMI(in)
	}
}

func BenchmarkRocmFloat(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = rocmFloat("45.0")
	}
}

func BenchmarkCollectSysfsGPUs(b *testing.B) {
	root := benchSysfsRoot(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = collectSysfsGPUs(root)
	}
}

func BenchmarkDiscoverSysfsCards(b *testing.B) {
	root := benchSysfsRoot(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = discoverSysfsCards(root)
	}
}

func BenchmarkReadSysfsScaledFloat(b *testing.B) {
	root := benchSysfsRoot(b)
	path := filepath.Join(root, "class", "drm", "card0", "device", "gpu_busy_percent")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = readSysfsScaledFloat(path, 1)
	}
}

func BenchmarkFinalizeGPU(b *testing.B) {
	used, total, temp := 79544.0, 81920.0, 91.0
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g := GPU{MemoryUsedMB: &used, MemoryTotalMB: &total, TemperatureC: &temp}
		finalizeGPU(&g)
	}
}

func BenchmarkRound1(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = round1(97.099609375)
	}
}
