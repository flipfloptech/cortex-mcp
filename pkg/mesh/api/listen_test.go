package api

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/nucleus"
	"github.com/flipfloptech/cortex-mcp/pkg/mesh/transport"
	"github.com/flipfloptech/cortex-mcp/testutil"
)

// --- Listen ---

// TestNode_Listen_AcceptsInboundPeers verifies that Listen starts a TCP
// listener which accepts inbound connections and runs them through AddPeer.
func TestNode_Listen_AcceptsInboundPeers(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ca, caKey, pool := testutil.GenerateTestCA(t)
	serverCfg := testutil.GenerateDualCert(t, ca, caKey, pool, "listener-node")
	clientCfg := testutil.GenerateDualCert(t, ca, caKey, pool, "dialer-node")

	// Create the listening node.
	nodeA, err := NewNode(ctx, NodeConfig{NodeID: "listener-node"})
	if err != nil {
		t.Fatalf("NewNode A: %v", err)
	}
	defer func() {
		if err := nodeA.Close(); err != nil {
			t.Logf("close nodeA: %v", err)
		}
	}()
	nodeA.SetMembraneConfig(serverCfg)

	// Start listener.
	lis, err := nodeA.Listen(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() {
		if err := lis.Close(); err != nil {
			t.Logf("close listener: %v", err)
		}
	}()

	// Verify listen address is non-empty.
	if nodeA.ListenAddr() == "" {
		t.Fatal("ListenAddr should be non-empty after Listen")
	}

	// Create the dialing node.
	nodeB, err := NewNode(ctx, NodeConfig{NodeID: "dialer-node"})
	if err != nil {
		t.Fatalf("NewNode B: %v", err)
	}
	defer func() {
		if err := nodeB.Close(); err != nil {
			t.Logf("close nodeB: %v", err)
		}
	}()
	nodeB.SetMembraneConfig(clientCfg)

	// Dial the listener via raw TCP.
	conn, err := net.DialTimeout("tcp", lis.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("TCP dial: %v", err)
	}

	// Run AddPeer on the dialing side (client role).
	stdioCon := transport.NewStdioConn(conn, conn)
	if err := nodeB.AddPeer(ctx, stdioCon, false); err != nil {
		t.Fatalf("AddPeer (client): %v", err)
	}

	// Wait for the listener's accept loop to process the connection.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if nodeA.PeerCount() >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if nodeA.PeerCount() != 1 {
		t.Fatalf("listener node: expected 1 peer, got %d", nodeA.PeerCount())
	}
	if nodeB.PeerCount() != 1 {
		t.Fatalf("dialer node: expected 1 peer, got %d", nodeB.PeerCount())
	}
}

// TestNode_Listen_RegistersInResolver verifies that Listen registers
// the listen address in the node's resolver so other nodes can find it
// via gossip-propagated resolver entries.
func TestNode_Listen_RegistersInResolver(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	node, err := NewNode(ctx, NodeConfig{NodeID: "resolver-node"})
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	defer func() {
		if err := node.Close(); err != nil {
			t.Logf("close: %v", err)
		}
	}()

	lis, err := node.Listen(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() {
		if err := lis.Close(); err != nil {
			t.Logf("close listener: %v", err)
		}
	}()

	// The node's own address should be in the resolver.
	addrs := node.resolver.Resolve("resolver-node")
	if len(addrs) == 0 {
		t.Fatal("expected listen address in resolver, got none")
	}

	// The resolved address should match the listener's actual address.
	found := false
	for _, a := range addrs {
		if a == lis.Addr().String() {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("resolver contains %v, but listener is at %s", addrs, lis.Addr().String())
	}
}

// TestNode_Listen_ContextCancellation verifies the accept loop stops
// when the context is cancelled.
func TestNode_Listen_ContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())

	node, err := NewNode(ctx, NodeConfig{NodeID: "cancel-node"})
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	defer func() {
		if err := node.Close(); err != nil {
			t.Logf("close: %v", err)
		}
	}()

	lis, err := node.Listen(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	// Cancel context — should stop accept loop.
	cancel()

	// Close listener — should not panic.
	if err := lis.Close(); err != nil {
		t.Logf("close listener: %v", err)
	}
}

// --- defaultDialer ---

// TestNode_DefaultDialer_ConnectsToListener verifies that the built-in
// default dialer can connect to a listening node via TCP + AddPeer.
func TestNode_DefaultDialer_ConnectsToListener(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ca, caKey, pool := testutil.GenerateTestCA(t)
	cfgA := testutil.GenerateDualCert(t, ca, caKey, pool, "node-a")
	cfgB := testutil.GenerateDualCert(t, ca, caKey, pool, "node-b")

	// Node A: listener.
	nodeA, err := NewNode(ctx, NodeConfig{NodeID: "node-a"})
	if err != nil {
		t.Fatalf("NewNode A: %v", err)
	}
	defer func() {
		if err := nodeA.Close(); err != nil {
			t.Logf("close nodeA: %v", err)
		}
	}()
	nodeA.SetMembraneConfig(cfgA)

	lis, err := nodeA.Listen(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() {
		if err := lis.Close(); err != nil {
			t.Logf("close listener: %v", err)
		}
	}()

	// Node B: dialer.
	nodeB, err := NewNode(ctx, NodeConfig{NodeID: "node-b"})
	if err != nil {
		t.Fatalf("NewNode B: %v", err)
	}
	defer func() {
		if err := nodeB.Close(); err != nil {
			t.Logf("close nodeB: %v", err)
		}
	}()
	nodeB.SetMembraneConfig(cfgB)

	// Use the default dialer to connect.
	target := nucleus.DialTarget{
		Hostname: "node-a",
		Address:  lis.Addr().String(),
	}

	peerID, err := nodeB.defaultDialer(ctx, target)
	if err != nil {
		t.Fatalf("defaultDialer: %v", err)
	}

	if peerID != "node-a" {
		t.Fatalf("peerID = %q, want %q", peerID, "node-a")
	}

	// Wait for both sides to register the peer.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if nodeA.PeerCount() >= 1 && nodeB.PeerCount() >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if nodeB.PeerCount() != 1 {
		t.Fatalf("node B: expected 1 peer, got %d", nodeB.PeerCount())
	}
	if nodeA.PeerCount() != 1 {
		t.Fatalf("node A: expected 1 peer, got %d", nodeA.PeerCount())
	}
}

// TestNode_DefaultDialer_UnreachableTarget verifies the dialer returns
// a meaningful error for unreachable addresses.
func TestNode_DefaultDialer_UnreachableTarget(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	node, err := NewNode(ctx, NodeConfig{NodeID: "lonely-node"})
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	defer func() {
		if err := node.Close(); err != nil {
			t.Logf("close: %v", err)
		}
	}()

	target := nucleus.DialTarget{
		Hostname: "nonexistent",
		Address:  "127.0.0.1:1", // port 1 is almost certainly refused
	}

	_, err = node.defaultDialer(ctx, target)
	if err == nil {
		t.Fatal("expected error for unreachable target")
	}
}

// TestNode_DefaultDialer_ContextCancelled verifies the dialer respects
// context cancellation during the TCP dial phase.
func TestNode_DefaultDialer_ContextCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	node, err := NewNode(context.Background(), NodeConfig{NodeID: "cancel-node"})
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	defer func() {
		if err := node.Close(); err != nil {
			t.Logf("close: %v", err)
		}
	}()

	target := nucleus.DialTarget{
		Hostname: "unreachable",
		Address:  "127.0.0.1:12345",
	}

	_, err = node.defaultDialer(ctx, target)
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

// --- Full reconnect lifecycle with real transport ---

// TestNode_Reconnect_FullLifecycle_WithListener verifies the complete
// self-healing cycle: two nodes connected, one peer dies, the reconnect
// loop dials back via the listener and re-establishes connectivity.
func TestNode_Reconnect_FullLifecycle_WithListener(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	ca, caKey, pool := testutil.GenerateTestCA(t)
	cfgA := testutil.GenerateDualCert(t, ca, caKey, pool, "stable-node")
	cfgB := testutil.GenerateDualCert(t, ca, caKey, pool, "reconnecting-node")

	var reconnectedPeer atomic.Value
	var isolatedCalled atomic.Bool

	// Node A: stable listener.
	nodeA, err := NewNode(ctx, NodeConfig{NodeID: "stable-node"})
	if err != nil {
		t.Fatalf("NewNode A: %v", err)
	}
	defer func() {
		if err := nodeA.Close(); err != nil {
			t.Logf("close nodeA: %v", err)
		}
	}()
	nodeA.SetMembraneConfig(cfgA)

	lisA, err := nodeA.Listen(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() {
		if err := lisA.Close(); err != nil {
			t.Logf("close listener: %v", err)
		}
	}()

	// Node B: will lose its peer and reconnect.
	nodeB, err := NewNode(ctx, NodeConfig{
		NodeID: "reconnecting-node",
		// Seed resolver with A's listener address so reconnect can find it.
		KnownHosts: map[string][]string{
			"stable-node": {lisA.Addr().String()},
		},
		Events: NodeEvents{
			OnIsolated: func() {
				isolatedCalled.Store(true)
			},
			OnReconnected: func(peerID string) {
				reconnectedPeer.Store(peerID)
			},
		},
		Reconnect: ReconnectPolicy{
			Enabled:      true,
			InitialDelay: 100 * time.Millisecond,
			MaxDelay:     500 * time.Millisecond,
			MaxAttempts:  0, // unlimited — rely on Timeout for the boundary
			Timeout:      30 * time.Second,
		},
	})
	if err != nil {
		t.Fatalf("NewNode B: %v", err)
	}
	defer func() {
		if err := nodeB.Close(); err != nil {
			t.Logf("close nodeB: %v", err)
		}
	}()
	nodeB.SetMembraneConfig(cfgB)

	// Connect A ←→ B via net.Pipe (initial connection).
	rawA, rawB := net.Pipe()

	addDone := make(chan error, 2)
	go func() {
		connA := transport.NewStdioConn(rawA, rawA)
		addDone <- nodeA.AddPeer(ctx, connA, true)
	}()
	go func() {
		connB := transport.NewStdioConn(rawB, rawB)
		addDone <- nodeB.AddPeer(ctx, connB, false)
	}()

	for i := 0; i < 2; i++ {
		if err := <-addDone; err != nil {
			t.Fatalf("initial AddPeer: %v", err)
		}
	}

	// Verify initial connectivity.
	if nodeA.PeerCount() != 1 || nodeB.PeerCount() != 1 {
		t.Fatalf("expected 1 peer each, got A=%d B=%d", nodeA.PeerCount(), nodeB.PeerCount())
	}

	// Kill the connection by closing the underlying transport.
	// This simulates a hard network partition. yamux detects session death
	// and triggers deathOnce → handlePeerDeath → OnIsolated → reconnect.
	if err := rawB.Close(); err != nil {
		t.Logf("close rawB: %v", err)
	}

	// Wait for B to detect isolation and reconnect via A's listener.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if v := reconnectedPeer.Load(); v != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !isolatedCalled.Load() {
		t.Fatal("OnIsolated should have been called")
	}

	v := reconnectedPeer.Load()
	if v == nil {
		t.Fatal("OnReconnected should have been called — reconnect loop failed")
	}
	if v.(string) != "stable-node" {
		t.Fatalf("reconnected to %q, want %q", v.(string), "stable-node")
	}

	// Verify B has a peer again after reconnection.
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if nodeB.PeerCount() >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if nodeB.PeerCount() < 1 {
		t.Fatalf("node B should have reconnected, peers=%d", nodeB.PeerCount())
	}
}

// TestNode_DefaultDialer_SkipsSelf verifies the default dialer skips
// targets that match the node's own ID (don't dial yourself).
func TestNode_DefaultDialer_SkipsSelf(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	node, err := NewNode(ctx, NodeConfig{NodeID: "self-node"})
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	defer func() {
		if err := node.Close(); err != nil {
			t.Logf("close: %v", err)
		}
	}()

	target := nucleus.DialTarget{
		Hostname: "self-node", // same as our own ID
		Address:  "127.0.0.1:9999",
	}

	_, err = node.defaultDialer(ctx, target)
	if err == nil {
		t.Fatal("expected error when dialing self")
	}
}
