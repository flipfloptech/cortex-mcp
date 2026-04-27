package nucleus

import (
	"encoding/json"
	"sync"
	"testing"
)

// --- NewManifest ---

func TestNewManifest_PreservesNodeID(t *testing.T) {
	t.Parallel()

	m := NewManifest("node-alpha")
	if m.NodeID() != "node-alpha" {
		t.Fatalf("NodeID = %q, want %q", m.NodeID(), "node-alpha")
	}
}

func TestNewManifest_DefaultImpedance(t *testing.T) {
	t.Parallel()

	m := NewManifest("node-1")
	imp := m.Impedance()
	// Default impedance should be 1.0 (idle).
	if imp != 1.0 {
		t.Fatalf("default Impedance = %f, want 1.0", imp)
	}
}

func TestNewManifest_EmptyCapabilities(t *testing.T) {
	t.Parallel()

	m := NewManifest("node-1")
	caps := m.Capabilities()
	if len(caps) != 0 {
		t.Fatalf("new manifest should have empty capabilities, got %v", caps)
	}
}

// --- RegisterCapability ---

func TestManifest_RegisterCapability(t *testing.T) {
	t.Parallel()

	m := NewManifest("node-1")
	m.RegisterCapability("role:mds")
	m.RegisterCapability("tool:lustre_health")

	caps := m.Capabilities()
	if len(caps) != 2 {
		t.Fatalf("expected 2 capabilities, got %d: %v", len(caps), caps)
	}
}

func TestManifest_RegisterCapability_Deduplicate(t *testing.T) {
	t.Parallel()

	m := NewManifest("node-1")
	m.RegisterCapability("role:mds")
	m.RegisterCapability("role:mds") // duplicate
	m.RegisterCapability("role:mds") // duplicate

	caps := m.Capabilities()
	if len(caps) != 1 {
		t.Fatalf("expected 1 capability (deduped), got %d: %v", len(caps), caps)
	}
}

// --- HasCapability ---

func TestManifest_HasCapability(t *testing.T) {
	t.Parallel()

	m := NewManifest("node-1")
	m.RegisterCapability("tool:lustre_health")

	if !m.HasCapability("tool:lustre_health") {
		t.Fatal("HasCapability should return true for registered capability")
	}
	if m.HasCapability("tool:nonexistent") {
		t.Fatal("HasCapability should return false for unregistered capability")
	}
}

func TestManifest_HasCapability_WildcardMatch(t *testing.T) {
	t.Parallel()

	m := NewManifest("node-1")
	m.RegisterCapability("tool:lustre_health")
	m.RegisterCapability("tool:lnet_status")
	m.RegisterCapability("role:mds")

	// "tool:*" should match any tool.
	if !m.HasCapability("tool:*") {
		t.Fatal("HasCapability(tool:*) should match tool capabilities")
	}
	// "role:*" should match any role.
	if !m.HasCapability("role:*") {
		t.Fatal("HasCapability(role:*) should match role capabilities")
	}
	// "storage:*" should not match.
	if m.HasCapability("storage:*") {
		t.Fatal("HasCapability(storage:*) should not match when no storage caps")
	}
}

// --- SetImpedance ---

func TestManifest_SetImpedance(t *testing.T) {
	t.Parallel()

	m := NewManifest("node-1")
	m.SetImpedance(42.5)
	if m.Impedance() != 42.5 {
		t.Fatalf("Impedance = %f, want 42.5", m.Impedance())
	}
}

func TestManifest_SetImpedance_ClampsLow(t *testing.T) {
	t.Parallel()

	m := NewManifest("node-1")
	m.SetImpedance(0.5) // below minimum 1.0
	if m.Impedance() != 1.0 {
		t.Fatalf("Impedance = %f, want 1.0 (clamped)", m.Impedance())
	}
}

func TestManifest_SetImpedance_ClampsHigh(t *testing.T) {
	t.Parallel()

	m := NewManifest("node-1")
	m.SetImpedance(150.0) // above maximum 100.0
	if m.Impedance() != 100.0 {
		t.Fatalf("Impedance = %f, want 100.0 (clamped)", m.Impedance())
	}
}

// --- Capabilities returns a copy ---

func TestManifest_Capabilities_ReturnsCopy(t *testing.T) {
	t.Parallel()

	m := NewManifest("node-1")
	m.RegisterCapability("tool:a")

	caps := m.Capabilities()
	_ = append(caps, "tool:injected") // mutate the returned slice — should not affect manifest

	// The manifest's internal state should be unaffected.
	if len(m.Capabilities()) != 1 {
		t.Fatal("mutating returned Capabilities slice should not affect manifest")
	}
}

// --- Goroutine safety ---

func TestManifest_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	m := NewManifest("node-1")

	var wg sync.WaitGroup
	wg.Add(4)

	// Concurrent registrations.
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			m.RegisterCapability("tool:a")
		}
	}()

	// Concurrent impedance updates.
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			m.SetImpedance(float64(i))
		}
	}()

	// Concurrent reads.
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = m.Capabilities()
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = m.Impedance()
			_ = m.HasCapability("tool:a")
		}
	}()

	wg.Wait()
}

// --- Snapshot for gossip ---

func TestManifest_Snapshot(t *testing.T) {
	t.Parallel()

	m := NewManifest("node-omega")
	m.RegisterCapability("role:oss")
	m.RegisterCapability("tool:lustre_health")
	m.SetImpedance(25.0)

	snap := m.Snapshot()

	if snap.NodeID != "node-omega" {
		t.Fatalf("Snapshot NodeID = %q, want %q", snap.NodeID, "node-omega")
	}
	if snap.Impedance != 25.0 {
		t.Fatalf("Snapshot Impedance = %f, want 25.0", snap.Impedance)
	}
	if len(snap.Capabilities) != 2 {
		t.Fatalf("Snapshot Capabilities count = %d, want 2", len(snap.Capabilities))
	}

	// Snapshot should be JSON-serializable.
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var decoded ManifestSnapshot
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if decoded.NodeID != "node-omega" {
		t.Fatalf("decoded NodeID = %q, want %q", decoded.NodeID, "node-omega")
	}
	if decoded.Impedance != 25.0 {
		t.Fatalf("decoded Impedance = %f, want 25.0", decoded.Impedance)
	}
}

// --- Addresses ---

func TestManifest_SetAddresses(t *testing.T) {
	t.Parallel()

	m := NewManifest("node-1")
	m.SetAddresses([]string{"10.0.1.5:443", "10.0.1.5:22"})

	snap := m.Snapshot()
	if len(snap.Addresses) != 2 {
		t.Fatalf("expected 2 addresses, got %d", len(snap.Addresses))
	}
}

func TestManifest_SetAddresses_ReturnsCopy(t *testing.T) {
	t.Parallel()

	m := NewManifest("node-1")
	addrs := []string{"10.0.1.5:443"}
	m.SetAddresses(addrs)

	// Mutate the original slice.
	addrs[0] = "INJECTED"

	snap := m.Snapshot()
	if snap.Addresses[0] == "INJECTED" {
		t.Fatal("SetAddresses should copy, not reference the input slice")
	}
}
