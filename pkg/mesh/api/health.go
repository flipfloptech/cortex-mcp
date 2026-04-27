package api

import (
	"log/slog"
	"time"
)

// NodeEvents defines lifecycle callbacks for mesh state transitions.
// All callbacks are optional. All are dispatched on a fresh goroutine
// to avoid blocking library internals (peer death cleanup, reconnect
// loop, etc.). Handlers may safely perform blocking work.
type NodeEvents struct {
	// OnPeerJoined is called when a new peer completes the membrane
	// handshake and is added to the mesh.
	OnPeerJoined func(nodeID string)

	// OnPeerLost is called when a peer's connection dies.
	// The peer has already been removed from the peer manager
	// and gradient table when this fires.
	OnPeerLost func(nodeID string)

	// OnIsolated is called when the last peer dies and the node
	// has zero connections. The reconnect loop (if enabled) will
	// start automatically after this fires.
	OnIsolated func()

	// OnReconnected is called when the node recovers from an
	// isolated state by successfully connecting to a peer.
	OnReconnected func(nodeID string)

	// OnOrphaned is called when the reconnect loop exhausts all
	// attempts and the node cannot reach any known host.
	// The consumer decides what to do: exit, cleanup, retry, etc.
	OnOrphaned func()
}

// ReconnectPolicy configures automatic reconnection behavior.
// Zero values provide sensible defaults.
type ReconnectPolicy struct {
	// Enabled controls whether automatic reconnection is attempted.
	// Default: true (reconnection happens automatically).
	Enabled bool

	// InitialDelay is the delay before the first reconnection attempt.
	// Default: 1s.
	InitialDelay time.Duration

	// MaxDelay is the maximum backoff delay between attempts.
	// Default: 30s.
	MaxDelay time.Duration

	// MaxAttempts is the maximum number of reconnection rounds.
	// 0 means unlimited — keep trying until Timeout.
	// Default: 0 (unlimited).
	MaxAttempts int

	// Timeout is the total time allowed for reconnection before
	// declaring the node orphaned. 0 means no timeout.
	// Default: 5m.
	Timeout time.Duration
}

// DefaultReconnectPolicy returns the default reconnection policy.
func DefaultReconnectPolicy() ReconnectPolicy {
	return ReconnectPolicy{
		Enabled:      true,
		InitialDelay: 1 * time.Second,
		MaxDelay:     30 * time.Second,
		MaxAttempts:  0,
		Timeout:      5 * time.Minute,
	}
}

// handlePeerDeath cleans up after a peer connection dies.
// Called via peerConn.deathOnce to ensure exactly-once execution.
//
// Sequence:
//  1. Remove peer from peerManager
//  2. Remove all routes through this peer from gradient table
//  3. Stop the async control writer goroutine
//  4. Close the yamux session
//  5. Close the dedicated control connection (dual mode only)
//  6. Fire OnPeerLost callback (if set)
//  7. If no peers remain, fire OnIsolated (if set)
func (n *Node) handlePeerDeath(pc *peerConn) {
	slog.Info("api: peer died", "nodeID", pc.nodeID)

	// Signal death immediately so blocking senders (e.g. bootstrap)
	// can exit before we tear down the write infrastructure.
	pc.closeDead()
	n.peers.Remove(pc.nodeID)

	remaining := n.peers.Count()

	// 2. Purge routes through this peer.
	n.gradient.RemoveNeighbor(pc.nodeID)

	// 3. Close the yamux session FIRST (best-effort, may already be closed).
	// This MUST happen before stopWriter because the writer goroutine may
	// be blocked on a yamux stream write. Closing the session unblocks all
	// stream I/O, allowing the writer to exit and writerWg.Wait to return.
	if pc.session != nil {
		if err := pc.session.Close(); err != nil {
			slog.Debug("api: close dead peer session", "peer", pc.nodeID, "error", err)
		}
	}

	// 4. Stop the async control writer (closes control stream, drains queue).
	// Now safe — the session close above unblocked any stuck writes.
	if pc.writeCh != nil {
		pc.stopWriter()
	}

	// 5. Close the dedicated control connection in dual mode.
	// In single-connection mode, controlConn is nil (control is a yamux
	// stream already closed by the session above).
	if pc.controlConn != nil {
		if err := pc.controlConn.Close(); err != nil {
			slog.Debug("api: close dead peer control conn", "peer", pc.nodeID, "error", err)
		}
	}

	// 6. Fire OnPeerLost (async — must not block cleanup/isolation check).
	if n.events.OnPeerLost != nil {
		cb := n.events.OnPeerLost
		id := pc.nodeID
		go cb(id)
	}

	// 7. Check isolation.
	if remaining == 0 {
		slog.Warn("api: node isolated — zero peers remaining")
		if n.events.OnIsolated != nil {
			go n.events.OnIsolated()
		}
		// Start reconnect loop if enabled.
		if n.reconnect.Enabled && n.reconnLoop != nil {
			n.reconnLoop.Start(n.ctx)
		}
	}
}

// peerConnected fires the OnPeerJoined callback (async).
// Called from AddPeer after the peer is fully registered.
func (n *Node) peerConnected(nodeID string) {
	if n.events.OnPeerJoined != nil {
		cb := n.events.OnPeerJoined
		go cb(nodeID)
	}
}
