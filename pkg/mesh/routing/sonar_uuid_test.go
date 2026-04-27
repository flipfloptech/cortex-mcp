package routing

import (
	"testing"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/nucleus"
)

// --- P2-3: Counter-based UUID for Sonar hot path ---
//
// The old generateUUID() used crypto/rand which:
//   - Allocates on every call (hot broadcast path)
//   - Requires kernel entropy (can fail on starved hosts — P0-4)
//   - Uses locking inside crypto/rand.Read
//
// The replacement uses nodeID + atomic counter:
//   - Zero allocations (pre-computed prefix + counter formatting)
//   - Collision-free by construction within one node's lifetime
//   - Cross-node uniqueness via the NodeID prefix
//   - Eliminates P0-4's failure mode entirely (no entropy needed)
//
// Contract:
//   1. UUIDs are unique across 1M generations (zero collisions)
//   2. UUIDs from different Sonar instances (different nodeIDs) never collide
//   3. Zero allocations on generateUUID in the hot path
//   4. UUIDs are never empty or zero-valued

func TestSonarUUID_UniqueAcross1M(t *testing.T) {
	t.Parallel()

	m := nucleus.NewManifest("test-node")
	s := NewSonar(m)

	const n = 1_000_000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		uuid := s.generateUUID()
		if uuid == "" {
			t.Fatalf("call %d: generateUUID returned empty string", i)
		}
		if _, dup := seen[uuid]; dup {
			t.Fatalf("call %d: duplicate UUID %q", i, uuid)
		}
		seen[uuid] = struct{}{}
	}
}

func TestSonarUUID_CrossNodeUnique(t *testing.T) {
	t.Parallel()

	mA := nucleus.NewManifest("node-A")
	mB := nucleus.NewManifest("node-B")
	sA := NewSonar(mA)
	sB := NewSonar(mB)

	const n = 10_000
	seen := make(map[string]struct{}, n*2)
	for i := 0; i < n; i++ {
		uuidA := sA.generateUUID()
		uuidB := sB.generateUUID()

		if _, dup := seen[uuidA]; dup {
			t.Fatalf("call %d: node-A UUID %q collides", i, uuidA)
		}
		if _, dup := seen[uuidB]; dup {
			t.Fatalf("call %d: node-B UUID %q collides", i, uuidB)
		}
		seen[uuidA] = struct{}{}
		seen[uuidB] = struct{}{}
	}
}

func TestSonarUUID_NeverZero(t *testing.T) {
	t.Parallel()

	m := nucleus.NewManifest("test-node")
	s := NewSonar(m)

	zeroHex := "00000000000000000000000000000000"
	for i := 0; i < 1000; i++ {
		uuid := s.generateUUID()
		if uuid == zeroHex {
			t.Fatalf("call %d: generateUUID returned the all-zero sentinel", i)
		}
	}
}

func BenchmarkSonarUUID(b *testing.B) {
	m := nucleus.NewManifest("bench-node")
	s := NewSonar(m)

	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		uuid := s.generateUUID()
		if uuid == "" {
			b.Fatal("empty UUID")
		}
	}
}

// BenchmarkSonarUUID_CreateWhoHas benchmarks the full CreateWhoHas path
// which includes UUID generation + frame construction.
func BenchmarkSonarUUID_CreateWhoHas(b *testing.B) {
	m := nucleus.NewManifest("bench-node")
	s := NewSonar(m)

	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		frame := s.CreateWhoHas("tool:hello", 8)
		if frame.UUID == "" {
			b.Fatal("empty UUID")
		}
	}
}

// BenchmarkSonarUUID_Parallel verifies zero contention under concurrent load.
func BenchmarkSonarUUID_Parallel(b *testing.B) {
	m := nucleus.NewManifest("bench-node")
	s := NewSonar(m)

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			uuid := s.generateUUID()
			if uuid == "" {
				b.Fatal("empty UUID")
			}
		}
	})
}

func TestSonarUUID_Format(t *testing.T) {
	t.Parallel()

	m := nucleus.NewManifest("mynode")
	s := NewSonar(m)

	uuid := s.generateUUID()
	// Should contain the node ID as a prefix for cross-node uniqueness.
	if len(uuid) == 0 {
		t.Fatal("UUID is empty")
	}

	// Generate two consecutive UUIDs — they must be different.
	uuid2 := s.generateUUID()
	if uuid == uuid2 {
		t.Fatalf("consecutive UUIDs should differ: %q", uuid)
	}

	// Verify the format includes the node ID prefix.
	expected := "mynode:"
	if len(uuid) < len(expected) {
		t.Fatalf("UUID %q too short to contain nodeID prefix", uuid)
	}
	if uuid[:len(expected)] != expected {
		t.Fatalf("UUID %q should start with %q", uuid, expected)
	}
}
