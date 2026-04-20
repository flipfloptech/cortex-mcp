package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cortex-mesh/cortex-mesh/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"
)

// Dispatcher represents the interface to the Cortex Mesh Gateway.
type Dispatcher interface {
	Dispatch(ctx context.Context, toolName string, args json.RawMessage) (*tools.ToolResult, error)
}

// Server wraps the official MCP SDK server.
type Server struct {
	mcpServer  *mcp.Server
	dispatcher Dispatcher
}

// NewServer initializes a new MCP Server mapping to the mesh gateway.
func NewServer(dispatcher Dispatcher) *Server {
	s := mcp.NewServer(&mcp.Implementation{
		Name:    "cortex-mcp",
		Version: "1.0.0",
	}, &mcp.ServerOptions{
		Capabilities: &mcp.ServerCapabilities{
			Tools: &mcp.ToolCapabilities{},
		},
	})

	srv := &Server{
		mcpServer:  s,
		dispatcher: dispatcher,
	}

	// list_tools
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_tools",
		Description: "Discover available tools across the entire Cortex Mesh. Returns a list of tool names, categories, and descriptions.",
	}, srv.handleListTools)

	// tool_help
	mcp.AddTool(s, &mcp.Tool{
		Name:        "tool_help",
		Description: "Get the detailed JSON schema and description for a specific tool in the mesh.",
	}, srv.handleToolHelp)

	// call_tool
	mcp.AddTool(s, &mcp.Tool{
		Name:        "call_tool",
		Description: "Execute a tool on a remote node in the Cortex Mesh.",
	}, srv.handleCallTool)

	return srv
}

// MCPServer returns the underlying SDK server so transports can attach to it.
func (s *Server) MCPServer() *mcp.Server {
	return s.mcpServer
}

// --- Handlers ---

type EmptyInput struct{}

func (s *Server) handleListTools(ctx context.Context, req *mcp.CallToolRequest, input EmptyInput) (*mcp.CallToolResult, any, error) {
	return s.dispatchToMesh(ctx, "list_tools", nil)
}

type ToolHelpInput struct {
	ToolName string `json:"tool_name" jsonschema:"the exact name of the tool to lookup"`
}

func (s *Server) handleToolHelp(ctx context.Context, req *mcp.CallToolRequest, input ToolHelpInput) (*mcp.CallToolResult, any, error) {
	args, _ := json.Marshal(input)
	return s.dispatchToMesh(ctx, "tool_help", args)
}

type CallToolInput struct {
	ToolName string                 `json:"tool_name" jsonschema:"the exact name of the tool to execute"`
	NodeName string                 `json:"node_name,omitempty" jsonschema:"Optional: Specific node ID to run the tool on. Use '*' to fan-out to all nodes, or '@group' for node groups. If empty, auto-routes."`
	Args     map[string]interface{} `json:"args" jsonschema:"The JSON arguments required by the tool"`
}

func (s *Server) handleCallTool(ctx context.Context, req *mcp.CallToolRequest, input CallToolInput) (*mcp.CallToolResult, any, error) {
	args, _ := json.Marshal(input)
	return s.dispatchToMesh(ctx, "call_tool", args)
}

func (s *Server) dispatchToMesh(ctx context.Context, toolName string, args json.RawMessage) (*mcp.CallToolResult, any, error) {
	zap.S().Debugw("MCP tool call received", "name", toolName)

	res, err := s.dispatcher.Dispatch(ctx, toolName, args)
	if err != nil {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{
				&mcp.TextContent{
					Text: fmt.Sprintf("dispatch error: %v", err),
				},
			},
		}, nil, nil
	}

	return &mcp.CallToolResult{
		IsError: res.IsError,
		Content: []mcp.Content{
			&mcp.TextContent{
				Text: string(res.Content),
			},
		},
	}, nil, nil
}
