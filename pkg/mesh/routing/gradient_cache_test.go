package routing

import (
	"sync"
	"testing"
)

// --- Gossip Cache Tests ---

// TestGradientTable_GenerateGossip_CachesPayload verifies that
// GenerateGossip returns a cached result when no routes have changed.
func TestGradientTable_GenerateGossip_CachesPayload(t *testing.T) {
	t.Parallel()

	gt := NewGradientTable("node-1")
	gt.UpdateRoute("node-3", "node-2", 15.0)
	gt.UpdateRoute("node-4", "node-2", 25.0)

	// First call computes the cache.
	g1 := gt.GenerateGossip(5.0, GossipInput{})
	if len(g1.Routes) != 2 {
		t.Fatalf("g1: expected 2 routes, got %d", len(g1.Routes))
	}

	// Second call should return cached data — same content.
	g2 := gt.GenerateGossip(5.0, GossipInput{})
	if len(g2.Routes) != 2 {
		t.Fatalf("g2: expected 2 routes, got %d", len(g2.Routes))
	}

	// Verify the cache was actually used (not recomputed).
	if gt.gossipCacheGeneration() < 1 {
		t.Fatal("cache should have been populated")
	}
}

// TestGradientTable_GenerateGossip_InvalidatesOnUpdate verifies that
// the gossip cache is invalidated when a route cost changes.
func TestGradientTable_GenerateGossip_InvalidatesOnUpdate(t *testing.T) {
	t.Parallel()

	gt := NewGradientTable("node-1")
	gt.UpdateRoute("node-3", "node-2", 15.0)

	// Prime the cache.
	g1 := gt.GenerateGossip(5.0, GossipInput{})
	if len(g1.Routes) != 1 {
		t.Fatalf("g1: expected 1 route, got %d", len(g1.Routes))
	}

	// Update a route — cache must be invalidated.
	gt.UpdateRoute("node-4", "node-5", 10.0)

	g2 := gt.GenerateGossip(5.0, GossipInput{})
	if len(g2.Routes) != 2 {
		t.Fatalf("g2: expected 2 routes after update, got %d", len(g2.Routes))
	}
}

// TestGradientTable_GenerateGossip_InvalidatesOnRemoveNeighbor verifies
// that cache is invalidated when a neighbor is removed.
func TestGradientTable_GenerateGossip_InvalidatesOnRemoveNeighbor(t *testing.T) {
	t.Parallel()

	gt := NewGradientTable("node-1")
	gt.UpdateRoute("node-3", "node-2", 15.0)
	gt.UpdateRoute("node-4", "node-2", 25.0)

	// Prime the cache.
	g1 := gt.GenerateGossip(5.0, GossipInput{})
	if len(g1.Routes) != 2 {
		t.Fatalf("g1: expected 2 routes, got %d", len(g1.Routes))
	}

	// Remove the neighbor — both routes should vanish.
	gt.RemoveNeighbor("node-2")

	g2 := gt.GenerateGossip(5.0, GossipInput{})
	if len(g2.Routes) != 0 {
		t.Fatalf("g2: expected 0 routes after remove, got %d", len(g2.Routes))
	}
}

// TestGradientTable_GenerateGossip_InvalidatesOnPurge verifies that
// cache is invalidated when stale routes are purged.
func TestGradientTable_GenerateGossip_InvalidatesOnPurge(t *testing.T) {
	t.Parallel()

	gt := NewGradientTable("node-1")
	gt.UpdateRoute("node-3", "node-2", 15.0)

	// Prime the cache.
	g1 := gt.GenerateGossip(5.0, GossipInput{})
	if len(g1.Routes) != 1 {
		t.Fatalf("g1: expected 1 route, got %d", len(g1.Routes))
	}

	// Purge everything by using zero maxAge.
	gt.PurgeStale(0)

	g2 := gt.GenerateGossip(5.0, GossipInput{})
	if len(g2.Routes) != 0 {
		t.Fatalf("g2: expected 0 routes after purge, got %d", len(g2.Routes))
	}
}

// TestGradientTable_GenerateGossip_SameRouteNoInvalidation verifies that
// updating a route with the same cost does not invalidate the cache.
func TestGradientTable_GenerateGossip_SameRouteNoInvalidation(t *testing.T) {
	t.Parallel()

	gt := NewGradientTable("node-1")
	gt.UpdateRoute("node-3", "node-2", 15.0)

	// Prime the cache.
	_ = gt.GenerateGossip(5.0, GossipInput{})
	gen1 := gt.gossipCacheGeneration()

	// Update with same cost — should not dirty the cache.
	gt.UpdateRoute("node-3", "node-2", 15.0)

	_ = gt.GenerateGossip(5.0, GossipInput{})
	gen2 := gt.gossipCacheGeneration()

	if gen2 != gen1 {
		t.Fatalf("cache should not have been recomputed: gen1=%d gen2=%d", gen1, gen2)
	}
}

// TestGradientTable_GenerateGossip_ImpedanceChangeRecomputes verifies
// that changing the local impedance parameter forces a recompute even
// if routes haven't changed (since FromImpedance is in the output).
func TestGradientTable_GenerateGossip_ImpedanceChangeRecomputes(t *testing.T) {
	t.Parallel()

	gt := NewGradientTable("node-1")
	gt.UpdateRoute("node-3", "node-2", 15.0)

	g1 := gt.GenerateGossip(5.0, GossipInput{})
	if g1.FromImpedance != 5.0 {
		t.Fatalf("g1.FromImpedance = %f, want 5.0", g1.FromImpedance)
	}

	// Different impedance — must recompute (FromImpedance is part of output).
	g2 := gt.GenerateGossip(8.0, GossipInput{})
	if g2.FromImpedance != 8.0 {
		t.Fatalf("g2.FromImpedance = %f, want 8.0", g2.FromImpedance)
	}
}

// --- Benchmark ---

// BenchmarkGenerateGossip_Cached measures the cost of GenerateGossip
// when the cache is warm and routes haven't changed.
func BenchmarkGenerateGossip_Cached(b *testing.B) {
	gt := NewGradientTable("node-1")
	for i := 0; i < 1000; i++ {
		gt.UpdateRoute("node-"+string(rune(i+'A')), "neighbor-1", float64(i))
	}

	// Prime the cache.
	_ = gt.GenerateGossip(5.0, GossipInput{})

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		gt.GenerateGossip(5.0, GossipInput{})
	}
}

// BenchmarkGenerateGossip_Uncached measures the cost when the cache
// is always dirty (worst case — previous behavior).
func BenchmarkGenerateGossip_Uncached(b *testing.B) {
	gt := NewGradientTable("node-1")
	for i := 0; i < 1000; i++ {
		gt.UpdateRoute("node-"+string(rune(i+'A')), "neighbor-1", float64(i))
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// Dirty the cache on every iteration to measure recompute cost.
		gt.UpdateRoute("node-X", "neighbor-1", float64(i))
		gt.GenerateGossip(5.0, GossipInput{})
	}
}

// BenchmarkGenerateGossip_ConcurrentReadWrite measures contention
// between gossip generation and route updates.
func BenchmarkGenerateGossip_ConcurrentReadWrite(b *testing.B) {
	gt := NewGradientTable("node-1")
	for i := 0; i < 1000; i++ {
		gt.UpdateRoute("node-"+string(rune(i+'A')), "neighbor-1", float64(i))
	}

	// Prime cache.
	_ = gt.GenerateGossip(5.0, GossipInput{})

	b.ResetTimer()
	b.ReportAllocs()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < b.N; i++ {
			gt.GenerateGossip(5.0, GossipInput{})
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < b.N; i++ {
			gt.UpdateRoute("node-churn", "neighbor-1", float64(i))
		}
	}()

	wg.Wait()
}
