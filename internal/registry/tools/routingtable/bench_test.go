package routingtable

import (
	"context"
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

func BenchmarkParseIPv4Routes(b *testing.B) {
	content := []byte(`Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
wlan0	00000000	0156A8C0	0003	0	0	600	00000000	0	0	0
wlan0	0056A8C0	00000000	0001	0	0	600	00FFFFFF	0	0	0
`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = parseIPv4Routes(content)
	}
}

func BenchmarkParseIPv6Routes(b *testing.B) {
	content := []byte(`fdbad78e093e76b80000000000000000 40 00000000000000000000000000000000 00 00000000000000000000000000000000 00000258 00000002 00000000 00000001    wlan0
fdc3bf47eaee00010000000000000000 40 00000000000000000000000000000000 00 fe80000000000000b623a2fffe6d03a7 0000025d 00000001 00000000 00000003    wlan0
`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = parseIPv6Routes(content)
	}
}

func BenchmarkParseIpRouteJSON(b *testing.B) {
	content := []byte(`[
		{"dst":"default","gateway":"192.168.86.1","dev":"wlan0","metric":600},
		{"dst":"192.168.86.0/24","dev":"wlan0","metric":600}
	]`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = parseIpRouteJSON(content, "ipv4")
	}
}
