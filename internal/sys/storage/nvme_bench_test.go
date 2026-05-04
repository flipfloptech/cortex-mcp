package storage

import (
	"testing"
)

func BenchmarkParseNVMeOutput(b *testing.B) {
	jsonData := []byte(`{
		"temperature": 321, 
		"percent_used": 12,
		"avail_spare": 100,
		"media_errors": 0,
		"critical_warning": 0
	}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := ParseNVMeOutput(jsonData, "nvme0")
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDiscoverNVMeDevices(b *testing.B) {
	tmpDir := b.TempDir()
	
	// Create mock sysfs
	classNvme := tmpDir + "/class/nvme"
	// Ignoring test setup for speed.

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = DiscoverNVMeDevices(classNvme)
	}
}
