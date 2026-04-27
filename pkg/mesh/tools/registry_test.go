package tools

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

// --- NewRegistry ---

func TestNewRegistry(t *testing.T) {
	t.Parallel()

	r := NewRegistry(nil) // no mesh connection for local-only tests
	if r == nil {
		t.Fatal("NewRegistry returned nil")
	}
}

// --- Register ---

func TestRegistry_Register(t *testing.T) {
	t.Parallel()

	r := NewRegistry(nil)
	r.Register(ToolDefinition{
		Name:        "lustre_health",
		Description: "Check Lustre health",
		Category:    "lustre",
	}, func(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
		return NewTextResult("healthy"), nil
	})

	tools := r.ListLocal()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Name != "lustre_health" {
		t.Fatalf("tool name = %q, want %q", tools[0].Name, "lustre_health")
	}
}

func TestRegistry_Register_Multiple(t *testing.T) {
	t.Parallel()

	r := NewRegistry(nil)
	r.Register(ToolDefinition{Name: "tool_a", Category: "system"}, noopHandler)
	r.Register(ToolDefinition{Name: "tool_b", Category: "network"}, noopHandler)
	r.Register(ToolDefinition{Name: "tool_c", Category: "lustre"}, noopHandler)

	tools := r.ListLocal()
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(tools))
	}
}

func TestRegistry_Register_DuplicateOverwrites(t *testing.T) {
	t.Parallel()

	r := NewRegistry(nil)
	r.Register(ToolDefinition{Name: "tool_a", Description: "v1"}, noopHandler)
	r.Register(ToolDefinition{Name: "tool_a", Description: "v2"}, noopHandler)

	tools := r.ListLocal()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool after duplicate registration, got %d", len(tools))
	}
	if tools[0].Description != "v2" {
		t.Fatalf("description = %q, want v2 (latest)", tools[0].Description)
	}
}

// --- Register notifies capability tracker ---

func TestRegistry_Register_NotifiesCapabilities(t *testing.T) {
	t.Parallel()

	tracker := &mockCapabilityTracker{}
	r := NewRegistry(tracker)
	r.Register(ToolDefinition{Name: "lustre_health"}, noopHandler)

	if !tracker.hasCapability("tool:lustre_health") {
		t.Fatal("expected capability 'tool:lustre_health' to be registered")
	}
}

// --- ListLocal ---

func TestRegistry_ListLocal_Empty(t *testing.T) {
	t.Parallel()

	r := NewRegistry(nil)
	tools := r.ListLocal()
	if len(tools) != 0 {
		t.Fatalf("expected 0 tools, got %d", len(tools))
	}
}

func TestRegistry_ListLocal_ByCategory(t *testing.T) {
	t.Parallel()

	r := NewRegistry(nil)
	r.Register(ToolDefinition{Name: "tool_a", Category: "lustre"}, noopHandler)
	r.Register(ToolDefinition{Name: "tool_b", Category: "network"}, noopHandler)
	r.Register(ToolDefinition{Name: "tool_c", Category: "lustre"}, noopHandler)

	tools := r.ListLocalByCategory("lustre")
	if len(tools) != 2 {
		t.Fatalf("expected 2 lustre tools, got %d", len(tools))
	}
}

// --- InvokeLocal ---

func TestRegistry_InvokeLocal_Success(t *testing.T) {
	t.Parallel()

	r := NewRegistry(nil)
	r.Register(ToolDefinition{Name: "echo"}, func(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
		return NewJSONResult(map[string]string{"echo": string(args)}), nil
	})

	result, err := r.InvokeLocal(context.Background(), "echo", json.RawMessage(`"hello"`))
	if err != nil {
		t.Fatalf("InvokeLocal: %v", err)
	}
	if result.IsError {
		t.Fatal("result should not be error")
	}
}

func TestRegistry_InvokeLocal_NotFound(t *testing.T) {
	t.Parallel()

	r := NewRegistry(nil)
	_, err := r.InvokeLocal(context.Background(), "nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
	if !errors.Is(err, ErrToolNotFound) {
		t.Fatalf("expected ErrToolNotFound, got %v", err)
	}
}

func TestRegistry_InvokeLocal_HandlerError(t *testing.T) {
	t.Parallel()

	r := NewRegistry(nil)
	r.Register(ToolDefinition{Name: "failing"}, func(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
		return nil, errors.New("handler exploded")
	})

	_, err := r.InvokeLocal(context.Background(), "failing", nil)
	if err == nil {
		t.Fatal("expected error from handler")
	}
}

func TestRegistry_InvokeLocal_HandlerReturnsToolError(t *testing.T) {
	t.Parallel()

	r := NewRegistry(nil)
	r.Register(ToolDefinition{Name: "tool_error"}, func(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
		return NewErrorResult("bad input"), nil
	})

	result, err := r.InvokeLocal(context.Background(), "tool_error", nil)
	if err != nil {
		t.Fatalf("transport error should be nil: %v", err)
	}
	if !result.IsError {
		t.Fatal("result should be a tool error")
	}
}

func TestRegistry_InvokeLocal_ContextCancellation(t *testing.T) {
	t.Parallel()

	r := NewRegistry(nil)
	r.Register(ToolDefinition{Name: "slow"}, func(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := r.InvokeLocal(ctx, "slow", nil)
	if err == nil {
		t.Fatal("expected context error")
	}
}

// --- GetDefinition ---

func TestRegistry_GetDefinition(t *testing.T) {
	t.Parallel()

	r := NewRegistry(nil)
	r.Register(ToolDefinition{Name: "test_tool", Description: "A test tool"}, noopHandler)

	def, ok := r.GetDefinition("test_tool")
	if !ok {
		t.Fatal("tool should exist")
	}
	if def.Description != "A test tool" {
		t.Fatalf("description = %q, want %q", def.Description, "A test tool")
	}
}

func TestRegistry_GetDefinition_NotFound(t *testing.T) {
	t.Parallel()

	r := NewRegistry(nil)
	_, ok := r.GetDefinition("nonexistent")
	if ok {
		t.Fatal("should return false for unknown tool")
	}
}

// --- Goroutine safety ---

func TestRegistry_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	r := NewRegistry(nil)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(3)
		go func(id int) {
			defer wg.Done()
			name := string(rune('A' + id%26))
			r.Register(ToolDefinition{Name: name}, noopHandler)
		}(i)
		go func() {
			defer wg.Done()
			_ = r.ListLocal()
		}()
		go func() {
			defer wg.Done()
			_, _ = r.InvokeLocal(context.Background(), "A", nil)
		}()
	}
	wg.Wait()
}

// --- Test helpers ---

var noopHandler ToolHandler = func(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	return NewTextResult("ok"), nil
}

// mockCapabilityTracker records registered capabilities.
type mockCapabilityTracker struct {
	mu   sync.Mutex
	caps []string
}

func (m *mockCapabilityTracker) RegisterCapability(capability string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.caps = append(m.caps, capability)
}

func (m *mockCapabilityTracker) hasCapability(cap string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.caps {
		if c == cap {
			return true
		}
	}
	return false
}
