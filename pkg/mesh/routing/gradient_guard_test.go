package routing

import (
	"sort"
	"sync"
	"testing"
)

// defaultHorizon is the route horizon for the default byte budget.
// Used by guard tests to validate horizon behavior.
var defaultHorizon = RouteBudget(DefaultGossipFrameBudget)

// --- C-2: Gossip Horizon — bound gossip vector to budget-derived limit ---

func TestGenerateGossip_HorizonCapsRoutes(t *testing.T) {
	t.Parallel()
	gt := NewGradientTable("self")

	// Insert routes well above the default horizon.
	for i := 0; i < defaultHorizon+1000; i++ {
		cost := float64(i + 1)
		gt.UpdateRoute(string(rune('A'+i)), "neighbor-1", cost)
	}

	gossip := gt.GenerateGossip(1.0, GossipInput{})

	if len(gossip.Routes) > defaultHorizon {
		t.Fatalf("gossip should cap at %d routes, got %d", defaultHorizon, len(gossip.Routes))
	}
}

func TestGenerateGossip_HorizonSelectsCheapest(t *testing.T) {
	t.Parallel()
	gt := NewGradientTable("self")

	// Insert more routes than the horizon to trigger truncation + sort.
	numRoutes := defaultHorizon + 100
	for i := 0; i < numRoutes; i++ {
		cost := float64(numRoutes - i) // descending: highest cost inserted first
		gt.UpdateRoute(string(rune(i)), "neighbor-1", cost)
	}

	gossip := gt.GenerateGossip(1.0, GossipInput{})

	if len(gossip.Routes) != defaultHorizon {
		t.Fatalf("expected %d routes after horizon, got %d", defaultHorizon, len(gossip.Routes))
	}

	// All routes should be sorted by cost ascending (cheapest first).
	sorted := sort.SliceIsSorted(gossip.Routes, func(i, j int) bool {
		return gossip.Routes[i].Cost < gossip.Routes[j].Cost
	})
	if !sorted {
		t.Fatal("gossip routes should be sorted by cost ascending")
	}

	// The cheapest route should be cost 1.0.
	if gossip.Routes[0].Cost != 1.0 {
		t.Fatalf("cheapest route cost = %f, want 1.0", gossip.Routes[0].Cost)
	}
}

func TestGenerateGossip_UnderHorizon(t *testing.T) {
	t.Parallel()
	gt := NewGradientTable("self")

	// Insert 10 routes — well under the horizon.
	for i := 0; i < 10; i++ {
		gt.UpdateRoute(string(rune('A'+i)), "neighbor-1", float64(i+1))
	}

	gossip := gt.GenerateGossip(1.0, GossipInput{})

	if len(gossip.Routes) != 10 {
		t.Fatalf("under-horizon: got %d routes, want 10", len(gossip.Routes))
	}
}

// --- M-4: Double-checked locking — RLock for cache hit ---

func TestGenerateGossip_CacheHitDoesNotBlock(t *testing.T) {
	t.Parallel()
	gt := NewGradientTable("self")

	gt.UpdateRoute("target-1", "neighbor-1", 5.0)

	// First call: cache miss → populates cache.
	gossip1 := gt.GenerateGossip(1.0, GossipInput{})
	gen1 := gt.gossipCacheGeneration()

	// Second call with same impedance: cache hit → should not mutate gen.
	gossip2 := gt.GenerateGossip(1.0, GossipInput{})
	gen2 := gt.gossipCacheGeneration()

	if gen1 != gen2 {
		t.Fatalf("cache hit should not increment generation: gen1=%d, gen2=%d", gen1, gen2)
	}

	if len(gossip1.Routes) != len(gossip2.Routes) {
		t.Fatalf("cache hit should return same routes: len1=%d, len2=%d",
			len(gossip1.Routes), len(gossip2.Routes))
	}
}

func TestGenerateGossip_CacheMissAfterRouteChange(t *testing.T) {
	t.Parallel()
	gt := NewGradientTable("self")

	gt.UpdateRoute("target-1", "neighbor-1", 5.0)

	// First call: populates cache.
	gt.GenerateGossip(1.0, GossipInput{})
	gen1 := gt.gossipCacheGeneration()

	// Change a route: cache becomes dirty.
	gt.UpdateRoute("target-2", "neighbor-1", 10.0)

	// Second call: cache miss → increments generation.
	gt.GenerateGossip(1.0, GossipInput{})
	gen2 := gt.gossipCacheGeneration()

	if gen2 <= gen1 {
		t.Fatalf("cache miss should increment generation: gen1=%d, gen2=%d", gen1, gen2)
	}
}

// --- P0-1: Gossip Horizon must hold across cache-hit ticks ---
//
// Regression coverage for the bug where GenerateGossip stores the
// pre-truncation route slice into gt.gossipCache and only reslices the
// local `routes` variable. The cache-miss path returns a horizon-capped
// slice, but every subsequent cache-hit tick returns the full, uncapped
// backing slice via gt.gossipCache.
//
// These tests exercise the cache-HIT path with more routes than the
// horizon. They fail against the buggy version (second call returns
// len(routes) == totalRoutes) and pass once the cache stores the
// already-capped slice.

func TestGenerateGossip_HorizonHoldsAcrossCacheHits(t *testing.T) {
	t.Parallel()
	gt := NewGradientTable("self")

	// Seed well over the horizon so the cap is meaningful.
	totalRoutes := defaultHorizon + 100
	for i := 0; i < totalRoutes; i++ {
		// Use distinct, ordered costs so the cheapest subset is stable
		// and we can assert on it.
		cost := float64(i + 1)
		gt.UpdateRoute(string(rune(i)), "neighbor-1", cost)
	}

	// First call: cache miss → recompute + cap.
	first := gt.GenerateGossip(1.0, GossipInput{})
	gen1 := gt.gossipCacheGeneration()

	if len(first.Routes) != defaultHorizon {
		t.Fatalf("cache miss: got %d routes, want %d",
			len(first.Routes), defaultHorizon)
	}

	// Second call: cache hit (no mutations between calls). Must still
	// return a horizon-capped slice — this is the bug surface.
	second := gt.GenerateGossip(1.0, GossipInput{})
	gen2 := gt.gossipCacheGeneration()

	if gen1 != gen2 {
		t.Fatalf("cache hit should not recompute: gen1=%d, gen2=%d", gen1, gen2)
	}

	if len(second.Routes) != defaultHorizon {
		t.Fatalf("cache hit returned uncapped routes: got %d, want %d (horizon breach)",
			len(second.Routes), defaultHorizon)
	}

	// Cache-hit output must match cache-miss output exactly — same
	// targets, same costs, same order. Otherwise the cap is "working"
	// but the cached content is inconsistent.
	if len(first.Routes) != len(second.Routes) {
		t.Fatalf("cache hit length drift: first=%d second=%d",
			len(first.Routes), len(second.Routes))
	}
	for i := range first.Routes {
		if first.Routes[i] != second.Routes[i] {
			t.Fatalf("cache hit content drift at idx %d: first=%+v second=%+v",
				i, first.Routes[i], second.Routes[i])
		}
	}
}

func TestGenerateGossip_HorizonStableAcrossManyCacheHits(t *testing.T) {
	t.Parallel()
	gt := NewGradientTable("self")

	totalRoutes := defaultHorizon + 50
	for i := 0; i < totalRoutes; i++ {
		gt.UpdateRoute(string(rune(i)), "neighbor-1", float64(i+1))
	}

	// Prime the cache.
	_ = gt.GenerateGossip(1.0, GossipInput{})

	// Every subsequent tick (no mutations) must stay horizon-capped.
	// This catches a regression where the cap only held for some
	// number of ticks before drifting.
	for tick := 0; tick < 20; tick++ {
		gossip := gt.GenerateGossip(1.0, GossipInput{})
		if len(gossip.Routes) != defaultHorizon {
			t.Fatalf("tick %d: cache hit returned %d routes, want %d",
				tick, len(gossip.Routes), defaultHorizon)
		}
	}
}

func TestGenerateGossip_HorizonRecomputedAfterMutation(t *testing.T) {
	t.Parallel()
	gt := NewGradientTable("self")

	// Seed over the horizon with uniformly expensive routes.
	totalRoutes := defaultHorizon + 10
	for i := 0; i < totalRoutes; i++ {
		// Costs in [100, 100+totalRoutes) — all expensive.
		gt.UpdateRoute(string(rune(i)), "neighbor-1", float64(100+i))
	}

	// Prime the cache with the initial cheapest subset.
	first := gt.GenerateGossip(1.0, GossipInput{})
	if len(first.Routes) != defaultHorizon {
		t.Fatalf("initial cache miss: got %d routes, want %d",
			len(first.Routes), defaultHorizon)
	}

	// Inject a route cheaper than anything currently emitted. It must
	// appear in the next gossip, and the output must remain capped.
	gt.UpdateRoute("cheapest", "neighbor-1", 0.5)

	second := gt.GenerateGossip(1.0, GossipInput{})

	if len(second.Routes) != defaultHorizon {
		t.Fatalf("post-mutation: got %d routes, want %d",
			len(second.Routes), defaultHorizon)
	}

	// Routes are sorted cheapest-first after the cap path.
	if second.Routes[0].TargetID != "cheapest" || second.Routes[0].Cost != 0.5 {
		t.Fatalf("cheapest route missing from post-mutation gossip: head=%+v",
			second.Routes[0])
	}
}

// --- P0-2: Deterministic capHorizon selection ---
//
// capHorizonRanked previously iterated a Go map and took the first
// defaultHorizon entries. Go randomizes map iteration order per call,
// so each gossip tick emitted a different arbitrary subset of known
// capabilities. Receivers saw capability-index churn: a node B two
// hops away would appear in some ticks and vanish in others, turning
// LookupCapability into a random oracle.
//
// The fix ranks candidates deterministically:
//   1. Nodes reachable via the cheapest known routes, in route order.
//   2. Remaining nodes (caps known, no route) filling any slack,
//      sorted by nodeID so output is stable.
//
// These tests pin the determinism contract. They currently FAIL
// against the map-iteration implementation.

func TestCapHorizon_StableAcrossCalls(t *testing.T) {
	t.Parallel()

	// Build an input with many candidates so map-iteration randomness
	// is observable. Enough entries that Go reliably shuffles them.
	caps := make(map[string][]string, 50)
	for i := 0; i < 50; i++ {
		nodeID := "node-" + string(rune('a'+(i%26))) + string(rune('a'+(i/26)))
		caps[nodeID] = []string{"tool:hello"}
	}

	// Ranked routes: a subset of caps, in a specific cost order.
	ranked := []GossipRoute{
		{TargetID: "node-aa", Cost: 1.0},
		{TargetID: "node-ba", Cost: 2.0},
		{TargetID: "node-ca", Cost: 3.0},
	}

	first := capHorizonRanked(caps, "self", ranked, 10)
	if first == nil {
		t.Fatal("capHorizonRanked returned nil for non-empty input")
	}

	// Run the selection many times with identical inputs. The returned
	// node-ID sets must be identical on every call.
	firstKeys := sortedKeys(first)
	for i := 0; i < 100; i++ {
		got := capHorizonRanked(caps, "self", ranked, 10)
		gotKeys := sortedKeys(got)
		if !equalStrings(firstKeys, gotKeys) {
			t.Fatalf("call %d: selection drifted\nfirst: %v\n  got: %v",
				i, firstKeys, gotKeys)
		}
	}
}

func TestCapHorizon_PrefersRankedRoutes(t *testing.T) {
	t.Parallel()

	caps := map[string][]string{
		"cheap-1":  {"tool:x"},
		"cheap-2":  {"tool:x"},
		"cheap-3":  {"tool:x"},
		"no-route": {"tool:x"},
		"self":     {"tool:x"}, // excluded
	}

	// Ranked routes sorted cheapest-first (same order capHorizon will
	// receive from GenerateGossip after the horizon truncation).
	ranked := []GossipRoute{
		{TargetID: "cheap-1", Cost: 1.0},
		{TargetID: "cheap-2", Cost: 2.0},
		{TargetID: "cheap-3", Cost: 3.0},
	}

	// Budget of 2: only the two cheapest ranked nodes should appear.
	got := capHorizonRanked(caps, "self", ranked, 2)

	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %d: %v", len(got), sortedKeys(got))
	}
	if _, ok := got["cheap-1"]; !ok {
		t.Error("cheap-1 (cheapest) missing")
	}
	if _, ok := got["cheap-2"]; !ok {
		t.Error("cheap-2 (second-cheapest) missing")
	}
	if _, ok := got["cheap-3"]; ok {
		t.Error("cheap-3 should be excluded by horizon budget")
	}
	if _, ok := got["no-route"]; ok {
		t.Error("no-route should not appear while ranked routes fit in budget")
	}
	if _, ok := got["self"]; ok {
		t.Error("self must always be excluded")
	}
}

func TestCapHorizon_FillsSlackDeterministically(t *testing.T) {
	t.Parallel()

	// More caps than ranked routes — slack must be filled deterministically.
	caps := map[string][]string{
		"ranked-1": {"tool:x"},
		"ranked-2": {"tool:x"},
		"extra-a":  {"tool:x"},
		"extra-b":  {"tool:x"},
		"extra-c":  {"tool:x"},
	}
	ranked := []GossipRoute{
		{TargetID: "ranked-1", Cost: 1.0},
		{TargetID: "ranked-2", Cost: 2.0},
	}

	// Budget 4: both ranked, plus 2 of the extras by deterministic order.
	got := capHorizonRanked(caps, "self", ranked, 4)

	if len(got) != 4 {
		t.Fatalf("want 4 entries, got %d: %v", len(got), sortedKeys(got))
	}
	// Ranked entries must be present.
	for _, id := range []string{"ranked-1", "ranked-2"} {
		if _, ok := got[id]; !ok {
			t.Errorf("ranked %q missing", id)
		}
	}
	// Slack is filled by sorted-by-nodeID order → extra-a, extra-b.
	if _, ok := got["extra-a"]; !ok {
		t.Error("slack fill should include extra-a (lexicographically first)")
	}
	if _, ok := got["extra-b"]; !ok {
		t.Error("slack fill should include extra-b (lexicographically second)")
	}
	if _, ok := got["extra-c"]; ok {
		t.Error("extra-c should be excluded — lexicographically last, no budget")
	}
}

func TestCapHorizon_EmptyInput(t *testing.T) {
	t.Parallel()
	if capHorizonRanked(nil, "self", nil, 10) != nil {
		t.Fatal("nil input must yield nil output")
	}
	if capHorizonRanked(map[string][]string{}, "self", nil, 10) != nil {
		t.Fatal("empty input must yield nil output")
	}
}

func TestCapHorizon_OnlySelf(t *testing.T) {
	t.Parallel()
	caps := map[string][]string{"self": {"tool:x"}}
	if got := capHorizonRanked(caps, "self", nil, 10); got != nil {
		t.Fatalf("self-only input must yield nil output, got %v", sortedKeys(got))
	}
}

func TestCapHorizon_ExcludesSelfEvenWhenRanked(t *testing.T) {
	t.Parallel()
	// A malformed/self-referential ranked entry must still not leak self.
	caps := map[string][]string{
		"self":  {"tool:x"},
		"other": {"tool:x"},
	}
	ranked := []GossipRoute{
		{TargetID: "self", Cost: 0.0},
		{TargetID: "other", Cost: 1.0},
	}
	got := capHorizonRanked(caps, "self", ranked, 10)
	if _, ok := got["self"]; ok {
		t.Fatal("self must never appear in NodeCapabilities output")
	}
	if _, ok := got["other"]; !ok {
		t.Fatal("other should be present")
	}
}

// TestGenerateGossip_CapHorizonStableAcrossTicks is an integration
// check through the full GenerateGossip path. Seeds a table with many
// known capabilities and asserts the emitted NodeCapabilities set is
// identical across 50 consecutive calls (no route mutations between
// calls). Catches wiring regressions where GenerateGossip forgets to
// pass the ranked slice through.
func TestGenerateGossip_CapHorizonStableAcrossTicks(t *testing.T) {
	t.Parallel()

	gt := NewGradientTable("self")
	// Seed a handful of routes so the ranked slice is non-trivial.
	for i := 0; i < 20; i++ {
		gt.UpdateRoute("node-"+string(rune('a'+i)), "neighbor", float64(i+1))
	}

	// Build a KnownCapabilities map larger than the route set so slack
	// fill is exercised too.
	known := make(map[string][]string, 40)
	for i := 0; i < 40; i++ {
		known["node-"+string(rune('a'+(i%26)))+string(rune('a'+(i/26)))] =
			[]string{"tool:x"}
	}

	input := GossipInput{KnownCapabilities: known}

	first := gt.GenerateGossip(1.0, input)
	firstKeys := sortedKeys(first.NodeCapabilities)

	for tick := 0; tick < 50; tick++ {
		got := gt.GenerateGossip(1.0, input)
		gotKeys := sortedKeys(got.NodeCapabilities)
		if !equalStrings(firstKeys, gotKeys) {
			t.Fatalf("tick %d: NodeCapabilities drifted\nfirst: %v\n  got: %v",
				tick, firstKeys, gotKeys)
		}
	}
}

// --- helpers ---

func sortedKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestGenerateGossip_ConcurrentWithBestRoute(t *testing.T) {
	t.Parallel()
	gt := NewGradientTable("self")

	for i := 0; i < 100; i++ {
		gt.UpdateRoute(string(rune('A'+i)), "neighbor-1", float64(i+1))
	}

	var wg sync.WaitGroup
	// Concurrent GenerateGossip (read + potential write lock).
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				gt.GenerateGossip(float64(j), GossipInput{})
			}
		}()
	}
	// Concurrent BestRoute (read lock).
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				gt.BestRoute(string(rune('A' + (id+j)%100)))
			}
		}(i)
	}
	wg.Wait()
	// Race detector will catch any issues.
}

// --- M-5: FromImpedance propagation ---

func TestProcessGossip_UsesFromImpedance(t *testing.T) {
	t.Parallel()
	gt := NewGradientTable("self")

	gossip := GossipVector{
		FromNodeID:    "neighbor-1",
		FromImpedance: 25.0, // non-trivial impedance
		Routes: []GossipRoute{
			{TargetID: "target-a", Cost: 10.0},
		},
	}

	gt.ProcessGossip(gossip)

	route, ok := gt.BestRoute("target-a")
	if !ok {
		t.Fatal("route should exist after gossip")
	}

	// Total cost = route.Cost (10.0) + FromImpedance (25.0) = 35.0
	expectedCost := 35.0
	if route.TotalCost != expectedCost {
		t.Fatalf("total cost = %f, want %f (10.0 + 25.0 impedance)", route.TotalCost, expectedCost)
	}
}

func TestProcessGossip_ZeroImpedance(t *testing.T) {
	t.Parallel()
	gt := NewGradientTable("self")

	gossip := GossipVector{
		FromNodeID:    "neighbor-1",
		FromImpedance: 0.0,
		Routes: []GossipRoute{
			{TargetID: "target-b", Cost: 5.0},
		},
	}

	gt.ProcessGossip(gossip)

	route, ok := gt.BestRoute("target-b")
	if !ok {
		t.Fatal("route should exist")
	}

	// Total cost = route.Cost (5.0) + 0.0 impedance = 5.0
	if route.TotalCost != 5.0 {
		t.Fatalf("total cost = %f, want 5.0", route.TotalCost)
	}
}
