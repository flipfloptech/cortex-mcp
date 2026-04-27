package api

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/transport"
	"github.com/flipfloptech/cortex-mcp/testutil"
)

// TestNode_AddPeer verifies the full peer addition lifecycle:
// raw conn → membrane upgrade → yamux → peer registered.
func TestNode_AddPeer(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	serverCfg, clientCfg := testutil.GenerateTestCertsWithIDs(t, "server-node", "client-node")

	node, err := NewNode(ctx, NodeConfig{NodeID: "server-node"})
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	defer func() {
		if err := node.Close(); err != nil {
			t.Logf("close node: %v", err)
		}
	}()

	// Set the membrane config on the node.
	node.SetMembraneConfig(serverCfg)

	// Simulate a raw incoming connection.
	rawA, rawB := net.Pipe()

	// Client side: run the membrane upgrade manually (external caller).
	type result struct {
		err error
	}
	clientDone := make(chan result, 1)
	go func() {
		connB := transport.NewStdioConn(rawB, rawB)
		err := upgradeAndHold(ctx, connB, clientCfg)
		clientDone <- result{err}
	}()

	// Server side: add the peer through the Node.
	connA := transport.NewStdioConn(rawA, rawA)
	if err := node.AddPeer(ctx, connA, true); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}

	cResult := <-clientDone
	if cResult.err != nil {
		t.Fatalf("client upgrade: %v", cResult.err)
	}

	// Verify peer is registered.
	if node.PeerCount() != 1 {
		t.Fatalf("expected 1 peer, got %d", node.PeerCount())
	}
}

// TestNode_AcceptStdio verifies AcceptStdio creates a peer from stdin/stdout.
func TestNode_AcceptStdio(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	serverCfg, clientCfg := testutil.GenerateTestCertsWithIDs(t, "deployed-node", "deployer-node")

	node, err := NewNode(ctx, NodeConfig{NodeID: "deployed-node"})
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	defer func() {
		if err := node.Close(); err != nil {
			t.Logf("close node: %v", err)
		}
	}()
	node.SetMembraneConfig(serverCfg)

	// net.Pipe simulates the stdin/stdout connection.
	rawA, rawB := net.Pipe()

	// Client (deployer) runs upgrade.
	clientDone := make(chan error, 1)
	go func() {
		connB := transport.NewStdioConn(rawB, rawB)
		err := upgradeAndHold(ctx, connB, clientCfg)
		clientDone <- err
	}()

	// Server (deployed node) accepts stdio.
	if err := node.AcceptStdio(rawA, rawA); err != nil {
		t.Fatalf("AcceptStdio: %v", err)
	}

	if err := <-clientDone; err != nil {
		t.Fatalf("client: %v", err)
	}

	if node.PeerCount() != 1 {
		t.Fatalf("expected 1 peer, got %d", node.PeerCount())
	}
}

// TestNode_GrpcDialer_DirectPeer verifies that GrpcDialer returns a
// net.Conn to a directly connected peer.
func TestNode_GrpcDialer_DirectPeer(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	serverCfg, clientCfg := testutil.GenerateTestCertsWithIDs(t, "node-a", "node-b")

	nodeA, err := NewNode(ctx, NodeConfig{NodeID: "node-a"})
	if err != nil {
		t.Fatalf("NewNode A: %v", err)
	}
	defer func() {
		if err := nodeA.Close(); err != nil {
			t.Logf("close nodeA: %v", err)
		}
	}()
	nodeA.SetMembraneConfig(serverCfg)

	nodeB, err := NewNode(ctx, NodeConfig{NodeID: "node-b"})
	if err != nil {
		t.Fatalf("NewNode B: %v", err)
	}
	defer func() {
		if err := nodeB.Close(); err != nil {
			t.Logf("close nodeB: %v", err)
		}
	}()
	nodeB.SetMembraneConfig(clientCfg)

	// Connect A ←→ B via net.Pipe.
	rawA, rawB := net.Pipe()

	addDone := make(chan error, 2)
	go func() {
		connA := transport.NewStdioConn(rawA, rawA)
		addDone <- nodeA.AddPeer(ctx, connA, true) // server
	}()
	go func() {
		connB := transport.NewStdioConn(rawB, rawB)
		addDone <- nodeB.AddPeer(ctx, connB, false) // client
	}()

	for i := 0; i < 2; i++ {
		if err := <-addDone; err != nil {
			t.Fatalf("AddPeer: %v", err)
		}
	}

	// Node B opens a gRPC connection to Node A.
	grpcConn, err := nodeB.GrpcDialer(ctx, "node-a")
	if err != nil {
		t.Fatalf("GrpcDialer: %v", err)
	}

	// Node A should accept the stream through its listener.
	lis, _ := nodeA.GrpcListener()

	// Verify bidirectional data flow.
	// We must write concurrently BEFORE Accept(), because the Magic Byte
	// peek in acceptDataStreams blocks delivery until the first byte arrives.
	go func() {
		if _, err := grpcConn.Write([]byte("grpc-request")); err != nil {
			// expected in test
			return
		}
	}()

	accepted, err := lis.Accept()
	if err != nil {
		t.Fatalf("GrpcListener Accept: %v", err)
	}

	buf := make([]byte, 12)
	if _, err := io.ReadFull(accepted, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != "grpc-request" {
		t.Fatalf("data = %q, want %q", buf, "grpc-request")
	}

	if err := grpcConn.Close(); err != nil {
		t.Logf("close grpcConn: %v", err)
	}
	if err := accepted.Close(); err != nil {
		t.Logf("close accepted: %v", err)
	}
}

// TestNode_GrpcDialer_UnknownPeer returns error for unknown node.
func TestNode_GrpcDialer_UnknownPeer(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	node, err := NewNode(ctx, NodeConfig{NodeID: "test-node"})
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	defer func() {
		if err := node.Close(); err != nil {
			t.Logf("close node: %v", err)
		}
	}()

	_, err = node.GrpcDialer(ctx, "unknown-node")
	if err == nil {
		t.Fatal("expected error for unknown peer")
	}
}
