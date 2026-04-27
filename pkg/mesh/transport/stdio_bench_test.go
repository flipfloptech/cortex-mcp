package transport

import (
	"io"
	"testing"
	"time"
)

type dummyReader struct{}

func (dummyReader) Read(p []byte) (n int, err error) {
	return len(p), nil
}

func BenchmarkNewStdioConn(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewStdioConn(dummyReader{}, io.Discard)
	}
}

func BenchmarkRead(b *testing.B) {
	conn := NewStdioConn(dummyReader{}, io.Discard)
	readBuf := make([]byte, 1024)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = conn.Read(readBuf)
	}
}

func BenchmarkWrite(b *testing.B) {
	conn := NewStdioConn(dummyReader{}, io.Discard)
	writeBuf := make([]byte, 1024)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = conn.Write(writeBuf)
	}
}

func BenchmarkClose(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		conn := NewStdioConn(dummyReader{}, io.Discard)
		_ = conn.Close()
	}
}

func BenchmarkLocalAddr(b *testing.B) {
	conn := NewStdioConn(dummyReader{}, io.Discard)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = conn.LocalAddr()
	}
}

func BenchmarkRemoteAddr(b *testing.B) {
	conn := NewStdioConn(dummyReader{}, io.Discard)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = conn.RemoteAddr()
	}
}

func BenchmarkNetwork(b *testing.B) {
	addr := stdioAddr{}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = addr.Network()
	}
}

func BenchmarkString(b *testing.B) {
	addr := stdioAddr{}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = addr.String()
	}
}

func BenchmarkSetDeadline(b *testing.B) {
	conn := NewStdioConn(dummyReader{}, io.Discard)
	t := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = conn.SetDeadline(t)
	}
}

func BenchmarkSetReadDeadline(b *testing.B) {
	conn := NewStdioConn(dummyReader{}, io.Discard)
	t := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = conn.SetReadDeadline(t)
	}
}

func BenchmarkSetWriteDeadline(b *testing.B) {
	conn := NewStdioConn(dummyReader{}, io.Discard)
	t := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = conn.SetWriteDeadline(t)
	}
}
