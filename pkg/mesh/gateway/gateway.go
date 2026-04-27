// Package gateway translates between the LLM-facing MCP protocol
// (JSON-RPC) and the mesh's internal tool protocol. It exposes a small
// set of meta-tools (list_tools, tool_help, call_tool) to the LLM,
// preventing tool list overflow in IDEs and LLMs.
//
// The gateway IS a neuron — it joins the mesh like any other node.
// It just happens to also speak MCP.
package gateway

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/nodeset"
	"github.com/flipfloptech/cortex-mcp/pkg/mesh/tools"
)

// MeshBridge abstracts the mesh operations needed by the gateway
// for remote tool invocation and node discovery.
type MeshBridge interface {
	// InvokeRemote invokes a tool on a specific node via Laser.
	// If nodeID is empty, auto-routes to the lowest-impedance node
	// via Sonar.
	InvokeRemote(ctx context.Context, nodeID, toolName string, args json.RawMessage) (*tools.ToolResult, error)

	// DiscoverNodes finds nodes offering a tool and returns their IDs.
	// Used by the gateway to resolve glob patterns before fan-out.
	DiscoverNodes(ctx context.Context, toolName string) ([]tools.AgentInfo, error)
}

// Gateway is the MCP gateway adapter. It translates between the
// LLM-facing MCP protocol and the mesh's internal tool protocol.
//
// The gateway exposes exactly 3 meta-tools:
//   - list_tools: Discover available tools
//   - tool_help:  Get detailed help for a tool
//   - call_tool:  Invoke a tool (unicast, fan-out, or auto-route)
type Gateway struct {
	registry      *tools.Registry
	mesh          MeshBridge // may be nil (local-only mode)
	groupResolver nodeset.GroupResolver
}

// GatewayOption allows configuring the Gateway.
type GatewayOption func(*Gateway)

// WithGroupResolver configures a custom GroupResolver for expanding
// @group patterns in node_name targets.
func WithGroupResolver(r nodeset.GroupResolver) GatewayOption {
	return func(gw *Gateway) {
		gw.groupResolver = r
	}
}

// New creates a new MCP gateway backed by the given tool registry.
// The mesh parameter enables remote invocation — if nil, only local
// tools can be invoked.
func New(registry *tools.Registry, mesh MeshBridge, opts ...GatewayOption) *Gateway {
	gw := &Gateway{
		registry: registry,
		mesh:     mesh,
	}
	for _, opt := range opts {
		opt(gw)
	}
	return gw
}

// Dispatch routes an MCP tool call to the appropriate meta-tool handler.
// This is the main entry point for processing incoming MCP requests.
func (gw *Gateway) Dispatch(ctx context.Context, toolName string, args json.RawMessage) (*tools.ToolResult, error) {
	switch toolName {
	case "list_tools":
		return gw.HandleListTools(ctx, args)
	case "tool_help":
		return gw.HandleToolHelp(ctx, args)
	case "call_tool":
		return gw.HandleCallTool(ctx, args)
	default:
		return tools.NewErrorResult("unknown meta-tool: " + toolName), nil
	}
}

// isNodesetPattern reports whether s contains nodeset metacharacters
// that indicate a fan-out pattern rather than a plain unicast target.
// Detects globs (*, ?), bracket ranges ([), set operators (!, &, ^),
// and comma-separated unions.
func isNodesetPattern(s string) bool {
	return strings.ContainsAny(s, "*?[!&^,@")
}
