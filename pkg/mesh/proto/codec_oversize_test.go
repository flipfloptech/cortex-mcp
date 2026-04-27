package proto

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

// --- P2-5: Bound oversize-frame drain ---
//
// When a frame's length header exceeds MaxFrameSize, ReadFrame drains
// the payload with io.CopyN(io.Discard, r, int64(length)) to keep the
// stream synchronized. However, `length` is attacker-chosen (up to
// MaxUint32 = 4GB). On a raw (non-yamux) control connection, this pins
// the reader for as long as the attacker can sustain bandwidth.
//
// Fix: cap the drain at MaxFrameSize bytes. Beyond that, the stream
// is desynchronized anyway — the control loop will close the peer on
// the returned error. There's no value in consuming gigabytes of an
// attacker's payload to "stay synchronized."
//
// Contract:
//   1. Oversize frames drain at most MaxFrameSize bytes.
//   2. ReadFrame returns an error that mentions the size violation.
//   3. The same cap applies to both package-level ReadFrame and
//      FrameReader.ReadFrame.

// trackingReader wraps a reader and counts how many bytes were read.
type trackingReader struct {
	r    io.Reader
	read int64
}

func (t *trackingReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	t.read += int64(n)
	return n, err
}

func TestFrameReaderReadFrame_OversizeDoesNotDrainMoreThanMax(t *testing.T) {
	t.Parallel()

	claimedLength := MaxFrameSize * 10
	actualPayloadSize := int(MaxFrameSize) + 4

	var buf bytes.Buffer
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], claimedLength)
	buf.Write(header[:])
	buf.Write(make([]byte, actualPayloadSize))

	tr := &trackingReader{r: &buf}
	fr := NewFrameReader(2 * 1024 * 1024)

	_, err := fr.ReadFrame(tr)
	if err == nil {
		t.Fatal("expected error for oversize frame")
	}

	maxExpectedRead := int64(4) + int64(MaxFrameSize)
	if tr.read > maxExpectedRead {
		t.Fatalf("drained %d bytes, expected at most %d (4 header + %d payload cap)",
			tr.read, maxExpectedRead, MaxFrameSize)
	}
}

// TestFrameReader_OversizeBudgetExceeded_DoesNotDrainMoreThanMax verifies
// the same cap on the memory-budget-exceeded drain path.
func TestFrameReader_OversizeBudgetExceeded_DoesNotDrainMoreThanMax(t *testing.T) {
	t.Parallel()

	// Create a FrameReader with a tiny budget so even a valid-size frame
	// blows the budget. The drain on the budget path must also be capped.
	fr := NewFrameReader(1) // 1 byte budget — anything blows it

	claimedLength := MaxFrameSize // within max, but blows budget
	var buf bytes.Buffer
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], claimedLength)
	buf.Write(header[:])
	buf.Write(make([]byte, int(MaxFrameSize)))

	tr := &trackingReader{r: &buf}

	_, err := fr.ReadFrame(tr)
	if err == nil {
		t.Fatal("expected error for budget exceeded")
	}

	// The drain on the budget path should also cap at MaxFrameSize.
	maxExpectedRead := int64(4) + int64(MaxFrameSize)
	if tr.read > maxExpectedRead {
		t.Fatalf("drained %d bytes, expected at most %d",
			tr.read, maxExpectedRead)
	}
}
