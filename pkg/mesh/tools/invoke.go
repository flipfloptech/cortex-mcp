package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// ErrNoNodesAvailable is returned when Sonar finds no nodes offering
// the requested tool capability.
var ErrNoNodesAvailable = errors.New("no nodes available for tool")

// MeshTransport abstracts the mesh operations needed for remote tool
// invocation. This interface decouples the tools layer from the Neuron
// implementation, enabling deterministic testing with mocks.
//
// In production, this is implemented by a thin adapter over Neuron's
// Sonar and GrpcDialer methods.
type MeshTransport interface {
	// DiscoverTool finds nodes offering a specific tool via Sonar.
	DiscoverTool(ctx context.Context, toolName string) ([]AgentInfo, error)

	// InvokeRemote executes a tool on a specific node via Laser.
	InvokeRemote(ctx context.Context, nodeID, toolName string, args json.RawMessage) (*ToolResult, error)

	// DiscoverAllTools finds all tools across the mesh.
	DiscoverAllTools(ctx context.Context) ([]ToolInfo, error)
}

// RemoteInvoker handles mesh-wide tool discovery and remote invocation.
// It composes with MeshTransport (which wraps Neuron's Sonar and Laser).
type RemoteInvoker struct {
	mesh MeshTransport
}

// NewRemoteInvoker creates a new remote invoker backed by the given
// mesh transport. The transport handles the actual network operations.
func NewRemoteInvoker(mesh MeshTransport) *RemoteInvoker {
	return &RemoteInvoker{mesh: mesh}
}

// Invoke executes a tool on the mesh.
//
// If nodeID is non-empty, the tool is invoked directly on that node
// via Laser (unicast).
//
// If nodeID is empty, Sonar is used to discover the lowest-impedance
// node offering the tool, and the invocation is auto-routed there.
func (ri *RemoteInvoker) Invoke(ctx context.Context, nodeID, toolName string, args json.RawMessage) (*ToolResult, error) {
	if nodeID == "" {
		// Auto-route: find the best node via Sonar.
		agents, err := ri.mesh.DiscoverTool(ctx, toolName)
		if err != nil {
			return nil, fmt.Errorf("invoke: discover %q: %w", toolName, err)
		}
		if len(agents) == 0 {
			return nil, fmt.Errorf("invoke: %w: %s", ErrNoNodesAvailable, toolName)
		}

		// Pick the lowest-impedance node.
		sort.Slice(agents, func(i, j int) bool {
			return agents[i].Impedance < agents[j].Impedance
		})
		nodeID = agents[0].NodeID
	}

	return ri.mesh.InvokeRemote(ctx, nodeID, toolName, args)
}

// FanOut executes a tool across multiple nodes concurrently.
// Results are returned per-node. Individual node failures do NOT
// cause the entire fan-out to fail — they are captured in NodeResult.Error.
func (ri *RemoteInvoker) FanOut(ctx context.Context, nodeIDs []string, toolName string, args json.RawMessage) ([]NodeResult, error) {
	if len(nodeIDs) == 0 {
		return nil, nil
	}

	results := make([]NodeResult, len(nodeIDs))
	var wg sync.WaitGroup

	for i, nodeID := range nodeIDs {
		wg.Add(1)
		go func(idx int, node string) {
			defer wg.Done()
			result, err := ri.mesh.InvokeRemote(ctx, node, toolName, args)
			results[idx] = NodeResult{
				NodeID: node,
				Result: result,
				Error:  err,
			}
		}(i, nodeID)
	}

	wg.Wait()
	return results, nil
}

// ListAll discovers all tools across the mesh via Sonar.
// Returns tool definitions enriched with per-node availability.
func (ri *RemoteInvoker) ListAll(ctx context.Context) ([]ToolInfo, error) {
	return ri.mesh.DiscoverAllTools(ctx)
}
