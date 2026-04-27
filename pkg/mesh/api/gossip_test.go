package api

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/transport"
	"github.com/flipfloptech/cortex-mcp/testutil"
)

// TestGossipTicker_EmitsToAllPeers verifies that the gossip ticker
// periodically sends gradient vectors to connected peers.
func TestGossipTicker_EmitsToAllPeers(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	nodeA, nodeB := mustConnectedPair(t, ctx, "ticker-a", "ticker-b")
	defer func() {
		if err := nodeA.Close(); err != nil {
			t.Logf("close nodeA: %v", err)
		}
	}()
	defer func() {
		if err := nodeB.Close(); err != nil {
			t.Logf("close nodeB: %v", err)
		}
	}()

	// Start the gossip ticker on A with a fast interval.
	nodeA.StartGossipTicker(ctx, 50*time.Millisecond)

	// Wait for B to learn about A via gossip.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout: B never received gossip from A")
		default:
			if _, ok := nodeB.gradient.BestRoute("ticker-a"); ok {
				return // success
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// TestGrpcDialer_MultiHop_RouteNotFound verifies error when no route exists.
func TestGrpcDialer_MultiHop_RouteNotFound(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	nodeA, _ := mustConnectedPair(t, ctx, "noroute-a", "noroute-b")
	defer func() {
		if err := nodeA.Close(); err != nil {
			t.Logf("close nodeA: %v", err)
		}
	}()

	_, err := nodeA.GrpcDialer(ctx, "unknown-node")
	if err == nil {
		t.Fatal("expected error for unknown node")
	}
}

// TestSonar_ThreeNodePropagation verifies WhoHas forwarding across
// a 3-node chain: A → B → C. A sends WhoHas, C has the capability.
func TestSonar_ThreeNodePropagation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Build A ↔ B ↔ C chain.
	cfgAB_A, cfgAB_B := testutil.GenerateTestCertsWithIDs(t, "sonar-a", "sonar-b")
	cfgBC_B, cfgBC_C := testutil.GenerateTestCertsWithIDs(t, "sonar-b", "sonar-c")

	nodeA := mustNewControlNode(t, ctx, "sonar-a", cfgAB_A)
	defer func() {
		if err := nodeA.Close(); err != nil {
			t.Logf("close nodeA: %v", err)
		}
	}()

	nodeB := mustNewControlNode(t, ctx, "sonar-b", cfgAB_B)
	defer func() {
		if err := nodeB.Close(); err != nil {
			t.Logf("close nodeB: %v", err)
		}
	}()

	nodeC := mustNewControlNode(t, ctx, "sonar-c", cfgBC_C)
	defer func() {
		if err := nodeC.Close(); err != nil {
			t.Logf("close nodeC: %v", err)
		}
	}()

	// Register capability on C.
	nodeC.RegisterCapability("tool:deep_scan")

	// Connect A ↔ B.
	rawAB_A, rawAB_B := net.Pipe()
	abDone := make(chan error, 2)
	go func() {
		abDone <- nodeA.AddPeer(ctx, transport.NewStdioConn(rawAB_A, rawAB_A), true)
	}()
	go func() {
		abDone <- nodeB.AddPeer(ctx, transport.NewStdioConn(rawAB_B, rawAB_B), false)
	}()
	for i := 0; i < 2; i++ {
		if err := <-abDone; err != nil {
			t.Fatalf("A↔B: %v", err)
		}
	}

	// Connect B ↔ C.
	nodeB.SetMembraneConfig(cfgBC_B)
	rawBC_B, rawBC_C := net.Pipe()
	bcDone := make(chan error, 2)
	go func() {
		bcDone <- nodeB.AddPeer(ctx, transport.NewStdioConn(rawBC_B, rawBC_B), true)
	}()
	go func() {
		bcDone <- nodeC.AddPeer(ctx, transport.NewStdioConn(rawBC_C, rawBC_C), false)
	}()
	for i := 0; i < 2; i++ {
		if err := <-bcDone; err != nil {
			t.Fatalf("B↔C: %v", err)
		}
	}

	// A sends Sonar — B should forward to C, C should respond.
	sonarCtx, sonarCancel := context.WithTimeout(ctx, 1*time.Second)
	defer sonarCancel()

	results, err := nodeA.Sonar(sonarCtx, "tool:deep_scan")
	if err != nil {
		t.Fatalf("Sonar: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("expected response from C via B forwarding")
	}

	found := false
	for _, r := range results {
		if r.NodeID == "sonar-c" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected sonar-c in results, got: %+v", results)
	}
}
