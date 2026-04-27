package api

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/transport"
	"github.com/flipfloptech/cortex-mcp/testutil"
)

// --- P2-6: Consumer NodeEvents callbacks must not block library internals ---

// TestHandlePeerDeath_CallbackDoesNotBlockReconnect verifies that a
// slow OnIsolated callback does not delay handlePeerDeath return.
//
// Before P2-6: OnIsolated ran synchronously on the detection goroutine,
// so reconnLoop.Start waited behind it.
// After P2-6: OnIsolated is dispatched on a fresh goroutine.
func TestHandlePeerDeath_CallbackDoesNotBlockReconnect(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	serverCfg, clientCfg := testutil.GenerateTestCertsWithIDs(t, "node-a", "node-b")

	nodeA, err := NewNode(ctx, NodeConfig{
		NodeID: "node-a",
		Events: NodeEvents{
			OnIsolated: func() {
				// Simulate a slow consumer callback.
				time.Sleep(2 * time.Second)
			},
		},
		Reconnect: ReconnectPolicy{Enabled: false},
	})
	if err != nil {
		t.Fatalf("NewNode A: %v", err)
	}
	defer func() { _ = nodeA.Close() }()
	nodeA.SetMembraneConfig(serverCfg)

	nodeB, err := NewNode(ctx, NodeConfig{NodeID: "node-b"})
	if err != nil {
		t.Fatalf("NewNode B: %v", err)
	}
	defer func() { _ = nodeB.Close() }()
	nodeB.SetMembraneConfig(clientCfg)

	rawA, rawB := net.Pipe()
	addDone := make(chan error, 2)
	go func() {
		addDone <- nodeA.AddPeer(ctx, transport.NewStdioConn(rawA, rawA), true)
	}()
	go func() {
		addDone <- nodeB.AddPeer(ctx, transport.NewStdioConn(rawB, rawB), false)
	}()
	for i := 0; i < 2; i++ {
		if err := <-addDone; err != nil {
			t.Fatalf("AddPeer: %v", err)
		}
	}

	peerB, _ := nodeA.peers.Get("node-b")
	beforeDeath := time.Now()

	peerB.deathOnce.Do(func() {
		nodeA.handlePeerDeath(peerB)
	})

	// handlePeerDeath must return promptly — the 2s sleep in OnIsolated
	// runs on a separate goroutine and does not block.
	elapsed := time.Since(beforeDeath)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("handlePeerDeath blocked for %v — OnIsolated callback blocked library internals", elapsed)
	}
}

// TestOnPeerLost_DoesNotBlockCleanup verifies that OnPeerLost runs
// asynchronously so remaining cleanup proceeds immediately.
func TestOnPeerLost_DoesNotBlockCleanup(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var callbackFired atomic.Bool

	serverCfg, clientCfg := testutil.GenerateTestCertsWithIDs(t, "fast-node", "slow-cb-peer")

	node, err := NewNode(ctx, NodeConfig{
		NodeID: "fast-node",
		Events: NodeEvents{
			OnPeerLost: func(nodeID string) {
				// Slow callback — should not block handlePeerDeath return.
				time.Sleep(2 * time.Second)
				callbackFired.Store(true)
			},
		},
		Reconnect: ReconnectPolicy{Enabled: false},
	})
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	defer func() { _ = node.Close() }()
	node.SetMembraneConfig(serverCfg)

	nodeB, err := NewNode(ctx, NodeConfig{NodeID: "slow-cb-peer"})
	if err != nil {
		t.Fatalf("NewNode B: %v", err)
	}
	defer func() { _ = nodeB.Close() }()
	nodeB.SetMembraneConfig(clientCfg)

	rawA, rawB := net.Pipe()
	addDone := make(chan error, 2)
	go func() {
		addDone <- node.AddPeer(ctx, transport.NewStdioConn(rawA, rawA), true)
	}()
	go func() {
		addDone <- nodeB.AddPeer(ctx, transport.NewStdioConn(rawB, rawB), false)
	}()
	for i := 0; i < 2; i++ {
		if err := <-addDone; err != nil {
			t.Fatalf("AddPeer: %v", err)
		}
	}

	peer, _ := node.peers.Get("slow-cb-peer")
	start := time.Now()

	peer.deathOnce.Do(func() {
		node.handlePeerDeath(peer)
	})

	elapsed := time.Since(start)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("handlePeerDeath blocked for %v — OnPeerLost callback blocked library internals", elapsed)
	}

	// Callback should eventually fire (it's on a goroutine).
	time.Sleep(3 * time.Second)
	if !callbackFired.Load() {
		t.Fatal("OnPeerLost callback was never called")
	}
}

// TestOnPeerJoined_DoesNotBlockAddPeer verifies that OnPeerJoined
// runs asynchronously so AddPeer returns promptly.
func TestOnPeerJoined_DoesNotBlockAddPeer(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var callbackFired atomic.Bool

	serverCfg, clientCfg := testutil.GenerateTestCertsWithIDs(t, "join-a", "join-b")

	node, err := NewNode(ctx, NodeConfig{
		NodeID: "join-a",
		Events: NodeEvents{
			OnPeerJoined: func(nodeID string) {
				time.Sleep(2 * time.Second)
				callbackFired.Store(true)
			},
		},
	})
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	defer func() { _ = node.Close() }()
	node.SetMembraneConfig(serverCfg)

	nodeB, err := NewNode(ctx, NodeConfig{NodeID: "join-b"})
	if err != nil {
		t.Fatalf("NewNode B: %v", err)
	}
	defer func() { _ = nodeB.Close() }()
	nodeB.SetMembraneConfig(clientCfg)

	rawA, rawB := net.Pipe()
	start := time.Now()
	addDone := make(chan error, 2)
	go func() {
		addDone <- node.AddPeer(ctx, transport.NewStdioConn(rawA, rawA), true)
	}()
	go func() {
		addDone <- nodeB.AddPeer(ctx, transport.NewStdioConn(rawB, rawB), false)
	}()
	for i := 0; i < 2; i++ {
		if err := <-addDone; err != nil {
			t.Fatalf("AddPeer: %v", err)
		}
	}

	elapsed := time.Since(start)
	if elapsed > 1*time.Second {
		t.Fatalf("AddPeer blocked for %v — OnPeerJoined callback blocked library internals", elapsed)
	}

	// Callback should eventually fire.
	time.Sleep(3 * time.Second)
	if !callbackFired.Load() {
		t.Fatal("OnPeerJoined callback was never called")
	}
}
