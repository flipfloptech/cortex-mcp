package registry

import (
	"go.uber.org/zap"
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
	nodeID        string
	supportedMap  map[string]Tool
	supportedList []Tool
	unsupported   map[string]string // name → reason
	allMap        map[string]Tool   // all registered tools, supported or not
}

// NewPluginRegistry creates a registry from the global tool pool.
// Each tool's IsSupported() is evaluated against the local environment.
// Unsupported tools are logged and excluded from the active set.
func NewPluginRegistry(nodeID string) *PluginRegistry {
	globalMu.Lock()
	tools := globalTools
	globalMu.Unlock()

	return NewPluginRegistryFrom(nodeID, tools)
}

// NewPluginRegistryFrom creates a registry from an explicit tool list.
// Used in tests to avoid global state. Each tool's IsSupported() is
// evaluated against the local environment.
func NewPluginRegistryFrom(nodeID string, tools []Tool) *PluginRegistry {
	pr := &PluginRegistry{
		nodeID:        nodeID,
		supportedMap:  make(map[string]Tool),
		supportedList: make([]Tool, 0, len(tools)),
		unsupported:   make(map[string]string),
		allMap:        make(map[string]Tool),
	}

	for _, t := range tools {
		pr.allMap[t.Name()] = t
		ok, reason := t.IsSupported()
		if ok {
			pr.supportedMap[t.Name()] = t
			pr.supportedList = append(pr.supportedList, t)
			zap.S().Debugw("plugin loaded", "tool", t.Name(), "category", t.Category())
		} else {
			pr.unsupported[t.Name()] = reason
			zap.S().Debugw("plugin skipped", "tool", t.Name(), "reason", reason)
		}
	}

	return pr
}

func (pr *PluginRegistry) Supported() []Tool {
	return pr.supportedList
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

func (pr *PluginRegistry) GetTool(name string) (Tool, bool) {
	t, ok := pr.supportedMap[name]
	return t, ok
}

// GetAnyTool returns a tool definition regardless of local support status.
// This is used by the gateway to resolve schemas for remote tools discovered via gossip.
func (pr *PluginRegistry) GetAnyTool(name string) (Tool, bool) {
	t, ok := pr.allMap[name]
	return t, ok
}

// NodeID returns the node identity this registry was built for.
func (pr *PluginRegistry) NodeID() string {
	return pr.nodeID
}
