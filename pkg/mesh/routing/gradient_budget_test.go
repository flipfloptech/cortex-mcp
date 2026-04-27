package routing

import (
	"sort"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/flipfloptech/cortex-mcp/pkg/mesh/proto"
)

// --- P1-1: Byte-budget-derived gossip horizon ---
//
// These tests replace the old maxGossipRoutes = 200 constant with a
// byte-budget derivation. The route horizon is computed from
// GossipFrameBudgetBytes at gossip-emit time, not from a hardcoded
// node count.
//
// Contract:
//   1. Default budget (DefaultGossipFrameBudget) is derived from
//      proto.PooledFrameSize (32 KiB), keeping gossip in pooled buffers.
//   2. At emit time, routeBudget = (budget - overhead) / avgRouteOnWire.
//      This scales with the operator's budget choice, not with fleet size.
//   3. When known routes exceed the budget, only the cheapest subset is
//      emitted. The gossip frame serializes to ≤ budget bytes.
//   4. capHorizon uses the same budget-derived count.
//   5. There are NO hardcoded route-count constants (maxGossipRoutes = 200
//      is deleted).

func TestGenerateGossipBudgeted_DefaultBudgetDerivesSaneLimit(t *testing.T) {
	t.Parallel()
	gt := NewGradientTable("self")

	// Insert 10k routes — far beyond any reasonable horizon.
	for i := 0; i < 10_000; i++ {
		gt.UpdateRoute(routeID(i), "neighbor-1", float64(i+1))
	}

	gossip := gt.GenerateGossipBudgeted(1.0, GossipInput{}, DefaultGossipFrameBudget)

	// With default 32 KiB budget, the horizon should be well under 10k
	// but reasonably large (hundreds, not tens).
	if len(gossip.Routes) >= 10_000 {
		t.Fatalf("budget should limit routes, got all %d", len(gossip.Routes))
	}
	if len(gossip.Routes) < 100 {
		t.Fatalf("default 32KiB budget should allow ≥100 routes, got %d", len(gossip.Routes))
	}
}

func TestGenerateGossipBudgeted_SmallBudgetLimitsRoutes(t *testing.T) {
	t.Parallel()
	gt := NewGradientTable("self")

	// Insert 10k routes.
	for i := 0; i < 10_000; i++ {
		gt.UpdateRoute(routeID(i), "neighbor-1", float64(i+1))
	}

	// 8 KiB budget — acceptance test from audit spec.
	budget := 8 * 1024
	gossip := gt.GenerateGossipBudgeted(1.0, GossipInput{}, budget)

	// Verify the emitted routes are the cheapest subset.
	if len(gossip.Routes) == 0 {
		t.Fatal("budget should allow at least some routes")
	}
	if len(gossip.Routes) >= 10_000 {
		t.Fatalf("8KiB budget must cap 10k routes, got %d", len(gossip.Routes))
	}

	// Routes must be sorted cheapest-first.
	sorted := sort.SliceIsSorted(gossip.Routes, func(i, j int) bool {
		return gossip.Routes[i].Cost < gossip.Routes[j].Cost
	})
	if !sorted {
		t.Fatal("emitted routes must be sorted by cost ascending")
	}

	// Cheapest route must be cost 1.0 (the first one we inserted).
	if gossip.Routes[0].Cost != 1.0 {
		t.Fatalf("cheapest route = %f, want 1.0", gossip.Routes[0].Cost)
	}
}

func TestGenerateGossipBudgeted_MarshaledSizeWithinBudget(t *testing.T) {
	t.Parallel()
	gt := NewGradientTable("self")

	// Insert 10k routes with realistic node IDs.
	for i := 0; i < 10_000; i++ {
		gt.UpdateRoute(routeID(i), "neighbor-1", float64(i+1))
	}

	budget := 8 * 1024
	gossip := gt.GenerateGossipBudgeted(1.0, GossipInput{}, budget)

	// Marshal to protobuf and verify the result fits within budget.
	frame := gossipVectorToProto(gossip)
	data, err := proto.Marshal(frame)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if len(data) > budget {
		t.Fatalf("marshaled gossip frame = %d bytes, exceeds budget %d",
			len(data), budget)
	}
}

func TestGenerateGossipBudgeted_UnderBudgetEmitsAll(t *testing.T) {
	t.Parallel()
	gt := NewGradientTable("self")

	// Insert 10 routes — well under any budget.
	for i := 0; i < 10; i++ {
		gt.UpdateRoute(routeID(i), "neighbor-1", float64(i+1))
	}

	gossip := gt.GenerateGossipBudgeted(1.0, GossipInput{}, DefaultGossipFrameBudget)

	if len(gossip.Routes) != 10 {
		t.Fatalf("under-budget: got %d routes, want 10", len(gossip.Routes))
	}
}

func TestGenerateGossipBudgeted_CapHorizonUsesSameBudget(t *testing.T) {
	t.Parallel()
	gt := NewGradientTable("self")

	// Insert many routes.
	for i := 0; i < 5_000; i++ {
		gt.UpdateRoute(routeID(i), "neighbor-1", float64(i+1))
	}

	// Build capability data.
	caps := make(map[string][]string, 5_000)
	for i := 0; i < 5_000; i++ {
		caps[routeID(i)] = []string{"tool:x"}
	}
	input := GossipInput{KnownCapabilities: caps}

	budget := 8 * 1024
	gossip := gt.GenerateGossipBudgeted(1.0, input, budget)

	// NodeCapabilities should be bounded to at most len(Routes).
	if len(gossip.NodeCapabilities) > len(gossip.Routes) {
		t.Fatalf("NodeCapabilities (%d) exceeds route budget (%d)",
			len(gossip.NodeCapabilities), len(gossip.Routes))
	}
}

func TestGenerateGossipBudgeted_CacheRespectsBudget(t *testing.T) {
	t.Parallel()
	gt := NewGradientTable("self")

	for i := 0; i < 10_000; i++ {
		gt.UpdateRoute(routeID(i), "neighbor-1", float64(i+1))
	}

	budget := 8 * 1024

	// First call: cache miss.
	first := gt.GenerateGossipBudgeted(1.0, GossipInput{}, budget)
	gen1 := gt.gossipCacheGeneration()

	// Second call: cache hit — must still respect budget.
	second := gt.GenerateGossipBudgeted(1.0, GossipInput{}, budget)
	gen2 := gt.gossipCacheGeneration()

	if gen1 != gen2 {
		t.Fatalf("expected cache hit: gen1=%d gen2=%d", gen1, gen2)
	}
	if len(second.Routes) != len(first.Routes) {
		t.Fatalf("cache hit length drift: first=%d second=%d",
			len(first.Routes), len(second.Routes))
	}
}

func TestDefaultGossipFrameBudget_DerivedFromPooledFrameSize(t *testing.T) {
	t.Parallel()

	// The default budget must be ≤ PooledFrameSize so gossip stays in
	// the pooled buffer path, avoiding heap allocations.
	if DefaultGossipFrameBudget > int(pb.PooledFrameSize) {
		t.Fatalf("DefaultGossipFrameBudget (%d) exceeds PooledFrameSize (%d)",
			DefaultGossipFrameBudget, pb.PooledFrameSize)
	}
	// But shouldn't be unreasonably small.
	if DefaultGossipFrameBudget < 1024 {
		t.Fatalf("DefaultGossipFrameBudget (%d) unreasonably small", DefaultGossipFrameBudget)
	}
}

func TestRouteBudget_ScalesWithByteBudget(t *testing.T) {
	t.Parallel()

	small := RouteBudget(4 * 1024)
	large := RouteBudget(32 * 1024)

	if large <= small {
		t.Fatalf("larger byte budget (%d) should yield more routes than smaller (%d): got small=%d large=%d",
			32*1024, 4*1024, small, large)
	}
	if small <= 0 {
		t.Fatalf("even small budget should allow some routes, got %d", small)
	}
}

// --- helpers ---

// routeID generates a deterministic, realistic node ID for testing.
func routeID(i int) string {
	// Use a format that's typical for HPC node names: "node-XXXXX"
	return "node-" + padInt(i, 5)
}

func padInt(n, width int) string {
	s := ""
	for i := 0; i < width; i++ {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

// gossipVectorToProto converts a GossipVector to a protobuf GossipFrame
// for marshaling size verification.
func gossipVectorToProto(g GossipVector) *pb.GossipFrame {
	routes := make(map[string]float64, len(g.Routes))
	for _, r := range g.Routes {
		routes[r.TargetID] = r.Cost
	}
	return &pb.GossipFrame{
		FromNode:         g.FromNodeID,
		FromImpedance:    g.FromImpedance,
		Routes:           routes,
		FromCapabilities: g.FromCapabilities,
	}
}
