package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/transport"
	"github.com/flipfloptech/cortex-mcp/testutil"
)

// --- GrpcDialer: dead session detection ---

// TestGrpcDialer_DeadSession_ReturnsErrPeerDead verifies that when a
// direct peer's yamux session is closed, GrpcDialer returns ErrPeerDead
// (not an opaque transport error).
func TestGrpcDialer_DeadSession_ReturnsErrPeerDead(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	nodeA, nodeB := mustConnectedPair(t, ctx, "dead-a", "dead-b")
	defer func() { _ = nodeA.Close() }()
	defer func() { _ = nodeB.Close() }()

	// Kill B's session (simulate peer death without triggering handlePeerDeath).
	pcB, ok := nodeA.peers.Get("dead-b")
	if !ok {
		t.Fatal("expected peer dead-b in nodeA")
	}
	_ = pcB.session.Close()

	// GrpcDialer should detect the dead session and return ErrPeerDead.
	_, err := nodeA.GrpcDialer(ctx, "dead-b")
	if err == nil {
		t.Fatal("expected error when session is dead")
	}
	// Note: We might get 'no route' instead of ErrPeerDead if handlePeerDeath
	// beats GrpcDialer and removes the peer before GrpcDialer runs.
	if !errors.Is(err, ErrPeerDead) && !strings.Contains(err.Error(), "no route") {
		t.Fatalf("expected ErrPeerDead or no route, got: %v", err)
	}
}

// --- GrpcDialer: retry via gradient table ---

// TestGrpcDialer_DeadSession_RetriesViaGradient verifies that when a
// direct peer dies but an alternative route exists in the gradient table,
// GrpcDialer attempts to reroute through the next-best peer.
//
// Note: There's an inherent race between GrpcDialer and handlePeerDeath
// (fired by acceptDataStreams). Depending on timing:
//   - Path A: peers.Get finds dead peer → session.Open fails → grpcDialerFallback → ErrPeerDead
//   - Path B: handlePeerDeath removes peer first → standard gradient path
//
// Both paths should mention the target node and produce a gradient reroute error.
func TestGrpcDialer_DeadSession_RetriesViaGradient(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfgAB_A, cfgAB_B := testutil.GenerateTestCertsWithIDs(t, "retry-a", "retry-b")
	cfgAC_A, cfgAC_C := testutil.GenerateTestCertsWithIDs(t, "retry-a", "retry-c")

	nodeA := mustNewControlNode(t, ctx, "retry-a", cfgAB_A)
	defer func() { _ = nodeA.Close() }()

	nodeB := mustNewControlNode(t, ctx, "retry-b", cfgAB_B)
	defer func() { _ = nodeB.Close() }()

	nodeC := mustNewControlNode(t, ctx, "retry-c", cfgAC_C)
	defer func() { _ = nodeC.Close() }()

	// Connect A ↔ B.
	rawAB_A, rawAB_B := net.Pipe()
	abDone := make(chan error, 2)
	go func() { abDone <- nodeA.AddPeer(ctx, transport.NewStdioConn(rawAB_A, rawAB_A), true) }()
	go func() { abDone <- nodeB.AddPeer(ctx, transport.NewStdioConn(rawAB_B, rawAB_B), false) }()
	for i := 0; i < 2; i++ {
		if err := <-abDone; err != nil {
			t.Fatalf("A↔B: %v", err)
		}
	}

	// Connect A ↔ C (with A's cfg for AC).
	nodeA.SetMembraneConfig(cfgAC_A)
	rawAC_A, rawAC_C := net.Pipe()
	acDone := make(chan error, 2)
	go func() { acDone <- nodeA.AddPeer(ctx, transport.NewStdioConn(rawAC_A, rawAC_A), true) }()
	go func() { acDone <- nodeC.AddPeer(ctx, transport.NewStdioConn(rawAC_C, rawAC_C), false) }()
	for i := 0; i < 2; i++ {
		if err := <-acDone; err != nil {
			t.Fatalf("A↔C: %v", err)
		}
	}

	// Inject a gradient route: A knows "retry-b" is reachable via "retry-c".
	nodeA.gradient.UpdateRoute("retry-b", "retry-c", 10.0)

	// Kill B's direct session on A (simulate peer death).
	pcB, ok := nodeA.peers.Get("retry-b")
	if !ok {
		t.Fatal("expected peer retry-b in nodeA")
	}
	_ = pcB.session.Close()

	// GrpcDialer should fail — either via dead session detection (ErrPeerDead)
	// or via standard gradient check (handlePeerDeath raced and removed peer).
	_, err := nodeA.GrpcDialer(ctx, "retry-b")
	if err == nil {
		t.Fatal("expected error (multi-hop not implemented)")
	}

	// Regardless of which path was taken, the error should mention the target.
	errMsg := err.Error()
	if !strings.Contains(errMsg, "retry-b") {
		t.Fatalf("error should mention target node, got: %s", errMsg)
	}
	// Depending on timing, we get one of four outcomes:
	//   1. grpcDialerFallback finds gradient route retry-b→retry-c → ErrMultiHopRequired (mentions "multi-hop")
	//   2. grpcDialerFallback reroutes through dead peer retry-b itself → ErrPeerDead (mentions "reroute")
	//   3. handlePeerDeath removes retry-b first, standard path finds gradient route → mentions "retry-c" or "multi-hop"
	//   4. handlePeerDeath removes retry-b first, next-hop lookup fails → mentions "not connected"
	if !strings.Contains(errMsg, "retry-c") && !strings.Contains(errMsg, "multi-hop") && !strings.Contains(errMsg, "reroute") && !strings.Contains(errMsg, "not connected") {
		t.Fatalf("error should mention reroute via retry-c, multi-hop, reroute, or not connected, got: %s", errMsg)
	}
}

// --- GrpcDialer: dead session, no alternative route ---

// TestGrpcDialer_DeadSession_NoAlternativeRoute verifies the error when
// a peer's session dies and no gradient route exists.
//
// Race-tolerant: handlePeerDeath may remove the peer before GrpcDialer
// runs, resulting in either ErrPeerDead (caught session death) or
// "no route" (peer already removed).
func TestGrpcDialer_DeadSession_NoAlternativeRoute(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	nodeA, nodeB := mustConnectedPair(t, ctx, "noalt-a", "noalt-b")
	defer func() { _ = nodeA.Close() }()
	defer func() { _ = nodeB.Close() }()

	// Kill B's session.
	pcB, ok := nodeA.peers.Get("noalt-b")
	if !ok {
		t.Fatal("expected peer noalt-b in nodeA")
	}
	_ = pcB.session.Close()

	// No gradient routes to noalt-b through anyone else.
	_, err := nodeA.GrpcDialer(ctx, "noalt-b")
	if err == nil {
		t.Fatal("expected error when session is dead and no alt route")
	}
	// Accept either ErrPeerDead (caught dead session) or "no route"
	// (handlePeerDeath already cleaned up the peer).
	errMsg := err.Error()
	if !errors.Is(err, ErrPeerDead) && !strings.Contains(errMsg, "no route") {
		t.Fatalf("expected ErrPeerDead or 'no route', got: %v", err)
	}
}

// --- ErrPeerDead: error semantics ---

// TestErrPeerDead_Wrapping verifies that ErrPeerDead works correctly with
// errors.Is for callers that need to distinguish retryable mesh errors.
func TestErrPeerDead_Wrapping(t *testing.T) {
	t.Parallel()

	baseErr := errors.New("session shutdown")
	wrapped := fmt.Errorf("api: open stream to %q: %w: %w", "node-x", ErrPeerDead, baseErr)

	if !errors.Is(wrapped, ErrPeerDead) {
		t.Fatal("errors.Is should find ErrPeerDead in wrapped error")
	}
}

// --- GrpcDialer: context cancellation during retry ---

// TestGrpcDialer_DeadSession_ContextCancelled verifies that GrpcDialer
// respects context cancellation and doesn't hang during retry logic.
func TestGrpcDialer_DeadSession_ContextCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	nodeA, nodeB := mustConnectedPair(t, ctx, "ctxcancel-a", "ctxcancel-b")
	defer func() { _ = nodeA.Close() }()
	defer func() { _ = nodeB.Close() }()

	// Kill B's session.
	pcB, ok := nodeA.peers.Get("ctxcancel-b")
	if !ok {
		t.Fatal("expected peer ctxcancel-b in nodeA")
	}
	_ = pcB.session.Close()

	// Cancel context before calling GrpcDialer.
	cancelledCtx, cancelNow := context.WithCancel(ctx)
	cancelNow()

	_, err := nodeA.GrpcDialer(cancelledCtx, "ctxcancel-b")
	if err == nil {
		t.Fatal("expected error with cancelled context")
	}
}

// --- GrpcDialer: concurrent calls during peer death ---

// TestGrpcDialer_DeadSession_ConcurrentCallers verifies that multiple
// concurrent GrpcDialer calls during peer death don't race or panic.
func TestGrpcDialer_DeadSession_ConcurrentCallers(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	nodeA, nodeB := mustConnectedPair(t, ctx, "conc-a", "conc-b")
	defer func() { _ = nodeA.Close() }()
	defer func() { _ = nodeB.Close() }()

	// Kill B's session.
	pcB, ok := nodeA.peers.Get("conc-b")
	if !ok {
		t.Fatal("expected peer conc-b in nodeA")
	}
	_ = pcB.session.Close()

	// Concurrently dial the dead peer.
	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, err := nodeA.GrpcDialer(ctx, "conc-b")
			if err == nil {
				t.Error("expected error from dead session")
			}
		}()
	}
	wg.Wait()
}
