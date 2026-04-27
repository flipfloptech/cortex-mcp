package transport

import (
	"context"
	"testing"
)

func BenchmarkDialProxy(b *testing.B) {
	echoAddr, echoCleanup := newEchoServer(b)
	defer echoCleanup()

	proxy := newTestProxy(b, nil)
	defer proxy.Close()

	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		conn, err := DialProxy(ctx, proxy.URL, echoAddr)
		if err == nil {
			_ = conn.Close()
		}
	}
}
