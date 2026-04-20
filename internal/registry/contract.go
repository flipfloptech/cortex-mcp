// Package registry provides the tool plugin framework for cortex-mcp.
//
// Each tool is a self-contained unit that implements the Tool interface.
// Tools register themselves at program startup via init() calls to Register().
// At runtime, the PluginRegistry evaluates each tool's IsSupported() method
// against the local environment to build the active tool set.
//
// This is the "fat binary" pattern: all tools are compiled in, but only
// the ones supported on this node are activated and advertised to the mesh.
package registry

import (
	"context"
	"encoding/json"
)

// Tool is the contract that every tool plugin must implement.
// Each tool is a self-contained unit: it knows its identity, its
// requirements, and how to execute.
type Tool interface {
	// Name returns the unique tool identifier (e.g., "lustre_mds_health").
	Name() string

	// Description returns a short, one-line summary for list_tools output.
	Description() string

	// Help returns the full tool help text. This is rendered by tool_help
	// and should include:
	//   - What the tool does and why
	//   - What kind of filtering/analysis it performs (deterministic vs heuristic)
	//   - Output format description
	//   - Any caveats or limitations
	Help() string

	// Category returns the tool category (e.g., "lustre", "system", "network").
	Category() string

	// Parameters returns the parameter schema for this tool.
	Parameters() []ToolParam

	// IsSupported inspects the local environment and returns true if this
	// tool can operate on this node. This is called once at startup.
	// The reason string explains WHY the tool is unsupported (for logging).
	//
	// Examples of checks:
	//   - OS/distro detection (e.g., Rocky Linux, Ubuntu)
	//   - Binary availability (e.g., lctl, nvidia-smi)
	//   - Node type detection (e.g., MDS, OSS, client)
	//   - Config file presence (e.g., /etc/lustre/)
	//   - Path requirements (e.g., /proc/fs/lustre/)
	IsSupported() (supported bool, reason string)

	// Execute runs the tool with the given arguments.
	// Must respect context cancellation.
	Execute(ctx context.Context, args json.RawMessage) (*ToolResult, error)
}

// ToolParam describes a single parameter accepted by a tool.
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
