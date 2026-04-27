// Package nucleus provides the node's self-awareness: identity, capabilities,
// impedance state, and hostname resolution. Every mesh node has a nucleus
// that tracks what it can do and how busy it is.
package nucleus

import (
	"strings"
	"sync"
)

// MinImpedance is the minimum impedance value (idle node).
const MinImpedance = 1.0

// MaxImpedance is the maximum impedance value (saturated node).
const MaxImpedance = 100.0

// Manifest represents a mesh node's identity and runtime state.
// It is goroutine-safe — all methods can be called concurrently.
//
// The manifest tracks:
//   - NodeID: unique identifier (from the TLS certificate CommonName)
//   - Capabilities: what this node can do (roles, tools)
//   - Impedance: how busy this node is (1.0 = idle, 100.0 = saturated)
//   - Addresses: known network addresses for this node
type Manifest struct {
	mu           sync.RWMutex
	nodeID       string
	capabilities map[string]struct{}
	impedance    float64
	addresses    []string
}

// NewManifest creates a new Manifest with the given NodeID.
// The initial impedance is 1.0 (idle) and capabilities are empty.
func NewManifest(nodeID string) *Manifest {
	return &Manifest{
		nodeID:       nodeID,
		capabilities: make(map[string]struct{}),
		impedance:    MinImpedance,
	}
}

// NodeID returns this node's unique identifier.
func (m *Manifest) NodeID() string {
	// nodeID is immutable after construction — no lock needed.
	return m.nodeID
}

// RegisterCapability adds a capability to this node's manifest.
// Capabilities are deduplicated — registering the same capability
// multiple times is a no-op.
//
// Examples: "role:mds", "role:oss", "tool:lustre_health", "tool:lnet_status"
func (m *Manifest) RegisterCapability(capability string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.capabilities[capability] = struct{}{}
}

// HasCapability reports whether this node has the given capability.
// Supports wildcard matching: "tool:*" matches any capability starting
// with "tool:", enabling discovery of all tools without knowing names.
func (m *Manifest) HasCapability(capability string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Wildcard match: "tool:*" matches any "tool:xxx".
	if strings.HasSuffix(capability, ":*") {
		prefix := strings.TrimSuffix(capability, "*")
		for cap := range m.capabilities {
			if strings.HasPrefix(cap, prefix) {
				return true
			}
		}
		return false
	}

	// Exact match.
	_, ok := m.capabilities[capability]
	return ok
}

// Capabilities returns a copy of this node's registered capabilities.
// The returned slice is safe to mutate — it does not reference internal state.
func (m *Manifest) Capabilities() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	caps := make([]string, 0, len(m.capabilities))
	for cap := range m.capabilities {
		caps = append(caps, cap)
	}
	return caps
}

// SetImpedance updates this node's impedance value.
// The value is clamped to [1.0, 100.0]:
//   - 1.0 = idle (lowest possible impedance)
//   - 100.0 = saturated (highest possible impedance)
//
// Impedance is updated continuously by the impedance calculator
// based on CPU load, stream count, and memory pressure.
func (m *Manifest) SetImpedance(value float64) {
	if value < MinImpedance {
		value = MinImpedance
	}
	if value > MaxImpedance {
		value = MaxImpedance
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.impedance = value
}

// Impedance returns this node's current impedance value.
func (m *Manifest) Impedance() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.impedance
}

// SetAddresses updates this node's known network addresses.
// The input slice is copied — mutations to the original do not affect
// the manifest.
func (m *Manifest) SetAddresses(addrs []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.addresses = make([]string, len(addrs))
	copy(m.addresses, addrs)
}

// ManifestSnapshot is an immutable, serializable snapshot of a node's manifest.
// Used for gossip exchange and routing table updates.
type ManifestSnapshot struct {
	NodeID       string   `json:"node_id"`
	Capabilities []string `json:"capabilities"`
	Impedance    float64  `json:"impedance"`
	Addresses    []string `json:"addresses,omitempty"`
}

// Snapshot returns an immutable, JSON-serializable copy of this manifest's
// current state. Used for gossip exchange over Stream 0.
func (m *Manifest) Snapshot() ManifestSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	caps := make([]string, 0, len(m.capabilities))
	for cap := range m.capabilities {
		caps = append(caps, cap)
	}

	addrs := make([]string, len(m.addresses))
	copy(addrs, m.addresses)

	return ManifestSnapshot{
		NodeID:       m.nodeID,
		Capabilities: caps,
		Impedance:    m.impedance,
		Addresses:    addrs,
	}
}
