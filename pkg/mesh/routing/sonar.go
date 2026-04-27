// Package routing provides discovery and forwarding for the mesh.
// Sonar handles broadcast capability discovery (WhoHas/IHave).
// Gradient handles impedance cost-vector gossip.
// Circuit handles zero-allocation stream stitching (Wave-Collapse).
package routing

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/nucleus"
)

// defaultSeenTTL bounds the WhoHas dedup set by freshness. Chosen so
// the TTL comfortably outlives a single flood traversal across a
// reasonably-sized mesh AND the single-timeout collection duration — roughly
// 3× DefaultSonarTimeout. The seen-set size is bounded by observed
// WhoHas rate × this TTL, NOT by total fleet size.
const defaultSeenTTL = 3 * time.Second

// WhoHasFrame is a broadcast discovery request sent over Stream 0.
// When a node receives a WhoHas, it checks local capabilities and
// responds with IHave if it matches. It also forwards the WhoHas
// to all neighbors (if not seen before, not self-originated, and
// MaxHops is still positive).
type WhoHasFrame struct {
	UUID       string `json:"uuid"`       // Unique ID for dedup
	Capability string `json:"capability"` // Capability being searched for
	OriginID   string `json:"origin_id"`  // NodeID of the original sender
	MaxHops    uint32 `json:"max_hops"`   // Remaining forward budget (P1-2)
}

// IHaveFrame is a unicast response to a WhoHas broadcast.
// Sent back toward the origin node when a matching capability is found.
type IHaveFrame struct {
	UUID      string  `json:"uuid"`      // Matches the WhoHas UUID
	NodeID    string  `json:"node_id"`   // NodeID of the responder
	Impedance float64 `json:"impedance"` // Current impedance of the responder
}

// Sonar handles broadcast capability discovery on the mesh.
// It processes WhoHas/IHave frames on the control plane (Stream 0),
// deduplicates broadcasts using a TTL-bounded seen set, and collects
// responses for pending queries.
type Sonar struct {
	manifest *nucleus.Manifest

	// uuidPrefix is "nodeID:" — pre-computed once at construction.
	// Combined with the atomic counter it produces collision-free
	// UUIDs without crypto/rand or heap allocations.
	uuidPrefix  string
	uuidCounter atomic.Uint64

	mu      sync.Mutex
	seen    map[string]time.Time // UUID → insertion time (TTL-bounded)
	seenTTL time.Duration

	collectorsMu sync.Mutex
	collectors   map[string]*ResponseCollector
}

// SonarOption configures a Sonar instance.
type SonarOption func(*Sonar)

// WithSeenTTL sets how long a UUID is retained in the dedup set before
// lazy eviction. The set's steady-state size is bounded by observed
// WhoHas rate × this TTL, NOT by fleet size. Pick a value large enough
// to outlive a flood traversal plus the DefaultSonarTimeout duration.
func WithSeenTTL(ttl time.Duration) SonarOption {
	return func(s *Sonar) {
		s.seenTTL = ttl
	}
}

// NewSonar creates a new Sonar instance bound to the given manifest.
func NewSonar(m *nucleus.Manifest, opts ...SonarOption) *Sonar {
	s := &Sonar{
		manifest:   m,
		uuidPrefix: m.NodeID() + ":",
		seen:       make(map[string]time.Time),
		seenTTL:    defaultSeenTTL,
		collectors: make(map[string]*ResponseCollector),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// addSeenLocked inserts uuid into the seen set with a current-time
// stamp and opportunistically sweeps expired entries. Caller must
// hold s.mu. The sweep is lazy — triggered on insert rather than by
// a goroutine — so there is no background work to stop at shutdown.
func (s *Sonar) addSeenLocked(uuid string) {
	now := time.Now()
	// Opportunistic sweep: walk the set and evict entries older than
	// now - ttl. O(N) but the set size is bounded by rate × TTL by
	// construction, so N is small in normal operation.
	cutoff := now.Add(-s.seenTTL)
	for k, t := range s.seen {
		if !t.After(cutoff) {
			delete(s.seen, k)
		}
	}
	s.seen[uuid] = now
}

// containsSeenLocked reports whether uuid is in the fresh (non-expired)
// portion of the seen set. Caller must hold s.mu. An expired entry is
// treated as absent AND evicted in place — the next insert path will
// then re-admit the UUID with a fresh timestamp.
func (s *Sonar) containsSeenLocked(uuid string) bool {
	t, ok := s.seen[uuid]
	if !ok {
		return false
	}
	if time.Since(t) > s.seenTTL {
		delete(s.seen, uuid)
		return false
	}
	return true
}

// seenSetSizeForTest returns the current size of the TTL seen set.
// Test-only accessor — there is no production use for the raw size,
// only the dedup predicate.
func (s *Sonar) seenSetSizeForTest() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.seen)
}

// SeenTTL returns the configured TTL for the UUID dedup set.
func (s *Sonar) SeenTTL() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seenTTL
}

// HandleWhoHas processes an incoming WhoHas frame.
// Returns an IHave response if this node has the requested capability.
// Returns nil if:
//   - The UUID has already been seen (dedup)
//   - This node doesn't have the capability
//
// The UUID is always recorded in the LRU cache (even if no response).
func (s *Sonar) HandleWhoHas(frame *WhoHasFrame) *IHaveFrame {
	resp, _ := s.ProcessWhoHas(frame)
	return resp
}

// ProcessWhoHas processes an incoming WhoHas frame.
// Returns the IHave response (nil if no match or duplicate) and
// whether this is the first time the UUID has been seen.
//
// This method is preferred over HandleWhoHas when the caller needs
// to know whether to forward the frame to other peers.
func (s *Sonar) ProcessWhoHas(frame *WhoHasFrame) (resp *IHaveFrame, firstSeen bool) {
	s.mu.Lock()
	seen := s.containsSeenLocked(frame.UUID)
	if !seen {
		s.addSeenLocked(frame.UUID)
	}
	s.mu.Unlock()

	if seen {
		return nil, false // duplicate
	}

	// Check local capabilities.
	if !s.manifest.HasCapability(frame.Capability) {
		return nil, true // first seen, but no match
	}

	return &IHaveFrame{
		UUID:      frame.UUID,
		NodeID:    s.manifest.NodeID(),
		Impedance: s.manifest.Impedance(),
	}, true
}

// ShouldForward reports whether a WhoHas frame should be forwarded
// to neighbors. Returns false if:
//   - The frame originated from this node.
//   - The frame's hop budget is exhausted (MaxHops == 0).
//   - The UUID has already been seen within the TTL window.
//
// Hop budget is the correctness guarantee — the seen-set check is
// defense in depth. Either alone terminates a flood; together they
// terminate it even under adversarial seen-set eviction.
func (s *Sonar) ShouldForward(frame *WhoHasFrame) bool {
	// Don't forward our own frames.
	if frame.OriginID == s.manifest.NodeID() {
		return false
	}

	// Hop budget exhausted — the local receiver still processes
	// (via ProcessWhoHas) but the frame must not be relayed.
	if frame.MaxHops == 0 {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.containsSeenLocked(frame.UUID) {
		return false
	}
	s.addSeenLocked(frame.UUID)
	return true
}

// CreateWhoHas creates a new WhoHas frame for broadcasting.
// Generates a unique UUID, sets the OriginID to this node's NodeID,
// and stamps the initial hop budget (MaxHops). Callers should derive
// the hop budget from the expected mesh diameter — grows as log(N),
// not N — so flood termination scales logarithmically with fleet size.
func (s *Sonar) CreateWhoHas(capability string, maxHops uint32) WhoHasFrame {
	return WhoHasFrame{
		UUID:       s.generateUUID(),
		Capability: capability,
		OriginID:   s.manifest.NodeID(),
		MaxHops:    maxHops,
	}
}

// StartCollection begins collecting IHave responses for a WhoHas UUID.
// Returns a ResponseCollector that aggregates incoming responses.
func (s *Sonar) StartCollection(uuid string) *ResponseCollector {
	collector := &ResponseCollector{
		uuid:    uuid,
		results: make(chan IHaveFrame, 100),
	}

	s.collectorsMu.Lock()
	s.collectors[uuid] = collector
	s.collectorsMu.Unlock()

	return collector
}

// StopCollection removes a collector from the map after collection is complete.
// This prevents the collectors map from growing unbounded over the node's lifetime.
// Safe to call multiple times or with nonexistent UUIDs.
func (s *Sonar) StopCollection(uuid string) {
	s.collectorsMu.Lock()
	delete(s.collectors, uuid)
	s.collectorsMu.Unlock()
}

// DeliverResponse routes an incoming IHave response to the appropriate collector.
// Returns false if no collector is waiting for this UUID.
func (s *Sonar) DeliverResponse(frame IHaveFrame) bool {
	s.collectorsMu.Lock()
	collector, ok := s.collectors[frame.UUID]
	s.collectorsMu.Unlock()

	if !ok {
		return false
	}

	select {
	case collector.results <- frame:
		return true
	default:
		return false // collector buffer full
	}
}

// ResponseCollector aggregates IHave responses for a single WhoHas query.
type ResponseCollector struct {
	uuid    string
	results chan IHaveFrame
}

// Add feeds an IHave response into the collector.
func (rc *ResponseCollector) Add(frame IHaveFrame) {
	select {
	case rc.results <- frame:
	default:
		// Buffer full — drop silently.
	}
}

// DefaultSonarTimeout is the overarching maximum time Collect will wait for
// responses if the caller's context does not specify a deadline.
// This guarantees that Sonar queries never block indefinitely on an offline mesh.
const DefaultSonarTimeout = 1 * time.Second

// Collect gathers IHave responses for the query until the context deadline expires.
//
// Behavior:
//   - If the provided context has no deadline, it is wrapped with DefaultSonarTimeout.
//   - Blocks until the context is canceled or its deadline expires.
//   - Returns all responses received during that window.
//
// This single-timeout approach replaces the old two-phase "wait forever for first,
// then 500ms" logic, mathematically guaranteeing termination even if no peers respond.
func (rc *ResponseCollector) Collect(ctx context.Context) []IHaveFrame {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultSonarTimeout)
		defer cancel()
	}

	var results []IHaveFrame
	for {
		select {
		case <-ctx.Done():
			return results
		case frame := <-rc.results:
			results = append(results, frame)
		}
	}
}

// generateUUID produces a unique identifier for WhoHas dedup.
//
// Format: "nodeID:counter" where counter is a monotonically-increasing
// uint64 from the Sonar instance's atomic counter. This replaces the
// previous crypto/rand approach (P2-3) and eliminates:
//   - Heap allocations on the hot broadcast path
//   - Kernel entropy dependency (P0-4 failure mode)
//   - Lock contention inside crypto/rand.Read
//
// Uniqueness properties:
//   - Within a node's lifetime: collision-free (monotonic counter)
//   - Across nodes: collision-free (nodeID prefix differs)
//   - Across process restarts: counter resets to 0, but the TTL-bounded
//     seen set (defaultSeenTTL = 3s) evicts stale entries far faster
//     than any restart cycle.
func (s *Sonar) generateUUID() string {
	seq := s.uuidCounter.Add(1)
	return s.uuidPrefix + strconv.FormatUint(seq, 36)
}
