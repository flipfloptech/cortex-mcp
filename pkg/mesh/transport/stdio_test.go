package transport

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

// --- Test helpers ---

// trackingCloser wraps an io.Reader or io.Writer and records whether Close was called.
type trackingCloser struct {
	io.Reader
	io.Writer
	closed bool
	mu     sync.Mutex
}

func (tc *trackingCloser) Close() error {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	tc.closed = true
	return nil
}

func (tc *trackingCloser) wasClosed() bool {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return tc.closed
}

// failingCloser returns an error on Close.
type failingCloser struct {
	io.Reader
	io.Writer
}

func (fc *failingCloser) Close() error {
	return errors.New("close failed")
}

// --- net.Conn contract ---

func TestStdioConn_ImplementsNetConn(t *testing.T) {
	t.Parallel()
	r, w := io.Pipe()
	defer func() {
		if err := r.Close(); err != nil {
			t.Logf("close pipe reader: %v", err)
		}
	}()
	defer func() {
		if err := w.Close(); err != nil {
			t.Logf("close pipe writer: %v", err)
		}
	}()

	conn := NewStdioConn(r, w)
	// Compile-time and runtime assertion that StdioConn satisfies net.Conn.
	var _ = conn // compile-time net.Conn assertion via NewStdioConn return type
	if conn == nil {
		t.Fatal("NewStdioConn returned nil")
	}
}

// --- Read / Write ---

func TestStdioConn_ReadWriteRoundTrip(t *testing.T) {
	t.Parallel()
	// conn reads from pr (simulating stdin), conn writes to outW (simulating stdout).
	pr, pw := io.Pipe()
	defer func() {
		if err := pr.Close(); err != nil {
			t.Logf("close pr: %v", err)
		}
	}()
	defer func() {
		if err := pw.Close(); err != nil {
			t.Logf("close pw: %v", err)
		}
	}()

	outR, outW := io.Pipe()
	defer func() {
		if err := outR.Close(); err != nil {
			t.Logf("close outR: %v", err)
		}
	}()
	defer func() {
		if err := outW.Close(); err != nil {
			t.Logf("close outW: %v", err)
		}
	}()

	conn := NewStdioConn(pr, outW)
	defer func() {
		if err := conn.Close(); err != nil {
			t.Logf("close conn: %v", err)
		}
	}()

	payload := []byte("hello, mesh")

	// Write payload into the conn's reader side (simulating stdin data arriving).
	go func() {
		if _, err := pw.Write(payload); err != nil {
			// pipe write errors are expected.
			return
		}
	}()

	// Read from the conn — should get the payload.
	buf := make([]byte, len(payload))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if n != len(payload) {
		t.Fatalf("Read %d bytes, want %d", n, len(payload))
	}
	if !bytes.Equal(buf, payload) {
		t.Fatalf("Read %q, want %q", buf, payload)
	}

	// Write through the conn — should appear on outR.
	outPayload := []byte("response from mesh")
	go func() {
		if _, err := conn.Write(outPayload); err != nil {
			// write errors expected.
			return
		}
	}()

	outBuf := make([]byte, len(outPayload))
	n, err = outR.Read(outBuf)
	if err != nil {
		t.Fatalf("outR.Read error: %v", err)
	}
	if n != len(outPayload) {
		t.Fatalf("outR.Read %d bytes, want %d", n, len(outPayload))
	}
	if !bytes.Equal(outBuf, outPayload) {
		t.Fatalf("outR.Read %q, want %q", outBuf, outPayload)
	}
}

// --- Close behavior ---

func TestStdioConn_CloseClosesUnderlyingClosers(t *testing.T) {
	t.Parallel()
	reader := &trackingCloser{Reader: bytes.NewReader(nil)}
	writer := &trackingCloser{Writer: io.Discard}

	conn := NewStdioConn(reader, writer)
	err := conn.Close()
	if err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if !reader.wasClosed() {
		t.Error("underlying reader was not closed")
	}
	if !writer.wasClosed() {
		t.Error("underlying writer was not closed")
	}
}

func TestStdioConn_CloseIdempotent(t *testing.T) {
	t.Parallel()
	r, w := io.Pipe()
	defer func() {
		if err := r.Close(); err != nil {
			t.Logf("close r: %v", err)
		}
	}()
	defer func() {
		if err := w.Close(); err != nil {
			t.Logf("close w: %v", err)
		}
	}()

	conn := NewStdioConn(r, w)

	// First close should succeed.
	if err := conn.Close(); err != nil {
		t.Fatalf("first Close error: %v", err)
	}
	// Second close should be a no-op (no panic, no error).
	if err := conn.Close(); err != nil {
		t.Fatalf("second Close error: %v", err)
	}
}

func TestStdioConn_CloseNonCloseableStreams(t *testing.T) {
	t.Parallel()
	// bytes.Reader implements io.Reader but not io.Closer.
	// io.Discard implements io.Writer but not io.Closer.
	reader := bytes.NewReader([]byte("data"))
	writer := io.Discard

	conn := NewStdioConn(reader, writer)
	// Close should succeed even though neither stream is Closeable.
	if err := conn.Close(); err != nil {
		t.Fatalf("Close returned error on non-closeable streams: %v", err)
	}
}

func TestStdioConn_CloseReturnsFirstError(t *testing.T) {
	t.Parallel()
	reader := &failingCloser{Reader: bytes.NewReader(nil)}
	writer := &trackingCloser{Writer: io.Discard}

	conn := NewStdioConn(reader, writer)
	err := conn.Close()
	if err == nil {
		t.Fatal("Close should return error when underlying closer fails")
	}
	// Writer should still be closed even when reader close fails.
	if !writer.wasClosed() {
		t.Error("writer was not closed despite reader close failure")
	}
}

// --- Address semantics ---

func TestStdioConn_LocalAddr(t *testing.T) {
	t.Parallel()
	conn := NewStdioConn(bytes.NewReader(nil), io.Discard)
	defer func() {
		if err := conn.Close(); err != nil {
			t.Logf("close conn: %v", err)
		}
	}()

	addr := conn.LocalAddr()
	if addr == nil {
		t.Fatal("LocalAddr returned nil")
	}
	if addr.Network() != "stdio" {
		t.Errorf("LocalAddr.Network() = %q, want %q", addr.Network(), "stdio")
	}
	if addr.String() == "" {
		t.Error("LocalAddr.String() should not be empty")
	}
}

func TestStdioConn_RemoteAddr(t *testing.T) {
	t.Parallel()
	conn := NewStdioConn(bytes.NewReader(nil), io.Discard)
	defer func() {
		if err := conn.Close(); err != nil {
			t.Logf("close conn: %v", err)
		}
	}()

	addr := conn.RemoteAddr()
	if addr == nil {
		t.Fatal("RemoteAddr returned nil")
	}
	if addr.Network() != "stdio" {
		t.Errorf("RemoteAddr.Network() = %q, want %q", addr.Network(), "stdio")
	}
	if addr.String() == "" {
		t.Error("RemoteAddr.String() should not be empty")
	}
}

// --- Deadline behavior ---

func TestStdioConn_SetDeadlineReturnsError(t *testing.T) {
	t.Parallel()
	conn := NewStdioConn(bytes.NewReader(nil), io.Discard)
	defer func() {
		if err := conn.Close(); err != nil {
			t.Logf("close conn: %v", err)
		}
	}()

	if err := conn.SetDeadline(time.Now()); err == nil {
		t.Error("SetDeadline should return ErrDeadlinesNotSupported")
	}
	if err := conn.SetReadDeadline(time.Now()); err == nil {
		t.Error("SetReadDeadline should return ErrDeadlinesNotSupported")
	}
	if err := conn.SetWriteDeadline(time.Now()); err == nil {
		t.Error("SetWriteDeadline should return ErrDeadlinesNotSupported")
	}
}

// --- Read/Write after close ---

func TestStdioConn_ReadAfterClose(t *testing.T) {
	t.Parallel()
	pr, pw := io.Pipe()
	defer func() {
		if err := pw.Close(); err != nil {
			t.Logf("close pw: %v", err)
		}
	}()

	conn := NewStdioConn(pr, io.Discard)
	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	buf := make([]byte, 10)
	_, err := conn.Read(buf)
	if err == nil {
		t.Error("Read after Close should return an error")
	}
}

func TestStdioConn_WriteAfterClose(t *testing.T) {
	t.Parallel()
	conn := NewStdioConn(bytes.NewReader(nil), io.Discard)
	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err := conn.Write([]byte("should fail"))
	if err == nil {
		t.Error("Write after Close should return an error")
	}
}

// --- EOF propagation ---

func TestStdioConn_ReadPropagatesEOF(t *testing.T) {
	t.Parallel()
	data := []byte("short")
	conn := NewStdioConn(bytes.NewReader(data), io.Discard)
	defer func() {
		if err := conn.Close(); err != nil {
			t.Logf("close conn: %v", err)
		}
	}()

	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("first Read error: %v", err)
	}
	if !bytes.Equal(buf[:n], data) {
		t.Fatalf("Read %q, want %q", buf[:n], data)
	}

	// Next read should return io.EOF.
	_, err = conn.Read(buf)
	if err != io.EOF {
		t.Fatalf("expected io.EOF, got %v", err)
	}
}

// --- Large payload ---

func TestStdioConn_LargePayload(t *testing.T) {
	t.Parallel()
	pr, pw := io.Pipe()

	conn := NewStdioConn(pr, io.Discard)
	defer func() {
		if err := conn.Close(); err != nil {
			t.Logf("close conn: %v", err)
		}
	}()

	// 1MB payload
	const size = 1 << 20
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i % 251) // prime modulus for varied data
	}

	// Writer goroutine: push payload into the conn's reader side.
	errCh := make(chan error, 2)
	go func() {
		_, err := pw.Write(payload)
		if cerr := pw.Close(); cerr != nil && err == nil {
			err = cerr
		}
		errCh <- err
	}()

	// Reader goroutine: read everything the conn reads.
	var readBuf bytes.Buffer
	go func() {
		_, err := io.Copy(&readBuf, conn)
		errCh <- err
	}()

	// Wait for both.
	if err := <-errCh; err != nil {
		t.Fatalf("pipe write error: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("conn read error: %v", err)
	}

	if readBuf.Len() != size {
		t.Fatalf("read %d bytes, want %d", readBuf.Len(), size)
	}
	if !bytes.Equal(readBuf.Bytes(), payload) {
		t.Fatal("payload mismatch on large read")
	}
}

// --- Concurrency safety ---

func TestStdioConn_ConcurrentReadWrite(t *testing.T) {
	t.Parallel()
	pr, pw := io.Pipe()
	outR, outW := io.Pipe()

	conn := NewStdioConn(pr, outW)
	defer func() {
		if err := conn.Close(); err != nil {
			t.Logf("close conn: %v", err)
		}
	}()

	const goroutines = 10
	const msgSize = 64

	var wg sync.WaitGroup

	// Concurrent writers through the conn.
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			msg := make([]byte, msgSize)
			if _, err := conn.Write(msg); err != nil {
				// expected when conn is closed.
				return
			}
		}()
	}

	// Drain the output side.
	go func() {
		wg.Wait()
		if err := outW.Close(); err != nil {
			// expected in test.
			return
		}
	}()
	if _, err := io.Copy(io.Discard, outR); err != nil {
		t.Logf("drain outR: %v", err)
	}

	// Concurrent readers from the conn.
	var wg2 sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg2.Add(1)
		go func() {
			defer wg2.Done()
			buf := make([]byte, msgSize)
			if _, err := conn.Read(buf); err != nil {
				// expected when conn is closed.
				return
			}
		}()
	}

	// Feed data to satisfy readers, then close.
	feedData := make([]byte, goroutines*msgSize)
	go func() {
		if _, err := pw.Write(feedData); err != nil {
			// expected.
			return
		}
		if err := pw.Close(); err != nil {
			// expected.
			return
		}
	}()

	wg2.Wait()
}
