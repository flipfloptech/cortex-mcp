package api

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/nucleus"
)

// dialFunc abstracts the transport dialing mechanism for testing.
// On success, returns the connected peer's nodeID.
type dialFunc func(ctx context.Context, target nucleus.DialTarget) (nodeID string, err error)

// reconnectLoop attempts to re-establish mesh connectivity after
// all peers are lost (isolation). It cycles through known dial
// targets with exponential backoff until either:
//
//   - A connection succeeds → fires OnReconnected, stops.
//   - MaxAttempts exhausted → fires OnOrphaned, stops.
//   - Timeout expires → fires OnOrphaned, stops.
//   - Context is cancelled → stops silently.
type reconnectLoop struct {
	node    *Node
	policy  ReconnectPolicy
	dial    dialFunc
	running atomic.Bool
}

// newReconnectLoop creates a reconnect loop with the given dialer.
func newReconnectLoop(n *Node, policy ReconnectPolicy, dialer dialFunc) *reconnectLoop {
	return &reconnectLoop{
		node:   n,
		policy: policy,
		dial:   dialer,
	}
}

// Start launches the reconnect loop in a background goroutine.
// Safe to call multiple times — only the first call starts the loop.
func (rl *reconnectLoop) Start(ctx context.Context) {
	if !rl.running.CompareAndSwap(false, true) {
		return // already running
	}
	go rl.run(ctx)
}

// Stop cancels the reconnect loop.
func (rl *reconnectLoop) Stop() {
	rl.running.Store(false)
}

// backoff calculates the delay for the given round using exponential
// backoff with a cap at MaxDelay.
func (rl *reconnectLoop) backoff(round int) time.Duration {
	delay := rl.policy.InitialDelay
	for i := 0; i < round; i++ {
		delay *= 2
		if delay > rl.policy.MaxDelay {
			delay = rl.policy.MaxDelay
			break
		}
	}
	return delay
}

// run is the main reconnection loop.
func (rl *reconnectLoop) run(ctx context.Context) {
	defer rl.running.Store(false)

	// Apply timeout if configured.
	if rl.policy.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, rl.policy.Timeout)
		defer cancel()
	}

	round := 0
	for {
		select {
		case <-ctx.Done():
			slog.Info("api: reconnect loop cancelled", "round", round)
			return
		default:
		}

		// Collect dial targets from the resolver.
		targets := rl.node.resolver.AllDialTargets()

		if len(targets) == 0 && rl.policy.MaxAttempts > 0 && round >= rl.policy.MaxAttempts {
			slog.Warn("api: reconnect loop orphaned — no targets, max attempts reached")
			if rl.node.events.OnOrphaned != nil {
				go rl.node.events.OnOrphaned()
			}
			return
		}

		// Try each target.
		for _, target := range targets {
			select {
			case <-ctx.Done():
				return
			default:
			}

			slog.Debug("api: reconnect attempt",
				"host", target.Hostname,
				"addr", target.Address,
				"transport", target.Transport,
				"round", round,
			)

			nodeID, err := rl.dial(ctx, target)
			if err != nil {
				slog.Debug("api: reconnect dial failed",
					"host", target.Hostname,
					"error", err,
				)
				continue
			}

			// Success!
			slog.Info("api: reconnected to mesh", "peer", nodeID)
			if rl.node.events.OnReconnected != nil {
				cb := rl.node.events.OnReconnected
				go cb(nodeID)
			}
			return
		}

		// All targets failed this round.
		round++

		// Check max attempts.
		if rl.policy.MaxAttempts > 0 && round >= rl.policy.MaxAttempts {
			slog.Warn("api: reconnect loop orphaned — max attempts exhausted",
				"rounds", round,
				"maxAttempts", rl.policy.MaxAttempts,
			)
			if rl.node.events.OnOrphaned != nil {
				go rl.node.events.OnOrphaned()
			}
			return
		}

		// Backoff before next round.
		delay := rl.backoff(round - 1)
		slog.Debug("api: reconnect backoff", "delay", delay, "round", round)

		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}
