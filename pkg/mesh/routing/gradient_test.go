package routing

import (
	"context"
	"sync"
	"testing"
	"time"
)

// --- NewGradientTable ---

func TestNewGradientTable(t *testing.T) {
	t.Parallel()

	gt := NewGradientTable("node-1")
	if gt == nil {
		t.Fatal("NewGradientTable returned nil")
	}
}

// --- Route entry management ---

func TestGradientTable_UpdateRoute(t *testing.T) {
	t.Parallel()

	gt := NewGradientTable("node-1")
	gt.UpdateRoute("node-target", "node-neighbor", 15.0)

	cost, ok := gt.BestRoute("node-target")
	if !ok {
		t.Fatal("route should exist after UpdateRoute")
	}
	if cost.NextHop != "node-neighbor" {
		t.Fatalf("NextHop = %q, want %q", cost.NextHop, "node-neighbor")
	}
	if cost.TotalCost != 15.0 {
		t.Fatalf("TotalCost = %f, want 15.0", cost.TotalCost)
	}
}

func TestGradientTable_UpdateRoute_PicksLowestCost(t *testing.T) {
	t.Parallel()

	gt := NewGradientTable("node-1")
	gt.UpdateRoute("node-target", "node-expensive", 50.0)
	gt.UpdateRoute("node-target", "node-cheap", 10.0)

	cost, ok := gt.BestRoute("node-target")
	if !ok {
		t.Fatal("route should exist")
	}
	if cost.NextHop != "node-cheap" {
		t.Fatalf("should pick lowest cost route: NextHop = %q, want %q", cost.NextHop, "node-cheap")
	}
}

func TestGradientTable_BestRoute_NotFound(t *testing.T) {
	t.Parallel()

	gt := NewGradientTable("node-1")
	_, ok := gt.BestRoute("nonexistent")
	if ok {
		t.Fatal("should return false for unknown target")
	}
}

// --- Process gossip vector ---

func TestGradientTable_ProcessGossip(t *testing.T) {
	t.Parallel()

	gt := NewGradientTable("node-1")

	// Neighbor "node-2" with impedance 5.0 knows about "node-3" at cost 10.0
	gossip := GossipVector{
		FromNodeID:    "node-2",
		FromImpedance: 5.0,
		Routes: []GossipRoute{
			{TargetID: "node-3", Cost: 10.0},
			{TargetID: "node-4", Cost: 20.0},
		},
	}

	gt.ProcessGossip(gossip)

	// Route to node-3 through node-2: cost = 10.0 + 5.0 (neighbor impedance) = 15.0
	cost, ok := gt.BestRoute("node-3")
	if !ok {
		t.Fatal("node-3 should be reachable after gossip")
	}
	if cost.NextHop != "node-2" {
		t.Fatalf("NextHop = %q, want %q", cost.NextHop, "node-2")
	}
	expectedCost := 10.0 + 5.0
	if cost.TotalCost != expectedCost {
		t.Fatalf("TotalCost = %f, want %f", cost.TotalCost, expectedCost)
	}
}

func TestGradientTable_ProcessGossip_SkipsSelf(t *testing.T) {
	t.Parallel()

	gt := NewGradientTable("node-1")

	// Gossip includes a route back to ourselves — should be ignored.
	gossip := GossipVector{
		FromNodeID:    "node-2",
		FromImpedance: 3.0,
		Routes: []GossipRoute{
			{TargetID: "node-1", Cost: 5.0}, // route to self — skip
			{TargetID: "node-3", Cost: 10.0},
		},
	}

	gt.ProcessGossip(gossip)

	_, ok := gt.BestRoute("node-1")
	if ok {
		t.Fatal("should not create route to self")
	}

	_, ok = gt.BestRoute("node-3")
	if !ok {
		t.Fatal("node-3 should be reachable")
	}
}

// --- Generate gossip vector ---

func TestGradientTable_GenerateGossip(t *testing.T) {
	t.Parallel()

	gt := NewGradientTable("node-1")
	gt.UpdateRoute("node-3", "node-2", 15.0)
	gt.UpdateRoute("node-4", "node-2", 25.0)

	gossip := gt.GenerateGossip(5.0, GossipInput{}) // our impedance is 5.0
	if gossip.FromNodeID != "node-1" {
		t.Fatalf("FromNodeID = %q, want %q", gossip.FromNodeID, "node-1")
	}
	if gossip.FromImpedance != 5.0 {
		t.Fatalf("FromImpedance = %f, want 5.0", gossip.FromImpedance)
	}
	if len(gossip.Routes) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(gossip.Routes))
	}
}

func TestGradientTable_GenerateGossip_IncludesCapabilities(t *testing.T) {
	t.Parallel()

	gt := NewGradientTable("node-1")
	gt.UpdateRoute("node-3", "node-2", 15.0)

	input := GossipInput{
		LocalCapabilities: []string{"tool:hello", "tool:system_info"},
		KnownCapabilities: map[string][]string{
			"node-2": {"tool:hello", "tool:disk_info"},
			"node-3": {"tool:sfa_logs"},
		},
	}

	gossip := gt.GenerateGossip(5.0, input)

	// FromCapabilities should be our own.
	if len(gossip.FromCapabilities) != 2 {
		t.Fatalf("expected 2 from_capabilities, got %d", len(gossip.FromCapabilities))
	}

	// NodeCapabilities should contain node-2 and node-3's caps.
	// (node-1 = self, excluded from node_capabilities).
	if len(gossip.NodeCapabilities) != 2 {
		t.Fatalf("expected 2 node_capabilities entries, got %d", len(gossip.NodeCapabilities))
	}
	if _, ok := gossip.NodeCapabilities["node-2"]; !ok {
		t.Fatal("node-2 should be in node_capabilities")
	}
	if _, ok := gossip.NodeCapabilities["node-3"]; !ok {
		t.Fatal("node-3 should be in node_capabilities")
	}
}

func TestGradientTable_GenerateGossip_ExcludesSelfFromNodeCaps(t *testing.T) {
	t.Parallel()

	gt := NewGradientTable("node-1")

	input := GossipInput{
		LocalCapabilities: []string{"tool:hello"},
		KnownCapabilities: map[string][]string{
			"node-1": {"tool:hello"}, // self — should be excluded
			"node-2": {"tool:disk_info"},
		},
	}

	gossip := gt.GenerateGossip(5.0, input)
	if _, ok := gossip.NodeCapabilities["node-1"]; ok {
		t.Fatal("self should be excluded from node_capabilities")
	}
	if len(gossip.NodeCapabilities) != 1 {
		t.Fatalf("expected 1 node_capabilities entry, got %d", len(gossip.NodeCapabilities))
	}
}

func TestGradientTable_GenerateGossip_EmptyCapabilities(t *testing.T) {
	t.Parallel()

	gt := NewGradientTable("node-1")

	gossip := gt.GenerateGossip(5.0, GossipInput{})

	if gossip.FromCapabilities != nil {
		t.Fatalf("expected nil FromCapabilities, got %v", gossip.FromCapabilities)
	}
	if gossip.NodeCapabilities != nil {
		t.Fatalf("expected nil NodeCapabilities, got %v", gossip.NodeCapabilities)
	}
}

// --- Direct neighbor registration ---

func TestGradientTable_AddDirectNeighbor(t *testing.T) {
	t.Parallel()

	gt := NewGradientTable("node-1")
	gt.AddDirectNeighbor("node-2", 3.0)

	cost, ok := gt.BestRoute("node-2")
	if !ok {
		t.Fatal("direct neighbor should be routable")
	}
	if cost.NextHop != "node-2" {
		t.Fatalf("NextHop = %q, want %q", cost.NextHop, "node-2")
	}
	if cost.TotalCost != 3.0 {
		t.Fatalf("TotalCost = %f, want 3.0 (neighbor's impedance)", cost.TotalCost)
	}
}

// --- Remove routes through a neighbor ---

func TestGradientTable_RemoveNeighbor(t *testing.T) {
	t.Parallel()

	gt := NewGradientTable("node-1")
	gt.UpdateRoute("node-3", "node-2", 15.0)
	gt.UpdateRoute("node-4", "node-2", 25.0)
	gt.UpdateRoute("node-5", "node-6", 10.0) // via different neighbor

	gt.RemoveNeighbor("node-2")

	_, ok := gt.BestRoute("node-3")
	if ok {
		t.Fatal("routes through removed neighbor should be gone")
	}
	_, ok = gt.BestRoute("node-4")
	if ok {
		t.Fatal("routes through removed neighbor should be gone")
	}
	// Route via node-6 should survive.
	_, ok = gt.BestRoute("node-5")
	if !ok {
		t.Fatal("routes through other neighbors should survive")
	}
}

// --- AllRoutes ---

func TestGradientTable_AllRoutes(t *testing.T) {
	t.Parallel()

	gt := NewGradientTable("node-1")
	gt.UpdateRoute("node-3", "node-2", 15.0)
	gt.UpdateRoute("node-4", "node-2", 25.0)

	routes := gt.AllRoutes()
	if len(routes) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(routes))
	}
}

// --- Goroutine safety ---

func TestGradientTable_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	gt := NewGradientTable("node-1")

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(3)
		go func(id int) {
			defer wg.Done()
			gt.UpdateRoute("node-target", "node-neighbor", float64(id))
		}(i)
		go func() {
			defer wg.Done()
			gt.BestRoute("node-target")
		}()
		go func() {
			defer wg.Done()
			gt.GenerateGossip(5.0, GossipInput{})
		}()
	}
	wg.Wait()
}

// --- Periodic gossip ticker ---

func TestGradientTable_StartGossip(t *testing.T) {
	t.Parallel()

	gt := NewGradientTable("node-1")
	gt.UpdateRoute("node-3", "node-2", 15.0)

	var mu sync.Mutex
	var gossips []GossipVector

	ctx, cancel := context.WithCancel(context.Background())
	gt.StartGossip(ctx, 10*time.Millisecond, 5.0, func() GossipInput {
		return GossipInput{}
	}, func(g GossipVector) {
		mu.Lock()
		gossips = append(gossips, g)
		mu.Unlock()
	})

	time.Sleep(80 * time.Millisecond)
	cancel()

	mu.Lock()
	count := len(gossips)
	mu.Unlock()

	if count < 2 {
		t.Fatalf("expected at least 2 gossip ticks, got %d", count)
	}
}

// --- Route TTL: PurgeStale ---

func TestGradientTable_PurgeStale_RemovesOld(t *testing.T) {
	t.Parallel()

	gt := NewGradientTable("node-1")
	gt.UpdateRoute("node-target", "node-2", 15.0)

	// Verify route exists.
	_, ok := gt.BestRoute("node-target")
	if !ok {
		t.Fatal("route should exist before purge")
	}

	// Wait for the route to become stale.
	time.Sleep(20 * time.Millisecond)

	// Purge with a very short maxAge — route should be removed.
	purged := gt.PurgeStale(10 * time.Millisecond)
	if purged != 1 {
		t.Fatalf("expected 1 purged entry, got %d", purged)
	}

	_, ok = gt.BestRoute("node-target")
	if ok {
		t.Fatal("stale route should have been purged")
	}
}

func TestGradientTable_PurgeStale_KeepsFresh(t *testing.T) {
	t.Parallel()

	gt := NewGradientTable("node-1")
	gt.UpdateRoute("node-target", "node-2", 15.0)

	// Purge with a generous maxAge — route should survive.
	purged := gt.PurgeStale(10 * time.Second)
	if purged != 0 {
		t.Fatalf("expected 0 purged entries, got %d", purged)
	}

	_, ok := gt.BestRoute("node-target")
	if !ok {
		t.Fatal("fresh route should survive purge")
	}
}

func TestGradientTable_PurgeStale_UpdateRefreshesTimestamp(t *testing.T) {
	t.Parallel()

	gt := NewGradientTable("node-1")
	gt.UpdateRoute("node-target", "node-2", 15.0)

	// Wait a bit, then refresh the route.
	time.Sleep(15 * time.Millisecond)
	gt.UpdateRoute("node-target", "node-2", 12.0) // refresh

	// Purge with maxAge between the original and the refresh.
	// The route was refreshed so it should survive.
	time.Sleep(5 * time.Millisecond)
	purged := gt.PurgeStale(15 * time.Millisecond)
	if purged != 0 {
		t.Fatalf("refreshed route should survive purge, got %d purged", purged)
	}
}

func TestGradientTable_PurgeStale_PartialPurge(t *testing.T) {
	t.Parallel()

	gt := NewGradientTable("node-1")

	// Add two paths to same target via different neighbors.
	gt.UpdateRoute("node-target", "node-old", 50.0)
	gt.UpdateRoute("node-target", "node-new", 10.0)

	// Backdate the old path's lastSeen to make it stale.
	// This tests the purge logic directly without relying on time.Sleep.
	gt.mu.Lock()
	paths := gt.routes["node-target"]
	for i := range paths {
		if paths[i].nextHop == "node-old" {
			paths[i].lastSeen = time.Now().Add(-time.Second)
		}
	}
	gt.mu.Unlock()

	// Purge with 100ms threshold: old path (1s ago) goes, fresh path stays.
	purged := gt.PurgeStale(100 * time.Millisecond)
	if purged != 1 {
		t.Fatalf("expected 1 stale path purged, got %d", purged)
	}

	// Target should still be routable via the fresh path.
	route, ok := gt.BestRoute("node-target")
	if !ok {
		t.Fatal("target should still be routable via fresh path")
	}
	if route.NextHop != "node-new" {
		t.Fatalf("NextHop = %q, want %q", route.NextHop, "node-new")
	}
}

func TestGradientTable_PurgeStale_Concurrent(t *testing.T) {
	t.Parallel()

	gt := NewGradientTable("node-1")
	for i := 0; i < 50; i++ {
		gt.UpdateRoute("node-target", "node-neighbor", float64(i))
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			gt.PurgeStale(1 * time.Hour) // nothing to purge
		}()
		go func(id int) {
			defer wg.Done()
			gt.UpdateRoute("node-target", "node-neighbor", float64(id))
		}(i)
	}
	wg.Wait()
}

func TestGradientTable_PurgeStale_EmptyTable(t *testing.T) {
	t.Parallel()

	gt := NewGradientTable("node-1")
	purged := gt.PurgeStale(10 * time.Millisecond)
	if purged != 0 {
		t.Fatalf("expected 0 purged from empty table, got %d", purged)
	}
}
