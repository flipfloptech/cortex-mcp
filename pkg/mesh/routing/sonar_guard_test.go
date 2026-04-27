package routing

import (
	"testing"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/nucleus"
)

// --- C-3 removed (P1-2): the count-bounded ring-buffer LRU was replaced
// by a TTL-bounded seen set. New contract tests live in
// sonar_hopcount_test.go (TestSonar_SeenSet_*).

// --- L-2: ResponseCollector cleanup ---

func TestSonar_StopCollection_RemovesCollector(t *testing.T) {
	t.Parallel()
	m := nucleus.NewManifest("node-1")
	s := NewSonar(m)

	collector := s.StartCollection("test-uuid")
	if collector == nil {
		t.Fatal("StartCollection should return non-nil collector")
	}

	// Deliver should succeed before cleanup.
	delivered := s.DeliverResponse(IHaveFrame{
		UUID:   "test-uuid",
		NodeID: "responder",
	})
	if !delivered {
		t.Fatal("DeliverResponse should succeed before StopCollection")
	}

	// Cleanup.
	s.StopCollection("test-uuid")

	// Deliver should fail after cleanup.
	delivered = s.DeliverResponse(IHaveFrame{
		UUID:   "test-uuid",
		NodeID: "responder-2",
	})
	if delivered {
		t.Fatal("DeliverResponse should return false after StopCollection")
	}
}

func TestSonar_StopCollection_Idempotent(t *testing.T) {
	t.Parallel()
	m := nucleus.NewManifest("node-1")
	s := NewSonar(m)

	s.StartCollection("uuid-1")
	s.StopCollection("uuid-1")
	// Second stop should not panic.
	s.StopCollection("uuid-1")
}

func TestSonar_StopCollection_NonexistentUUID(t *testing.T) {
	t.Parallel()
	m := nucleus.NewManifest("node-1")
	s := NewSonar(m)

	// Should not panic for unknown UUIDs.
	s.StopCollection("nonexistent-uuid")
}

// --- P0-4 (superseded by P2-3): UUID uniqueness guarantees ---
//
// The original P0-4 tests verified the crypto/rand fallback path when
// kernel entropy was unavailable. P2-3 eliminated the entropy dependency
// entirely by switching to a nodeID:counter format. The tests below
// verify that the new implementation still satisfies the original P0-4
// contract: non-zero, unique UUIDs under all conditions.

// zeroHexUUID is the all-zero sentinel that the OLD broken implementation
// produced. The new counter-based UUID can never produce this format,
// but we keep the guard as defense in depth.
const zeroHexUUID = "00000000000000000000000000000000"

func TestGenerateUUID_NeverAllZero(t *testing.T) {
	t.Parallel()
	m := nucleus.NewManifest("node-1")
	s := NewSonar(m)

	for i := 0; i < 1000; i++ {
		got := s.generateUUID()
		if got == zeroHexUUID {
			t.Fatalf("call %d: generateUUID returned the all-zero sentinel", i)
		}
	}
}

func TestGenerateUUID_AlwaysUnique(t *testing.T) {
	t.Parallel()
	m := nucleus.NewManifest("node-1")
	s := NewSonar(m)

	const n = 10000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		got := s.generateUUID()
		if got == zeroHexUUID {
			t.Fatalf("call %d: generated the all-zero sentinel", i)
		}
		if _, dup := seen[got]; dup {
			t.Fatalf("call %d: duplicate UUID %q", i, got)
		}
		seen[got] = struct{}{}
	}
}
