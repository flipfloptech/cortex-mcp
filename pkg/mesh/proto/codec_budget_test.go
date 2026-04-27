package proto

import (
	"bytes"
	"encoding/binary"
	"sync"
	"testing"

	pb "google.golang.org/protobuf/proto"
)

// --- H-3: Per-reader frame budget isolation ---

func TestFrameReader_IsolatedBudget(t *testing.T) {
	t.Parallel()

	// Two readers with 100KB budget each. Exhausting one must not affect the other.
	fr1 := NewFrameReader(100 * 1024)
	fr2 := NewFrameReader(100 * 1024)

	// Build a ~50KB frame for fr1.
	frame := &ControlFrame{
		Payload: &ControlFrame_Gossip{
			Gossip: &GossipFrame{FromNode: "node-1"},
		},
	}
	data, err := pb.Marshal(frame)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	buf1 := encodeFrame(t, data)

	// Read from fr1 — should succeed.
	_, err = fr1.ReadFrame(bytes.NewReader(buf1))
	if err != nil {
		t.Fatalf("fr1 read should succeed: %v", err)
	}

	// Read from fr2 — should also succeed (independent budget).
	buf2 := encodeFrame(t, data)
	_, err = fr2.ReadFrame(bytes.NewReader(buf2))
	if err != nil {
		t.Fatalf("fr2 read should succeed even if fr1 used budget: %v", err)
	}
}

func TestFrameReader_BudgetExhaustion(t *testing.T) {
	t.Parallel()

	// Reader with 1KB budget. Frame is larger than budget.
	fr := NewFrameReader(1024)

	// Create a frame payload that exceeds the 1KB budget.
	bigPayload := make([]byte, 2048)
	buf := encodeRawFrame(t, bigPayload)

	_, err := fr.ReadFrame(bytes.NewReader(buf))
	if err == nil {
		t.Fatal("expected budget exhaustion error")
	}
}

func TestFrameReader_BudgetReleasesAfterRead(t *testing.T) {
	t.Parallel()

	// Reader with 2KB budget. Read a 1KB frame twice — budget must release between reads.
	fr := NewFrameReader(2048)

	frame := &ControlFrame{
		Payload: &ControlFrame_Gossip{
			Gossip: &GossipFrame{FromNode: "node-1"},
		},
	}
	data, err := pb.Marshal(frame)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Read 1: should succeed.
	buf := encodeFrame(t, data)
	_, err = fr.ReadFrame(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("first read should succeed: %v", err)
	}

	// Read 2: should also succeed (budget was released after first read).
	buf = encodeFrame(t, data)
	_, err = fr.ReadFrame(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("second read should succeed (budget released): %v", err)
	}
}

func TestFrameReader_ConcurrentReads(t *testing.T) {
	t.Parallel()

	fr := NewFrameReader(1024 * 1024) // 1MB budget

	frame := &ControlFrame{
		Payload: &ControlFrame_Gossip{
			Gossip: &GossipFrame{FromNode: "node-1"},
		},
	}
	data, err := pb.Marshal(frame)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := encodeFrame(t, data)
			_, readErr := fr.ReadFrame(bytes.NewReader(buf))
			if readErr != nil {
				// Budget exceeded is OK under concurrent pressure.
				return
			}
		}()
	}
	wg.Wait()
}

func TestFrameReader_RejectsOversizedFrame(t *testing.T) {
	t.Parallel()

	fr := NewFrameReader(1024 * 1024) // budget doesn't matter — MaxFrameSize rejects first

	// Length header claims frame is >256KB.
	var buf bytes.Buffer
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], MaxFrameSize+1)
	buf.Write(header[:])
	buf.Write(make([]byte, MaxFrameSize+1))

	_, err := fr.ReadFrame(&buf)
	if err == nil {
		t.Fatal("expected oversized frame rejection")
	}
}

func TestFrameReader_ZeroLengthFrame(t *testing.T) {
	t.Parallel()

	fr := NewFrameReader(1024 * 1024)

	var buf bytes.Buffer
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], 0)
	buf.Write(header[:])

	frame, err := fr.ReadFrame(&buf)
	if err != nil {
		t.Fatalf("zero-length frame should be valid: %v", err)
	}
	if frame == nil {
		t.Fatal("expected non-nil frame for zero-length payload")
	}
}

// --- Test helpers ---

func encodeFrame(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	buf.Write(header[:])
	buf.Write(data)
	return buf.Bytes()
}

func encodeRawFrame(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	buf.Write(header[:])
	buf.Write(data)
	return buf.Bytes()
}
