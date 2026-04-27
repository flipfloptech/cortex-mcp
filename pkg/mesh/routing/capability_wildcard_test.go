package routing

import (
	"fmt"
	"testing"
)

// --- P2-1: Namespace-bucketed LookupWildcard ---
//
// LookupWildcard("tool:") must be O(caps in the "tool" namespace), not
// O(all caps in the mesh). The fix adds a namespace-bucketed secondary
// index keyed on the substring before the first ':'.
//
// Contract:
//   1. LookupWildcard("tool:") ignores capabilities in other namespaces.
//   2. Exact capability lookups still work.
//   3. Capabilities without ':' go into the empty-namespace bucket.
//   4. Performance: tool: lookup unaffected by 100k role: entries.

func TestLookupWildcard_NamespaceBucketed(t *testing.T) {
	t.Parallel()

	ci := NewCapabilityIndex()

	// Node A has tool capabilities.
	ci.Update("node-A", 1.0, []string{"tool:hello", "tool:system_info"})
	// Node B has role capabilities.
	ci.Update("node-B", 2.0, []string{"role:admin", "role:viewer"})
	// Node C has mixed.
	ci.Update("node-C", 3.0, []string{"tool:netcheck", "role:admin"})

	// Wildcard for "tool:" should return A and C, not B.
	results := ci.LookupWildcard("tool:")
	if len(results) != 2 {
		t.Fatalf("expected 2 nodes with tool: caps, got %d", len(results))
	}

	// Sorted by impedance: A (1.0), C (3.0).
	if results[0].NodeID != "node-A" || results[1].NodeID != "node-C" {
		t.Fatalf("expected [node-A, node-C], got [%s, %s]",
			results[0].NodeID, results[1].NodeID)
	}

	// Wildcard for "role:" should return B and C.
	results = ci.LookupWildcard("role:")
	if len(results) != 2 {
		t.Fatalf("expected 2 nodes with role: caps, got %d", len(results))
	}
}

func TestLookupWildcard_NoNamespaceDelimiter(t *testing.T) {
	t.Parallel()

	ci := NewCapabilityIndex()

	// Capabilities without ':' should still be findable.
	ci.Update("node-A", 1.0, []string{"standalone", "other"})
	ci.Update("node-B", 2.0, []string{"tool:hello"})

	// Wildcard for "stand" should match "standalone".
	results := ci.LookupWildcard("stand")
	if len(results) != 1 || results[0].NodeID != "node-A" {
		t.Fatalf("expected [node-A] for 'stand' wildcard, got %v", results)
	}
}

func TestLookupWildcard_PurgeRemovesFromNamespaceIndex(t *testing.T) {
	t.Parallel()

	ci := NewCapabilityIndex()
	ci.Update("node-A", 1.0, []string{"tool:hello"})

	// Purge node-A.
	ci.PurgeNode("node-A")

	results := ci.LookupWildcard("tool:")
	if len(results) != 0 {
		t.Fatalf("expected 0 results after purge, got %d", len(results))
	}
}

func TestLookupWildcard_UpdateReplacesInNamespaceIndex(t *testing.T) {
	t.Parallel()

	ci := NewCapabilityIndex()
	ci.Update("node-A", 1.0, []string{"tool:hello", "role:admin"})

	// Replace with different caps — old tool: should be gone.
	ci.Update("node-A", 1.0, []string{"role:viewer"})

	toolResults := ci.LookupWildcard("tool:")
	if len(toolResults) != 0 {
		t.Fatalf("expected 0 tool results after update, got %d", len(toolResults))
	}

	roleResults := ci.LookupWildcard("role:")
	if len(roleResults) != 1 {
		t.Fatalf("expected 1 role result after update, got %d", len(roleResults))
	}
}

func BenchmarkLookupWildcard_NamespaceIsolation(b *testing.B) {
	ci := NewCapabilityIndex()

	// Populate 100k role: capabilities across 10k nodes.
	for i := 0; i < 10_000; i++ {
		caps := make([]string, 10)
		for j := 0; j < 10; j++ {
			caps[j] = fmt.Sprintf("role:perm-%d-%d", i, j)
		}
		ci.Update(fmt.Sprintf("role-node-%d", i), 1.0, caps)
	}

	// Add a single tool: capability.
	ci.Update("tool-node", 1.0, []string{"tool:hello"})

	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		results := ci.LookupWildcard("tool:")
		if len(results) != 1 {
			b.Fatalf("expected 1 tool result, got %d", len(results))
		}
	}
}
