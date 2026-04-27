package api

import (
	"context"
	"testing"
	"time"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/routing"
)

// --- P1-3: Node-level wiring of capability-index GC & ingest guards ---
//
// These tests pin the api-layer contract:
//
//   1. NodeConfig exposes CapabilityStaleTTL, MaxCapabilityLength, and
//      MaxCapabilitiesPerNode; zero values default to safe values
//      derived from GossipInterval or from routing defaults.
//   2. The limits are applied to the node's CapabilityIndex at startup.
//   3. sendGossipToAll invokes PurgeStale against the index using the
//      configured TTL, so stale entries age out on the gossip tick
//      without any explicit operator action.

func TestNodeConfig_CapabilityStaleTTL_DefaultIs3xGossip(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	n, err := NewNode(ctx, NodeConfig{
		NodeID:         "n1",
		GossipInterval: 2 * time.Second,
		// CapabilityStaleTTL zero → default
	})
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	defer func() { _ = n.Close() }()

	got := n.CapabilityStaleTTL()
	want := 6 * time.Second // 3 × 2s
	if got != want {
		t.Fatalf("CapabilityStaleTTL default = %v, want %v (3 × GossipInterval)", got, want)
	}
}

func TestNodeConfig_CapabilityStaleTTL_Override(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	n, err := NewNode(ctx, NodeConfig{
		NodeID:             "n1",
		GossipInterval:     2 * time.Second,
		CapabilityStaleTTL: 45 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	defer func() { _ = n.Close() }()

	if got := n.CapabilityStaleTTL(); got != 45*time.Second {
		t.Fatalf("CapabilityStaleTTL = %v, want 45s", got)
	}
}

func TestNodeConfig_IngestLimits_ApplyToIndex(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	n, err := NewNode(ctx, NodeConfig{
		NodeID:                 "n1",
		MaxCapabilityLength:    8,
		MaxCapabilitiesPerNode: 2,
	})
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	defer func() { _ = n.Close() }()

	// Inject a mix: one in-bounds, one over-length, three within count.
	// Expect: "ok:a" stored; "x:this-is-too-long" dropped; count capped at 2.
	n.CapabilityIndex().Update("peer", 1.0, []string{
		"ok:a",
		"x:this-is-too-long", // > 8 bytes
		"ok:b",
		"ok:c", // 3rd in-bounds entry — count cap should trim it
	})

	snap := n.CapabilityIndex().Snapshot()["peer"]
	if len(snap) != 2 {
		t.Fatalf("per-node count limit not applied: got %d entries, want 2: %v",
			len(snap), snap)
	}
	for _, c := range snap {
		if len(c) > 8 {
			t.Fatalf("oversize cap leaked into index: %q", c)
		}
	}
}

func TestSendGossipToAll_PurgesStaleCapabilityEntries(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	n, err := NewNode(ctx, NodeConfig{
		NodeID:             "n1",
		GossipInterval:     1 * time.Second,
		CapabilityStaleTTL: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	defer func() { _ = n.Close() }()

	// Seed a capability entry, then back-date it past the TTL.
	idx := n.CapabilityIndex()
	idx.Update("ghost", 1.0, []string{"tool:ghost"})
	idx.BackdateForTest("ghost", time.Now().Add(-time.Hour))

	// Add a second, fresh entry that must survive.
	idx.Update("alive", 1.0, []string{"tool:alive"})

	// Trigger gossip emission — PurgeStale must run as part of it.
	n.sendGossipToAll()

	if got := idx.Lookup("tool:ghost"); len(got) != 0 {
		t.Fatalf("stale 'ghost' entry should be purged on gossip tick, got %v", got)
	}
	if got := idx.Lookup("tool:alive"); len(got) != 1 {
		t.Fatalf("fresh 'alive' entry must survive gossip-tick purge, got %v", got)
	}
}

// Sanity: a node with a zero TTL (never purge) does not crash and keeps
// entries indefinitely. Protects against a "clever" config accidentally
// wiping the whole index.
func TestSendGossipToAll_ZeroTTL_PurgesNothing(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	n, err := NewNode(ctx, NodeConfig{
		NodeID:             "n1",
		GossipInterval:     1 * time.Second,
		CapabilityStaleTTL: -1, // sentinel: disable purging
	})
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	defer func() { _ = n.Close() }()

	idx := n.CapabilityIndex()
	idx.Update("old", 1.0, []string{"tool:old"})
	idx.BackdateForTest("old", time.Now().Add(-time.Hour))

	n.sendGossipToAll()

	if got := idx.Lookup("tool:old"); len(got) != 1 {
		t.Fatalf("TTL=-1 must disable purge; 'old' entry gone, got %v", got)
	}
}

// Compile-check that the routing package defaults are reachable from
// api code — some file somewhere needs to use these symbols or they
// will be flagged as unused imports when we wire them up.
var _ = routing.DefaultMaxCapabilityLength
var _ = routing.DefaultMaxCapabilitiesPerNode
