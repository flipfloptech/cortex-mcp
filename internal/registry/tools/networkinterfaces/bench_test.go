package networkinterfaces

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkNew(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = New()
	}
}

func BenchmarkName(b *testing.B) {
	t := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Name()
	}
}

func BenchmarkDescription(b *testing.B) {
	t := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Description()
	}
}

func BenchmarkHelp(b *testing.B) {
	t := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Help()
	}
}

func BenchmarkCategory(b *testing.B) {
	t := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Category()
	}
}

func BenchmarkParameters(b *testing.B) {
	t := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Parameters()
	}
}

func BenchmarkHidden(b *testing.B) {
	t := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Hidden()
	}
}

func BenchmarkIsSupported(b *testing.B) {
	t := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = t.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	t := New()
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = t.Execute(ctx, nil)
	}
}

func BenchmarkParseSysfsInterfaces(b *testing.B) {
	tmpDir := b.TempDir()
	sysfs := filepath.Join(tmpDir, "sys", "class", "net")
	_ = os.MkdirAll(filepath.Join(sysfs, "eth0"), 0755)
	_ = os.WriteFile(filepath.Join(sysfs, "eth0", "address"), []byte("00:11:22:33:44:55\n"), 0644)
	_ = os.WriteFile(filepath.Join(sysfs, "eth0", "mtu"), []byte("1500\n"), 0644)
	_ = os.WriteFile(filepath.Join(sysfs, "eth0", "operstate"), []byte("up\n"), 0644)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = parseSysfsInterfaces(sysfs)
	}
}

func BenchmarkParseIpAddrJSON(b *testing.B) {
	mockJSON := []byte(`[
		{
			"ifindex": 2,
			"ifname": "eth0",
			"flags": ["BROADCAST", "MULTICAST", "UP", "LOWER_UP"],
			"mtu": 1500,
			"operstate": "UP",
			"address": "00:11:22:33:44:55",
			"addr_info": [
				{"family": "inet", "local": "192.168.1.50", "prefixlen": 24, "scope": "global"}
			]
		}
	]`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = parseIpAddrJSON(mockJSON)
	}
}
