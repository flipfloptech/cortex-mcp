package membrane

import (
	"net"
	"testing"
)

// BenchmarkNewZstdConn measures the allocation and initialization overhead
// of creating new ZstdConn instances.
func BenchmarkNewZstdConn(b *testing.B) {
	b.ReportAllocs()

	clientRaw, serverRaw := net.Pipe()
	defer func() { _ = clientRaw.Close() }()
	defer func() { _ = serverRaw.Close() }()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		zc, err := NewZstdConn(clientRaw)
		if err != nil {
			b.Fatalf("NewZstdConn: %v", err)
		}
		_ = zc.Close()
	}
}

// BenchmarkWrite measures the raw throughput of the ZstdConn wrapper
// when writing and flushing compressed data.
func BenchmarkWrite(b *testing.B) {
	b.ReportAllocs()

	clientRaw, serverRaw := net.Pipe()

	clientZstd, err := NewZstdConn(clientRaw)
	if err != nil {
		b.Fatalf("NewZstdConn: %v", err)
	}

	serverZstd, err := NewZstdConn(serverRaw)
	if err != nil {
		b.Fatalf("NewZstdConn: %v", err)
	}

	defer func() { _ = clientZstd.Close() }()
	defer func() { _ = serverZstd.Close() }()

	payload := make([]byte, 32*1024) // 32KB buffer typical for circuit forwarding
	for i := range payload {
		payload[i] = byte(i)
	}

	// Start a background reader that discards data
	go func() {
		buf := make([]byte, 32*1024)
		for {
			_, err := serverZstd.Read(buf)
			if err != nil {
				return
			}
		}
	}()

	b.SetBytes(int64(len(payload)))
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := clientZstd.Write(payload)
		if err != nil {
			b.Fatalf("Write: %v", err)
		}
	}
}

// BenchmarkRead measures the raw throughput of the ZstdConn wrapper
// when reading and decompressing data.
func BenchmarkRead(b *testing.B) {
	b.ReportAllocs()

	clientRaw, serverRaw := net.Pipe()

	clientZstd, err := NewZstdConn(clientRaw)
	if err != nil {
		b.Fatalf("NewZstdConn: %v", err)
	}

	serverZstd, err := NewZstdConn(serverRaw)
	if err != nil {
		b.Fatalf("NewZstdConn: %v", err)
	}

	defer func() { _ = clientZstd.Close() }()
	defer func() { _ = serverZstd.Close() }()

	payload := make([]byte, 32*1024)
	for i := range payload {
		payload[i] = byte(i)
	}

	// Start a background writer that continuously feeds data
	go func() {
		for {
			_, err := clientZstd.Write(payload)
			if err != nil {
				return
			}
		}
	}()

	buf := make([]byte, 32*1024)
	b.SetBytes(int64(len(buf)))
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := serverZstd.Read(buf)
		if err != nil {
			b.Fatalf("Read: %v", err)
		}
	}
}
