package proto

import (
	"bytes"
	"io"
	"testing"
)

// TestWriteRawFrame_WritesPreMarshaledBytes verifies that WriteRawFrame
// writes a length-prefixed payload from pre-marshaled bytes, and that
// ReadFrame can decode it back into the original ControlFrame.
func TestWriteRawFrame_WritesPreMarshaledBytes(t *testing.T) {
	t.Parallel()

	// Create a frame, marshal it once.
	frame := &ControlFrame{
		Payload: &ControlFrame_Gossip{
			Gossip: &GossipFrame{
				FromNode: "node-1",
				Routes: map[string]float64{
					"node-2": 10.0,
					"node-3": 20.0,
				},
			},
		},
	}

	data, err := MarshalFrame(frame)
	if err != nil {
		t.Fatalf("MarshalFrame: %v", err)
	}

	// Write the pre-marshaled bytes using WriteRawFrame.
	var buf bytes.Buffer
	if err := WriteRawFrame(&buf, data); err != nil {
		t.Fatalf("WriteRawFrame: %v", err)
	}

	// Read it back using a FrameReader.
	fr := NewFrameReader(2 * 1024 * 1024)
	decoded, err := fr.ReadFrame(&buf)
	if err != nil {
		t.Fatalf("fr.ReadFrame: %v", err)
	}

	// Verify the decoded frame matches the original.
	gossip := decoded.GetGossip()
	if gossip == nil {
		t.Fatal("decoded frame has no gossip payload")
	}
	if gossip.FromNode != "node-1" {
		t.Fatalf("FromNode = %q, want %q", gossip.FromNode, "node-1")
	}
	if len(gossip.Routes) != 2 {
		t.Fatalf("len(Routes) = %d, want 2", len(gossip.Routes))
	}
}

// TestWriteRawFrame_MatchesWriteFrame verifies that WriteRawFrame
// produces the exact same byte output as WriteFrame for the same frame.
func TestWriteRawFrame_MatchesWriteFrame(t *testing.T) {
	t.Parallel()

	frame := &ControlFrame{
		Payload: &ControlFrame_Gossip{
			Gossip: &GossipFrame{
				FromNode: "node-x",
				Routes:   map[string]float64{"target": 5.5},
			},
		},
	}

	// Marshal once.
	data, err := MarshalFrame(frame)
	if err != nil {
		t.Fatalf("MarshalFrame: %v", err)
	}

	// Write via WriteFrame (marshal + write).
	var writeFrameBuf bytes.Buffer
	if err := WriteFrame(&writeFrameBuf, frame); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}

	// Write via WriteRawFrame (pre-marshaled write).
	var writeRawBuf bytes.Buffer
	if err := WriteRawFrame(&writeRawBuf, data); err != nil {
		t.Fatalf("WriteRawFrame: %v", err)
	}

	// Both should produce identical wire output.
	if !bytes.Equal(writeFrameBuf.Bytes(), writeRawBuf.Bytes()) {
		t.Fatalf("WriteRawFrame output differs from WriteFrame:\n  WriteFrame:    %x\n  WriteRawFrame: %x",
			writeFrameBuf.Bytes(), writeRawBuf.Bytes())
	}
}

// TestWriteRawFrame_EmptyPayload verifies WriteRawFrame handles empty data.
func TestWriteRawFrame_EmptyPayload(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	if err := WriteRawFrame(&buf, nil); err != nil {
		t.Fatalf("WriteRawFrame with nil: %v", err)
	}

	// Should write a 4-byte header with length 0.
	if buf.Len() != 4 {
		t.Fatalf("expected 4-byte header for empty payload, got %d bytes", buf.Len())
	}
}

// TestWriteRawFrame_ErrorOnBrokenWriter verifies WriteRawFrame returns
// an error when the writer fails.
func TestWriteRawFrame_ErrorOnBrokenWriter(t *testing.T) {
	t.Parallel()

	data := []byte("test-payload")

	// Header write fails.
	err := WriteRawFrame(&brokenWriter{failAt: 0}, data)
	if err == nil {
		t.Fatal("expected error on header write failure")
	}

	// Payload write fails.
	err = WriteRawFrame(&brokenWriter{failAt: 1}, data)
	if err == nil {
		t.Fatal("expected error on payload write failure")
	}
}

// brokenWriter is a test writer that fails after a specified number of writes.
type brokenWriter struct {
	writes int
	failAt int
}

func (bw *brokenWriter) Write(p []byte) (int, error) {
	if bw.writes >= bw.failAt {
		return 0, io.ErrClosedPipe
	}
	bw.writes++
	return len(p), nil
}
