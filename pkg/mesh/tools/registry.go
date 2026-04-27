package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// ErrToolNotFound is returned when attempting to invoke a tool that
// is not registered on this node.
var ErrToolNotFound = errors.New("tool not found")

// CapabilityTracker is implemented by any component that tracks node
// capabilities (typically the Manifest). The registry notifies this
// when tools are registered so they become discoverable via Sonar.
type CapabilityTracker interface {
	RegisterCapability(capability string)
}

// registeredTool pairs a tool definition with its handler.
type registeredTool struct {
	definition *ToolDefinition
	handler    ToolHandler
}

// Registry manages tool registration on the local node.
// It provides local tool dispatch and discovery.
//
// The Registry is goroutine-safe — Register, ListLocal, InvokeLocal,
// and GetDefinition can all be called concurrently.
//
// For mesh-wide discovery and remote invocation, the Registry composes
// with a Neuron instance (via Sonar and Laser). Those operations are
// in invoke.go.
type Registry struct {
	mu      sync.RWMutex
	tools   map[string]*registeredTool
	tracker CapabilityTracker // may be nil (local-only mode)
}

// NewRegistry creates a new tool registry.
// The tracker parameter is optional — if provided, each Register call
// will notify the tracker that a new tool capability is available.
// This is typically the node's Manifest (which implements CapabilityTracker).
func NewRegistry(tracker CapabilityTracker) *Registry {
	return &Registry{
		tools:   make(map[string]*registeredTool),
		tracker: tracker,
	}
}

// Register adds a tool to this node's local registry.
// If a tool with the same name already exists, it is overwritten.
//
// The definition is stored by pointer to enable sync.Once schema caching.
// Callers passing a value will have it referenced internally.
//
// The tool is automatically advertised as a capability ("tool:<name>")
// via the CapabilityTracker, making it discoverable via Sonar.
func (r *Registry) Register(def ToolDefinition, handler ToolHandler) {
	p := &def // take address of the copy — caller's value is never mutated
	r.mu.Lock()
	r.tools[p.Name] = &registeredTool{
		definition: p,
		handler:    handler,
	}
	r.mu.Unlock()

	// Notify the capability tracker (if present).
	if r.tracker != nil {
		r.tracker.RegisterCapability("tool:" + p.Name)
	}
}

// ListLocal returns the tool definitions registered on this node.
// Hidden tools are excluded — use InvokeLocal to call them directly.
// The returned slice contains pointers to the stored definitions —
// callers must not modify them.
func (r *Registry) ListLocal() []*ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	defs := make([]*ToolDefinition, 0, len(r.tools))
	for _, tool := range r.tools {
		if !tool.definition.Hidden {
			defs = append(defs, tool.definition)
		}
	}
	return defs
}

// ListLocalByCategory returns tool definitions filtered by category.
// Hidden tools are excluded.
func (r *Registry) ListLocalByCategory(category string) []*ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var defs []*ToolDefinition
	for _, tool := range r.tools {
		if tool.definition.Category == category && !tool.definition.Hidden {
			defs = append(defs, tool.definition)
		}
	}
	return defs
}

// InvokeLocal executes a tool registered on this node.
// Returns ErrToolNotFound if the tool is not registered.
// Handler errors are returned as Go errors (transport-level).
// Tool-level errors are returned in ToolResult.IsError.
func (r *Registry) InvokeLocal(ctx context.Context, toolName string, args json.RawMessage) (*ToolResult, error) {
	r.mu.RLock()
	tool, ok := r.tools[toolName]
	r.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("registry: %w: %s", ErrToolNotFound, toolName)
	}

	return tool.handler(ctx, args)
}

// GetDefinition returns the ToolDefinition for a registered tool.
// Returns nil if the tool is not registered.
func (r *Registry) GetDefinition(toolName string) (*ToolDefinition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tool, ok := r.tools[toolName]
	if !ok {
		return nil, false
	}
	return tool.definition, true
}
