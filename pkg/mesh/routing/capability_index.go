package routing

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// NodeCapEntry represents a node offering a capability with its impedance.
// Used by Lookup results, sorted by impedance (lowest first).
type NodeCapEntry struct {
	NodeID    string
	Impedance float64
}

// IngestLimits bounds per-peer ingest size. These are per-peer byte/count
// budgets — NOT fleet-size caps. A malicious or chatty peer cannot blow
// the index up with arbitrarily long capability strings or thousands of
// fake caps per node. Scaling to larger meshes is achieved by adding
// more peers, not by relaxing these limits.
type IngestLimits struct {
	// MaxCapabilityLength bounds an individual capability string in bytes.
	// Capabilities longer than this are dropped at Update time. Zero uses
	// the safe default (DefaultMaxCapabilityLength).
	MaxCapabilityLength int

	// MaxCapabilitiesPerNode bounds how many capabilities we accept from
	// a single peer per Update. Excess entries are truncated. Zero uses
	// the safe default (DefaultMaxCapabilitiesPerNode).
	MaxCapabilitiesPerNode int
}

// Defaults for IngestLimits when SetLimits is not called, or when a
// caller passes the zero value for an individual field. These are
// generous enough for normal operation (256 × 256 = 64 KiB worst-case
// per node) but strict enough that a single gossip frame cannot smuggle
// a megabyte-sized capability string into the index.
const (
	DefaultMaxCapabilityLength    = 256
	DefaultMaxCapabilitiesPerNode = 256
)

// CapabilityIndex maintains a distributed view of node capabilities.
// It is updated by gossip — capabilities propagate transitively just
// like routes: if B knows C's capabilities, B includes them in its
// gossip to A, so A learns about C without direct contact.
//
// The index supports:
//   - O(1) exact capability lookup (e.g., "tool:hello")
//   - O(N) wildcard prefix lookup (e.g., "tool:")
//   - O(1) per-node purge (when a peer dies)
//   - O(N) snapshot for gossip generation
//   - O(N) TTL-bounded garbage collection via PurgeStale
//
// CapabilityIndex is goroutine-safe — all methods can be called concurrently.
type CapabilityIndex struct {
	mu sync.RWMutex

	// byCap maps capability → nodeID → impedance.
	// Primary lookup index. O(1) for exact capability queries.
	byCap map[string]map[string]float64

	// byNs maps namespace → capability → nodeID → impedance.
	// Secondary index for O(namespace) wildcard prefix lookups.
	// The namespace is the substring before the first ':' in a
	// capability string. Capabilities without ':' go into the
	// empty-string ("") bucket.
	byNs map[string]map[string]map[string]float64

	// byNode maps nodeID → capabilities.
	// Inverse index for efficient PurgeNode and Snapshot.
	byNode map[string][]string

	// impedances maps nodeID → impedance.
	// Decoupled from capability lists — impedance changes frequently,
	// capabilities change rarely.
	impedances map[string]float64

	// lastSeen maps nodeID → wall-clock time of the last Update/Refresh.
	// PurgeStale uses this to evict entries that have aged out — the TTL
	// floor that makes additive ingest (P0-2 follow-up) safe.
	lastSeen map[string]time.Time

	// limits bounds per-peer ingest size. Defaults applied lazily on read
	// so a zero-value CapabilityIndex works correctly.
	limits IngestLimits
}

// NewCapabilityIndex creates a new, empty capability index with default
// ingest limits.
func NewCapabilityIndex() *CapabilityIndex {
	return &CapabilityIndex{
		byCap:      make(map[string]map[string]float64),
		byNs:       make(map[string]map[string]map[string]float64),
		byNode:     make(map[string][]string),
		impedances: make(map[string]float64),
		lastSeen:   make(map[string]time.Time),
	}
}

// SetLimits configures per-peer ingest budgets. Zero values in either
// field fall back to the package defaults. Safe to call concurrently
// with other methods.
func (ci *CapabilityIndex) SetLimits(l IngestLimits) {
	ci.mu.Lock()
	defer ci.mu.Unlock()
	ci.limits = l
}

// effectiveLimits returns the current limits with zero values replaced
// by their defaults. Caller must hold ci.mu (read or write).
func (ci *CapabilityIndex) effectiveLimits() IngestLimits {
	l := ci.limits
	if l.MaxCapabilityLength <= 0 {
		l.MaxCapabilityLength = DefaultMaxCapabilityLength
	}
	if l.MaxCapabilitiesPerNode <= 0 {
		l.MaxCapabilitiesPerNode = DefaultMaxCapabilitiesPerNode
	}
	return l
}

// Update sets or overwrites a node's capabilities and impedance and
// refreshes its lastSeen timestamp.
//
// If the node already exists, its old capabilities are removed first,
// then the new set is applied. This is idempotent — calling Update
// with the same data is a no-op in terms of observable state.
//
// Ingest is bounded by IngestLimits:
//   - capability strings longer than MaxCapabilityLength are dropped.
//   - at most MaxCapabilitiesPerNode capabilities are stored per node.
//
// These are per-peer byte/count budgets and do not scale with fleet size.
func (ci *CapabilityIndex) Update(nodeID string, impedance float64, caps []string) {
	ci.mu.Lock()
	defer ci.mu.Unlock()

	// Remove old capabilities for this node (if any).
	ci.purgeNodeLocked(nodeID)

	// Store impedance and refresh lastSeen.
	ci.impedances[nodeID] = impedance
	ci.lastSeen[nodeID] = time.Now()

	// Apply ingest limits.
	limits := ci.effectiveLimits()
	capsCopy := make([]string, 0, min(len(caps), limits.MaxCapabilitiesPerNode))
	for _, c := range caps {
		if len(capsCopy) >= limits.MaxCapabilitiesPerNode {
			break
		}
		if len(c) > limits.MaxCapabilityLength {
			continue // oversize — drop
		}
		capsCopy = append(capsCopy, c)
	}
	ci.byNode[nodeID] = capsCopy

	// Build forward index + namespace index.
	for _, cap := range capsCopy {
		nodes, ok := ci.byCap[cap]
		if !ok {
			nodes = make(map[string]float64)
			ci.byCap[cap] = nodes
		}
		nodes[nodeID] = impedance

		// Namespace index.
		ns := capNamespace(cap)
		nsBucket, ok := ci.byNs[ns]
		if !ok {
			nsBucket = make(map[string]map[string]float64)
			ci.byNs[ns] = nsBucket
		}
		nsNodes, ok := nsBucket[cap]
		if !ok {
			nsNodes = make(map[string]float64)
			nsBucket[cap] = nsNodes
		}
		nsNodes[nodeID] = impedance
	}
}

// Refresh updates a node's impedance and lastSeen timestamp WITHOUT
// touching its capability list. This is the additive-ingest primitive:
// when a gossip frame names a node but does not list its capabilities,
// we refresh the TTL so PurgeStale leaves the entry alone, but we do
// not drop caps we already know.
//
// No-op if the node is not in the index — we have no capabilities to
// associate with a bare nodeID, so creating an entry would be pointless.
func (ci *CapabilityIndex) Refresh(nodeID string, impedance float64) {
	ci.mu.Lock()
	defer ci.mu.Unlock()

	caps, ok := ci.byNode[nodeID]
	if !ok {
		return // unknown node — no-op
	}

	ci.impedances[nodeID] = impedance
	ci.lastSeen[nodeID] = time.Now()

	// Keep the forward index's per-cap impedance entries in sync so
	// Lookup returns the freshest impedance without a follow-up
	// UpdateImpedance call.
	for _, c := range caps {
		if nodes, ok := ci.byCap[c]; ok {
			nodes[nodeID] = impedance
		}
		// Keep namespace index in sync.
		ns := capNamespace(c)
		if nsBucket, ok := ci.byNs[ns]; ok {
			if nsNodes, ok := nsBucket[c]; ok {
				nsNodes[nodeID] = impedance
			}
		}
	}
}

// PurgeStale removes nodes whose lastSeen is older than now - maxAge.
// Returns the number of nodes evicted.
//
// Typical usage: call with NodeConfig.CapabilityStaleTTL (default
// 3 × GossipInterval) on each gossip tick. This bounds the index by
// freshness, not by entry count — the only scale-safe way to keep a
// gossiped index from growing forever.
func (ci *CapabilityIndex) PurgeStale(maxAge time.Duration) int {
	ci.mu.Lock()
	defer ci.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	purged := 0

	// Collect stale IDs first — purgeNodeLocked mutates the maps we
	// would otherwise iterate over.
	var stale []string
	for nodeID, ts := range ci.lastSeen {
		if !ts.After(cutoff) {
			stale = append(stale, nodeID)
		}
	}

	for _, nodeID := range stale {
		ci.purgeNodeLocked(nodeID)
		purged++
	}

	return purged
}

// BackdateForTest overrides a node's lastSeen timestamp. This is a
// test-only seam so tests in this package AND in higher-level packages
// (e.g. api) can exercise PurgeStale semantics without relying on
// time.Sleep. It must not be called from production code — there is no
// legitimate runtime use for rewinding a node's freshness.
func (ci *CapabilityIndex) BackdateForTest(nodeID string, t time.Time) {
	ci.mu.Lock()
	defer ci.mu.Unlock()
	if _, ok := ci.byNode[nodeID]; ok {
		ci.lastSeen[nodeID] = t
	}
}

// UpdateImpedance modifies a node's impedance without changing its
// capability set. No-op if the node is not in the index.
func (ci *CapabilityIndex) UpdateImpedance(nodeID string, impedance float64) {
	ci.mu.Lock()
	defer ci.mu.Unlock()

	caps, ok := ci.byNode[nodeID]
	if !ok {
		return // unknown node — no-op
	}

	ci.impedances[nodeID] = impedance

	// Update impedance in the forward index.
	for _, cap := range caps {
		if nodes, ok := ci.byCap[cap]; ok {
			nodes[nodeID] = impedance
		}
		// Keep namespace index in sync.
		ns := capNamespace(cap)
		if nsBucket, ok := ci.byNs[ns]; ok {
			if nsNodes, ok := nsBucket[cap]; ok {
				nsNodes[nodeID] = impedance
			}
		}
	}
}

// Lookup returns all nodes offering the given capability, sorted by
// impedance (lowest first). Returns an empty (non-nil) slice if no
// nodes match.
func (ci *CapabilityIndex) Lookup(capability string) []NodeCapEntry {
	ci.mu.RLock()
	defer ci.mu.RUnlock()

	nodes, ok := ci.byCap[capability]
	if !ok {
		return []NodeCapEntry{} // not nil — contract
	}

	entries := make([]NodeCapEntry, 0, len(nodes))
	for nodeID, impedance := range nodes {
		entries = append(entries, NodeCapEntry{
			NodeID:    nodeID,
			Impedance: impedance,
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Impedance < entries[j].Impedance
	})

	return entries
}

// LookupWildcard returns all nodes with capabilities matching the
// given prefix (e.g., "tool:" matches "tool:hello", "tool:system_info").
// Results are deduplicated by node and sorted by impedance.
//
// Performance: when the prefix contains a ':' delimiter (the common case
// for namespaced capabilities like "tool:", "role:"), the lookup uses the
// namespace-bucketed secondary index and is O(caps in that namespace),
// independent of the total capability count in the mesh.
func (ci *CapabilityIndex) LookupWildcard(prefix string) []NodeCapEntry {
	ci.mu.RLock()
	defer ci.mu.RUnlock()

	// Determine the search domain. If the prefix ends with the namespace
	// delimiter ':', we can narrow to a single namespace bucket.
	seen := make(map[string]float64)

	ns, hasNs := extractNamespacePrefix(prefix)
	if hasNs {
		// Fast path: scan only the namespace bucket.
		nsBucket, ok := ci.byNs[ns]
		if ok {
			for cap, nodes := range nsBucket {
				if strings.HasPrefix(cap, prefix) {
					for nodeID, impedance := range nodes {
						if existing, ok := seen[nodeID]; !ok || impedance < existing {
							seen[nodeID] = impedance
						}
					}
				}
			}
		}
	} else {
		// Slow path: no namespace delimiter — must scan all capabilities.
		for cap, nodes := range ci.byCap {
			if strings.HasPrefix(cap, prefix) {
				for nodeID, impedance := range nodes {
					if existing, ok := seen[nodeID]; !ok || impedance < existing {
						seen[nodeID] = impedance
					}
				}
			}
		}
	}

	entries := make([]NodeCapEntry, 0, len(seen))
	for nodeID, impedance := range seen {
		entries = append(entries, NodeCapEntry{
			NodeID:    nodeID,
			Impedance: impedance,
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Impedance < entries[j].Impedance
	})

	return entries
}

// PurgeNode removes all entries for a node from both indexes.
// No-op if the node is not in the index.
func (ci *CapabilityIndex) PurgeNode(nodeID string) {
	ci.mu.Lock()
	defer ci.mu.Unlock()
	ci.purgeNodeLocked(nodeID)
}

// purgeNodeLocked removes a node's entries. Caller must hold ci.mu.
func (ci *CapabilityIndex) purgeNodeLocked(nodeID string) {
	caps, ok := ci.byNode[nodeID]
	if !ok {
		return
	}

	// Remove from forward index and namespace index.
	for _, cap := range caps {
		if nodes, ok := ci.byCap[cap]; ok {
			delete(nodes, nodeID)
			if len(nodes) == 0 {
				delete(ci.byCap, cap)
			}
		}

		// Remove from namespace index.
		ns := capNamespace(cap)
		if nsBucket, ok := ci.byNs[ns]; ok {
			if nsNodes, ok := nsBucket[cap]; ok {
				delete(nsNodes, nodeID)
				if len(nsNodes) == 0 {
					delete(nsBucket, cap)
				}
			}
			if len(nsBucket) == 0 {
				delete(ci.byNs, ns)
			}
		}
	}

	// Remove from inverse index, impedances, and lastSeen.
	delete(ci.byNode, nodeID)
	delete(ci.impedances, nodeID)
	delete(ci.lastSeen, nodeID)
}

// Snapshot returns a copy of all known node capabilities.
// The returned map is safe to mutate — it does not reference internal state.
// Used by GenerateGossip to include capability data in gossip frames.
func (ci *CapabilityIndex) Snapshot() map[string][]string {
	ci.mu.RLock()
	defer ci.mu.RUnlock()

	snap := make(map[string][]string, len(ci.byNode))
	for nodeID, caps := range ci.byNode {
		capsCopy := make([]string, len(caps))
		copy(capsCopy, caps)
		snap[nodeID] = capsCopy
	}
	return snap
}

// Count returns the number of nodes in the index.
func (ci *CapabilityIndex) Count() int {
	ci.mu.RLock()
	defer ci.mu.RUnlock()
	return len(ci.byNode)
}

// capNamespace extracts the namespace from a capability string.
// The namespace is the substring before the first ':'.
// If no ':' is present, returns "" (empty-namespace bucket).
//
// Examples:
//
//	"tool:hello"  → "tool"
//	"role:admin"  → "role"
//	"standalone"  → ""
func capNamespace(cap string) string {
	if i := strings.IndexByte(cap, ':'); i >= 0 {
		return cap[:i]
	}
	return ""
}

// extractNamespacePrefix checks whether a wildcard prefix targets a
// single namespace (i.e., begins with "foo:" where "foo" is the
// namespace). Returns the namespace and true if the prefix contains
// a ':', allowing the caller to narrow to a single byNs bucket.
//
// Examples:
//
//	"tool:"       → ("tool", true)
//	"tool:hel"    → ("tool", true)  — further narrowed by HasPrefix in the bucket
//	"standalone"  → ("", false)     — no ':' → must scan all
func extractNamespacePrefix(prefix string) (string, bool) {
	if i := strings.IndexByte(prefix, ':'); i >= 0 {
		return prefix[:i], true
	}
	return "", false
}
