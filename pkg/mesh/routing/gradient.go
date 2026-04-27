package routing

import (
	"context"
	"sort"
	"sync"
	"time"
)

// DefaultGossipFrameBudget is the default byte budget for a single gossip
// frame. It is set to 32 KiB — half of proto.PooledFrameSize (64 KiB) —
// so gossip comfortably stays in the pooled-buffer fast path and leaves
// headroom for protobuf framing overhead.
//
// Operators can override this via NodeConfig.GossipFrameBudgetBytes.
const DefaultGossipFrameBudget = 32 * 1024

const (
	// avgRouteOnWire is the conservative estimate of bytes per route
	// entry in a serialized GossipFrame. Derived from protobuf map
	// encoding: key (string nodeID ~10-20 bytes) + value (float64, 8
	// bytes) + protobuf tag/length overhead (~4 bytes). Using 40 as a
	// safe upper bound for typical HPC node IDs ("node-XXXXX").
	avgRouteOnWire = 40

	// gossipOverheadEstimate accounts for the fixed-size fields in a
	// GossipFrame: from_node (string), from_impedance (float64),
	// from_capabilities (repeated string), plus protobuf framing.
	gossipOverheadEstimate = 128
)

// PathCost represents the best-known path to a target node.
type PathCost struct {
	NextHop   string  // NodeID of the next hop toward the target
	TotalCost float64 // Total path cost (sum of impedances along the route)
}

// GossipRoute is a single route entry in a gossip vector.
type GossipRoute struct {
	TargetID string  `json:"target_id"` // Destination NodeID
	Cost     float64 `json:"cost"`      // Total cost from the gossip sender to the target
}

// GossipVector is the impedance cost-vector and capability data
// exchanged between neighbors on the control plane (Stream 0).
type GossipVector struct {
	FromNodeID    string        `json:"from_node_id"`   // NodeID of the gossip sender
	FromImpedance float64       `json:"from_impedance"` // Sender's current impedance
	Routes        []GossipRoute `json:"routes"`         // Sender's routing knowledge

	// FromCapabilities lists the sender's own capabilities.
	// Always included — cheapest path to learn a direct peer's tools.
	FromCapabilities []string `json:"from_capabilities,omitempty"`

	// NodeCapabilities carries capabilities of other nodes the sender
	// knows about (learned transitively from its own peers via gossip).
	// Capped to the same budget-derived horizon as routes.
	NodeCapabilities map[string][]string `json:"node_capabilities,omitempty"`
}

// GradientTable maintains the impedance-based routing table for a node.
// Routes are learned through gossip and updated continuously.
// The table always selects the lowest-cost path to each destination.
//
// GenerateGossip uses a cached payload that is invalidated only when
// routes actually change. This eliminates heavy allocation and lock
// contention on the 3-second gossip interval for large meshes.
//
// GradientTable is goroutine-safe — all methods can be called concurrently.
type GradientTable struct {
	mu     sync.RWMutex
	selfID string

	// routes maps target NodeID → list of known paths.
	// We keep multiple paths per target to support route updates —
	// when a cheaper path arrives, it replaces the expensive one.
	routes map[string][]pathEntry

	// --- Gossip cache ---
	// Pre-computed gossip payload, invalidated on route mutations.

	// gossipCache holds the pre-computed best-route slice.
	// nil when dirty (needs recompute).
	gossipCache []GossipRoute

	// gossipImpedance is the impedance used to build the cached payload.
	// If the caller passes a different impedance, the cache is stale.
	gossipImpedance float64

	// gossipBudget is the byte budget used to build the cached payload.
	// If the caller passes a different budget, the cache is stale.
	gossipBudget int

	// gossipGen counts how many times the cache has been (re)computed.
	// Used by benchmarks and tests to verify cache hits.
	gossipGen uint64

	// dirty is set to true when any route mutation occurs.
	// GenerateGossip checks this flag to decide whether to recompute.
	dirty bool
}

// pathEntry is an internal route entry with its source neighbor.
type pathEntry struct {
	nextHop   string
	totalCost float64
	lastSeen  time.Time // when this route was last updated
}

// NewGradientTable creates a new routing table for the given node.
func NewGradientTable(selfID string) *GradientTable {
	return &GradientTable{
		selfID: selfID,
		routes: make(map[string][]pathEntry),
	}
}

// UpdateRoute adds or updates a route to a target through a specific next-hop.
// If a route through this next-hop already exists, it is updated only if
// the cost actually changed. If not, a new path entry is added.
//
// No-op updates (same cost) do not invalidate the gossip cache.
func (gt *GradientTable) UpdateRoute(targetID, nextHop string, totalCost float64) {
	gt.mu.Lock()
	defer gt.mu.Unlock()

	now := time.Now()
	paths := gt.routes[targetID]

	// Update existing path through this next-hop, or add new.
	for i, p := range paths {
		if p.nextHop == nextHop {
			// Refresh timestamp always.
			paths[i].lastSeen = now
			// Only dirty the cache if cost actually changed.
			if p.totalCost != totalCost {
				paths[i].totalCost = totalCost
				gt.dirty = true
			}
			gt.routes[targetID] = paths
			return
		}
	}

	// New path entry — always dirties the cache.
	gt.routes[targetID] = append(paths, pathEntry{
		nextHop:   nextHop,
		totalCost: totalCost,
		lastSeen:  now,
	})
	gt.dirty = true
}

// BestRoute returns the lowest-cost path to a target node.
// Returns false if no route is known.
func (gt *GradientTable) BestRoute(targetID string) (PathCost, bool) {
	gt.mu.RLock()
	defer gt.mu.RUnlock()

	paths, ok := gt.routes[targetID]
	if !ok || len(paths) == 0 {
		return PathCost{}, false
	}

	best := paths[0]
	for _, p := range paths[1:] {
		if p.totalCost < best.totalCost {
			best = p
		}
	}

	return PathCost{
		NextHop:   best.nextHop,
		TotalCost: best.totalCost,
	}, true
}

// ProcessGossip ingests a gossip vector from a neighbor.
// For each route in the vector, the total cost is:
//
//	route.Cost + gossip.FromImpedance
//
// Routes pointing back to self are ignored.
func (gt *GradientTable) ProcessGossip(gossip GossipVector) {
	for _, route := range gossip.Routes {
		// Don't create routes to ourselves.
		if route.TargetID == gt.selfID {
			continue
		}

		totalCost := route.Cost + gossip.FromImpedance
		gt.UpdateRoute(route.TargetID, gossip.FromNodeID, totalCost)
	}
}

// RouteBudget computes how many route entries fit within a given byte
// budget for a gossip frame. This is a pure function — callers can use
// it to inspect the derivation without generating a gossip vector.
//
// Formula: (frameBudgetBytes - overhead) / avgRouteOnWire.
// The result is always ≥ 1 (at least one route is emitted).
func RouteBudget(frameBudgetBytes int) int {
	available := frameBudgetBytes - gossipOverheadEstimate
	if available < avgRouteOnWire {
		return 1
	}
	return available / avgRouteOnWire
}

// GossipInput provides capability data for gossip generation.
// The gradient table owns route data; capabilities come from outside.
type GossipInput struct {
	// LocalCapabilities is this node's own capability list.
	LocalCapabilities []string

	// KnownCapabilities is the full capability snapshot from the
	// CapabilityIndex (nodeID → caps). Budget-derived horizon applied.
	KnownCapabilities map[string][]string
}

// GenerateGossip creates a gossip vector using the default byte budget
// (DefaultGossipFrameBudget). This is the convenience wrapper used by
// most callers. See GenerateGossipBudgeted for the budget-aware version.
func (gt *GradientTable) GenerateGossip(localImpedance float64, input GossipInput) GossipVector {
	return gt.GenerateGossipBudgeted(localImpedance, input, DefaultGossipFrameBudget)
}

// GenerateGossipBudgeted creates a gossip vector containing this node's
// routing knowledge, impedance, and capability data. The route horizon
// is derived from frameBudgetBytes — the maximum serialized size of the
// gossip frame. Sent to all neighbors.
//
// Uses a cached payload for routes when they haven't changed and the
// budget is the same. Capabilities are always included from the
// provided GossipInput.
func (gt *GradientTable) GenerateGossipBudgeted(localImpedance float64, input GossipInput, frameBudgetBytes int) GossipVector {
	gt.mu.Lock()
	defer gt.mu.Unlock()

	horizon := RouteBudget(frameBudgetBytes)

	// Check if the route cache is valid.
	if !gt.dirty && gt.gossipCache != nil && gt.gossipImpedance == localImpedance && gt.gossipBudget == frameBudgetBytes {
		// Route cache hit — still need to include capability data.
		// gt.gossipCache is already horizon-capped and cost-ordered
		// (P0-1 fix), so it is exactly the ranked slice capHorizonRanked
		// needs to produce a stable, tick-over-tick capability subset
		// (P0-2 fix).
		return GossipVector{
			FromNodeID:       gt.selfID,
			FromImpedance:    localImpedance,
			Routes:           gt.gossipCache,
			FromCapabilities: input.LocalCapabilities,
			NodeCapabilities: capHorizonRanked(input.KnownCapabilities, gt.selfID, gt.gossipCache, horizon),
		}
	}

	// Cache miss — recompute routes.
	routes := make([]GossipRoute, 0, len(gt.routes))
	for targetID, paths := range gt.routes {
		if len(paths) == 0 {
			continue
		}
		best := paths[0]
		for _, p := range paths[1:] {
			if p.totalCost < best.totalCost {
				best = p
			}
		}
		routes = append(routes, GossipRoute{
			TargetID: targetID,
			Cost:     best.totalCost,
		})
	}

	// P1-1: Gossip Horizon — cap routes to the budget-derived count.
	//
	// The cap MUST be applied before caching. Earlier revisions stored
	// the pre-truncation slice in gt.gossipCache and only resliced the
	// local `routes` variable, which meant every subsequent cache-hit
	// tick returned the full, uncapped route table through the hit
	// branch above. Pin the length with a 3-arg slice so a future
	// append cannot grow the cached slice past the horizon.
	if len(routes) > horizon {
		sort.Slice(routes, func(i, j int) bool {
			return routes[i].Cost < routes[j].Cost
		})
		routes = routes[:horizon:horizon]
	}

	// Store the already-capped slice in the cache.
	gt.gossipCache = routes
	gt.gossipImpedance = localImpedance
	gt.gossipBudget = frameBudgetBytes
	gt.dirty = false
	gt.gossipGen++

	return GossipVector{
		FromNodeID:       gt.selfID,
		FromImpedance:    localImpedance,
		Routes:           routes,
		FromCapabilities: input.LocalCapabilities,
		NodeCapabilities: capHorizonRanked(input.KnownCapabilities, gt.selfID, routes, horizon),
	}
}

// capHorizonRanked selects a deterministic subset of known node
// capabilities for emission in a gossip frame. selfID is always
// excluded (the sender's own capabilities travel in FromCapabilities).
//
// Selection priority:
//  1. Nodes that appear in rankedRoutes, in ranked order. Callers pass
//     a cost-ordered, horizon-capped slice (either the freshly-trimmed
//     routes slice from the cache-miss path, or gt.gossipCache from the
//     cache-hit path — both are ordered cheapest-first after the P0-1
//     fix).
//  2. If budget remains after draining ranked, slack is filled by the
//     remaining known-capability nodes sorted by nodeID (lexicographic,
//     stable). This replaces Go's randomized map-iteration order so
//     output is identical across ticks for identical inputs.
//
// Returns nil when the input is empty or contains only selfID, matching
// the previous capHorizon contract.
func capHorizonRanked(caps map[string][]string, selfID string, rankedRoutes []GossipRoute, budget int) map[string][]string {
	if len(caps) == 0 || budget <= 0 {
		return nil
	}

	result := make(map[string][]string, budget)

	// 1. Drain ranked routes in order, taking only those we have
	//    capability data for (and never selfID).
	for _, r := range rankedRoutes {
		if len(result) >= budget {
			break
		}
		if r.TargetID == selfID {
			continue
		}
		c, ok := caps[r.TargetID]
		if !ok {
			continue
		}
		if _, already := result[r.TargetID]; already {
			continue
		}
		result[r.TargetID] = c
	}

	// 2. Slack fill: remaining caps not yet selected, sorted by nodeID
	//    for determinism. Skipped entirely when budget is already full.
	if len(result) < budget {
		remaining := make([]string, 0, len(caps))
		for nodeID := range caps {
			if nodeID == selfID {
				continue
			}
			if _, already := result[nodeID]; already {
				continue
			}
			remaining = append(remaining, nodeID)
		}
		sort.Strings(remaining)
		for _, nodeID := range remaining {
			if len(result) >= budget {
				break
			}
			result[nodeID] = caps[nodeID]
		}
	}

	if len(result) == 0 {
		return nil
	}
	return result
}

// AddDirectNeighbor registers a directly connected peer.
// The cost is the neighbor's impedance (no intermediate hops).
func (gt *GradientTable) AddDirectNeighbor(neighborID string, impedance float64) {
	gt.UpdateRoute(neighborID, neighborID, impedance)
}

// RemoveNeighbor purges all routes that transit through the given neighbor.
// Called when a peer disconnects.
func (gt *GradientTable) RemoveNeighbor(neighborID string) {
	gt.mu.Lock()
	defer gt.mu.Unlock()

	mutated := false
	for targetID, paths := range gt.routes {
		filtered := paths[:0]
		for _, p := range paths {
			if p.nextHop != neighborID {
				filtered = append(filtered, p)
			}
		}
		if len(filtered) != len(paths) {
			mutated = true
		}
		if len(filtered) == 0 {
			delete(gt.routes, targetID)
		} else {
			gt.routes[targetID] = filtered
		}
	}

	if mutated {
		gt.dirty = true
	}
}

// PurgeStale removes path entries that haven't been updated within maxAge.
// Returns the number of individual path entries removed.
//
// Typical usage: call with 3× the gossip interval to expire routes
// that haven't been refreshed by gossip ticks from alive neighbors.
func (gt *GradientTable) PurgeStale(maxAge time.Duration) int {
	gt.mu.Lock()
	defer gt.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	purged := 0

	for targetID, paths := range gt.routes {
		filtered := paths[:0]
		for _, p := range paths {
			if p.lastSeen.After(cutoff) {
				filtered = append(filtered, p)
			} else {
				purged++
			}
		}
		if len(filtered) == 0 {
			delete(gt.routes, targetID)
		} else {
			gt.routes[targetID] = filtered
		}
	}

	if purged > 0 {
		gt.dirty = true
	}

	return purged
}

// gossipCacheGeneration returns the number of times the gossip cache
// has been (re)computed. Used by tests to verify cache hit/miss behavior.
func (gt *GradientTable) gossipCacheGeneration() uint64 {
	gt.mu.RLock()
	defer gt.mu.RUnlock()
	return gt.gossipGen
}

// AllRoutes returns a snapshot of all known routes.
func (gt *GradientTable) AllRoutes() map[string]PathCost {
	gt.mu.RLock()
	defer gt.mu.RUnlock()

	result := make(map[string]PathCost, len(gt.routes))
	for targetID, paths := range gt.routes {
		if len(paths) == 0 {
			continue
		}
		best := paths[0]
		for _, p := range paths[1:] {
			if p.totalCost < best.totalCost {
				best = p
			}
		}
		result[targetID] = PathCost{
			NextHop:   best.nextHop,
			TotalCost: best.totalCost,
		}
	}
	return result
}

// StartGossip begins periodic gossip emission in a background goroutine.
// The emit function is called with the gossip vector every interval.
// The inputFn is called each tick to provide current capability data.
// The goroutine runs until the context is canceled.
func (gt *GradientTable) StartGossip(ctx context.Context, interval time.Duration, localImpedance float64, inputFn func() GossipInput, emit func(GossipVector)) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				emit(gt.GenerateGossip(localImpedance, inputFn()))
			}
		}
	}()
}
