package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/nodeset"
	"github.com/flipfloptech/cortex-mcp/pkg/mesh/tools"
)

// --- Response types ---

// ListToolsResponse is the response from the list_tools meta-tool.
type ListToolsResponse struct {
	Tools []ListToolsEntry `json:"tools"`
}

// ListToolsEntry is a single tool in the list_tools response.
type ListToolsEntry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`
}

// ToolHelpResponse is the response from the tool_help meta-tool.
type ToolHelpResponse struct {
	Name            string            `json:"name"`
	Description     string            `json:"description"`
	LongDescription string            `json:"long_description,omitempty"`
	Category        string            `json:"category"`
	Parameters      []tools.ToolParam `json:"parameters,omitempty"`
	InputSchema     json.RawMessage   `json:"input_schema,omitempty"`
}

// CallToolResponse is the response from call_tool for remote invocations.
// Both unicast and fan-out return a results array for consistency.
type CallToolResponse struct {
	Results []NodeResult `json:"results"`
}

// NodeResult is a single node's result in a call_tool response.
type NodeResult struct {
	NodeID  string          `json:"node_id"`
	Content json.RawMessage `json:"content,omitempty"`
	IsError bool            `json:"is_error"`
	Error   string          `json:"error,omitempty"`
}

// --- Meta-tool definitions ---

// MetaTools returns the tool definitions for all gateway meta-tools.
// These are the only tools exposed to the LLM via MCP.
func (gw *Gateway) MetaTools() []*tools.ToolDefinition {
	return []*tools.ToolDefinition{
		{
			Name:        "list_tools",
			Description: "Discover available tools across the mesh",
			Category:    "meta",
			Parameters: []tools.ToolParam{
				{Name: "category", Type: "string", Description: "Filter by category (e.g., 'lustre', 'network', 'system')", Required: false},
			},
		},
		{
			Name:        "tool_help",
			Description: "Get detailed help for a specific tool",
			Category:    "meta",
			Parameters: []tools.ToolParam{
				{Name: "tool_name", Type: "string", Description: "Name of the tool to get help for", Required: true},
			},
		},
		{
			Name:        "call_tool",
			Description: "Invoke a tool in the mesh",
			Category:    "meta",
			Parameters: []tools.ToolParam{
				{Name: "tool_name", Type: "string", Description: "Name of the tool to invoke", Required: true},
				{Name: "node_name", Type: "string", Description: "Target node(s). Exact name for unicast, nodeset pattern (e.g., '*', 'mds-*', 'node[1-10]', 'oss[01-72]!oss[10-15]') for fan-out, omit for auto-route to best node", Required: false},
				{Name: "args", Type: "object", Description: "Tool-specific arguments", Required: false},
			},
		},
	}
}

// --- Meta-tool handlers ---

// listToolsArgs is the parsed arguments for list_tools.
type listToolsArgs struct {
	Category string `json:"category"`
}

// HandleListTools handles the list_tools meta-tool.
// Returns all registered tools, optionally filtered by category.
func (gw *Gateway) HandleListTools(ctx context.Context, args json.RawMessage) (*tools.ToolResult, error) {
	var params listToolsArgs
	if len(args) > 0 {
		_ = json.Unmarshal(args, &params)
	}

	var defs []*tools.ToolDefinition
	if params.Category != "" {
		defs = gw.registry.ListLocalByCategory(params.Category)
	} else {
		defs = gw.registry.ListLocal()
	}

	entries := make([]ListToolsEntry, len(defs))
	for i, d := range defs {
		entries[i] = ListToolsEntry{
			Name:        d.Name,
			Description: d.Description,
			Category:    d.Category,
		}
	}

	return tools.NewJSONResult(ListToolsResponse{Tools: entries}), nil
}

// toolHelpArgs is the parsed arguments for tool_help.
type toolHelpArgs struct {
	ToolName string `json:"tool_name"`
}

// HandleToolHelp handles the tool_help meta-tool.
// Returns the full definition, parameters, and input schema for a tool.
func (gw *Gateway) HandleToolHelp(ctx context.Context, args json.RawMessage) (*tools.ToolResult, error) {
	var params toolHelpArgs
	if len(args) > 0 {
		_ = json.Unmarshal(args, &params)
	}

	if params.ToolName == "" {
		return tools.NewErrorResult("tool_help requires 'tool_name' parameter"), nil
	}

	def, ok := gw.registry.GetDefinition(params.ToolName)
	if !ok {
		return tools.NewErrorResult(fmt.Sprintf("tool not found: %s", params.ToolName)), nil
	}

	response := ToolHelpResponse{
		Name:            def.Name,
		Description:     def.Description,
		LongDescription: def.LongDescription,
		Category:        def.Category,
		Parameters:      def.Parameters,
		InputSchema:     def.EffectiveSchema(),
	}

	return tools.NewJSONResult(response), nil
}

// callToolArgs is the parsed arguments for call_tool.
type callToolArgs struct {
	ToolName string          `json:"tool_name"`
	NodeName string          `json:"node_name"`
	Args     json.RawMessage `json:"args"`
}

// HandleCallTool handles the call_tool meta-tool.
//
// Dispatch modes based on node_name:
//   - Omitted: auto-route via Sonar to the lowest-impedance node
//   - Exact name: unicast to that specific node
//   - Nodeset pattern (*, ?, [], !, &, ^, comma): fan-out to all matching nodes
func (gw *Gateway) HandleCallTool(ctx context.Context, args json.RawMessage) (*tools.ToolResult, error) {
	var params callToolArgs
	if len(args) > 0 {
		_ = json.Unmarshal(args, &params)
	}

	if params.ToolName == "" {
		return tools.NewErrorResult("call_tool requires 'tool_name' parameter"), nil
	}

	// No node_name → auto-route or local.
	if params.NodeName == "" {
		return gw.handleAutoRoute(ctx, params)
	}

	// Nodeset pattern → fan-out.
	if isNodesetPattern(params.NodeName) {
		return gw.handleFanOut(ctx, params)
	}

	// Exact node_name → unicast.
	return gw.handleUnicast(ctx, params)
}

// handleAutoRoute invokes a tool with no specific target.
// Tries local invocation first (if the tool is registered locally),
// then falls back to remote auto-routing via the capability index / Sonar.
func (gw *Gateway) handleAutoRoute(ctx context.Context, params callToolArgs) (*tools.ToolResult, error) {
	// Try local invocation first — zero network overhead.
	if _, ok := gw.registry.GetDefinition(params.ToolName); ok {
		result, err := gw.registry.InvokeLocal(ctx, params.ToolName, params.Args)
		if err != nil {
			return tools.NewErrorResult(err.Error()), nil
		}
		return result, nil
	}

	// Tool not available locally — auto-route via mesh.
	if gw.mesh == nil {
		return tools.NewErrorResult(fmt.Sprintf("tool %q not found locally and no mesh connection", params.ToolName)), nil
	}

	// Discover all nodes offering this tool.
	agents, err := gw.mesh.DiscoverNodes(ctx, params.ToolName)
	if err != nil {
		return tools.NewErrorResult(fmt.Sprintf("auto-route failed (discovery): %v", err)), nil
	}
	if len(agents) == 0 {
		return tools.NewErrorResult(fmt.Sprintf("auto-route failed: no nodes available for tool %q", params.ToolName)), nil
	}

	// For simple auto-routing, we just pick the first available node.
	// Sonar returns agents sorted by impedance/distance.
	targetNode := agents[0].NodeID

	result, err := gw.mesh.InvokeRemote(ctx, targetNode, params.ToolName, params.Args)
	if err != nil {
		return tools.NewErrorResult(fmt.Sprintf("auto-route failed on %q: %v", targetNode, err)), nil
	}
	return result, nil
}

// handleUnicast invokes a tool on a specific named node.
func (gw *Gateway) handleUnicast(ctx context.Context, params callToolArgs) (*tools.ToolResult, error) {
	if gw.mesh == nil {
		return tools.NewErrorResult("remote invocation not available (no mesh connection)"), nil
	}

	result, err := gw.mesh.InvokeRemote(ctx, params.NodeName, params.ToolName, params.Args)
	if err != nil {
		return tools.NewErrorResult(fmt.Sprintf("invoke on %q failed: %v", params.NodeName, err)), nil
	}

	// Wrap in CallToolResponse for consistent shape.
	nr := NodeResult{
		NodeID:  params.NodeName,
		IsError: result.IsError,
		Content: result.Content,
	}
	return tools.NewJSONResult(CallToolResponse{Results: []NodeResult{nr}}), nil
}

// handleFanOut invokes a tool across all nodes matching a glob pattern.
func (gw *Gateway) handleFanOut(ctx context.Context, params callToolArgs) (*tools.ToolResult, error) {
	if gw.mesh == nil {
		return tools.NewErrorResult("fan-out not available (no mesh connection)"), nil
	}

	// Discover all nodes offering this tool.
	agents, err := gw.mesh.DiscoverNodes(ctx, params.ToolName)
	if err != nil {
		return tools.NewErrorResult(fmt.Sprintf("discover nodes for %q: %v", params.ToolName, err)), nil
	}

	// Filter by glob pattern.
	var opts []nodeset.Option
	if gw.groupResolver != nil {
		opts = append(opts, nodeset.WithGroupResolver(gw.groupResolver))
	}
	var matched []string
	for _, a := range agents {
		ok, matchErr := nodeset.Match(params.NodeName, a.NodeID, opts...)
		if matchErr != nil {
			return tools.NewErrorResult(fmt.Sprintf("invalid pattern %q: %v", params.NodeName, matchErr)), nil
		}
		if ok {
			matched = append(matched, a.NodeID)
		}
	}

	if len(matched) == 0 {
		return tools.NewErrorResult(fmt.Sprintf("no nodes matching %q have tool %q", params.NodeName, params.ToolName)), nil
	}

	// Concurrent fan-out.
	results := make([]NodeResult, len(matched))
	var wg sync.WaitGroup

	for i, nodeID := range matched {
		wg.Add(1)
		go func(idx int, node string) {
			defer wg.Done()
			result, invokeErr := gw.mesh.InvokeRemote(ctx, node, params.ToolName, params.Args)

			r := NodeResult{NodeID: node}
			if invokeErr != nil {
				r.IsError = true
				r.Error = invokeErr.Error()
			} else if result != nil {
				r.Content = result.Content
				r.IsError = result.IsError
			}
			results[idx] = r
		}(i, nodeID)
	}

	wg.Wait()

	return tools.NewJSONResult(CallToolResponse{Results: results}), nil
}
