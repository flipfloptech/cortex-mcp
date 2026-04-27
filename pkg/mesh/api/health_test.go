package api

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/nucleus"
	"github.com/flipfloptech/cortex-mcp/pkg/mesh/routing"
)

// --- handlePeerDeath ---

func TestHandlePeerDeath_RemovesPeer(t *testing.T) {
	t.Parallel()

	n := &Node{
		peers:    newPeerManager(),
		gradient: routing.NewGradientTable("self"),
	}

	pc := &peerConn{nodeID: "peer-1"}
	n.peers.Add("peer-1", pc)
	n.gradient.AddDirectNeighbor("peer-1", 1.0)

	n.handlePeerDeath(pc)

	if n.peers.Count() != 0 {
		t.Fatalf("peer count should be 0 after death, got %d", n.peers.Count())
	}

	// Routes through the dead peer should be gone.
	_, ok := n.gradient.BestRoute("peer-1")
	if ok {
		t.Fatal("routes to dead peer should be purged from gradient table")
	}
}

func TestHandlePeerDeath_FiresOnPeerLost(t *testing.T) {
	t.Parallel()

	var called atomic.Bool
	var lostNodeID string
	var mu sync.Mutex

	n := &Node{
		peers:    newPeerManager(),
		gradient: routing.NewGradientTable("self"),
		events: NodeEvents{
			OnPeerLost: func(nodeID string) {
				mu.Lock()
				lostNodeID = nodeID
				mu.Unlock()
				called.Store(true)
			},
		},
	}

	pc := &peerConn{nodeID: "peer-1"}
	n.peers.Add("peer-1", pc)

	n.handlePeerDeath(pc)

	// Callback fires async (P2-6) — poll briefly.
	deadline := time.After(1 * time.Second)
	for !called.Load() {
		select {
		case <-deadline:
			t.Fatal("OnPeerLost should have been called")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	mu.Lock()
	if lostNodeID != "peer-1" {
		t.Fatalf("OnPeerLost called with %q, want %q", lostNodeID, "peer-1")
	}
	mu.Unlock()
}

func TestHandlePeerDeath_LastPeer_FiresOnIsolated(t *testing.T) {
	t.Parallel()

	var isolatedCalled atomic.Bool

	n := &Node{
		peers:    newPeerManager(),
		gradient: routing.NewGradientTable("self"),
		events: NodeEvents{
			OnIsolated: func() {
				isolatedCalled.Store(true)
			},
		},
	}

	// Only one peer — when it dies, we're isolated.
	pc := &peerConn{nodeID: "peer-1"}
	n.peers.Add("peer-1", pc)

	n.handlePeerDeath(pc)

	// Callback fires async (P2-6) — poll briefly.
	deadline := time.After(1 * time.Second)
	for !isolatedCalled.Load() {
		select {
		case <-deadline:
			t.Fatal("OnIsolated should have been called when last peer dies")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func TestHandlePeerDeath_NotIsolated_WithRemainingPeers(t *testing.T) {
	t.Parallel()

	var isolatedCalled atomic.Bool

	n := &Node{
		peers:    newPeerManager(),
		gradient: routing.NewGradientTable("self"),
		events: NodeEvents{
			OnIsolated: func() {
				isolatedCalled.Store(true)
			},
		},
	}

	// Two peers — one dies, one remains.
	pc1 := &peerConn{nodeID: "peer-1"}
	pc2 := &peerConn{nodeID: "peer-2"}
	n.peers.Add("peer-1", pc1)
	n.peers.Add("peer-2", pc2)

	n.handlePeerDeath(pc1)

	time.Sleep(10 * time.Millisecond)

	if isolatedCalled.Load() {
		t.Fatal("OnIsolated should NOT be called when peers remain")
	}
}

func TestHandlePeerDeath_Idempotent_ViaOnce(t *testing.T) {
	t.Parallel()

	var lostCount atomic.Int32

	n := &Node{
		peers:    newPeerManager(),
		gradient: routing.NewGradientTable("self"),
		events: NodeEvents{
			OnPeerLost: func(_ string) {
				lostCount.Add(1)
			},
		},
	}

	pc := &peerConn{nodeID: "peer-1"}
	n.peers.Add("peer-1", pc)

	// Call twice via deathOnce — should fire exactly once.
	pc.deathOnce.Do(func() { n.handlePeerDeath(pc) })
	pc.deathOnce.Do(func() { n.handlePeerDeath(pc) })

	// Callback fires async (P2-6) — wait for it.
	time.Sleep(100 * time.Millisecond)

	if lostCount.Load() != 1 {
		t.Fatalf("OnPeerLost called %d times, want exactly 1", lostCount.Load())
	}
}

func TestHandlePeerDeath_Concurrent(t *testing.T) {
	t.Parallel()

	var lostCount atomic.Int32

	n := &Node{
		peers:    newPeerManager(),
		gradient: routing.NewGradientTable("self"),
		events: NodeEvents{
			OnPeerLost: func(_ string) {
				lostCount.Add(1)
			},
		},
	}

	pc := &peerConn{nodeID: "peer-1"}
	n.peers.Add("peer-1", pc)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pc.deathOnce.Do(func() { n.handlePeerDeath(pc) })
		}()
	}
	wg.Wait()

	// Callback fires async (P2-6) — wait for it.
	time.Sleep(100 * time.Millisecond)

	if lostCount.Load() != 1 {
		t.Fatalf("OnPeerLost called %d times under concurrency, want exactly 1", lostCount.Load())
	}
}

// --- peerConnected event ---

func TestPeerConnected_FiresOnPeerJoined(t *testing.T) {
	t.Parallel()

	var joinedNodeID string
	var mu sync.Mutex
	var called atomic.Bool

	n := &Node{
		peers:    newPeerManager(),
		gradient: routing.NewGradientTable("self"),
		events: NodeEvents{
			OnPeerJoined: func(nodeID string) {
				mu.Lock()
				joinedNodeID = nodeID
				mu.Unlock()
				called.Store(true)
			},
		},
	}

	n.peerConnected("peer-1")

	// Callback fires async (P2-6) — poll briefly.
	deadline := time.After(1 * time.Second)
	for !called.Load() {
		select {
		case <-deadline:
			t.Fatal("OnPeerJoined should have been called")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	mu.Lock()
	if joinedNodeID != "peer-1" {
		t.Fatalf("OnPeerJoined called with %q, want %q", joinedNodeID, "peer-1")
	}
	mu.Unlock()
}

// --- NodeEvents nil safety ---

func TestHandlePeerDeath_NilEvents_NoPanic(t *testing.T) {
	t.Parallel()

	n := &Node{
		peers:    newPeerManager(),
		gradient: routing.NewGradientTable("self"),
		// No events set — should not panic.
	}

	pc := &peerConn{nodeID: "peer-1"}
	n.peers.Add("peer-1", pc)

	// Should not panic with zero-value NodeEvents.
	n.handlePeerDeath(pc)

	if n.peers.Count() != 0 {
		t.Fatalf("peer should be removed even without events, got count %d", n.peers.Count())
	}
}

// --- Reconnect loop activation ---

func TestHandlePeerDeath_StartsReconnectLoop(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var isolatedCalled atomic.Bool
	var orphanedCalled atomic.Bool

	n := &Node{
		ctx:      ctx,
		cancel:   cancel,
		peers:    newPeerManager(),
		gradient: routing.NewGradientTable("self"),
		reconnect: ReconnectPolicy{
			Enabled:      true,
			InitialDelay: 10 * time.Millisecond,
			MaxDelay:     20 * time.Millisecond,
			MaxAttempts:  1, // fail fast for test
			Timeout:      1 * time.Second,
		},
		resolver: nucleus.NewResolver(),
		events: NodeEvents{
			OnIsolated: func() {
				isolatedCalled.Store(true)
			},
			OnOrphaned: func() {
				orphanedCalled.Store(true)
			},
		},
	}

	// Wire the reconnect loop with a no-op dialer.
	n.reconnLoop = newReconnectLoop(n, n.reconnect, func(ctx context.Context, target nucleus.DialTarget) (string, error) {
		return "", fmt.Errorf("no dialer configured")
	})

	// Add one peer, then kill it — should trigger reconnect loop.
	pc := &peerConn{nodeID: "peer-1"}
	n.peers.Add("peer-1", pc)

	n.handlePeerDeath(pc)

	// Callback fires async (P2-6) — poll briefly.
	deadlineIso := time.After(1 * time.Second)
	for !isolatedCalled.Load() {
		select {
		case <-deadlineIso:
			t.Fatal("OnIsolated should have been called")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	// Wait for the reconnect loop to complete (MaxAttempts=1, no targets → orphan).
	deadline := time.After(2 * time.Second)
	for !orphanedCalled.Load() {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for OnOrphaned — reconnect loop never started or completed")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestHandlePeerDeath_ReconnectDisabled_NoLoop(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	n := &Node{
		ctx:      ctx,
		cancel:   cancel,
		peers:    newPeerManager(),
		gradient: routing.NewGradientTable("self"),
		reconnect: ReconnectPolicy{
			Enabled: false,
		},
		resolver: nucleus.NewResolver(),
	}

	// Wire the reconnect loop even though disabled — it should not start.
	n.reconnLoop = newReconnectLoop(n, n.reconnect, func(ctx context.Context, target nucleus.DialTarget) (string, error) {
		t.Fatal("dialer should never be called when reconnect is disabled")
		return "", nil
	})

	pc := &peerConn{nodeID: "peer-1"}
	n.peers.Add("peer-1", pc)

	n.handlePeerDeath(pc)

	// Give time for any accidental start.
	time.Sleep(100 * time.Millisecond)

	if n.reconnLoop.running.Load() {
		t.Fatal("reconnect loop should NOT run when Enabled=false")
	}
}
