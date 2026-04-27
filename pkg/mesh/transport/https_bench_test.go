package transport

import (
	"context"
	"testing"
)

func BenchmarkListenHTTPS(b *testing.B) {
	_, serverConf := testMTLSPair(b)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		ln, err := ListenHTTPS(ctx, "127.0.0.1:0", serverConf)
		if err != nil {
			b.Fatalf("ListenHTTPS error: %v", err)
		}
		cancel()
		_ = ln.Close()
	}
}

func BenchmarkDialHTTPS(b *testing.B) {
	serverConf, clientConf := testMTLSPair(b)
	ctx := context.Background()

	addr, cleanup := startTLSEchoServer(b, serverConf)
	defer cleanup()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		conn, err := DialHTTPS(ctx, addr, clientConf)
		if err == nil {
			_ = conn.Close()
		}
	}
}
