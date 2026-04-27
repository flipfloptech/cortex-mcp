package testutil

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

func TestBufferedPipe_ReadWrite(t *testing.T) {
	t.Parallel()
	c1, c2 := BufferedPipe(1024)

	msg := []byte("hello world")
	n, err := c1.Write(msg)
	if err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}
	if n != len(msg) {
		t.Fatalf("expected write %d bytes, got %d", len(msg), n)
	}

	buf := make([]byte, 100)
	n, err = c2.Read(buf)
	if err != nil {
		t.Fatalf("unexpected read error: %v", err)
	}
	if n != len(msg) {
		t.Fatalf("expected read %d bytes, got %d", len(msg), n)
	}
	if !bytes.Equal(buf[:n], msg) {
		t.Fatalf("expected %q, got %q", msg, buf[:n])
	}

	// Also verify opposite direction
	msg2 := []byte("hello back")
	_, _ = c2.Write(msg2)
	n, _ = c1.Read(buf)
	if !bytes.Equal(buf[:n], msg2) {
		t.Fatalf("expected %q, got %q", msg2, buf[:n])
	}
}

func TestBufferedPipe_CloseRead(t *testing.T) {
	t.Parallel()
	c1, c2 := BufferedPipe(1024)

	_ = c1.Close()

	// Read from c2 should return io.EOF since c1 is closed
	buf := make([]byte, 100)
	_, err := c2.Read(buf)
	if err != io.EOF {
		t.Fatalf("expected io.EOF on read, got %v", err)
	}

	// Write to c1 should return error (closed)
	_, err = c1.Write([]byte("foo"))
	if err == nil {
		t.Fatalf("expected error writing to closed pipe")
	}
}

func TestBufferedPipe_BlockingWrite(t *testing.T) {
	t.Parallel()
	c1, c2 := BufferedPipe(10)

	// Write 10 bytes (full capacity)
	n, err := c1.Write([]byte("0123456789"))
	if err != nil || n != 10 {
		t.Fatalf("failed initial write: %d, %v", n, err)
	}

	writeDone := make(chan struct{})
	go func() {
		// This should block because buffer is full
		_, _ = c1.Write([]byte("blocked!"))
		close(writeDone)
	}()

	select {
	case <-writeDone:
		t.Fatalf("write didn't block when buffer was full")
	case <-time.After(50 * time.Millisecond):
		// Expected behavior
	}

	// Now read 8 bytes to free up enough space for the whole write
	buf := make([]byte, 8)
	_, _ = c2.Read(buf)

	// The write should now eventually unblock (partially)
	select {
	case <-writeDone:
		// Success
	case <-time.After(1 * time.Second):
		t.Fatalf("write didn't unblock after space was freed")
	}
}

func TestBufferedPipe_ReadDeadline(t *testing.T) {
	t.Parallel()
	c1, _ := BufferedPipe(1024)

	// Should immediately timeout
	_ = c1.SetReadDeadline(time.Now().Add(10 * time.Millisecond))

	buf := make([]byte, 10)
	_, err := c1.Read(buf)
	if err == nil {
		t.Fatalf("expected deadline timeout, got nil")
	}
	if nerr, ok := err.(net.Error); !ok || !nerr.Timeout() {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func TestBufferedPipe_WriteDeadline(t *testing.T) {
	t.Parallel()
	c1, _ := BufferedPipe(10)

	// Fill buffer
	_, _ = c1.Write([]byte("0123456789"))

	_ = c1.SetWriteDeadline(time.Now().Add(10 * time.Millisecond))
	_, err := c1.Write([]byte("fail"))
	if err == nil {
		t.Fatalf("expected deadline timeout, got nil")
	}
	if nerr, ok := err.(net.Error); !ok || !nerr.Timeout() {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func BenchmarkNetPipe(b *testing.B) {
	c1, c2 := net.Pipe()
	defer func() { _ = c1.Close() }()
	defer func() { _ = c2.Close() }()

	payload := make([]byte, 1024)
	go func() {
		buf := make([]byte, 1024)
		for {
			_, err := c2.Read(buf)
			if err != nil {
				return
			}
		}
	}()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = c1.Write(payload)
	}
}

func BenchmarkBufferedPipe(b *testing.B) {
	c1, c2 := BufferedPipe(1 << 20) // 1MB buffer
	defer func() { _ = c1.Close() }()
	defer func() { _ = c2.Close() }()

	payload := make([]byte, 1024)
	go func() {
		buf := make([]byte, 1024)
		for {
			_, err := c2.Read(buf)
			if err != nil {
				return
			}
		}
	}()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = c1.Write(payload)
	}
}
