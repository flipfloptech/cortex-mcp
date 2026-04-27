// Package proto provides protobuf-based wire format for the cortex-mesh
// control plane and tool protocol.
//
// This file provides convenience functions for marshaling/unmarshaling
// control frames and tool messages using length-prefixed framing suitable
// for streaming over yamux Stream 0.
package proto

import (
	"encoding/binary"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	pb "google.golang.org/protobuf/proto"
)

// --- Security-critical frame size constants ---

// MaxFrameSize is the absolute maximum size of a single control frame.
// Frames exceeding this are rejected immediately. Set to 256KB which
// comfortably accommodates bootstrap frames with 3000+ hosts. No
// legitimate control frame should ever approach this limit.
//
// Rationale for 256KB (down from 4MB):
//   - Gossip frames: ~200 bytes (routes for a handful of peers)
//   - WhoHas/IHave: ~100 bytes
//   - Bootstrap with 1000 hosts: ~80KB worst case
//   - Credential frames: ~1KB
const MaxFrameSize uint32 = 256 * 1024

// PooledFrameSize is the buffer size maintained in the frame pool.
// Frames at or below this size use pooled buffers (zero heap allocation
// in the hot path). Frames above this but below MaxFrameSize use dynamic
// allocation (rare, logged as anomalous).
const PooledFrameSize uint32 = 64 * 1024

// framePool provides reusable buffers for common-sized control frames.
// This eliminates heap allocations for the vast majority of control
// plane traffic (gossip every 3s, Sonar requests, IHave responses).
var framePool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, PooledFrameSize)
		return &buf
	},
}

// MarshalFrame serializes a ControlFrame to bytes.
func MarshalFrame(frame *ControlFrame) ([]byte, error) {
	return pb.Marshal(frame)
}

// UnmarshalFrame deserializes bytes into a ControlFrame.
func UnmarshalFrame(data []byte) (*ControlFrame, error) {
	frame := &ControlFrame{}
	if err := pb.Unmarshal(data, frame); err != nil {
		return nil, fmt.Errorf("proto: unmarshal frame: %w", err)
	}
	return frame, nil
}

// MarshalToolRequest serializes a ToolRequest to bytes.
func MarshalToolRequest(req *ToolRequest) ([]byte, error) {
	return pb.Marshal(req)
}

// UnmarshalToolRequest deserializes bytes into a ToolRequest.
func UnmarshalToolRequest(data []byte) (*ToolRequest, error) {
	req := &ToolRequest{}
	if err := pb.Unmarshal(data, req); err != nil {
		return nil, fmt.Errorf("proto: unmarshal tool request: %w", err)
	}
	return req, nil
}

// MarshalToolResponse serializes a ToolResponse to bytes.
func MarshalToolResponse(resp *ToolResponse) ([]byte, error) {
	return pb.Marshal(resp)
}

// UnmarshalToolResponse deserializes bytes into a ToolResponse.
func UnmarshalToolResponse(data []byte) (*ToolResponse, error) {
	resp := &ToolResponse{}
	if err := pb.Unmarshal(data, resp); err != nil {
		return nil, fmt.Errorf("proto: unmarshal tool response: %w", err)
	}
	return resp, nil
}

// --- Stream-based framing ---
// Length-prefixed framing: 4-byte big-endian length + protobuf payload.

// WriteFrame writes a length-prefixed protobuf ControlFrame to a writer.
func WriteFrame(w io.Writer, frame *ControlFrame) error {
	data, err := pb.Marshal(frame)
	if err != nil {
		return fmt.Errorf("proto: marshal frame: %w", err)
	}

	// Write 4-byte big-endian length header.
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	if _, err := w.Write(header[:]); err != nil {
		return fmt.Errorf("proto: write header: %w", err)
	}

	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("proto: write payload: %w", err)
	}

	return nil
}

// WriteRawFrame writes a length-prefixed frame from pre-marshaled bytes.
// Use this when the same ControlFrame is sent to multiple peers — marshal
// once with MarshalFrame, then WriteRawFrame to each peer. This eliminates
// redundant protobuf marshaling (the N-1 extra marshals in a broadcast).
//
// The wire format is identical to WriteFrame: 4-byte big-endian length + payload.
func WriteRawFrame(w io.Writer, data []byte) error {
	// Write 4-byte big-endian length header.
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	if _, err := w.Write(header[:]); err != nil {
		return fmt.Errorf("proto: write header: %w", err)
	}

	if len(data) > 0 {
		if _, err := w.Write(data); err != nil {
			return fmt.Errorf("proto: write payload: %w", err)
		}
	}

	return nil
}

// FrameReader provides per-reader frame budget isolation (H-3).
//
// The global ReadFrame function uses a shared atomic counter across all
// concurrent readers. In a dense mesh (50+ peers, each with a control
// loop calling ReadFrame), the shared 16MB budget is exhausted by
// normal gossip traffic, causing legitimate frames to be dropped.
//
// FrameReader gives each control loop its own budget. A busy peer
// cannot starve other peers' control frame processing.
type FrameReader struct {
	memoryUsed   atomic.Int64
	memoryBudget int64
}

// NewFrameReader creates a FrameReader with the given per-reader memory budget.
// Typical budget: 2MB per peer (enough for ~8 concurrent 256KB frames).
func NewFrameReader(budget int64) *FrameReader {
	return &FrameReader{
		memoryBudget: budget,
	}
}

// ReadFrame reads a length-prefixed protobuf ControlFrame using this
// reader's isolated memory budget. The behavior is identical to the
// package-level ReadFrame, but the budget tracking is per-reader.
func (fr *FrameReader) ReadFrame(r io.Reader) (*ControlFrame, error) {
	// Read 4-byte length header.
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, fmt.Errorf("proto: read header: %w", err)
	}

	length := binary.BigEndian.Uint32(header[:])

	// Layer 1: Reject oversized frames.
	if length > MaxFrameSize {
		// Drain at most MaxFrameSize bytes (see package-level ReadFrame).
		drainCap := int64(MaxFrameSize)
		_, _ = io.CopyN(io.Discard, r, drainCap)
		return nil, fmt.Errorf("proto: frame too large: %d bytes (max %d)", length, MaxFrameSize)
	}

	// Handle zero-length frames (valid empty protobuf).
	if length == 0 {
		return UnmarshalFrame(nil)
	}

	// Layer 3: Check per-reader memory budget before allocating.
	newTotal := fr.memoryUsed.Add(int64(length))
	if newTotal > fr.memoryBudget {
		fr.memoryUsed.Add(-int64(length))
		_, _ = io.CopyN(io.Discard, r, int64(length))
		return nil, fmt.Errorf("proto: frame memory budget exceeded: %d bytes in flight (budget %d)", newTotal, fr.memoryBudget)
	}
	defer fr.memoryUsed.Add(-int64(length))

	// Layer 2: Use pooled buffer for common-sized frames.
	var data []byte
	var poolBuf *[]byte
	if length <= PooledFrameSize {
		poolBuf = framePool.Get().(*[]byte)
		data = (*poolBuf)[:length]
	} else {
		data = make([]byte, length)
	}

	// Read payload into the buffer.
	if _, err := io.ReadFull(r, data); err != nil {
		if poolBuf != nil {
			*poolBuf = (*poolBuf)[:cap(*poolBuf)]
			framePool.Put(poolBuf)
		}
		return nil, fmt.Errorf("proto: read payload: %w", err)
	}

	frame, err := UnmarshalFrame(data)

	if poolBuf != nil {
		*poolBuf = (*poolBuf)[:cap(*poolBuf)]
		framePool.Put(poolBuf)
	}

	return frame, err
}
