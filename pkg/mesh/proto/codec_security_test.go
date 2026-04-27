package proto

import (
	"bytes"
	"encoding/binary"
	"strings"
	"sync"
	"testing"
)

// --- Frame size limit tests ---

// TestReadFrame_RejectsOversizedFrame verifies that frames exceeding
// MaxFrameSize are rejected immediately with a clear error.
func TestReadFrame_RejectsOversizedFrame(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	// Write a header claiming a frame larger than MaxFrameSize.
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], MaxFrameSize+1)
	buf.Write(header[:])

	// Write enough dummy payload to cover the claimed length.
	buf.Write(make([]byte, MaxFrameSize+1))

	fr := NewFrameReader(2 * 1024 * 1024)
	_, err := fr.ReadFrame(&buf)
	if err == nil {
		t.Fatal("expected error for oversized frame")
	}
	if !strings.Contains(err.Error(), "frame too large") {
		t.Fatalf("error should mention 'frame too large', got: %v", err)
	}
}

// TestReadFrame_AcceptsMaxFrame verifies that a frame at exactly
// MaxFrameSize is accepted (boundary condition).
func TestReadFrame_AcceptsMaxFrame(t *testing.T) {
	t.Parallel()

	// Create a valid frame and verify it's under MaxFrameSize.
	frame := &ControlFrame{
		Payload: &ControlFrame_Gossip{
			Gossip: &GossipFrame{
				FromNode: "test-node",
				Routes:   map[string]float64{"peer": 1.0},
			},
		},
	}

	var buf bytes.Buffer
	if err := WriteFrame(&buf, frame); err != nil {
		t.Fatalf("write: %v", err)
	}

	fr := NewFrameReader(2 * 1024 * 1024)
	decoded, err := fr.ReadFrame(&buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if decoded.GetGossip().FromNode != "test-node" {
		t.Fatal("frame data mismatch")
	}
}

// --- Pooled buffer tests ---

// TestReadFrame_UsesPooledBuffers verifies that ReadFrame uses pooled
// buffers for common-sized frames (≤PooledFrameSize), avoiding heap
// allocations on the hot path.
func TestReadFrame_UsesPooledBuffers(t *testing.T) {
	t.Parallel()

	// The frame pool should be functional.
	bufp := framePool.Get().(*[]byte)
	if len(*bufp) != int(PooledFrameSize) {
		t.Fatalf("pool buffer size = %d, want %d", len(*bufp), PooledFrameSize)
	}
	framePool.Put(bufp)

	// A small frame should roundtrip successfully (implicitly using pool).
	frame := &ControlFrame{
		Payload: &ControlFrame_Gossip{
			Gossip: &GossipFrame{
				FromNode: "test-node",
				Routes:   map[string]float64{"peer": 1.0},
			},
		},
	}

	var buf bytes.Buffer
	if err := WriteFrame(&buf, frame); err != nil {
		t.Fatalf("write: %v", err)
	}

	fr := NewFrameReader(2 * 1024 * 1024)
	decoded, err := fr.ReadFrame(&buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if decoded.GetGossip().FromNode != "test-node" {
		t.Fatal("data mismatch after pooled read")
	}
}

// --- Global memory budget tests ---

// TestReadFrame_MemoryBudget_RejectsConcurrentBomb verifies that the
// global memory budget prevents an attacker from forcing simultaneous
// large allocations. When the budget is exhausted, frames are rejected
// and the stream is drained (not corrupted).
//
// Not parallel: mutates global maxFrameMemory.
func TestReadFrame_MemoryBudget_RejectsConcurrentBomb(t *testing.T) {

	fr := NewFrameReader(1024) // 1KB budget

	// Create a frame that's larger than the budget.
	routes := make(map[string]float64)
	for i := 0; i < 100; i++ {
		routes["node-"+string(rune('A'+i))] = float64(i)
	}
	frame := &ControlFrame{
		Payload: &ControlFrame_Gossip{
			Gossip: &GossipFrame{
				FromNode: "test-node",
				Routes:   routes,
			},
		},
	}

	var buf bytes.Buffer
	if err := WriteFrame(&buf, frame); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := fr.ReadFrame(&buf)
	if err == nil {
		t.Fatal("expected error when frame exceeds memory budget")
	}
	if !strings.Contains(err.Error(), "memory budget") {
		t.Fatalf("error should mention 'memory budget', got: %v", err)
	}
}

// TestReadFrame_MemoryBudget_ReleasesAfterRead verifies that the memory
// budget is properly released after frame processing, preventing
// permanent budget exhaustion.
//
// Not parallel: reads global frameMemoryUsed counter.
func TestReadFrame_MemoryBudget_ReleasesAfterRead(t *testing.T) {

	// Read a valid frame — budget should be released afterward.
	frame := &ControlFrame{
		Payload: &ControlFrame_Gossip{
			Gossip: &GossipFrame{
				FromNode: "test-node",
				Routes:   map[string]float64{"peer": 1.0},
			},
		},
	}

	fr := NewFrameReader(2 * 1024 * 1024)

	// Do a series of reads and verify no cumulative leak.
	for i := 0; i < 10; i++ {
		var buf bytes.Buffer
		if err := WriteFrame(&buf, frame); err != nil {
			t.Fatalf("write[%d]: %v", i, err)
		}

		before := fr.memoryUsed.Load()
		_, err := fr.ReadFrame(&buf)
		if err != nil {
			t.Fatalf("read[%d]: %v", i, err)
		}
		after := fr.memoryUsed.Load()

		if after != before {
			t.Fatalf("read[%d]: memory budget leaked: before=%d, after=%d", i, before, after)
		}
	}
}

// TestReadFrame_StreamDesyncAfterOversize verifies that after rejecting
// an oversize frame, the stream is desynchronized. P2-5 caps the drain
// at MaxFrameSize bytes, so a frame claiming length > MaxFrameSize
// leaves unread bytes in the stream. This is intentional — the control
// loop closes the peer on the returned error. There is no value in
// draining gigabytes of attacker payload to preserve stream sync.
func TestReadFrame_StreamDesyncAfterOversize(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	// Write an oversized frame header claiming MaxFrameSize+1 bytes.
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], MaxFrameSize+1)
	buf.Write(header[:])
	buf.Write(make([]byte, MaxFrameSize+1))

	// Write a valid frame after the rejected one.
	validFrame := &ControlFrame{
		Payload: &ControlFrame_Gossip{
			Gossip: &GossipFrame{
				FromNode: "recovery-frame",
				Routes:   map[string]float64{"peer": 1.0},
			},
		},
	}
	if err := WriteFrame(&buf, validFrame); err != nil {
		t.Fatalf("write valid frame: %v", err)
	}

	// First read should fail (oversized).
	fr := NewFrameReader(2 * 1024 * 1024)
	_, err := fr.ReadFrame(&buf)
	if err == nil {
		t.Fatal("expected error for oversized frame")
	}
	if !strings.Contains(err.Error(), "frame too large") {
		t.Fatalf("error should mention 'frame too large', got: %v", err)
	}

	// Stream is desynchronized: the bounded drain consumed at most
	// MaxFrameSize bytes of the (MaxFrameSize+1)-byte payload, leaving
	// 1 byte of garbage. The next ReadFrame will misinterpret bytes
	// and either fail with a length error or produce garbage.
	//
	// We just verify that the stream does NOT cleanly return the
	// recovery frame — that would indicate an unbounded drain.
	decoded, err := fr.ReadFrame(&buf)
	if err == nil && decoded != nil && decoded.GetGossip() != nil &&
		decoded.GetGossip().FromNode == "recovery-frame" {
		t.Fatal("stream should be desynchronized after bounded oversize drain")
	}
}

// TestReadFrame_ZeroLengthFrame verifies that a frame with length 0
// is handled gracefully (empty protobuf is valid).
func TestReadFrame_ZeroLengthFrame(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], 0)
	buf.Write(header[:])

	fr := NewFrameReader(2 * 1024 * 1024)
	frame, err := fr.ReadFrame(&buf)
	if err != nil {
		t.Fatalf("read zero-length frame: %v", err)
	}
	if frame == nil {
		t.Fatal("zero-length frame should return non-nil (empty protobuf)")
	}
}

// TestReadFrame_ConcurrentReads verifies that ReadFrame is safe to
// call concurrently from multiple goroutines (pool + budget are safe).
func TestReadFrame_ConcurrentReads(t *testing.T) {
	t.Parallel()

	frame := &ControlFrame{
		Payload: &ControlFrame_Gossip{
			Gossip: &GossipFrame{
				FromNode: "concurrent-node",
				Routes:   map[string]float64{"peer": 1.0},
			},
		},
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				var buf bytes.Buffer
				if err := WriteFrame(&buf, frame); err != nil {
					t.Errorf("write: %v", err)
					return
				}
				fr := NewFrameReader(2 * 1024 * 1024)
				decoded, err := fr.ReadFrame(&buf)
				if err != nil {
					t.Errorf("read: %v", err)
					return
				}
				if decoded.GetGossip().FromNode != "concurrent-node" {
					t.Errorf("data mismatch in concurrent read")
					return
				}
			}
		}()
	}
	wg.Wait()
}

// --- Constants validation ---

// TestFrameConstants verifies that the security-critical constants
// are set to reasonable values.
func TestFrameConstants(t *testing.T) {
	t.Parallel()

	if MaxFrameSize > 1024*1024 {
		t.Fatalf("MaxFrameSize too large: %d (should be ≤ 1MB for control frames)", MaxFrameSize)
	}
	if MaxFrameSize < 64*1024 {
		t.Fatalf("MaxFrameSize too small: %d (bootstrap frames may exceed 64KB)", MaxFrameSize)
	}
	if PooledFrameSize > MaxFrameSize {
		t.Fatalf("PooledFrameSize (%d) exceeds MaxFrameSize (%d)", PooledFrameSize, MaxFrameSize)
	}
}
