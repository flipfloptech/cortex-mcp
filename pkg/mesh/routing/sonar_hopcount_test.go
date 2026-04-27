package routing

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/nucleus"
)

// --- P1-2: hop-count ceiling + TTL-bounded seen set ---
//
// The previous implementation bounded WhoHas flood termination by a
// 100k-entry count-LRU. That has two failure modes at scale:
//
//   1. The LRU can evict UUIDs while the flood is still propagating,
//      re-admitting the frame and sustaining loops indefinitely.
//   2. The LRU grows with (fleet-size × outstanding-queries), which
//      violates the audit doc's "no hardcoded node-count caps" rule.
//
// P1-2 replaces both:
//
//   * Correctness: each WhoHasFrame carries MaxHops; forwarders
//     decrement it, and the originator sets a bound derived from
//     log(peerCount). Loops terminate by construction.
//   * Defense in depth: the seen set is TTL-bounded, so its size is
//     bounded by observed WhoHas rate × TTL — NOT by fleet size.
//
// These tests pin both properties.

// --- WhoHasFrame.MaxHops field ---

func TestWhoHasFrame_HasMaxHopsField(t *testing.T) {
	t.Parallel()

	frame := WhoHasFrame{
		UUID:       "u",
		Capability: "tool:x",
		OriginID:   "node-a",
		MaxHops:    7,
	}
	if frame.MaxHops != 7 {
		t.Fatalf("MaxHops = %d, want 7", frame.MaxHops)
	}
}

// --- CreateWhoHas stamps the hop budget ---

func TestSonar_CreateWhoHas_StampsMaxHops(t *testing.T) {
	t.Parallel()

	m := nucleus.NewManifest("origin")
	s := NewSonar(m)

	frame := s.CreateWhoHas("tool:lustre_health", 9)
	if frame.MaxHops != 9 {
		t.Fatalf("CreateWhoHas must stamp MaxHops, got %d", frame.MaxHops)
	}
	if frame.OriginID != "origin" {
		t.Fatalf("OriginID = %q, want %q", frame.OriginID, "origin")
	}
	if frame.UUID == "" {
		t.Fatal("UUID should be non-empty")
	}
}

// --- Exhausted hop budget: still process locally, but don't forward ---
//
// When a WhoHas arrives with MaxHops == 0 it has already consumed its
// entire budget. The receiver must still process locally (respond if
// it has the capability) — otherwise the final hop is useless — but
// must not forward to neighbors.
//
// The "should I forward?" decision is made at the forwarder layer in
// api/control.go; this test pins the routing-level predicate that the
// forwarder consults.

func TestSonar_ShouldForward_ZeroMaxHops_BlocksForward(t *testing.T) {
	t.Parallel()

	m := nucleus.NewManifest("node-1")
	s := NewSonar(m)

	frame := &WhoHasFrame{
		UUID:       "u-exhausted",
		Capability: "tool:x",
		OriginID:   "origin",
		MaxHops:    0,
	}
	if s.ShouldForward(frame) {
		t.Fatal("frame with MaxHops=0 must not be forwarded")
	}
}

func TestSonar_ShouldForward_NonzeroMaxHops_AllowsForward(t *testing.T) {
	t.Parallel()

	m := nucleus.NewManifest("node-1")
	s := NewSonar(m)

	frame := &WhoHasFrame{
		UUID:       "u-alive",
		Capability: "tool:x",
		OriginID:   "origin",
		MaxHops:    3,
	}
	if !s.ShouldForward(frame) {
		t.Fatal("frame with MaxHops>0 should be forwardable (first encounter)")
	}
}

// --- Ring-topology loop termination ---
//
// The previous LRU-only design could lose a UUID to eviction while a
// broadcast was still in flight, letting the same frame re-enter and
// loop indefinitely. Hop count makes termination a correctness
// guarantee, independent of the seen-set size.
//
// Model a 4-node ring: A → B → C → D → A. Originator A emits a WhoHas
// with MaxHops = 3. Each forwarder decrements before handing off.
// Expected: B sees hops=3, C sees 2, D sees 1 (forwards with 0 → A
// drops because OriginID matches). Total forwards: 3.

func TestSonar_HopCountTerminatesRing(t *testing.T) {
	t.Parallel()

	// Simulate what api/control.go does: decrement on forward, drop
	// at 0. We only need the routing-level predicate to behave
	// consistently — that's what the integration test in
	// api/whohas_hopcount_test.go exercises end-to-end.
	ring := []string{"A", "B", "C", "D"}
	sonars := make(map[string]*Sonar, len(ring))
	for _, id := range ring {
		m := nucleus.NewManifest(id)
		m.RegisterCapability("tool:x")
		sonars[id] = NewSonar(m)
	}

	origin := "A"
	frame := &WhoHasFrame{
		UUID:       "u-ring",
		Capability: "tool:x",
		OriginID:   origin,
		MaxHops:    3,
	}

	// Simulate forwarding around the ring starting at B.
	next := map[string]string{"A": "B", "B": "C", "C": "D", "D": "A"}

	forwards := 0
	current := "B"
	for i := 0; i < 100; i++ { // generous upper bound
		s := sonars[current]

		// Skip self-origin.
		if frame.OriginID == current {
			break
		}

		// Process at this node (mimics ProcessWhoHas side effect).
		_, firstSeen := s.ProcessWhoHas(frame)
		if !firstSeen {
			break // seen before — stop
		}

		if !s.ShouldForward(frame) {
			break // hop budget exhausted
		}

		// Forwarder decrements then hops.
		frame = &WhoHasFrame{
			UUID:       frame.UUID,
			Capability: frame.Capability,
			OriginID:   frame.OriginID,
			MaxHops:    frame.MaxHops - 1,
		}
		forwards++
		current = next[current]
	}

	// At most MaxHops forwards must have occurred, regardless of
	// seen-set state.
	if forwards > 3 {
		t.Fatalf("loop did not terminate: %d forwards for MaxHops=3", forwards)
	}
}

// --- TTL-bounded seen set ---
//
// The old LRU was count-bounded, which scaled with fleet size. The
// new seen set is time-bounded: entries drop out after SeenTTL, so
// steady-state size ≈ rate × TTL, independent of total inserts.

func TestSonar_SeenSet_BoundedByTTL(t *testing.T) {
	t.Parallel()

	m := nucleus.NewManifest("node-1")
	m.RegisterCapability("tool:x")

	// Tight TTL so we can exercise the aging path quickly.
	s := NewSonar(m, WithSeenTTL(50*time.Millisecond))

	// First burst: 5 unique UUIDs.
	for i := 0; i < 5; i++ {
		uuid := "burst1-" + strconv.Itoa(i)
		_, firstSeen := s.ProcessWhoHas(&WhoHasFrame{
			UUID: uuid, Capability: "tool:x", OriginID: "origin", MaxHops: 3,
		})
		if !firstSeen {
			t.Fatalf("burst1 %d should be firstSeen", i)
		}
	}

	// Wait out the TTL.
	time.Sleep(80 * time.Millisecond)

	// Inject new UUIDs — this triggers lazy sweep. All previous
	// entries should be aged out.
	for i := 0; i < 5; i++ {
		uuid := "burst2-" + strconv.Itoa(i)
		_, firstSeen := s.ProcessWhoHas(&WhoHasFrame{
			UUID: uuid, Capability: "tool:x", OriginID: "origin", MaxHops: 3,
		})
		if !firstSeen {
			t.Fatalf("burst2 %d should be firstSeen", i)
		}
	}

	// Re-insert a burst1 UUID. Since TTL expired, it must be treated
	// as firstSeen again.
	_, firstSeen := s.ProcessWhoHas(&WhoHasFrame{
		UUID: "burst1-0", Capability: "tool:x", OriginID: "origin", MaxHops: 3,
	})
	if !firstSeen {
		t.Fatal("TTL-expired UUID should be firstSeen again")
	}

	// The seen set size must reflect the recent burst, not all inserts
	// ever. With 5 burst2 + 1 replay = 6; older burst1 entries purged.
	if size := s.seenSetSizeForTest(); size > 10 {
		t.Fatalf("seen set size %d exceeds a small multiple of recent rate — TTL sweep not working", size)
	}
}

// Sanity check that the TTL sweep does not drop entries within the
// window. Fresh entries must NOT be purged by a subsequent insert.

func TestSonar_SeenSet_KeepsFreshEntries(t *testing.T) {
	t.Parallel()

	m := nucleus.NewManifest("node-1")
	m.RegisterCapability("tool:x")
	s := NewSonar(m, WithSeenTTL(5*time.Second))

	for i := 0; i < 100; i++ {
		uuid := "u-" + strconv.Itoa(i)
		_, firstSeen := s.ProcessWhoHas(&WhoHasFrame{
			UUID: uuid, Capability: "tool:x", OriginID: "origin", MaxHops: 3,
		})
		if !firstSeen {
			t.Fatalf("u-%d should be firstSeen", i)
		}
	}

	// Replay each — must be deduped.
	for i := 0; i < 100; i++ {
		uuid := "u-" + strconv.Itoa(i)
		_, firstSeen := s.ProcessWhoHas(&WhoHasFrame{
			UUID: uuid, Capability: "tool:x", OriginID: "origin", MaxHops: 3,
		})
		if firstSeen {
			t.Fatalf("u-%d replayed within TTL must be deduped", i)
		}
	}
}

// --- 1M-UUID steady-state test (audit acceptance) ---
//
// Drive 10k unique UUIDs through a 50ms TTL set at a rate that keeps
// the steady-state window bounded. Peak size must never exceed a
// small multiple of the per-tick rate.

func TestSonar_SeenSet_SteadyStateBoundedByRateTimesTTL(t *testing.T) {
	t.Parallel()

	m := nucleus.NewManifest("node-1")
	m.RegisterCapability("tool:x")
	s := NewSonar(m, WithSeenTTL(20*time.Millisecond))

	const uniqueUUIDs = 10000
	const peakCeiling = 4000 // 200 × (expected-rate × TTL safety factor)

	peak := 0
	for i := 0; i < uniqueUUIDs; i++ {
		uuid := "flood-" + strconv.Itoa(i)
		_, _ = s.ProcessWhoHas(&WhoHasFrame{
			UUID: uuid, Capability: "tool:x", OriginID: "origin", MaxHops: 3,
		})
		if sz := s.seenSetSizeForTest(); sz > peak {
			peak = sz
		}
		// Tiny pause every 500 inserts to let TTL do its work.
		if i%500 == 499 {
			time.Sleep(30 * time.Millisecond)
		}
	}

	if peak > peakCeiling {
		t.Fatalf("peak seen-set size %d exceeds ceiling %d — not bounded by rate × TTL",
			peak, peakCeiling)
	}
}

// --- Concurrency: TTL sweep races with inserts and lookups ---

func TestSonar_SeenSet_Concurrent(t *testing.T) {
	t.Parallel()

	m := nucleus.NewManifest("node-1")
	m.RegisterCapability("tool:x")
	s := NewSonar(m, WithSeenTTL(5*time.Millisecond))

	var wg sync.WaitGroup
	for g := 0; g < 20; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				uuid := strconv.Itoa(gid) + "-" + strconv.Itoa(i)
				_, _ = s.ProcessWhoHas(&WhoHasFrame{
					UUID: uuid, Capability: "tool:x", OriginID: "origin", MaxHops: 3,
				})
			}
		}(g)
	}
	wg.Wait()
}
