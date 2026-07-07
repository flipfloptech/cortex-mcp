package socketstats

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

func BenchmarkParseProcSockets(b *testing.B) {
	content := []byte(`  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 01FAA8C0:0035 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 13851639 1 0000000000000000 100 0 0 10 5
   1: 0100007F:8121 0200007F:0050 01 00000000:00000000 00:00000000 00000000  1000        0 2545376 2 0000000000000000 100 0 0 10 0
`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = parseProcSockets(content, "tcp")
	}
}

func BenchmarkParseSsFallback(b *testing.B) {
	content := []byte(`udp   ESTAB     0      0            192.168.86.52:46812  64.233.180.101:443
tcp   LISTEN    0      128                [::1]:631                    [::]:*
`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = parseSsFallback(content)
	}
}
