// Package tools provides the distributed tool framework for the mesh.
// It handles tool registration, discovery, and invocation. Each node
// registers callable tools locally; the framework handles mesh-wide
// discovery via Sonar and remote invocation via Laser circuits.
//
// The tools package composes with the Neuron interface — it is NOT
// part of Neuron. This separation keeps the core mesh interface minimal.
package tools

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
)

// ToolDefinition describes a callable tool that a node can execute locally.
// Tool definitions are shared across the mesh during discovery.
// Each tool is self-documenting — all help info travels with the definition.
type ToolDefinition struct {
	// Name is the unique tool identifier (e.g., "lustre_health", "lnet_status").
	Name string `json:"name"`

	// Description is a short, one-line summary of what the tool does.
	// Used in list_tools output and search results.
	Description string `json:"description"`

	// LongDescription is a detailed explanation of the tool's behavior,
	// expected output, and usage notes. Rendered by tool_help.
	// Optional — if empty, tool_help falls back to Description.
	LongDescription string `json:"long_description,omitempty"`

	// Category groups related tools (e.g., "lustre", "network", "storage", "system").
	Category string `json:"category"`

	// Hidden marks the tool as invisible to list_tools discovery.
	// Hidden tools are still callable via call_tool — they are operational
	// tools (e.g., lifecycle management) that should not clutter the LLM's
	// tool namespace. tool_help will still work for hidden tools.
	Hidden bool `json:"hidden,omitempty"`

	// Parameters describes each accepted parameter with type and help text.
	// This is the structured help system — tool_help renders these as a
	// human-readable parameter table for the LLM.
	Parameters []ToolParam `json:"parameters,omitempty"`

	// InputSchema is a JSON Schema describing the tool's accepted parameters.
	// This is MCP-compatible: the schema is passed through to the LLM as-is.
	// If nil, auto-generated from Parameters via GenerateSchema().
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// schemaStore caches generated schemas keyed by *ToolDefinition pointer.
// This avoids embedding sync primitives (sync.Once, atomic.Pointer) in
// ToolDefinition, which would break go vet's noCopy checker when
// ToolDefinition is passed by value (e.g., into Register).
var schemaStore sync.Map // map[*ToolDefinition]json.RawMessage

// GenerateSchema creates a JSON Schema from the tool's Parameters.
// The generated schema is a JSON object with properties for each parameter
// and a required array for required parameters.
func (d *ToolDefinition) GenerateSchema() json.RawMessage {
	schema := map[string]interface{}{
		"type": "object",
	}

	if len(d.Parameters) > 0 {
		props := make(map[string]interface{})
		var required []string

		for _, p := range d.Parameters {
			prop := map[string]interface{}{
				"type":        p.Type,
				"description": p.Description,
			}
			if p.Default != "" {
				prop["default"] = p.Default
			}
			props[p.Name] = prop

			if p.Required {
				required = append(required, p.Name)
			}
		}

		schema["properties"] = props
		if len(required) > 0 {
			schema["required"] = required
		}
	}

	raw, _ := json.Marshal(schema)
	return raw
}

// EffectiveSchema returns the InputSchema if set, otherwise generates
// one from Parameters and caches the result. Subsequent calls return
// the cached bytes — zero allocations on the hot path.
//
// Thread-safe: uses sync.Once internally. Safe to call from multiple
// goroutines concurrently (e.g., during Sonar tool discovery).
func (d *ToolDefinition) EffectiveSchema() json.RawMessage {
	if d.InputSchema != nil {
		return d.InputSchema
	}

	// Check the package-level cache first.
	if cached, ok := schemaStore.Load(d); ok {
		return cached.(json.RawMessage) //nolint:errcheck // type is guaranteed
	}

	// Generate and cache. LoadOrStore is atomic — if another goroutine
	// raced us, we get their result and discard ours.
	generated := d.GenerateSchema()
	actual, _ := schemaStore.LoadOrStore(d, generated)
	return actual.(json.RawMessage) //nolint:errcheck // type is guaranteed
}

// ToolParam describes a single parameter accepted by a tool.
// Used by the help system to render parameter documentation.
type ToolParam struct {
	// Name is the parameter key (e.g., "verbose", "lines", "interface").
	Name string `json:"name"`

	// Type is the JSON Schema type (e.g., "string", "boolean", "number", "integer").
	Type string `json:"type"`

	// Description explains what the parameter controls.
	Description string `json:"description"`

	// Required indicates whether the parameter must be provided.
	Required bool `json:"required"`

	// Default is the default value if the parameter is omitted (as a string).
	// Empty string means no default.
	Default string `json:"default,omitempty"`
}

// ToolHandler is the function signature for local tool execution.
// It receives the raw JSON arguments and returns a result or error.
// Handlers execute locally on the node where the tool is registered.
type ToolHandler func(ctx context.Context, args json.RawMessage) (*ToolResult, error)

// ToolResult is the outcome of a tool invocation.
type ToolResult struct {
	// Content is the tool's output, serialized as JSON.
	// This is passed through to the LLM via the MCP gateway.
	Content json.RawMessage `json:"content"`

	// IsError indicates that the tool execution itself failed
	// (as opposed to a transport/routing failure, which surfaces as a Go error).
	IsError bool `json:"is_error"`
}

// NewErrorResult creates a ToolResult indicating a tool-level error.
func NewErrorResult(message string) *ToolResult {
	content, _ := json.Marshal(map[string]string{"error": message})
	return &ToolResult{
		Content: content,
		IsError: true,
	}
}

// NewJSONResult creates a ToolResult from any JSON-serializable value.
func NewJSONResult(v interface{}) *ToolResult {
	content, _ := json.Marshal(v)
	return &ToolResult{
		Content: content,
		IsError: false,
	}
}

// NewTextResult creates a ToolResult containing a text string.
func NewTextResult(text string) *ToolResult {
	content, _ := json.Marshal(map[string]string{"text": text})
	return &ToolResult{
		Content: content,
		IsError: false,
	}
}

// AgentInfo describes a mesh node with a capability.
// Used in Sonar responses and tool discovery results.
type AgentInfo struct {
	NodeID    string  `json:"node_id"`
	Impedance float64 `json:"impedance"` // 1.0 = idle, 100.0 = saturated
}

// ToolInfo is a tool definition enriched with mesh-wide availability info.
type ToolInfo struct {
	Definition ToolDefinition `json:"definition"`

	// Nodes lists all NodeIDs that offer this tool, ordered by impedance (ascending).
	Nodes []AgentInfo `json:"nodes"`
}

// SortNodesByImpedance orders the nodes by ascending impedance
// (lightest/idlest first).
func (ti *ToolInfo) SortNodesByImpedance() {
	sort.Slice(ti.Nodes, func(i, j int) bool {
		return ti.Nodes[i].Impedance < ti.Nodes[j].Impedance
	})
}

// NodeResult is the outcome of a tool invocation on a single node
// during a fan-out operation.
type NodeResult struct {
	NodeID string      `json:"node_id"`
	Result *ToolResult `json:"result,omitempty"`
	Error  error       `json:"error,omitempty"` // nil if invocation succeeded (even if Result.IsError is true)
}
