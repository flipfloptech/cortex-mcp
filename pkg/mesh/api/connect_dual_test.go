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

// TestNode_AddPeerDual verifies the dual-connection peer addition:
// two raw conns → mTLS on both → control raw + data yamux → peer registered.
func TestNode_AddPeerDual(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	serverCfg, clientCfg := testutil.GenerateTestCertsWithIDs(t, "server-dual", "client-dual")

	nodeA, err := NewNode(ctx, NodeConfig{NodeID: "server-dual"})
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	defer func() { _ = nodeA.Close() }()
	nodeA.SetMembraneConfig(serverCfg)

	nodeB, err := NewNode(ctx, NodeConfig{NodeID: "client-dual"})
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	defer func() { _ = nodeB.Close() }()
	nodeB.SetMembraneConfig(clientCfg)

	// Two connection pairs: control and data.
	controlA, controlB := net.Pipe()
	dataA, dataB := net.Pipe()
	t.Cleanup(func() {
		_ = controlA.Close()
		_ = controlB.Close()
		_ = dataA.Close()
		_ = dataB.Close()
	})

	addDone := make(chan error, 2)
	go func() {
		ctrlConn := transport.NewStdioConn(controlA, controlA)
		dataConn := transport.NewStdioConn(dataA, dataA)
		addDone <- nodeA.AddPeerDual(ctx, ctrlConn, dataConn, true)
	}()
	go func() {
		ctrlConn := transport.NewStdioConn(controlB, controlB)
		dataConn := transport.NewStdioConn(dataB, dataB)
		addDone <- nodeB.AddPeerDual(ctx, ctrlConn, dataConn, false)
	}()

	for i := 0; i < 2; i++ {
		if err := <-addDone; err != nil {
			t.Fatalf("AddPeerDual[%d]: %v", i, err)
		}
	}

	if nodeA.PeerCount() != 1 {
		t.Fatalf("nodeA: expected 1 peer, got %d", nodeA.PeerCount())
	}
	if nodeB.PeerCount() != 1 {
		t.Fatalf("nodeB: expected 1 peer, got %d", nodeB.PeerCount())
	}
}

// TestNode_AddPeerDual_GrpcStillWorks verifies that gRPC streams work
// over the data connection in dual mode.
func TestNode_AddPeerDual_GrpcStillWorks(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	serverCfg, clientCfg := testutil.GenerateTestCertsWithIDs(t, "server-grpc", "client-grpc")

	nodeA, err := NewNode(ctx, NodeConfig{NodeID: "server-grpc"})
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	defer func() { _ = nodeA.Close() }()
	nodeA.SetMembraneConfig(serverCfg)

	nodeB, err := NewNode(ctx, NodeConfig{NodeID: "client-grpc"})
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	defer func() { _ = nodeB.Close() }()
	nodeB.SetMembraneConfig(clientCfg)

	controlA, controlB := net.Pipe()
	dataA, dataB := net.Pipe()
	t.Cleanup(func() {
		_ = controlA.Close()
		_ = controlB.Close()
		_ = dataA.Close()
		_ = dataB.Close()
	})

	addDone := make(chan error, 2)
	go func() {
		ctrlConn := transport.NewStdioConn(controlA, controlA)
		dataConn := transport.NewStdioConn(dataA, dataA)
		addDone <- nodeA.AddPeerDual(ctx, ctrlConn, dataConn, true)
	}()
	go func() {
		ctrlConn := transport.NewStdioConn(controlB, controlB)
		dataConn := transport.NewStdioConn(dataB, dataB)
		addDone <- nodeB.AddPeerDual(ctx, ctrlConn, dataConn, false)
	}()

	for i := 0; i < 2; i++ {
		if err := <-addDone; err != nil {
			t.Fatalf("AddPeerDual[%d]: %v", i, err)
		}
	}

	// Node B opens gRPC to node A via data connection.
	grpcConn, err := nodeB.GrpcDialer(ctx, "server-grpc")
	if err != nil {
		t.Fatalf("GrpcDialer: %v", err)
	}

	lis, _ := nodeA.GrpcListener()

	// Bidirectional data flow.
	// We must write concurrently BEFORE Accept(), because the Magic Byte
	// peek in acceptDataStreams blocks delivery until the first byte arrives.
	msg := []byte("dual-grpc-test")
	go func() { _, _ = grpcConn.Write(msg) }()

	accepted, err := lis.Accept()
	if err != nil {
		t.Fatalf("GrpcListener Accept: %v", err)
	}

	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(accepted, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != string(msg) {
		t.Fatalf("data = %q, want %q", buf, msg)
	}

	_ = grpcConn.Close()
	_ = accepted.Close()
}

// TestNode_AddPeer_SingleConn_BackwardCompat verifies that the original
// single-connection AddPeer still works after the dual-connection changes.
func TestNode_AddPeer_SingleConn_BackwardCompat(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	serverCfg, clientCfg := testutil.GenerateTestCertsWithIDs(t, "compat-server", "compat-client")

	node, err := NewNode(ctx, NodeConfig{NodeID: "compat-server"})
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	defer func() { _ = node.Close() }()
	node.SetMembraneConfig(serverCfg)

	rawA, rawB := net.Pipe()
	clientDone := make(chan error, 1)
	go func() {
		connB := transport.NewStdioConn(rawB, rawB)
		err := upgradeAndHold(ctx, connB, clientCfg)
		clientDone <- err
	}()

	connA := transport.NewStdioConn(rawA, rawA)
	if err := node.AddPeer(ctx, connA, true); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}

	if err := <-clientDone; err != nil {
		t.Fatalf("client: %v", err)
	}

	if node.PeerCount() != 1 {
		t.Fatalf("expected 1 peer, got %d", node.PeerCount())
	}
}
