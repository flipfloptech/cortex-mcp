package api

import (
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/nucleus"
	pb "github.com/flipfloptech/cortex-mcp/pkg/mesh/proto"
	"github.com/flipfloptech/cortex-mcp/pkg/mesh/routing"
	"github.com/flipfloptech/cortex-mcp/pkg/mesh/transport"
	"github.com/flipfloptech/cortex-mcp/testutil"
)

// TestMultiHopRelay verifies that a node can dial a non-direct peer
// using the wave-collapse handshake and zero-allocation stitcher.
func TestMultiHopRelay(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ca, caKey, pool := testutil.GenerateTestCA(t)
	cfgA := testutil.GenerateDualCert(t, ca, caKey, pool, "node-a")
	cfgB := testutil.GenerateDualCert(t, ca, caKey, pool, "node-b")
	cfgC := testutil.GenerateDualCert(t, ca, caKey, pool, "node-c")

	nodeA, _ := NewNode(ctx, NodeConfig{NodeID: "node-a"})
	defer func() { _ = nodeA.Close() }()
	nodeA.SetMembraneConfig(cfgA)

	nodeB, _ := NewNode(ctx, NodeConfig{NodeID: "node-b"})
	defer func() { _ = nodeB.Close() }()
	nodeB.SetMembraneConfig(cfgB)

	nodeC, _ := NewNode(ctx, NodeConfig{NodeID: "node-c"})
	defer func() { _ = nodeC.Close() }()
	nodeC.SetMembraneConfig(cfgC)

	// Connect A to B
	rawA1, rawB1 := net.Pipe()
	addDone := make(chan error, 2)
	go func() { addDone <- nodeA.AddPeer(ctx, transport.NewStdioConn(rawA1, rawA1), true) }()
	go func() { addDone <- nodeB.AddPeer(ctx, transport.NewStdioConn(rawB1, rawB1), false) }()
	for i := 0; i < 2; i++ {
		if err := <-addDone; err != nil {
			t.Fatalf("AddPeer A-B: %v", err)
		}
	}

	// Connect B to C
	rawB2, rawC1 := net.Pipe()
	go func() { addDone <- nodeB.AddPeer(ctx, transport.NewStdioConn(rawB2, rawB2), true) }()
	go func() { addDone <- nodeC.AddPeer(ctx, transport.NewStdioConn(rawC1, rawC1), false) }()
	for i := 0; i < 2; i++ {
		if err := <-addDone; err != nil {
			t.Fatalf("AddPeer B-C: %v", err)
		}
	}

	// Wait for peer discovery via gossip
	// We can manually add the route to speed up test
	nodeA.gradient.UpdateRoute("node-c", "node-b", 5.0)

	// Accept data stream on C
	lisC, _ := nodeC.GrpcListener()
	acceptErrCh := make(chan error, 1)
	go func() {
		connC, err := lisC.Accept()
		if err != nil {
			acceptErrCh <- err
			return
		}
		defer func() { _ = connC.Close() }()

		// Read data from A
		buf := make([]byte, 10)
		if _, err := io.ReadFull(connC, buf); err != nil {
			acceptErrCh <- err
			return
		}
		if string(buf) != "ping-to-c!" {
			acceptErrCh <- fmt.Errorf("expected ping-to-c!, got %s", buf)
			return
		}

		// Write data back to A
		if _, err := connC.Write([]byte("pong-to-a!")); err != nil {
			acceptErrCh <- err
			return
		}
		acceptErrCh <- nil
	}()

	// Dial C from A
	connAtoC, err := nodeA.GrpcDialer(ctx, "node-c")
	if err != nil {
		t.Fatalf("GrpcDialer A->C: %v", err)
	}
	defer func() { _ = connAtoC.Close() }()

	// Since we don't have a true gRPC client wrapper in tests writing the magic HTTP preface,
	// our test connection just uses raw bytes, but wait!
	// Our acceptDataStreams logic now looks for 'MagicRelay' for circuits, OR forwards as a gRPC stream.
	// Since we are A, `GrpcDialer` to C already established the circuit via `dialRelayCircuit`.
	// The `dialRelayCircuit` writes `MagicRelay` AND `circuit_id` to establish it with B.
	// B stitches it to C.
	// However, B stitches A's stream to C's stream.
	// So B connects to C using its own `GrpcDialer(ctx, C)` but wait! B's `handleRelayOpenFrame` uses `GrpcDialer(ctx, C)` to connect to C.
	// Since B has a direct peer C, B's `GrpcDialer` just opens a Yamux stream on C!
	// Then C's `acceptDataStreams` will peek 1 byte.
	// A writes "ping-to-c!". The first byte is 'p' (0x70).
	// C sees 'p' != MagicRelay, treats it as gRPC stream, prepends it back, and delivers to `lisC`.
	// This works perfectly!

	if _, err := connAtoC.Write([]byte("ping-to-c!")); err != nil {
		t.Fatalf("Write A->C: %v", err)
	}

	buf := make([]byte, 10)
	if _, err := io.ReadFull(connAtoC, buf); err != nil {
		t.Fatalf("Read A<-C: %v", err)
	}

	if string(buf) != "pong-to-a!" {
		t.Fatalf("Expected pong-to-a!, got %s", buf)
	}

	if err := <-acceptErrCh; err != nil {
		t.Fatalf("C listener error: %v", err)
	}
}

// --- helpers ---

func newTestNode(id string) *Node {
	m := nucleus.NewManifest(id)
	return &Node{
		gradient:        routing.NewGradientTable(id),
		peers:           newPeerManager(),
		manifest:        m,
		resolver:        nucleus.NewResolver(),
		capIndex:        routing.NewCapabilityIndex(),
		sonar:           routing.NewSonar(m),
		ctx:             context.Background(),
		pendingCircuits: make(map[string]net.Conn),
		relayWaiters:    make(map[string]chan *pb.RelayAcceptFrame),
	}
}

// addDummyPeer registers a peer with a nil session in the peer manager.
// The peer won't support stream opening (session is nil) but it satisfies
// gradient routing lookups that check peers.Get().
func addDummyPeer(n *Node, nodeID string) {
	pc := &peerConn{
		nodeID: nodeID,
		deadCh: make(chan struct{}),
	}
	n.peers.Add(nodeID, pc)
}
