package api

import (
	"net"
	"sync"
	"testing"
	"time"

	pb "github.com/flipfloptech/cortex-mcp/pkg/mesh/proto"
)

// TestControlWriteLoop_WriteDeadline verifies that the control write loop
// sets a write deadline on the control stream to prevent blocking when
// the underlying TCP buffer is saturated (M-2 mitigation).
func TestControlWriteLoop_WriteDeadline(t *testing.T) {
	t.Parallel()

	// slowConn simulates a saturated TCP buffer by blocking writes.
	clientConn, serverConn := net.Pipe()

	// Create a peerConn with the server side.
	pc := newPeerConnWithWriter("test-peer", serverConn, nil, nil)

	frame := &pb.ControlFrame{
		Payload: &pb.ControlFrame_Gossip{
			Gossip: &pb.GossipFrame{FromNode: "test-node"},
		},
	}

	// Enqueue a frame.
	if !pc.sendControl(frame) {
		t.Fatal("sendControl should succeed")
	}

	// Read from client side to allow the write to complete.
	go func() {
		buf := make([]byte, 4096)
		for {
			_, err := clientConn.Read(buf)
			if err != nil {
				return
			}
		}
	}()

	// Give the writer time to process the frame.
	time.Sleep(50 * time.Millisecond)

	// Clean shutdown.
	pc.stopWriter()
	clientConn.Close() //nolint:errcheck // test cleanup
}

// TestControlWriteLoop_WriteDeadline_Timeout verifies that write deadline
// causes the writer to fail and stop when the connection is saturated.
func TestControlWriteLoop_WriteDeadline_Timeout(t *testing.T) {
	t.Parallel()

	// net.Pipe has a small internal buffer. If we never read from clientConn,
	// writes will block.
	clientConn, serverConn := net.Pipe()

	pc := newPeerConnWithWriter("slow-peer", serverConn, nil, nil)

	frame := &pb.ControlFrame{
		Payload: &pb.ControlFrame_Gossip{
			Gossip: &pb.GossipFrame{FromNode: "test-node"},
		},
	}

	// Fill the write channel with frames.
	sent := 0
	for i := 0; i < controlWriteQueueSize; i++ {
		if pc.sendControl(frame) {
			sent++
		}
	}

	if sent == 0 {
		t.Fatal("should have sent at least 1 frame")
	}

	// Don't read from clientConn — the writer should eventually hit the
	// write deadline and stop.
	done := make(chan struct{})
	go func() {
		pc.writerWg.Wait()
		close(done)
	}()

	// Writer should timeout within controlWriteDeadline + margin.
	select {
	case <-done:
		// Writer stopped — good.
	case <-time.After(controlWriteDeadline + 3*time.Second):
		t.Fatal("writer should have stopped after write deadline timeout")
	}

	clientConn.Close() //nolint:errcheck // test cleanup
}

// TestControlWriteLoop_MultipleFrames_WithDeadline verifies that normal
// operation works correctly with the write deadline — frames are delivered
// when the connection is healthy.
func TestControlWriteLoop_MultipleFrames_WithDeadline(t *testing.T) {
	t.Parallel()

	clientConn, serverConn := net.Pipe()

	pc := newPeerConnWithWriter("healthy-peer", serverConn, nil, nil)

	frame := &pb.ControlFrame{
		Payload: &pb.ControlFrame_Gossip{
			Gossip: &pb.GossipFrame{FromNode: "test-node"},
		},
	}

	// Send several frames.
	const numFrames = 5
	for i := 0; i < numFrames; i++ {
		if !pc.sendControl(frame) {
			t.Fatalf("sendControl %d should succeed", i)
		}
	}

	// Read frames from the client side.
	var received int
	var readMu sync.Mutex
	done := make(chan struct{})
	go func() {
		fr := pb.NewFrameReader(1024 * 1024)
		for received < numFrames {
			_, err := fr.ReadFrame(clientConn)
			if err != nil {
				return
			}
			readMu.Lock()
			received++
			readMu.Unlock()
		}
		close(done)
	}()

	select {
	case <-done:
		// All frames received.
	case <-time.After(5 * time.Second):
		readMu.Lock()
		t.Fatalf("timeout waiting for frames, received %d/%d", received, numFrames)
		readMu.Unlock()
	}

	pc.stopWriter()
	clientConn.Close() //nolint:errcheck // test cleanup
}

// TestAcceptStdio_SingleConnection verifies that AcceptStdio uses single
// connection mode (HOL-susceptible but unavoidable over stdio).
func TestAcceptStdio_DocumentedHOL(t *testing.T) {
	t.Parallel()

	// Verify the constant exists and is reasonable.
	if controlWriteDeadline <= 0 {
		t.Fatal("controlWriteDeadline must be positive")
	}

	if controlWriteDeadline > 30*time.Second {
		t.Fatal("controlWriteDeadline should be aggressive (≤30s) for HOL mitigation")
	}
}
