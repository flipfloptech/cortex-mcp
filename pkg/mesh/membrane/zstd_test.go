package membrane

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

func TestZstdConn_Roundtrip(t *testing.T) {
	t.Parallel()

	clientRaw, serverRaw := net.Pipe()
	defer func() { _ = clientRaw.Close() }()
	defer func() { _ = serverRaw.Close() }()

	var clientZstd, serverZstd *ZstdConn

	errCh := make(chan error, 2)
	go func() {
		zc, err := NewZstdConn(clientRaw)
		clientZstd = zc
		errCh <- err
	}()
	go func() {
		zc, err := NewZstdConn(serverRaw)
		serverZstd = zc
		errCh <- err
	}()

	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("NewZstdConn failed: %v", err)
		}
	}
	defer func() { _ = clientZstd.Close() }()
	defer func() { _ = serverZstd.Close() }()

	payload := []byte("hello zstd compressed world!")

	// Write from client to server
	go func() {
		if _, err := clientZstd.Write(payload); err != nil {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(serverZstd, buf); err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	if !bytes.Equal(buf, payload) {
		t.Fatalf("got %q, want %q", buf, payload)
	}
}

func TestZstdConn_Close(t *testing.T) {
	t.Parallel()

	clientRaw, serverRaw := net.Pipe()

	clientZstd, err := NewZstdConn(clientRaw)
	if err != nil {
		t.Fatalf("NewZstdConn: %v", err)
	}
	serverZstd, err := NewZstdConn(serverRaw)
	if err != nil {
		t.Fatalf("NewZstdConn: %v", err)
	}

	if err := clientZstd.Close(); err != nil {
		t.Fatalf("clientZstd.Close() failed: %v", err)
	}

	// Read on server should return EOF because client closed
	buf := make([]byte, 10)
	_ = serverRaw.SetReadDeadline(time.Now().Add(1 * time.Second))
	_, err = serverZstd.Read(buf)
	if err != io.EOF && err != io.ErrUnexpectedEOF && err != io.ErrClosedPipe {
		t.Fatalf("expected EOF or closed pipe, got %v", err)
	}
}
