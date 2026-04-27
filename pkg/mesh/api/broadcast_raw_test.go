package api

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	pb "github.com/flipfloptech/cortex-mcp/pkg/mesh/proto"
)

// TestPeerConn_SendControlRaw_WritesPreMarshaledBytes verifies that
// sendControlRaw enqueues pre-marshaled bytes and the writer goroutine
// writes them directly without re-marshaling.
func TestPeerConn_SendControlRaw_WritesPreMarshaledBytes(t *testing.T) {
	t.Parallel()

	// Create a control frame and marshal it once.
	frame := &pb.ControlFrame{
		Payload: &pb.ControlFrame_Gossip{
			Gossip: &pb.GossipFrame{
				FromNode: "node-test",
				Routes:   map[string]float64{"target": 5.0},
			},
		},
	}
	rawData, err := pb.MarshalFrame(frame)
	if err != nil {
		t.Fatalf("MarshalFrame: %v", err)
	}

	// Set up a pipe to capture what the writer writes.
	connA, connB := net.Pipe()
	t.Cleanup(func() {
		_ = connA.Close()
		_ = connB.Close()
	})

	pc := newPeerConnWithWriter("test-peer", connA, nil, nil)
	t.Cleanup(func() { pc.stopWriter() })

	// Send the raw bytes.
	if !pc.sendControlRaw(rawData) {
		t.Fatal("sendControlRaw returned false")
	}

	// Read from the other end and verify we can decode the frame.
	_ = connB.SetReadDeadline(time.Now().Add(2 * time.Second))
	fr := pb.NewFrameReader(2 * 1024 * 1024)
	decoded, err := fr.ReadFrame(connB)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}

	gossip := decoded.GetGossip()
	if gossip == nil {
		t.Fatal("no gossip in decoded frame")
	}
	if gossip.FromNode != "node-test" {
		t.Fatalf("FromNode = %q, want %q", gossip.FromNode, "node-test")
	}
}

// TestBroadcastAllRaw_MarshaledOnce verifies that broadcastAllRaw
// writes the same pre-marshaled bytes to all peers without re-marshaling.
func TestBroadcastAllRaw_MarshaledOnce(t *testing.T) {
	t.Parallel()

	const numPeers = 5

	frame := &pb.ControlFrame{
		Payload: &pb.ControlFrame_Gossip{
			Gossip: &pb.GossipFrame{
				FromNode: "gossip-src",
				Routes:   map[string]float64{"target": 15.0},
			},
		},
	}
	rawData, err := pb.MarshalFrame(frame)
	if err != nil {
		t.Fatalf("MarshalFrame: %v", err)
	}

	// Create peers with pipes to capture output.
	pm := newPeerManager()
	readers := make([]net.Conn, numPeers)

	for i := 0; i < numPeers; i++ {
		connA, connB := net.Pipe()
		t.Cleanup(func() {
			_ = connA.Close()
			_ = connB.Close()
		})

		pc := newPeerConnWithWriter("peer-"+string(rune('A'+i)), connA, nil, nil)
		t.Cleanup(func() { pc.stopWriter() })
		pm.Add(pc.nodeID, pc)
		readers[i] = connB
	}

	// Broadcast the raw bytes once.
	broadcastAllRaw(pm, rawData)

	// Read from all peers — each should receive the frame.
	var wg sync.WaitGroup
	wireBytes := make([][]byte, numPeers)

	for i := 0; i < numPeers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_ = readers[idx].SetReadDeadline(time.Now().Add(2 * time.Second))

			// Read the raw wire bytes (4-byte header + payload).
			var header [4]byte
			if _, err := io.ReadFull(readers[idx], header[:]); err != nil {
				t.Errorf("peer %d: read header: %v", idx, err)
				return
			}

			length := int(header[0])<<24 | int(header[1])<<16 | int(header[2])<<8 | int(header[3])
			payload := make([]byte, length)
			if _, err := io.ReadFull(readers[idx], payload); err != nil {
				t.Errorf("peer %d: read payload: %v", idx, err)
				return
			}

			wireBytes[idx] = append(header[:], payload...)
		}(i)
	}
	wg.Wait()

	// All peers should have received identical wire bytes.
	for i := 1; i < numPeers; i++ {
		if !bytes.Equal(wireBytes[0], wireBytes[i]) {
			t.Fatalf("peer %d received different bytes than peer 0", i)
		}
	}

	// Verify the wire bytes can be decoded.
	reader := bytes.NewReader(wireBytes[0])
	frVerify := pb.NewFrameReader(2 * 1024 * 1024)
	decoded, err := frVerify.ReadFrame(reader)
	if err != nil {
		t.Fatalf("ReadFrame from captured bytes: %v", err)
	}
	if decoded.GetGossip().FromNode != "gossip-src" {
		t.Fatalf("FromNode = %q, want %q", decoded.GetGossip().FromNode, "gossip-src")
	}
}

// TestSendGossipToAll_MarshaledOnce verifies that the full
// sendGossipToAll pipeline marshals the protobuf exactly once,
// regardless of peer count.
func TestSendGossipToAll_MarshaledOnce(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	node, err := NewNode(ctx, NodeConfig{NodeID: "gossip-node"})
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	defer func() { _ = node.Close() }()

	const numPeers = 3
	readers := make([]net.Conn, numPeers)

	for i := 0; i < numPeers; i++ {
		connA, connB := net.Pipe()
		t.Cleanup(func() {
			_ = connA.Close()
			_ = connB.Close()
		})

		pc := newPeerConnWithWriter("peer-"+string(rune('A'+i)), connA, nil, nil)
		t.Cleanup(func() { pc.stopWriter() })
		node.peers.Add(pc.nodeID, pc)
		readers[i] = connB
	}

	// Add a route so gossip has something to emit.
	node.gradient.UpdateRoute("node-X", "peer-A", 10.0)

	// Trigger the gossip broadcast.
	node.sendGossipToAll()

	// Verify all peers received the gossip frame.
	for i := 0; i < numPeers; i++ {
		_ = readers[i].SetReadDeadline(time.Now().Add(2 * time.Second))
		frPeer := pb.NewFrameReader(2 * 1024 * 1024)
		decoded, err := frPeer.ReadFrame(readers[i])
		if err != nil {
			t.Fatalf("peer %d: ReadFrame: %v", i, err)
		}
		gossip := decoded.GetGossip()
		if gossip == nil {
			t.Fatalf("peer %d: no gossip payload", i)
		}
		if gossip.FromNode != "gossip-node" {
			t.Fatalf("peer %d: FromNode = %q, want %q", i, gossip.FromNode, "gossip-node")
		}
	}
}

// TestSendControl_StillWorks verifies that the existing sendControl
// (frame-based) path is unaffected by the raw byte additions.
func TestSendControl_StillWorks(t *testing.T) {
	t.Parallel()

	frame := &pb.ControlFrame{
		Payload: &pb.ControlFrame_WhoHas{
			WhoHas: &pb.WhoHasFrame{
				Uuid:       "test-uuid",
				Capability: "tool:check",
				OriginNode: "origin",
			},
		},
	}

	connA, connB := net.Pipe()
	t.Cleanup(func() {
		_ = connA.Close()
		_ = connB.Close()
	})

	pc := newPeerConnWithWriter("test-peer", connA, nil, nil)
	t.Cleanup(func() { pc.stopWriter() })

	// Use the original sendControl (frame-based).
	if !pc.sendControl(frame) {
		t.Fatal("sendControl returned false")
	}

	_ = connB.SetReadDeadline(time.Now().Add(2 * time.Second))
	frCheck := pb.NewFrameReader(2 * 1024 * 1024)
	decoded, err := frCheck.ReadFrame(connB)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}

	whoHas := decoded.GetWhoHas()
	if whoHas == nil {
		t.Fatal("no WhoHas in decoded frame")
	}
	if whoHas.Uuid != "test-uuid" {
		t.Fatalf("UUID = %q, want %q", whoHas.Uuid, "test-uuid")
	}
}
