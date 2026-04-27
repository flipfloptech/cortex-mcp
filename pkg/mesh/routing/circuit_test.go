package routing

import (
	"bytes"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// --- Stitch: bidirectional copy ---

func TestStitch_BidirectionalCopy(t *testing.T) {
	t.Parallel()

	a1, a2 := net.Pipe()
	b1, b2 := net.Pipe()
	defer func() { _ = a1.Close() }()
	defer func() { _ = a2.Close() }()
	defer func() { _ = b1.Close() }()
	defer func() { _ = b2.Close() }()

	// Stitch a2 ↔ b1 (the intermediate node's job)
	done := make(chan StitchResult, 1)
	go func() {
		done <- Stitch(a2, b1)
	}()

	// Write from a1 → should appear at b2
	payload := []byte("hello from A")
	go func() { _, _ = a1.Write(payload) }()

	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(b2, buf); err != nil {
		t.Fatalf("Read from B: %v", err)
	}
	if !bytes.Equal(buf, payload) {
		t.Fatalf("got %q, want %q", buf, payload)
	}

	// Write from b2 → should appear at a1
	response := []byte("hello from B")
	go func() { _, _ = b2.Write(response) }()

	rbuf := make([]byte, len(response))
	if _, err := io.ReadFull(a1, rbuf); err != nil {
		t.Fatalf("Read from A: %v", err)
	}
	if !bytes.Equal(rbuf, response) {
		t.Fatalf("got %q, want %q", rbuf, response)
	}

	// Close one side → stitch should complete
	_ = a1.Close()
	_ = b2.Close()

	result := <-done
	// At least one direction should have transferred data.
	if result.BytesAtoB == 0 && result.BytesBtoA == 0 {
		t.Fatal("expected non-zero bytes transferred")
	}
}

// --- Stitch uses pooled buffers ---

func TestStitch_UsesPooledBuffers(t *testing.T) {
	t.Parallel()

	// Verify the buffer pool works without panicking.
	buf := bufferPool.get()
	if len(buf) != stitchBufSize {
		t.Fatalf("pool buffer size = %d, want %d", len(buf), stitchBufSize)
	}
	bufferPool.put(buf)
}

// --- Large payload through stitch ---

func TestStitch_LargePayload(t *testing.T) {
	t.Parallel()

	a1, a2 := net.Pipe()
	b1, b2 := net.Pipe()
	defer func() { _ = a1.Close() }()
	defer func() { _ = a2.Close() }()
	defer func() { _ = b1.Close() }()
	defer func() { _ = b2.Close() }()

	done := make(chan StitchResult, 1)
	go func() {
		done <- Stitch(a2, b1)
	}()

	const size = 1024 * 1024 // 1MB
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i % 251)
	}

	// Write in background.
	writeErr := make(chan error, 1)
	go func() {
		_, err := a1.Write(payload)
		_ = a1.Close() // signal EOF
		writeErr <- err
	}()

	received := make([]byte, size)
	if _, err := io.ReadFull(b2, received); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !bytes.Equal(received, payload) {
		t.Fatal("payload mismatch through stitch")
	}
}

// --- StitchResult reports bytes transferred ---

func TestStitchResult_Fields(t *testing.T) {
	t.Parallel()

	a1, a2 := net.Pipe()
	b1, b2 := net.Pipe()

	done := make(chan StitchResult, 1)
	go func() {
		done <- Stitch(a2, b1)
	}()

	// Write known amount from a1 → b2
	payload := []byte("12345")
	go func() {
		_, _ = a1.Write(payload)
		_ = a1.Close()
	}()
	_, _ = io.ReadAll(b2)
	_ = b2.Close()

	result := <-done
	if result.BytesAtoB != int64(len(payload)) {
		t.Fatalf("BytesAtoB = %d, want %d", result.BytesAtoB, len(payload))
	}
}

// --- Stitch handles one-sided close ---

func TestStitch_OneSidedClose(t *testing.T) {
	t.Parallel()

	a1, a2 := net.Pipe()
	b1, b2 := net.Pipe()

	done := make(chan StitchResult, 1)
	go func() {
		done <- Stitch(a2, b1)
	}()

	// Close only one side.
	_ = a1.Close()

	select {
	case <-done:
		// Good — stitch completed.
	case <-time.After(2 * time.Second):
		t.Fatal("stitch should complete after one-sided close")
	}

	_ = b2.Close()
}

// --- Concurrent stitches ---

func TestStitch_ConcurrentMultiple(t *testing.T) {
	t.Parallel()

	const numStitches = 5
	var wg sync.WaitGroup
	wg.Add(numStitches)

	for i := 0; i < numStitches; i++ {
		go func() {
			defer wg.Done()
			a1, a2 := net.Pipe()
			b1, b2 := net.Pipe()

			done := make(chan StitchResult, 1)
			go func() {
				done <- Stitch(a2, b1)
			}()

			_, _ = a1.Write([]byte("ping"))
			_ = a1.Close()
			_, _ = io.ReadAll(b2)
			_ = b2.Close()
			<-done
		}()
	}

	wg.Wait()
}
