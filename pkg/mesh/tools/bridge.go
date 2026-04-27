package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/routing"
)

// MeshNode abstracts the mesh operations needed by NeuronBridge.
// In production, this is satisfied by *api.Node.
type MeshNode interface {
	// NodeID returns this node's unique identifier.
	NodeID() string

	// Sonar broadcasts a capability query and returns matching agents.
	Sonar(ctx context.Context, capability string) ([]AgentInfo, error)

	// GrpcDialer opens a data stream to the specified node via Laser routing.
	GrpcDialer(ctx context.Context, targetNodeID string) (net.Conn, error)

	// LookupCapability returns nodes offering a capability from the local
	// gossip-populated capability index. Zero network traffic — pure local
	// map read. Results are sorted by impedance (lowest first).
	// Returns an empty slice if no nodes match.
	LookupCapability(capability string) []NodeCapEntry
}

// NodeCapEntry represents a node offering a capability with its impedance.
// Re-exported from routing to avoid package dependency in callers.
type NodeCapEntry = routing.NodeCapEntry

// NeuronBridge is the concrete MeshTransport implementation that wires
// Sonar (capability discovery) and GrpcDialer (stream routing) to the
// tool wire protocol (DialInvoke).
//
// This is the adapter that connects the tools layer to the mesh layer,
// enabling remote tool invocation without the tools package depending
// directly on the api package.
type NeuronBridge struct {
	node MeshNode
}

// NewNeuronBridge creates a NeuronBridge backed by the given mesh node.
func NewNeuronBridge(node MeshNode) *NeuronBridge {
	return &NeuronBridge{node: node}
}

// DiscoverTool finds nodes offering a specific tool.
// First checks the local capability index (zero traffic), then falls
// back to Sonar broadcast if the index has no entries.
//
// The two-tier strategy means:
//   - Steady-state: local index hit (gossip has propagated caps). No traffic.
//   - Cold start / new tool: Sonar broadcast finds it. Index populated on
//     next gossip tick.
func (b *NeuronBridge) DiscoverTool(ctx context.Context, toolName string) ([]AgentInfo, error) {
	capability := "tool:" + toolName

	// Tier 1: Local capability index (zero traffic).
	entries := b.node.LookupCapability(capability)
	if len(entries) > 0 {
		agents := make([]AgentInfo, len(entries))
		for i, e := range entries {
			agents[i] = AgentInfo{
				NodeID:    e.NodeID,
				Impedance: e.Impedance,
			}
		}
		return agents, nil
	}

	// Tier 2: Sonar broadcast (fallback for cold start / multi-hop).
	agents, err := b.node.Sonar(ctx, capability)
	if err != nil {
		return nil, fmt.Errorf("bridge: discover %q: %w", toolName, err)
	}
	return agents, nil
}

// DiscoverNodes is an alias for DiscoverTool that satisfies the
// gateway.MeshBridge interface. Both methods discover nodes offering
// a specific tool via Sonar.
func (b *NeuronBridge) DiscoverNodes(ctx context.Context, toolName string) ([]AgentInfo, error) {
	return b.DiscoverTool(ctx, toolName)
}

// InvokeRemote executes a tool on a specific node via Laser circuit.
// It opens a data stream to the target node, sends a ToolRequest via
// the wire protocol, and reads the ToolResponse.
func (b *NeuronBridge) InvokeRemote(ctx context.Context, nodeID, toolName string, args json.RawMessage) (*ToolResult, error) {
	conn, err := b.node.GrpcDialer(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("bridge: dial %q: %w", nodeID, err)
	}
	defer func() {
		_ = conn.Close()
	}()

	return DialInvoke(ctx, conn, toolName, args)
}

// DiscoverAllTools finds all tools across the mesh via Sonar wildcard.
func (b *NeuronBridge) DiscoverAllTools(ctx context.Context) ([]ToolInfo, error) {
	agents, err := b.node.Sonar(ctx, "tool:*")
	if err != nil {
		return nil, fmt.Errorf("bridge: discover all tools: %w", err)
	}

	// Convert AgentInfo to ToolInfo — each agent is a node offering tools.
	var tools []ToolInfo
	for _, a := range agents {
		tools = append(tools, ToolInfo{
			Nodes: []AgentInfo{a},
		})
	}
	return tools, nil
}
