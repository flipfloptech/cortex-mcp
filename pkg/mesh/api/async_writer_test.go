package api

import (
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/flipfloptech/cortex-mcp/pkg/mesh/proto"
)

// --- Async writer per-peer tests ---

// TestAsyncWriter_DeliversFrame verifies that sendControl delivers
// a frame to a healthy peer through the async write queue.
func TestAsyncWriter_DeliversFrame(t *testing.T) {
	t.Parallel()

	local, remote := net.Pipe()
	defer func() { _ = local.Close() }()
	defer func() { _ = remote.Close() }()

	pc := newPeerConnWithWriter("test-peer", local, nil, nil)
	defer pc.stopWriter()

	frame := &pb.ControlFrame{
		Payload: &pb.ControlFrame_Gossip{
			Gossip: &pb.GossipFrame{
				FromNode: "node-a",
				Routes:   map[string]float64{"node-b": 1.0},
			},
		},
	}

	// Send via async path.
	pc.sendControl(frame)

	// Read on remote end.
	fr := pb.NewFrameReader(2 * 1024 * 1024)
	got, err := fr.ReadFrame(remote)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if got.GetGossip() == nil {
		t.Fatal("expected gossip payload")
	}
	if got.GetGossip().FromNode != "node-a" {
		t.Fatalf("FromNode = %q, want %q", got.GetGossip().FromNode, "node-a")
	}
}

// TestAsyncWriter_NonBlockingOnSlowPeer verifies that sendControl
// does NOT block when the peer's write queue is full. This is the
// core fix: one slow peer must not deadlock the control plane.
func TestAsyncWriter_NonBlockingOnSlowPeer(t *testing.T) {
	t.Parallel()

	local, remote := net.Pipe()
	defer func() { _ = local.Close() }()
	defer func() { _ = remote.Close() }()

	// Create a peer but NEVER read from the remote end.
	// This will cause the net.Pipe TCP buffer to fill up, and the
	// writer goroutine will eventually block on pb.WriteFrame.
	// Meanwhile, sendControl must not block when the channel is full.
	pc := newPeerConnWithWriter("slow-peer", local, nil, nil)
	defer pc.stopWriter()

	frame := &pb.ControlFrame{
		Payload: &pb.ControlFrame_Gossip{
			Gossip: &pb.GossipFrame{
				FromNode: "node-a",
				Routes:   map[string]float64{"node-b": 1.0},
			},
		},
	}

	// Fill the channel buffer beyond capacity.
	// sendControl must return immediately for all calls.
	done := make(chan struct{})
	go func() {
		for i := 0; i < controlWriteQueueSize*3; i++ {
			pc.sendControl(frame)
		}
		close(done)
	}()

	select {
	case <-done:
		// Good — sendControl did not block.
	case <-time.After(2 * time.Second):
		t.Fatal("sendControl blocked on full write queue — control plane deadlock")
	}
}

// TestAsyncWriter_DropsExcessFrames verifies that when the write queue
// is full, sendControl drops frames (returns false) rather than blocking.
func TestAsyncWriter_DropsExcessFrames(t *testing.T) {
	t.Parallel()

	local, remote := net.Pipe()
	defer func() { _ = local.Close() }()
	defer func() { _ = remote.Close() }()

	// Don't read from remote — writer goroutine will block on WriteFrame.
	pc := newPeerConnWithWriter("slow-peer", local, nil, nil)
	defer pc.stopWriter()

	frame := &pb.ControlFrame{
		Payload: &pb.ControlFrame_Gossip{
			Gossip: &pb.GossipFrame{
				FromNode: "node-a",
				Routes:   map[string]float64{},
			},
		},
	}

	// Fill the channel.
	var dropped int
	for i := 0; i < controlWriteQueueSize*2; i++ {
		if !pc.sendControl(frame) {
			dropped++
		}
	}

	if dropped == 0 {
		t.Fatal("expected at least some frames to be dropped when queue is full")
	}
}

// TestAsyncWriter_StopWriterDrains verifies that stopWriter terminates
// the writer goroutine cleanly and stops processing.
func TestAsyncWriter_StopWriterDrains(t *testing.T) {
	t.Parallel()

	local, remote := net.Pipe()
	defer func() { _ = local.Close() }()
	defer func() { _ = remote.Close() }()

	pc := newPeerConnWithWriter("test-peer", local, nil, nil)

	// Stop the writer.
	pc.stopWriter()

	// Sending after stop should not panic and should return false (dropped).
	frame := &pb.ControlFrame{
		Payload: &pb.ControlFrame_Gossip{
			Gossip: &pb.GossipFrame{FromNode: "node-a"},
		},
	}

	sent := pc.sendControl(frame)
	if sent {
		t.Fatal("sendControl should return false after writer is stopped")
	}
}

// TestAsyncWriter_ConcurrentSends verifies that sendControl is safe
// to call from multiple goroutines concurrently.
func TestAsyncWriter_ConcurrentSends(t *testing.T) {
	t.Parallel()

	local, remote := net.Pipe()
	defer func() { _ = local.Close() }()

	pc := newPeerConnWithWriter("test-peer", local, nil, nil)
	defer pc.stopWriter()

	frame := &pb.ControlFrame{
		Payload: &pb.ControlFrame_Gossip{
			Gossip: &pb.GossipFrame{
				FromNode: "node-a",
				Routes:   map[string]float64{"node-b": 1.0},
			},
		},
	}

	// Drain the remote side so the writer doesn't block.
	go func() {
		fr := pb.NewFrameReader(2 * 1024 * 1024)
		for {
			if _, err := fr.ReadFrame(remote); err != nil {
				return
			}
		}
	}()

	var wg sync.WaitGroup
	var sendCount atomic.Int64

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if pc.sendControl(frame) {
					sendCount.Add(1)
				}
			}
		}()
	}

	wg.Wait()

	// At least some sends should succeed.
	if sendCount.Load() == 0 {
		t.Fatal("expected at least some successful sends from concurrent goroutines")
	}
}

// TestBroadcastAll_AsyncDelivery verifies that broadcastAll uses the
// async writer path — frames are delivered to all healthy peers without
// blocking on any single one.
func TestBroadcastAll_AsyncDelivery(t *testing.T) {
	t.Parallel()

	// Create two fake peers: one healthy, one slow.
	healthyLocal, healthyRemote := net.Pipe()
	defer func() { _ = healthyLocal.Close() }()
	defer func() { _ = healthyRemote.Close() }()

	slowLocal, slowRemote := net.Pipe()
	defer func() { _ = slowLocal.Close() }()
	defer func() { _ = slowRemote.Close() }()

	pm := newPeerManager()
	healthyPC := newPeerConnWithWriter("healthy-peer", healthyLocal, nil, nil)
	slowPC := newPeerConnWithWriter("slow-peer", slowLocal, nil, nil)
	defer healthyPC.stopWriter()
	defer slowPC.stopWriter()

	pm.Add("healthy-peer", healthyPC)
	pm.Add("slow-peer", slowPC)

	frame := &pb.ControlFrame{
		Payload: &pb.ControlFrame_Gossip{
			Gossip: &pb.GossipFrame{
				FromNode: "node-origin",
				Routes:   map[string]float64{"node-x": 2.0},
			},
		},
	}

	// Don't read from slowRemote — it will block.
	// broadcastAll must still deliver to the healthy peer without blocking.
	done := make(chan struct{})
	go func() {
		broadcastAllPeers(pm, frame)
		close(done)
	}()

	select {
	case <-done:
		// Good — broadcastAll returned without blocking.
	case <-time.After(2 * time.Second):
		t.Fatal("broadcastAll blocked due to slow peer — control plane deadlock")
	}

	// Verify the healthy peer received the frame.
	fr := pb.NewFrameReader(2 * 1024 * 1024)
	got, err := fr.ReadFrame(healthyRemote)
	if err != nil {
		t.Fatalf("healthy peer ReadFrame: %v", err)
	}
	if got.GetGossip() == nil || got.GetGossip().FromNode != "node-origin" {
		t.Fatalf("unexpected frame: %v", got)
	}
}
