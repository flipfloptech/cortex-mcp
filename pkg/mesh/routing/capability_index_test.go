package routing

import (
	"sort"
	"sync"
	"testing"
)

// --- NewCapabilityIndex ---

func TestCapabilityIndex_New(t *testing.T) {
	t.Parallel()

	ci := NewCapabilityIndex()
	if ci == nil {
		t.Fatal("NewCapabilityIndex returned nil")
	}
	if ci.Count() != 0 {
		t.Fatalf("new index should be empty, got %d", ci.Count())
	}
}

// --- Update + Lookup ---

func TestCapabilityIndex_Update_Lookup(t *testing.T) {
	t.Parallel()

	ci := NewCapabilityIndex()
	ci.Update("oss-01", 5.0, []string{"tool:hello", "tool:system_info"})

	// Lookup tool:hello — should find oss-01.
	entries := ci.Lookup("tool:hello")
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry for tool:hello, got %d", len(entries))
	}
	if entries[0].NodeID != "oss-01" {
		t.Fatalf("expected NodeID=oss-01, got %q", entries[0].NodeID)
	}
	if entries[0].Impedance != 5.0 {
		t.Fatalf("expected Impedance=5.0, got %f", entries[0].Impedance)
	}

	// Lookup tool:system_info — should also find oss-01.
	entries = ci.Lookup("tool:system_info")
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry for tool:system_info, got %d", len(entries))
	}

	// Lookup unknown capability — empty slice.
	entries = ci.Lookup("tool:nonexistent")
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries for unknown cap, got %d", len(entries))
	}
}

func TestCapabilityIndex_Update_MultipleNodes(t *testing.T) {
	t.Parallel()

	ci := NewCapabilityIndex()
	ci.Update("oss-01", 5.0, []string{"tool:hello"})
	ci.Update("oss-02", 3.0, []string{"tool:hello"})
	ci.Update("mds-01", 8.0, []string{"tool:hello", "tool:mds_stats"})

	entries := ci.Lookup("tool:hello")
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries for tool:hello, got %d", len(entries))
	}

	// mds_stats should only have 1 entry.
	entries = ci.Lookup("tool:mds_stats")
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry for tool:mds_stats, got %d", len(entries))
	}
}

func TestCapabilityIndex_Update_OverwritesCapabilities(t *testing.T) {
	t.Parallel()

	ci := NewCapabilityIndex()
	ci.Update("oss-01", 5.0, []string{"tool:hello", "tool:old_tool"})

	// Verify old_tool exists.
	entries := ci.Lookup("tool:old_tool")
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry for tool:old_tool, got %d", len(entries))
	}

	// Update with a new capability set (old_tool removed, new_tool added).
	ci.Update("oss-01", 5.0, []string{"tool:hello", "tool:new_tool"})

	// old_tool should be gone.
	entries = ci.Lookup("tool:old_tool")
	if len(entries) != 0 {
		t.Fatalf("old_tool should be removed after cap overwrite, got %d", len(entries))
	}

	// new_tool should be present.
	entries = ci.Lookup("tool:new_tool")
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry for tool:new_tool, got %d", len(entries))
	}

	// hello should still be present.
	entries = ci.Lookup("tool:hello")
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry for tool:hello, got %d", len(entries))
	}
}

// --- Lookup sorted by impedance ---

func TestCapabilityIndex_Lookup_SortedByImpedance(t *testing.T) {
	t.Parallel()

	ci := NewCapabilityIndex()
	ci.Update("node-expensive", 50.0, []string{"tool:hello"})
	ci.Update("node-cheap", 2.0, []string{"tool:hello"})
	ci.Update("node-medium", 15.0, []string{"tool:hello"})

	entries := ci.Lookup("tool:hello")
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	// Should be sorted: cheap (2.0), medium (15.0), expensive (50.0).
	if entries[0].NodeID != "node-cheap" {
		t.Fatalf("first entry should be cheapest, got %q (impedance=%f)", entries[0].NodeID, entries[0].Impedance)
	}
	if entries[1].NodeID != "node-medium" {
		t.Fatalf("second entry should be medium, got %q", entries[1].NodeID)
	}
	if entries[2].NodeID != "node-expensive" {
		t.Fatalf("third entry should be expensive, got %q", entries[2].NodeID)
	}
}

// --- Wildcard lookup ---

func TestCapabilityIndex_LookupWildcard(t *testing.T) {
	t.Parallel()

	ci := NewCapabilityIndex()
	ci.Update("oss-01", 5.0, []string{"tool:hello", "tool:system_info", "role:oss"})
	ci.Update("mds-01", 3.0, []string{"tool:hello", "tool:mds_stats", "role:mds"})

	// Wildcard "tool:" should match all tool capabilities.
	entries := ci.LookupWildcard("tool:")
	// Both nodes have tool: capabilities — should get unique entries.
	nodeIDs := make(map[string]bool)
	for _, e := range entries {
		nodeIDs[e.NodeID] = true
	}
	if len(nodeIDs) != 2 {
		t.Fatalf("expected entries from 2 nodes, got %d (entries=%d)", len(nodeIDs), len(entries))
	}

	// Wildcard "role:" should match both role capabilities.
	entries = ci.LookupWildcard("role:")
	nodeIDs = make(map[string]bool)
	for _, e := range entries {
		nodeIDs[e.NodeID] = true
	}
	if len(nodeIDs) != 2 {
		t.Fatalf("expected entries from 2 nodes for role:, got %d", len(nodeIDs))
	}

	// Wildcard with no matches.
	entries = ci.LookupWildcard("sfa:")
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries for sfa: wildcard, got %d", len(entries))
	}
}

// --- PurgeNode ---

func TestCapabilityIndex_PurgeNode(t *testing.T) {
	t.Parallel()

	ci := NewCapabilityIndex()
	ci.Update("oss-01", 5.0, []string{"tool:hello", "tool:system_info"})
	ci.Update("oss-02", 3.0, []string{"tool:hello"})

	ci.PurgeNode("oss-01")

	// oss-01 entries should be gone.
	entries := ci.Lookup("tool:system_info")
	if len(entries) != 0 {
		t.Fatalf("purged node's tool:system_info should be gone, got %d entries", len(entries))
	}

	// tool:hello should only have oss-02 now.
	entries = ci.Lookup("tool:hello")
	if len(entries) != 1 {
		t.Fatalf("expected 1 remaining entry for tool:hello, got %d", len(entries))
	}
	if entries[0].NodeID != "oss-02" {
		t.Fatalf("remaining entry should be oss-02, got %q", entries[0].NodeID)
	}

	// Count should reflect removal.
	if ci.Count() != 1 {
		t.Fatalf("expected count=1 after purge, got %d", ci.Count())
	}
}

func TestCapabilityIndex_PurgeNode_NotFound(t *testing.T) {
	t.Parallel()

	ci := NewCapabilityIndex()
	ci.Update("oss-01", 5.0, []string{"tool:hello"})

	// Purging a non-existent node should not panic.
	ci.PurgeNode("nonexistent")

	if ci.Count() != 1 {
		t.Fatalf("count should be unchanged after purging nonexistent, got %d", ci.Count())
	}
}

// --- Snapshot ---

func TestCapabilityIndex_Snapshot(t *testing.T) {
	t.Parallel()

	ci := NewCapabilityIndex()
	ci.Update("oss-01", 5.0, []string{"tool:hello", "tool:system_info"})
	ci.Update("mds-01", 3.0, []string{"tool:hello", "tool:mds_stats"})

	snap := ci.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("expected 2 nodes in snapshot, got %d", len(snap))
	}

	ossCaps := snap["oss-01"]
	sort.Strings(ossCaps)
	if len(ossCaps) != 2 || ossCaps[0] != "tool:hello" || ossCaps[1] != "tool:system_info" {
		t.Fatalf("oss-01 caps = %v, want [tool:hello, tool:system_info]", ossCaps)
	}

	mdsCaps := snap["mds-01"]
	sort.Strings(mdsCaps)
	if len(mdsCaps) != 2 || mdsCaps[0] != "tool:hello" || mdsCaps[1] != "tool:mds_stats" {
		t.Fatalf("mds-01 caps = %v, want [tool:hello, tool:mds_stats]", mdsCaps)
	}
}

func TestCapabilityIndex_Snapshot_IsCopy(t *testing.T) {
	t.Parallel()

	ci := NewCapabilityIndex()
	ci.Update("oss-01", 5.0, []string{"tool:hello"})

	snap := ci.Snapshot()
	// Mutating snapshot should not affect the index.
	snap["oss-01"] = []string{"modified"}

	entries := ci.Lookup("tool:hello")
	if len(entries) != 1 {
		t.Fatal("mutating snapshot should not affect index")
	}
}

// --- UpdateImpedance ---

func TestCapabilityIndex_UpdateImpedance(t *testing.T) {
	t.Parallel()

	ci := NewCapabilityIndex()
	ci.Update("oss-01", 5.0, []string{"tool:hello"})

	// Impedance update — caps should remain the same.
	ci.UpdateImpedance("oss-01", 25.0)

	entries := ci.Lookup("tool:hello")
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry after impedance update, got %d", len(entries))
	}
	if entries[0].Impedance != 25.0 {
		t.Fatalf("impedance should be 25.0, got %f", entries[0].Impedance)
	}
}

func TestCapabilityIndex_UpdateImpedance_UnknownNode(t *testing.T) {
	t.Parallel()

	ci := NewCapabilityIndex()
	// Should not panic — no-op for unknown nodes.
	ci.UpdateImpedance("nonexistent", 10.0)
	if ci.Count() != 0 {
		t.Fatal("updating impedance for unknown node should not create entries")
	}
}

// --- Empty Lookup returns empty slice ---

func TestCapabilityIndex_EmptyLookup(t *testing.T) {
	t.Parallel()

	ci := NewCapabilityIndex()
	entries := ci.Lookup("tool:anything")
	if entries == nil {
		t.Fatal("Lookup should return empty slice, not nil")
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}
}

// --- Goroutine safety ---

func TestCapabilityIndex_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	ci := NewCapabilityIndex()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(4)
		go func(id int) {
			defer wg.Done()
			ci.Update("oss-01", float64(id), []string{"tool:hello"})
		}(i)
		go func() {
			defer wg.Done()
			ci.Lookup("tool:hello")
		}()
		go func() {
			defer wg.Done()
			ci.Snapshot()
		}()
		go func() {
			defer wg.Done()
			ci.LookupWildcard("tool:")
		}()
	}
	wg.Wait()
}

func TestCapabilityIndex_ConcurrentUpdatePurge(t *testing.T) {
	t.Parallel()

	ci := NewCapabilityIndex()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(id int) {
			defer wg.Done()
			ci.Update("oss-01", float64(id), []string{"tool:hello"})
		}(i)
		go func() {
			defer wg.Done()
			ci.PurgeNode("oss-01")
		}()
	}
	wg.Wait()
}
