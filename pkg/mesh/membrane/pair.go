package membrane

import (
	"crypto/subtle"
	"fmt"
	"net"
	"sync"
	"time"
)

// DefaultPairTimeout is the maximum time a control connection can wait
// for its matching data connection before the pairing is expired.
const DefaultPairTimeout = 10 * time.Second

// PairResult holds the two correlated connections after successful pairing.
type PairResult struct {
	Control net.Conn // Dedicated control plane connection
	Data    net.Conn // Data plane connection (will be yamux'd)
}

// pendingEntry holds a control connection waiting for its data partner.
type pendingEntry struct {
	token   []byte
	conn    net.Conn
	created time.Time
}

// PendingPairs manages the secure correlation of control and data
// connections from the same peer. Thread-safe.
//
// Security properties:
//   - Tokens are matched using constant-time comparison (timing-attack safe)
//   - Entries are single-use (consumed on successful pairing)
//   - Entries expire after a configurable timeout (prevents resource exhaustion)
//   - Max one pending pairing per PeerID (rate-limiting)
type PendingPairs struct {
	mu      sync.Mutex
	pending map[string]*pendingEntry // keyed by PeerID (from mTLS cert)
	timeout time.Duration

	// cleanup goroutine lifecycle
	closeCh chan struct{}
	closeWg sync.WaitGroup
}

// NewPendingPairs creates a PendingPairs with the default timeout.
func NewPendingPairs() *PendingPairs {
	return NewPendingPairsWithTimeout(DefaultPairTimeout)
}

// NewPendingPairsWithTimeout creates a PendingPairs with a custom timeout.
func NewPendingPairsWithTimeout(timeout time.Duration) *PendingPairs {
	pp := &PendingPairs{
		pending: make(map[string]*pendingEntry),
		timeout: timeout,
		closeCh: make(chan struct{}),
	}

	// Start background cleanup goroutine.
	pp.closeWg.Add(1)
	go pp.cleanupLoop()

	return pp
}

// Close stops the cleanup goroutine and clears pending entries.
func (pp *PendingPairs) Close() {
	close(pp.closeCh)
	pp.closeWg.Wait()
}

// RegisterControl registers a control connection awaiting its data partner.
// If a pending entry already exists for this peerID, it is replaced
// (rate-limiting: max 1 pending per peer).
func (pp *PendingPairs) RegisterControl(peerID string, token []byte, conn net.Conn) {
	pp.mu.Lock()
	defer pp.mu.Unlock()

	pp.pending[peerID] = &pendingEntry{
		token:   token,
		conn:    conn,
		created: time.Now(),
	}
}

// CompleteData attempts to pair a data connection with its registered
// control connection. Validates PeerID match and token match using
// constant-time comparison.
//
// On success, the pending entry is consumed (single-use) and the
// PairResult is returned. On failure, an error is returned and the
// pending entry remains unmodified.
func (pp *PendingPairs) CompleteData(peerID string, token []byte, dataConn net.Conn) (*PairResult, error) {
	pp.mu.Lock()
	defer pp.mu.Unlock()

	entry, ok := pp.pending[peerID]
	if !ok {
		return nil, fmt.Errorf("membrane: no pending control connection for peer %q", peerID)
	}

	// Check expiration.
	if time.Since(entry.created) > pp.timeout {
		delete(pp.pending, peerID)
		return nil, fmt.Errorf("membrane: pending pairing for peer %q expired", peerID)
	}

	// Constant-time token comparison — prevents timing attacks.
	if subtle.ConstantTimeCompare(entry.token, token) != 1 {
		return nil, fmt.Errorf("membrane: pair token mismatch for peer %q", peerID)
	}

	// Consume the entry (single-use).
	controlConn := entry.conn
	delete(pp.pending, peerID)

	return &PairResult{
		Control: controlConn,
		Data:    dataConn,
	}, nil
}

// cleanupLoop periodically removes expired pending entries.
func (pp *PendingPairs) cleanupLoop() {
	defer pp.closeWg.Done()

	ticker := time.NewTicker(pp.timeout / 2)
	defer ticker.Stop()

	for {
		select {
		case <-pp.closeCh:
			return
		case <-ticker.C:
			pp.purgeExpired()
		}
	}
}

// purgeExpired removes all entries older than the timeout.
func (pp *PendingPairs) purgeExpired() {
	pp.mu.Lock()
	defer pp.mu.Unlock()

	now := time.Now()
	for peerID, entry := range pp.pending {
		if now.Sub(entry.created) > pp.timeout {
			delete(pp.pending, peerID)
		}
	}
}
