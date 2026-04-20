package registry

import (
	"log/slog"
	"sync"
)

// globalTools holds all tools registered via init() at program startup.
// This is the "fat binary manifest" — every tool package's init() calls
// Register() to add itself here.
var (
	globalMu    sync.Mutex
	globalTools []Tool
)

// Register adds a tool to the global pool. Called from each tool package's
// init() function at program startup. Thread-safe for init() ordering.
func Register(t Tool) {
	globalMu.Lock()
	globalTools = append(globalTools, t)
	globalMu.Unlock()
}

// PluginRegistry manages the set of supported tools for this node.
// It evaluates each tool's IsSupported() at construction time and
// partitions tools into supported (active) and unsupported (logged) sets.
type PluginRegistry struct {
	nodeID      string
	supported   map[string]Tool
	unsupported map[string]string // name → reason
}

// NewPluginRegistry creates a registry from the global tool pool.
// Each tool's IsSupported() is evaluated against the local environment.
// Unsupported tools are logged and excluded from the active set.
func NewPluginRegistry(nodeID string) *PluginRegistry {
	globalMu.Lock()
	tools := make([]Tool, len(globalTools))
	copy(tools, globalTools)
	globalMu.Unlock()

	return NewPluginRegistryFrom(nodeID, tools)
}

// NewPluginRegistryFrom creates a registry from an explicit tool list.
// Used in tests to avoid global state. Each tool's IsSupported() is
// evaluated against the local environment.
func NewPluginRegistryFrom(nodeID string, tools []Tool) *PluginRegistry {
	pr := &PluginRegistry{
		nodeID:      nodeID,
		supported:   make(map[string]Tool),
		unsupported: make(map[string]string),
	}

	for _, t := range tools {
		ok, reason := t.IsSupported()
		if ok {
			pr.supported[t.Name()] = t
			slog.Debug("plugin loaded", "tool", t.Name(), "category", t.Category())
		} else {
			pr.unsupported[t.Name()] = reason
			slog.Debug("plugin skipped", "tool", t.Name(), "reason", reason)
		}
	}

	return pr
}

// Supported returns all tools that passed IsSupported().
func (pr *PluginRegistry) Supported() []Tool {
	tools := make([]Tool, 0, len(pr.supported))
	for _, t := range pr.supported {
		tools = append(tools, t)
	}
	return tools
}

// Unsupported returns a map of tool name → reason for all tools
// that failed IsSupported().
func (pr *PluginRegistry) Unsupported() map[string]string {
	out := make(map[string]string, len(pr.unsupported))
	for k, v := range pr.unsupported {
		out[k] = v
	}
	return out
}

// GetTool returns a supported tool by name.
func (pr *PluginRegistry) GetTool(name string) (Tool, bool) {
	t, ok := pr.supported[name]
	return t, ok
}

// NodeID returns the node identity this registry was built for.
func (pr *PluginRegistry) NodeID() string {
	return pr.nodeID
}
