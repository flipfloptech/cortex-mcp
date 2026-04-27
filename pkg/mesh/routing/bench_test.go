package routing

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/nucleus"
)

// --- BufPool Benchmarks ---

func BenchmarkNewBufPool(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = newBufPool(10, 1024)
	}
}

func BenchmarkNewShardedBufPool(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = newShardedBufPool(10, 1024)
	}
}

func BenchmarkGet(b *testing.B) {
	pool := newBufPool(10, 1024)
	pool.put(make([]byte, 1024))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf := pool.get()
		pool.put(buf)
	}
}

func BenchmarkPut(b *testing.B) {
	pool := newBufPool(10, 1024)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pool.put(make([]byte, 1024))
	}
}

// --- Circuit Benchmarks ---

func BenchmarkNewActivityTracker(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = newActivityTracker()
	}
}

func BenchmarkTouch(b *testing.B) {
	tracker := newActivityTracker()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tracker.touch()
	}
}

func BenchmarkIdleDuration(b *testing.B) {
	tracker := newActivityTracker()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tracker.idleDuration()
	}
}

func BenchmarkStartIdleWatchdog(b *testing.B) {
	tracker := newActivityTracker()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stop := startIdleWatchdog(tracker, 1*time.Hour)
		stop()
	}
}

type dummyConn struct{}

func (d dummyConn) Read(b []byte) (n int, err error)  { return 0, io.EOF }
func (d dummyConn) Write(b []byte) (n int, err error) { return len(b), nil }
func (d dummyConn) Close() error                      { return nil }

func BenchmarkStitch(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = Stitch(dummyConn{}, dummyConn{})
	}
}

func BenchmarkStitchWithTimeout(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = StitchWithTimeout(dummyConn{}, dummyConn{}, 1*time.Hour)
	}
}

func BenchmarkCopyPooledTracked(b *testing.B) {
	tracker := newActivityTracker()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = copyPooledTracked(dummyConn{}, dummyConn{}, tracker)
	}
}

// --- Sonar Benchmarks ---

func BenchmarkWithSeenTTL(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = WithSeenTTL(5 * time.Minute)
	}
}

func BenchmarkNewSonar(b *testing.B) {
	m := nucleus.NewManifest("node-a")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewSonar(m)
	}
}

func BenchmarkAddSeenLocked(b *testing.B) {
	m := nucleus.NewManifest("node-a")
	s := NewSonar(m)
	s.mu.Lock()
	defer s.mu.Unlock()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.addSeenLocked("test-uuid")
	}
}

func BenchmarkContainsSeenLocked(b *testing.B) {
	m := nucleus.NewManifest("node-a")
	s := NewSonar(m)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addSeenLocked("test-uuid")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.containsSeenLocked("test-uuid")
	}
}

func BenchmarkSeenSetSizeForTest(b *testing.B) {
	m := nucleus.NewManifest("node-a")
	s := NewSonar(m)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.seenSetSizeForTest()
	}
}

func BenchmarkSeenTTL(b *testing.B) {
	m := nucleus.NewManifest("node-a")
	s := NewSonar(m)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.SeenTTL()
	}
}

func BenchmarkHandleWhoHas(b *testing.B) {
	m := nucleus.NewManifest("node-a")
	m.RegisterCapability("test.cap")
	s := NewSonar(m)
	frame := &WhoHasFrame{Capability: "test.cap"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.HandleWhoHas(frame)
	}
}

func BenchmarkProcessWhoHas(b *testing.B) {
	m := nucleus.NewManifest("node-a")
	s := NewSonar(m)
	frame := &WhoHasFrame{UUID: "uuid", Capability: "test"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = s.ProcessWhoHas(frame)
	}
}

func BenchmarkShouldForward(b *testing.B) {
	m := nucleus.NewManifest("node-a")
	s := NewSonar(m)
	frame := &WhoHasFrame{UUID: "uuid", OriginID: "node-b", MaxHops: 5}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.ShouldForward(frame)
	}
}

func BenchmarkCreateWhoHas(b *testing.B) {
	m := nucleus.NewManifest("node-a")
	s := NewSonar(m)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.CreateWhoHas("test.cap", 5)
	}
}

func BenchmarkStartCollection(b *testing.B) {
	m := nucleus.NewManifest("node-a")
	s := NewSonar(m)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.StartCollection("test-uuid")
	}
}

func BenchmarkStopCollection(b *testing.B) {
	m := nucleus.NewManifest("node-a")
	s := NewSonar(m)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.StopCollection("test-uuid")
	}
}

func BenchmarkDeliverResponse(b *testing.B) {
	m := nucleus.NewManifest("node-a")
	s := NewSonar(m)
	frame := IHaveFrame{UUID: "test"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.DeliverResponse(frame)
	}
}

func BenchmarkAdd(b *testing.B) {
	rc := &ResponseCollector{results: make(chan IHaveFrame, 1)}
	frame := IHaveFrame{UUID: "test"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rc.Add(frame)
	}
}

func BenchmarkCollect(b *testing.B) {
	for i := 0; i < b.N; i++ {
		rc := &ResponseCollector{results: make(chan IHaveFrame, 10)}
		for j := 0; j < 10; j++ {
			rc.results <- IHaveFrame{UUID: "test"}
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Terminate immediately to avoid 1-second timeout per iteration
		_ = rc.Collect(ctx)
	}
}

func BenchmarkGenerateUUID(b *testing.B) {
	m := nucleus.NewManifest("node-a")
	s := NewSonar(m)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.generateUUID()
	}
}

// --- Gradient Benchmarks ---

func BenchmarkNewGradientTable(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewGradientTable("node-a")
	}
}

func BenchmarkUpdateRoute(b *testing.B) {
	gt := NewGradientTable("node-a")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		gt.UpdateRoute("node-b", "node-c", 10.0)
	}
}

func BenchmarkBestRoute(b *testing.B) {
	gt := NewGradientTable("node-a")
	gt.UpdateRoute("node-b", "node-c", 10.0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = gt.BestRoute("node-b")
	}
}

func BenchmarkProcessGossip(b *testing.B) {
	gt := NewGradientTable("node-a")
	msg := GossipVector{
		FromNodeID:    "node-c",
		FromImpedance: 50.0,
		Routes: []GossipRoute{
			{TargetID: "node-b", Cost: 10.0},
		},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		gt.ProcessGossip(msg)
	}
}

func BenchmarkGenerateGossip(b *testing.B) {
	gt := NewGradientTable("node-a")
	input := GossipInput{LocalCapabilities: []string{"cap1"}}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = gt.GenerateGossip(50.0, input)
	}
}

func BenchmarkGenerateGossipBudgeted(b *testing.B) {
	gt := NewGradientTable("node-a")
	input := GossipInput{LocalCapabilities: []string{"cap1"}}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = gt.GenerateGossipBudgeted(50.0, input, 100)
	}
}

func BenchmarkAddDirectNeighbor(b *testing.B) {
	gt := NewGradientTable("node-a")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		gt.AddDirectNeighbor("node-b", 10.0)
	}
}

func BenchmarkRemoveNeighbor(b *testing.B) {
	gt := NewGradientTable("node-a")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		gt.RemoveNeighbor("node-b")
	}
}

func BenchmarkAllRoutes(b *testing.B) {
	gt := NewGradientTable("node-a")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = gt.AllRoutes()
	}
}

func BenchmarkStartGossip(b *testing.B) {
	gt := NewGradientTable("node-a")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		gt.StartGossip(ctx, 1*time.Hour, 50.0, func() GossipInput { return GossipInput{} }, func(msg GossipVector) {})
	}
}

func BenchmarkPurgeStale(b *testing.B) {
	gt := NewGradientTable("node-a")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		gt.PurgeStale(1 * time.Hour)
	}
}

func BenchmarkGossipCacheGeneration(b *testing.B) {
	gt := NewGradientTable("node-a")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = gt.gossipCacheGeneration()
	}
}

// --- CapabilityIndex Benchmarks ---

func BenchmarkCapNamespace(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = capNamespace("foo.bar")
	}
}

func BenchmarkExtractNamespacePrefix(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = extractNamespacePrefix("foo.*")
	}
}

func BenchmarkNewCapabilityIndex(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = NewCapabilityIndex()
	}
}

func BenchmarkRouteBudget(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = RouteBudget(1024)
	}
}

func BenchmarkCapHorizonRanked(b *testing.B) {
	entries := []GossipRoute{
		{TargetID: "b", Cost: 1},
		{TargetID: "c", Cost: 2},
	}
	caps := map[string][]string{"b": {"cap1"}}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = capHorizonRanked(caps, "a", entries, 1)
	}
}

func BenchmarkEffectiveLimits(b *testing.B) {
	idx := NewCapabilityIndex()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = idx.effectiveLimits()
	}
}

func BenchmarkSetLimits(b *testing.B) {
	idx := NewCapabilityIndex()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx.SetLimits(IngestLimits{})
	}
}

func BenchmarkUpdateImpedance(b *testing.B) {
	idx := NewCapabilityIndex()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx.UpdateImpedance("node-b", 50.0)
	}
}

func BenchmarkLookup(b *testing.B) {
	idx := NewCapabilityIndex()
	idx.Update("node-a", 10.0, []string{"test.cap"})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = idx.Lookup("test.cap")
	}
}

func BenchmarkLookupWildcard(b *testing.B) {
	idx := NewCapabilityIndex()
	idx.Update("node-a", 10.0, []string{"test.cap"})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = idx.LookupWildcard("test.*")
	}
}

func BenchmarkPurgeNode(b *testing.B) {
	idx := NewCapabilityIndex()
	idx.Update("node-a", 10.0, []string{"test.cap"})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx.PurgeNode("node-a")
	}
}

func BenchmarkPurgeNodeLocked(b *testing.B) {
	idx := NewCapabilityIndex()
	idx.Update("node-a", 10.0, []string{"test.cap"})
	idx.mu.Lock()
	defer idx.mu.Unlock()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx.purgeNodeLocked("node-a")
	}
}

func BenchmarkSnapshot(b *testing.B) {
	idx := NewCapabilityIndex()
	idx.Update("node-a", 10.0, []string{"test.cap"})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = idx.Snapshot()
	}
}

func BenchmarkCount(b *testing.B) {
	idx := NewCapabilityIndex()
	idx.Update("node-a", 10.0, []string{"test.cap"})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = idx.Count()
	}
}

func BenchmarkUpdate(b *testing.B) {
	idx := NewCapabilityIndex()
	caps := []string{"cap1", "cap2"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx.Update("node-a", 10.0, caps)
	}
}

func BenchmarkRefresh(b *testing.B) {
	idx := NewCapabilityIndex()
	idx.Update("node-a", 10.0, []string{"test.cap"})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx.Refresh("node-a", 10.0)
	}
}

func BenchmarkBackdateForTest(b *testing.B) {
	idx := NewCapabilityIndex()
	idx.Update("node-a", 10.0, []string{"test.cap"})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx.BackdateForTest("node-a", time.Now().Add(-1*time.Hour))
	}
}
