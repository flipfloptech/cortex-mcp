package routing

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// --- P1-3: CapabilityIndex garbage collection & ingest guards ---
//
// Before P1-3 the index grew without bound — any nodeID that ever
// appeared in gossip stayed forever, and a malicious peer could
// advertise millions of fake nodes / arbitrarily long capability
// strings. These tests pin the new contract:
//
//   1. Each node has a lastSeen timestamp that Update/Refresh refresh.
//   2. PurgeStale(maxAge) removes nodes whose lastSeen is older than
//      now - maxAge, and returns the count purged.
//   3. Refresh(nodeID, impedance) updates lastSeen + impedance WITHOUT
//      touching the capability list — the additive-ingest primitive
//      that P0-2's receive-side fix will use.
//   4. Update enforces per-peer byte budgets:
//        - MaxCapabilityLength truncates/rejects oversized strings.
//        - MaxCapabilitiesPerNode caps per-node capability count.
//      Both are per-peer budgets, not fleet-size caps.
//   5. All of (1)-(4) are race-free.

// --- lastSeen + PurgeStale semantics ---

func TestCapabilityIndex_PurgeStale_RemovesOld(t *testing.T) {
	t.Parallel()

	ci := NewCapabilityIndex()
	ci.Update("stale-node", 5.0, []string{"tool:hello"})

	// Backdate the entry directly — avoids time.Sleep for this assertion.
	ci.BackdateForTest("stale-node", time.Now().Add(-1*time.Second))

	purged := ci.PurgeStale(100 * time.Millisecond)
	if purged != 1 {
		t.Fatalf("expected 1 stale node purged, got %d", purged)
	}

	// Lookup should no longer find the stale node.
	entries := ci.Lookup("tool:hello")
	if len(entries) != 0 {
		t.Fatalf("stale node should be gone from Lookup, got %d entries", len(entries))
	}
	if ci.Count() != 0 {
		t.Fatalf("Count should be 0 after purge, got %d", ci.Count())
	}
}

func TestCapabilityIndex_PurgeStale_KeepsFresh(t *testing.T) {
	t.Parallel()

	ci := NewCapabilityIndex()
	ci.Update("fresh-node", 5.0, []string{"tool:hello"})

	// Purge with a generous threshold — nothing should go.
	purged := ci.PurgeStale(1 * time.Hour)
	if purged != 0 {
		t.Fatalf("fresh node should survive purge, got %d purged", purged)
	}
	if ci.Count() != 1 {
		t.Fatalf("Count should still be 1, got %d", ci.Count())
	}
}

func TestCapabilityIndex_PurgeStale_PartialPurge(t *testing.T) {
	t.Parallel()

	ci := NewCapabilityIndex()
	ci.Update("stale-1", 5.0, []string{"tool:a", "tool:shared"})
	ci.Update("stale-2", 3.0, []string{"tool:b"})
	ci.Update("fresh-1", 1.0, []string{"tool:c", "tool:shared"})

	// Backdate the two stale entries.
	past := time.Now().Add(-time.Second)
	ci.BackdateForTest("stale-1", past)
	ci.BackdateForTest("stale-2", past)

	purged := ci.PurgeStale(100 * time.Millisecond)
	if purged != 2 {
		t.Fatalf("expected 2 stale purged, got %d", purged)
	}

	// Fresh node's capabilities still reachable.
	entries := ci.Lookup("tool:c")
	if len(entries) != 1 || entries[0].NodeID != "fresh-1" {
		t.Fatalf("fresh node should be reachable, got %v", entries)
	}

	// Shared capability: only fresh-1 remains.
	entries = ci.Lookup("tool:shared")
	if len(entries) != 1 || entries[0].NodeID != "fresh-1" {
		t.Fatalf("shared cap should only list fresh-1, got %v", entries)
	}

	// Stale caps entirely gone from the reverse index.
	if got := ci.Lookup("tool:a"); len(got) != 0 {
		t.Fatalf("stale-1's tool:a should be gone, got %v", got)
	}
	if got := ci.Lookup("tool:b"); len(got) != 0 {
		t.Fatalf("stale-2's tool:b should be gone, got %v", got)
	}
}

func TestCapabilityIndex_PurgeStale_EmptyIndex(t *testing.T) {
	t.Parallel()

	ci := NewCapabilityIndex()
	purged := ci.PurgeStale(10 * time.Millisecond)
	if purged != 0 {
		t.Fatalf("empty index should purge 0, got %d", purged)
	}
}

func TestCapabilityIndex_Update_RefreshesLastSeen(t *testing.T) {
	t.Parallel()

	ci := NewCapabilityIndex()
	ci.Update("node-1", 5.0, []string{"tool:hello"})

	// Backdate to well before "now".
	ci.BackdateForTest("node-1", time.Now().Add(-time.Hour))

	// Re-Update should refresh lastSeen to now.
	ci.Update("node-1", 5.0, []string{"tool:hello"})

	// A PurgeStale with a tiny TTL must NOT remove it — its lastSeen
	// was just refreshed.
	purged := ci.PurgeStale(1 * time.Millisecond)
	// The entry's lastSeen was set by Update at real-time "now"; a
	// 1ms TTL is extremely tight, so we allow 0 or 1 depending on
	// scheduler jitter. The assertion is that Update DID refresh —
	// checked via a second, more generous purge that should definitely
	// leave the entry alone.
	_ = purged

	purged = ci.PurgeStale(1 * time.Hour)
	if purged != 0 {
		t.Fatalf("freshly-updated entry must survive a 1h purge, got %d purged", purged)
	}
	if ci.Count() != 1 {
		t.Fatalf("entry should still be present, Count=%d", ci.Count())
	}
}

// --- Refresh: additive-ingest primitive for P0-2 follow-up ---

func TestCapabilityIndex_Refresh_UpdatesImpedanceAndLastSeen(t *testing.T) {
	t.Parallel()

	ci := NewCapabilityIndex()
	ci.Update("node-1", 5.0, []string{"tool:hello"})

	// Backdate, then Refresh.
	ci.BackdateForTest("node-1", time.Now().Add(-time.Hour))
	ci.Refresh("node-1", 12.5)

	// Impedance updated.
	entries := ci.Lookup("tool:hello")
	if len(entries) != 1 {
		t.Fatalf("entry should still be present after Refresh, got %d", len(entries))
	}
	if entries[0].Impedance != 12.5 {
		t.Fatalf("impedance should be 12.5 after Refresh, got %f", entries[0].Impedance)
	}

	// lastSeen refreshed: a 1h purge leaves it alone.
	purged := ci.PurgeStale(1 * time.Hour)
	if purged != 0 {
		t.Fatalf("refreshed entry must survive 1h purge, got %d purged", purged)
	}
}

func TestCapabilityIndex_Refresh_DoesNotTouchCapabilities(t *testing.T) {
	t.Parallel()

	ci := NewCapabilityIndex()
	ci.Update("node-1", 5.0, []string{"tool:alpha", "tool:beta"})

	// Refresh — must NOT drop alpha or beta even though no cap list
	// is provided. This is the whole point of P0-2's secondary fix:
	// a gossip that doesn't mention a cap must not erase it.
	ci.Refresh("node-1", 7.0)

	entries := ci.Lookup("tool:alpha")
	if len(entries) != 1 || entries[0].NodeID != "node-1" {
		t.Fatalf("tool:alpha should still list node-1 after Refresh, got %v", entries)
	}
	entries = ci.Lookup("tool:beta")
	if len(entries) != 1 || entries[0].NodeID != "node-1" {
		t.Fatalf("tool:beta should still list node-1 after Refresh, got %v", entries)
	}
}

func TestCapabilityIndex_Refresh_UnknownNode_IsNoOp(t *testing.T) {
	t.Parallel()

	ci := NewCapabilityIndex()
	// No Update first — Refresh on an unknown node must not create
	// an entry (we have no capabilities to associate with it).
	ci.Refresh("ghost", 1.0)

	if ci.Count() != 0 {
		t.Fatalf("Refresh on unknown node must not create an entry, Count=%d", ci.Count())
	}
}

// --- Ingest guards: per-peer byte budgets ---

func TestCapabilityIndex_Update_RejectsOversizeCapability(t *testing.T) {
	t.Parallel()

	ci := NewCapabilityIndex()
	ci.SetLimits(IngestLimits{
		MaxCapabilityLength:    16,
		MaxCapabilitiesPerNode: 256,
	})

	big := strings.Repeat("x", 17) // 1 byte over limit
	ci.Update("node-1", 5.0, []string{"ok:short", big, "ok:short2"})

	// Oversize string must not appear in the index.
	entries := ci.Lookup(big)
	if len(entries) != 0 {
		t.Fatalf("oversize capability must be rejected, got %d entries", len(entries))
	}

	// In-budget entries still present.
	if got := ci.Lookup("ok:short"); len(got) != 1 {
		t.Fatalf("in-budget cap should be kept, got %d entries", len(got))
	}
	if got := ci.Lookup("ok:short2"); len(got) != 1 {
		t.Fatalf("in-budget cap should be kept, got %d entries", len(got))
	}
}

func TestCapabilityIndex_Update_EnforcesPerNodeCapCount(t *testing.T) {
	t.Parallel()

	ci := NewCapabilityIndex()
	ci.SetLimits(IngestLimits{
		MaxCapabilityLength:    256,
		MaxCapabilitiesPerNode: 4,
	})

	caps := []string{"cap:1", "cap:2", "cap:3", "cap:4", "cap:5", "cap:6"}
	ci.Update("node-1", 5.0, caps)

	snap := ci.Snapshot()
	stored := snap["node-1"]
	if len(stored) > 4 {
		t.Fatalf("per-node cap count must be ≤ 4, got %d: %v", len(stored), stored)
	}
	if len(stored) == 0 {
		t.Fatalf("at least some caps should be stored, got 0")
	}
}

func TestCapabilityIndex_Update_ZeroLimits_UsesDefaults(t *testing.T) {
	t.Parallel()

	ci := NewCapabilityIndex()
	// No SetLimits call — default policy applies.
	// Defaults must be permissive enough for normal operation, but
	// strict enough that a malicious 1MB cap doesn't land in the
	// index. Test the second half of that.
	huge := strings.Repeat("a", 64*1024) // 64 KiB — must exceed default limit
	ci.Update("node-1", 5.0, []string{"ok:normal", huge})

	if got := ci.Lookup(huge); len(got) != 0 {
		t.Fatalf("64KiB cap must be rejected under default limits, got %d entries", len(got))
	}
	if got := ci.Lookup("ok:normal"); len(got) != 1 {
		t.Fatalf("normal cap must pass default limits, got %d entries", len(got))
	}
}

// --- Fleet-scale fuzz: malicious peer advertising many fake nodes ---

func TestCapabilityIndex_PurgeStale_FleetScaleFuzz(t *testing.T) {
	t.Parallel()

	ci := NewCapabilityIndex()

	// Inject 10k fake nodes, all with the same (tiny) cap list.
	// P1-3's TTL-based GC must let them all age out cleanly.
	const n = 10000
	for i := 0; i < n; i++ {
		ci.Update(fakeID(i), float64(i%1000), []string{"tool:fake"})
	}
	if ci.Count() != n {
		t.Fatalf("expected %d entries before purge, got %d", n, ci.Count())
	}

	// Backdate them all, then purge.
	past := time.Now().Add(-time.Hour)
	for i := 0; i < n; i++ {
		ci.BackdateForTest(fakeID(i), past)
	}

	purged := ci.PurgeStale(time.Minute)
	if purged != n {
		t.Fatalf("expected %d purged, got %d", n, purged)
	}
	if ci.Count() != 0 {
		t.Fatalf("all entries should be gone, got %d", ci.Count())
	}
}

// --- Concurrency: PurgeStale races with Update / Refresh / Lookup ---

func TestCapabilityIndex_PurgeStale_Concurrent(t *testing.T) {
	t.Parallel()

	ci := NewCapabilityIndex()
	for i := 0; i < 50; i++ {
		ci.Update(fakeID(i), float64(i), []string{"tool:hello"})
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(4)
		go func(id int) {
			defer wg.Done()
			ci.Update(fakeID(id), float64(id), []string{"tool:hello"})
		}(i)
		go func(id int) {
			defer wg.Done()
			ci.Refresh(fakeID(id), float64(id))
		}(i)
		go func() {
			defer wg.Done()
			ci.PurgeStale(1 * time.Hour) // nothing to purge — just exercising the lock
		}()
		go func() {
			defer wg.Done()
			ci.Lookup("tool:hello")
		}()
	}
	wg.Wait()
}

// --- Test helpers ---

// fakeID generates a synthetic node ID for fleet-scale fuzz tests.
func fakeID(i int) string {
	return "fake-node-" + strings.Repeat("0", 6-len(itoaShort(i))) + itoaShort(i)
}

func itoaShort(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [16]byte
	n := len(buf)
	for i > 0 {
		n--
		buf[n] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[n:])
}
