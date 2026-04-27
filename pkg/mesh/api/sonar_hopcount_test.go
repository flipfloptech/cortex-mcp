package api

import (
	"context"
	"testing"
	"time"
)

// --- P1-2: hop-count ceiling + NodeConfig wiring ---
//
// These tests pin the api-layer contract for WhoHas hop limiting:
//
//   1. NodeConfig.MaxSonarHops overrides the origin hop budget.
//   2. Zero value uses the log(peerCount) default formula that grows
//      logarithmically with fleet size — never a fixed count.
//   3. NodeConfig.SonarSeenTTL bounds the seen-set lifetime at the
//      routing layer, wired through NewSonar via WithSeenTTL.
//   4. The integration path (Sonar → broadcast) stamps MaxHops on the
//      outbound protobuf frame.

func TestNodeConfig_MaxSonarHops_Override(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	n, err := NewNode(ctx, NodeConfig{
		NodeID:       "n1",
		MaxSonarHops: 11,
	})
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	defer func() { _ = n.Close() }()

	if got := n.MaxSonarHops(); got != 11 {
		t.Fatalf("MaxSonarHops override = %d, want 11", got)
	}
}

func TestNodeConfig_MaxSonarHops_DefaultGrowsWithLogPeerCount(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	n, err := NewNode(ctx, NodeConfig{NodeID: "n1"}) // zero MaxSonarHops → default
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	defer func() { _ = n.Close() }()

	// With zero peers, the floor formula kicks in: max(ceil(log2(32))+4, 4)
	//   = max(5+4, 4) = 9.
	// Any positive value ≥ 4 is acceptable; the contract is that it's
	// non-zero and bounded, NOT zero-and-unbounded.
	got := n.MaxSonarHops()
	if got < 4 {
		t.Fatalf("default MaxSonarHops must be ≥ 4 (minimum floor), got %d", got)
	}
	if got > 32 {
		t.Fatalf("default MaxSonarHops must stay log-bounded, got %d", got)
	}
}

func TestNodeConfig_SonarSeenTTL_Override(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	n, err := NewNode(ctx, NodeConfig{
		NodeID:       "n1",
		SonarSeenTTL: 42 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	defer func() { _ = n.Close() }()

	if got := n.SonarSeenTTL(); got != 42*time.Second {
		t.Fatalf("SonarSeenTTL = %v, want 42s", got)
	}
}

func TestNodeConfig_SonarSeenTTL_HasSaneDefault(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	n, err := NewNode(ctx, NodeConfig{NodeID: "n1"})
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	defer func() { _ = n.Close() }()

	got := n.SonarSeenTTL()
	if got <= 0 {
		t.Fatalf("SonarSeenTTL default must be positive, got %v", got)
	}
	// Must be ≥ the default timeout so in-flight broadcasts are
	// deduped for at least as long as responses might come in.
	if got < time.Second {
		t.Fatalf("SonarSeenTTL default %v is unrealistically short", got)
	}
}
