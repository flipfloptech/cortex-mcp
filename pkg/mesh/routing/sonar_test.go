package routing

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/nucleus"
)

// --- NewSonar ---

func TestNewSonar_Creates(t *testing.T) {
	t.Parallel()

	m := nucleus.NewManifest("node-1")
	s := NewSonar(m)
	if s == nil {
		t.Fatal("NewSonar returned nil")
	}
}

// --- WhoHas/IHave frame types ---

func TestWhoHasFrame_Fields(t *testing.T) {
	t.Parallel()

	frame := WhoHasFrame{
		UUID:       "test-uuid-1234",
		Capability: "tool:lustre_health",
		OriginID:   "node-sender",
	}

	if frame.UUID == "" || frame.Capability == "" || frame.OriginID == "" {
		t.Fatal("WhoHasFrame fields should not be empty")
	}
}

func TestIHaveFrame_Fields(t *testing.T) {
	t.Parallel()

	frame := IHaveFrame{
		UUID:      "test-uuid-1234",
		NodeID:    "node-responder",
		Impedance: 25.0,
	}

	if frame.UUID == "" || frame.NodeID == "" || frame.Impedance == 0 {
		t.Fatal("IHaveFrame fields should not be empty")
	}
}

// --- Handle WhoHas: local capability match → IHave response ---

func TestSonar_HandleWhoHas_LocalMatch(t *testing.T) {
	t.Parallel()

	m := nucleus.NewManifest("node-responder")
	m.RegisterCapability("tool:lustre_health")
	m.SetImpedance(15.0)

	s := NewSonar(m)

	whoHas := &WhoHasFrame{
		UUID:       "uuid-001",
		Capability: "tool:lustre_health",
		OriginID:   "node-sender",
	}

	response := s.HandleWhoHas(whoHas)
	if response == nil {
		t.Fatal("should respond with IHave for matching capability")
	}
	if response.NodeID != "node-responder" {
		t.Fatalf("IHave NodeID = %q, want %q", response.NodeID, "node-responder")
	}
	if response.Impedance != 15.0 {
		t.Fatalf("IHave Impedance = %f, want 15.0", response.Impedance)
	}
	if response.UUID != "uuid-001" {
		t.Fatalf("IHave UUID = %q, want %q", response.UUID, "uuid-001")
	}
}

// --- Handle WhoHas: no match → no response ---

func TestSonar_HandleWhoHas_NoMatch(t *testing.T) {
	t.Parallel()

	m := nucleus.NewManifest("node-1")
	m.RegisterCapability("tool:other")
	s := NewSonar(m)

	whoHas := &WhoHasFrame{
		UUID:       "uuid-002",
		Capability: "tool:lustre_health",
		OriginID:   "node-sender",
	}

	response := s.HandleWhoHas(whoHas)
	if response != nil {
		t.Fatal("should not respond when capability doesn't match")
	}
}

// --- Handle WhoHas: wildcard match ---

func TestSonar_HandleWhoHas_WildcardMatch(t *testing.T) {
	t.Parallel()

	m := nucleus.NewManifest("node-1")
	m.RegisterCapability("tool:lustre_health")
	m.RegisterCapability("tool:lnet_status")
	s := NewSonar(m)

	whoHas := &WhoHasFrame{
		UUID:       "uuid-003",
		Capability: "tool:*",
		OriginID:   "node-sender",
	}

	response := s.HandleWhoHas(whoHas)
	if response == nil {
		t.Fatal("should respond to wildcard match")
	}
}

// --- UUID deduplication ---

func TestSonar_HandleWhoHas_DuplicateUUID(t *testing.T) {
	t.Parallel()

	m := nucleus.NewManifest("node-1")
	m.RegisterCapability("tool:lustre_health")
	s := NewSonar(m)

	whoHas := &WhoHasFrame{
		UUID:       "uuid-duplicate",
		Capability: "tool:lustre_health",
		OriginID:   "node-sender",
	}

	// First call should respond.
	if s.HandleWhoHas(whoHas) == nil {
		t.Fatal("first WhoHas should get response")
	}

	// Second call with same UUID should be silently dropped.
	if s.HandleWhoHas(whoHas) != nil {
		t.Fatal("duplicate UUID should be silently dropped")
	}
}

// --- ShouldForward: forward if not from self and not seen ---

func TestSonar_ShouldForward(t *testing.T) {
	t.Parallel()

	m := nucleus.NewManifest("node-1")
	s := NewSonar(m)

	whoHas := &WhoHasFrame{
		UUID:       "uuid-forward",
		Capability: "tool:something",
		OriginID:   "node-other", // from another node
		MaxHops:    5,
	}

	// First time: should forward.
	if !s.ShouldForward(whoHas) {
		t.Fatal("first encounter should be forwarded")
	}

	// Second time: should not forward (seen UUID).
	if s.ShouldForward(whoHas) {
		t.Fatal("seen UUID should not be forwarded again")
	}
}

func TestSonar_ShouldForward_NotSelfOriginated(t *testing.T) {
	t.Parallel()

	m := nucleus.NewManifest("node-1")
	s := NewSonar(m)

	whoHas := &WhoHasFrame{
		UUID:       "uuid-self",
		Capability: "tool:something",
		OriginID:   "node-1", // from self
	}

	// Should not forward our own WhoHas.
	if s.ShouldForward(whoHas) {
		t.Fatal("should not forward self-originated WhoHas")
	}
}

// --- CreateWhoHas ---

func TestSonar_CreateWhoHas(t *testing.T) {
	t.Parallel()

	m := nucleus.NewManifest("node-origin")
	s := NewSonar(m)

	frame := s.CreateWhoHas("tool:lustre_health", 5)
	if frame.UUID == "" {
		t.Fatal("CreateWhoHas should generate a UUID")
	}
	if frame.Capability != "tool:lustre_health" {
		t.Fatalf("Capability = %q, want %q", frame.Capability, "tool:lustre_health")
	}
	if frame.OriginID != "node-origin" {
		t.Fatalf("OriginID = %q, want %q", frame.OriginID, "node-origin")
	}
	if frame.MaxHops != 5 {
		t.Fatalf("MaxHops = %d, want 5", frame.MaxHops)
	}
}

// --- CollectResponses ---

func TestSonar_CollectResponses(t *testing.T) {
	t.Parallel()

	m := nucleus.NewManifest("node-1")
	s := NewSonar(m)

	uuid := "collection-uuid"
	collector := s.StartCollection(uuid)

	// Simulate responses arriving.
	go func() {
		time.Sleep(5 * time.Millisecond)
		collector.Add(IHaveFrame{UUID: uuid, NodeID: "node-a", Impedance: 10.0})
		collector.Add(IHaveFrame{UUID: uuid, NodeID: "node-b", Impedance: 20.0})
		collector.Add(IHaveFrame{UUID: uuid, NodeID: "node-c", Impedance: 5.0})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	results := collector.Collect(ctx)
	if len(results) != 3 {
		t.Fatalf("expected 3 responses, got %d", len(results))
	}
}

func TestSonar_CollectResponses_ContextCancel(t *testing.T) {
	t.Parallel()

	m := nucleus.NewManifest("node-1")
	s := NewSonar(m)

	collector := s.StartCollection("cancel-uuid")

	// Add one response, then cancel.
	collector.Add(IHaveFrame{UUID: "cancel-uuid", NodeID: "node-a", Impedance: 10.0})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	results := collector.Collect(ctx)
	// Should return at least the one added response (or empty if cancel beat it).
	if len(results) > 1 {
		t.Fatalf("expected at most 1 result after cancel, got %d", len(results))
	}
}

// --- Collection timeout logic ---

func TestCollect_EnforcesDefaultTimeout(t *testing.T) {
	t.Parallel()

	m := nucleus.NewManifest("node-1")
	s := NewSonar(m)

	uuid := "default-timeout-uuid"
	collector := s.StartCollection(uuid)

	// No responses — Collect should enforce the default 1s timeout on a background context.
	ctx := context.Background()

	start := time.Now()
	results := collector.Collect(ctx)
	elapsed := time.Since(start)

	if len(results) != 0 {
		t.Fatalf("expected 0 responses, got %d", len(results))
	}

	// Should wait around 1 second (DefaultSonarTimeout)
	if elapsed < 900*time.Millisecond || elapsed > 1500*time.Millisecond {
		t.Fatalf("Collect blocked for %v — should enforce ~1s default timeout", elapsed)
	}
}

func TestCollect_RespectsCallerDeadline(t *testing.T) {
	t.Parallel()

	m := nucleus.NewManifest("node-1")
	s := NewSonar(m)

	uuid := "caller-deadline-uuid"
	collector := s.StartCollection(uuid)

	// Add some responses early
	collector.Add(IHaveFrame{UUID: uuid, NodeID: "node-a", Impedance: 10.0})

	// Caller provides a short 100ms deadline
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	results := collector.Collect(ctx)
	elapsed := time.Since(start)

	if len(results) != 1 {
		t.Fatalf("expected 1 response, got %d", len(results))
	}

	// Should block exactly until the 100ms deadline, not return early, and not use the 1s default.
	if elapsed < 80*time.Millisecond || elapsed > 200*time.Millisecond {
		t.Fatalf("Collect blocked for %v — should respect 100ms caller deadline", elapsed)
	}
}

// --- Goroutine safety ---

func TestSonar_ConcurrentOperations(t *testing.T) {
	t.Parallel()

	m := nucleus.NewManifest("node-1")
	m.RegisterCapability("tool:test")
	s := NewSonar(m)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(id int) {
			defer wg.Done()
			s.HandleWhoHas(&WhoHasFrame{
				UUID:       string(rune(id)),
				Capability: "tool:test",
				OriginID:   "node-other",
			})
		}(i)
		go func() {
			defer wg.Done()
			_ = s.CreateWhoHas("tool:test", 8)
		}()
	}
	wg.Wait()
}
