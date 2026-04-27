package api

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/nucleus"
	"github.com/flipfloptech/cortex-mcp/pkg/mesh/routing"
)

// --- Backoff ---

func TestBackoff_Exponential(t *testing.T) {
	t.Parallel()

	rl := &reconnectLoop{
		policy: ReconnectPolicy{
			InitialDelay: 100 * time.Millisecond,
			MaxDelay:     800 * time.Millisecond,
		},
	}

	// Round 0: initial delay.
	if d := rl.backoff(0); d != 100*time.Millisecond {
		t.Fatalf("backoff(0) = %v, want 100ms", d)
	}

	// Round 1: 200ms.
	if d := rl.backoff(1); d != 200*time.Millisecond {
		t.Fatalf("backoff(1) = %v, want 200ms", d)
	}

	// Round 2: 400ms.
	if d := rl.backoff(2); d != 400*time.Millisecond {
		t.Fatalf("backoff(2) = %v, want 400ms", d)
	}

	// Round 3: 800ms (at cap).
	if d := rl.backoff(3); d != 800*time.Millisecond {
		t.Fatalf("backoff(3) = %v, want 800ms (cap)", d)
	}

	// Round 4: still 800ms (capped).
	if d := rl.backoff(4); d != 800*time.Millisecond {
		t.Fatalf("backoff(4) = %v, want 800ms (cap)", d)
	}
}

// --- reconnectLoop lifecycle ---

func TestReconnectLoop_StopsOnSuccess(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resolver := nucleus.NewResolver()
	resolver.AddEntryWithTransport("peer-1", []string{"10.0.1.1"}, 0, "ssh")

	var reconnectedPeer string
	var mu sync.Mutex

	n := &Node{
		ctx:      ctx,
		cancel:   cancel,
		peers:    newPeerManager(),
		gradient: routing.NewGradientTable("self"),
		resolver: resolver,
		events: NodeEvents{
			OnReconnected: func(nodeID string) {
				mu.Lock()
				reconnectedPeer = nodeID
				mu.Unlock()
			},
		},
	}

	// Dial succeeds immediately.
	dialer := func(_ context.Context, target nucleus.DialTarget) (string, error) {
		return "peer-1", nil
	}

	rl := newReconnectLoop(n, n.reconnect, dialer)
	rl.Start(ctx)

	// Wait for the loop to complete.
	time.Sleep(200 * time.Millisecond)

	if rl.running.Load() {
		t.Fatal("loop should have stopped after successful reconnection")
	}

	mu.Lock()
	if reconnectedPeer != "peer-1" {
		t.Fatalf("OnReconnected called with %q, want %q", reconnectedPeer, "peer-1")
	}
	mu.Unlock()
}

func TestReconnectLoop_OrphanedAfterMaxAttempts(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resolver := nucleus.NewResolver()
	resolver.AddEntryWithTransport("peer-1", []string{"10.0.1.1"}, 0, "ssh")

	var orphanedCalled atomic.Bool

	n := &Node{
		ctx:      ctx,
		cancel:   cancel,
		peers:    newPeerManager(),
		gradient: routing.NewGradientTable("self"),
		resolver: resolver,
		events: NodeEvents{
			OnOrphaned: func() {
				orphanedCalled.Store(true)
			},
		},
		reconnect: ReconnectPolicy{
			Enabled:      true,
			InitialDelay: 10 * time.Millisecond,
			MaxDelay:     20 * time.Millisecond,
			MaxAttempts:  3,
			Timeout:      0, // no timeout, rely on MaxAttempts
		},
	}

	// Dialer always fails.
	dialer := func(_ context.Context, _ nucleus.DialTarget) (string, error) {
		return "", errors.New("connection refused")
	}

	rl := newReconnectLoop(n, n.reconnect, dialer)
	rl.Start(ctx)

	// Wait for loop to exhaust attempts.
	time.Sleep(500 * time.Millisecond)

	if !orphanedCalled.Load() {
		t.Fatal("OnOrphaned should have been called after MaxAttempts exhausted")
	}

	if rl.running.Load() {
		t.Fatal("loop should have stopped after orphaned")
	}
}

func TestReconnectLoop_CancelledByContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())

	resolver := nucleus.NewResolver()
	resolver.AddEntryWithTransport("peer-1", []string{"10.0.1.1"}, 0, "ssh")

	n := &Node{
		ctx:      ctx,
		cancel:   cancel,
		peers:    newPeerManager(),
		gradient: routing.NewGradientTable("self"),
		resolver: resolver,
		reconnect: ReconnectPolicy{
			Enabled:      true,
			InitialDelay: 50 * time.Millisecond,
			MaxDelay:     100 * time.Millisecond,
			MaxAttempts:  0, // unlimited
		},
	}

	// Dialer always fails.
	dialer := func(_ context.Context, _ nucleus.DialTarget) (string, error) {
		return "", errors.New("connection refused")
	}

	rl := newReconnectLoop(n, n.reconnect, dialer)
	rl.Start(ctx)

	// Let it run for a bit.
	time.Sleep(100 * time.Millisecond)
	cancel()

	// Wait for the loop to notice cancellation.
	time.Sleep(200 * time.Millisecond)

	if rl.running.Load() {
		t.Fatal("loop should have stopped after context cancellation")
	}
}

func TestReconnectLoop_DoubleStartIgnored(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resolver := nucleus.NewResolver()

	n := &Node{
		ctx:      ctx,
		cancel:   cancel,
		peers:    newPeerManager(),
		gradient: routing.NewGradientTable("self"),
		resolver: resolver,
		reconnect: ReconnectPolicy{
			Enabled:      true,
			InitialDelay: 50 * time.Millisecond,
			MaxDelay:     100 * time.Millisecond,
		},
	}

	dialer := func(_ context.Context, _ nucleus.DialTarget) (string, error) {
		return "", errors.New("connection refused")
	}

	rl := newReconnectLoop(n, n.reconnect, dialer)
	rl.Start(ctx)
	rl.Start(ctx) // second start should be ignored

	time.Sleep(50 * time.Millisecond)
	cancel()

	// No panic, no double-goroutine issues.
}

func TestReconnectLoop_NoTargets_OrphansImmediately(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resolver := nucleus.NewResolver() // empty — no targets

	var orphanedCalled atomic.Bool

	n := &Node{
		ctx:      ctx,
		cancel:   cancel,
		peers:    newPeerManager(),
		gradient: routing.NewGradientTable("self"),
		resolver: resolver,
		events: NodeEvents{
			OnOrphaned: func() {
				orphanedCalled.Store(true)
			},
		},
		reconnect: ReconnectPolicy{
			Enabled:      true,
			InitialDelay: 10 * time.Millisecond,
			MaxDelay:     20 * time.Millisecond,
			MaxAttempts:  2,
		},
	}

	dialer := func(_ context.Context, _ nucleus.DialTarget) (string, error) {
		return "", errors.New("unreachable")
	}

	rl := newReconnectLoop(n, n.reconnect, dialer)
	rl.Start(ctx)

	time.Sleep(200 * time.Millisecond)

	if !orphanedCalled.Load() {
		t.Fatal("OnOrphaned should fire when no targets are available")
	}
}
