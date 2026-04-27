package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
	"github.com/flipfloptech/cortex-mcp/pkg/mesh/gateway"
	"github.com/flipfloptech/cortex-mcp/pkg/mesh/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"
)

// Dispatcher represents the capability to invoke mesh meta-tools.
// In production, this is the cortex-mesh Gateway.
type Dispatcher interface {
	Dispatch(ctx context.Context, toolName string, args json.RawMessage) (*tools.ToolResult, error)
}

// Server wraps the official MCP SDK server.
type Server struct {
	mcpServer    *mcp.Server
	dispatcher   Dispatcher
	topology     TopologyProvider
	plugins      *registry.PluginRegistry
	meshOverview *MeshOverviewHandler
}

// NewServer initializes a new MCP Server mapping to the mesh gateway.
// topology may be nil if the mesh is not yet available (local-only mode).
func NewServer(dispatcher Dispatcher, topology TopologyProvider, plugins *registry.PluginRegistry) *Server {
	s := mcp.NewServer(&mcp.Implementation{
		Name:    "cortex-mcp",
		Version: "1.0.0",
	}, &mcp.ServerOptions{
		Capabilities: &mcp.ServerCapabilities{
			Tools:   &mcp.ToolCapabilities{},
			Prompts: &mcp.PromptCapabilities{},
		},
	})

	srv := &Server{
		mcpServer:  s,
		dispatcher: dispatcher,
		topology:   topology,
		plugins:    plugins,
	}

	// Build cluster overview handler if topology is available.
	if topology != nil {
		srv.meshOverview = NewMeshOverviewHandler(dispatcher, topology)
	}

	// get_tool_list
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_tool_list",
		Description: "Discover available tools across the entire Cortex Mesh. Returns a list of tool names, categories, and descriptions.",
	}, srv.handleListTools)

	// get_tool_help
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_tool_help",
		Description: "Get the detailed JSON schema and description for a specific tool in the mesh.",
	}, srv.handleToolHelp)

	// call_tool
	mcp.AddTool(s, &mcp.Tool{
		Name:        "call_tool",
		Description: "Execute a tool on a remote node in the Cortex Mesh.",
	}, srv.handleCallTool)

	// get_mesh_overview
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_mesh_overview",
		Description: "Get a complete cluster topology: every node, its role (SFA/MGS/MDS/OSS/Client), connectivity, tools, and a Mermaid topology diagram. Fans out to all nodes and aggregates.",
	}, srv.handleMeshOverview)

	// system_introduction prompt
	s.AddPrompt(&mcp.Prompt{
		Name:        "system_introduction",
		Description: "An onboarding guide for LLMs explaining how to interact with the Cortex Mesh.",
	}, srv.handleSystemIntroduction)

	return srv
}

// MCPServer returns the underlying SDK server so transports can attach to it.
func (s *Server) MCPServer() *mcp.Server {
	return s.mcpServer
}

// --- Handlers ---

type EmptyInput struct{}

func (s *Server) handleListTools(ctx context.Context, req *mcp.CallToolRequest, input EmptyInput) (*mcp.CallToolResult, any, error) {
	if s.topology == nil || s.plugins == nil {
		return s.dispatchToMesh(ctx, "get_tool_list", nil)
	}

	snap := s.topology.MeshTopology()

	// Collect unique active tools from all nodes
	activeTools := make(map[string]struct{})
	for _, node := range snap.NodeDetails {
		for _, cap := range node.Capabilities {
			if strings.HasPrefix(cap, "tool:") {
				toolName := strings.TrimPrefix(cap, "tool:")
				activeTools[toolName] = struct{}{}
			}
		}
	}
	// Lookup schema for each active tool
	entries := make([]gateway.ListToolsEntry, 0)
	for toolName := range activeTools {
		if tool, ok := s.plugins.GetAnyTool(toolName); ok && !tool.Hidden() {
			entries = append(entries, gateway.ListToolsEntry{
				Name:        tool.Name(),
				Description: tool.Description(),
				Category:    tool.Category(),
			})
		}
	}

	data, _ := json.Marshal(gateway.ListToolsResponse{Tools: entries})
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(data)},
		},
	}, nil, nil
}

type ToolHelpInput struct {
	ToolName string `json:"tool_name" jsonschema:"the exact name of the tool to lookup"`
}

func (s *Server) handleToolHelp(ctx context.Context, req *mcp.CallToolRequest, input ToolHelpInput) (*mcp.CallToolResult, any, error) {
	if s.plugins == nil {
		args, _ := json.Marshal(input)
		return s.dispatchToMesh(ctx, "get_tool_help", args)
	}

	tool, ok := s.plugins.GetAnyTool(input.ToolName)
	if !ok {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{
				&mcp.TextContent{Text: fmt.Sprintf("tool not found: %s", input.ToolName)},
			},
		}, nil, nil
	}
	var meshParams []tools.ToolParam
	for _, p := range tool.Parameters() {
		meshParams = append(meshParams, tools.ToolParam{
			Name:        p.Name,
			Type:        p.Type,
			Description: p.Description,
			Required:    p.Required,
			Default:     p.Default,
		})
	}

	def := tools.ToolDefinition{
		Name:            tool.Name(),
		Description:     tool.Description(),
		LongDescription: tool.Help(),
		Category:        tool.Category(),
		Parameters:      meshParams,
	}

	response := gateway.ToolHelpResponse{
		Name:            def.Name,
		Description:     def.Description,
		LongDescription: def.LongDescription,
		Category:        def.Category,
		Parameters:      def.Parameters,
		InputSchema:     def.EffectiveSchema(),
	}

	data, _ := json.Marshal(response)
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(data)},
		},
	}, nil, nil
}

type CallToolInput struct {
	ToolName string                 `json:"tool_name" jsonschema:"the exact name of the tool to execute"`
	NodeName string                 `json:"node_name,omitempty" jsonschema:"Optional: Specific node ID to run the tool on. Use '*' to fan-out to all nodes, or '@group' for node groups. If empty, auto-routes."`
	Args     map[string]interface{} `json:"args,omitempty" jsonschema:"Optional: The JSON arguments required by the tool"`
}

func (s *Server) handleCallTool(ctx context.Context, req *mcp.CallToolRequest, input CallToolInput) (*mcp.CallToolResult, any, error) {
	if s.plugins != nil {
		if tool, ok := s.plugins.GetAnyTool(input.ToolName); ok {
			// Validate required arguments and types
			for _, param := range tool.Parameters() {
				val, exists := input.Args[param.Name]
				if param.Required && (!exists || val == nil) {
					return &mcp.CallToolResult{
						IsError: true,
						Content: []mcp.Content{
							&mcp.TextContent{
								Text: fmt.Sprintf("invalid arguments for '%s': missing required argument '%s'. Please call 'get_tool_help' with tool_name='%s' to see the full schema.", input.ToolName, param.Name, input.ToolName),
							},
						},
					}, nil, nil
				}
				if exists && val != nil {
					valid := true
					var expectedType string
					switch param.Type {
					case "string":
						_, valid = val.(string)
						expectedType = "string"
					case "boolean":
						_, valid = val.(bool)
						expectedType = "boolean"
					case "number":
						_, valid = val.(float64)
						expectedType = "number"
					case "integer":
						if f, ok := val.(float64); ok {
							valid = f == float64(int(f))
						} else {
							valid = false
						}
						expectedType = "integer"
					case "array":
						_, valid = val.([]interface{})
						expectedType = "array"
					}

					if !valid && expectedType != "" {
						return &mcp.CallToolResult{
							IsError: true,
							Content: []mcp.Content{
								&mcp.TextContent{
									Text: fmt.Sprintf("invalid arguments for '%s': argument '%s' must be of type '%s'. Please call 'get_tool_help' with tool_name='%s' to see the full schema.", input.ToolName, param.Name, expectedType, input.ToolName),
								},
							},
						}, nil, nil
					}
				}
			}

			// Check for unknown arguments
			for argName := range input.Args {
				known := false
				for _, param := range tool.Parameters() {
					if param.Name == argName {
						known = true
						break
					}
				}
				if !known {
					return &mcp.CallToolResult{
						IsError: true,
						Content: []mcp.Content{
							&mcp.TextContent{
								Text: fmt.Sprintf("invalid arguments for '%s': unknown argument '%s'. Please call 'get_tool_help' with tool_name='%s' to see the full schema.", input.ToolName, argName, input.ToolName),
							},
						},
					}, nil, nil
				}
			}
		}
	}

	args, _ := json.Marshal(input)
	return s.dispatchToMesh(ctx, "call_tool", args)
}

func (s *Server) handleMeshOverview(ctx context.Context, req *mcp.CallToolRequest, input EmptyInput) (*mcp.CallToolResult, any, error) {
	if s.meshOverview == nil {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{
				&mcp.TextContent{
					Text: "get_mesh_overview not available: no mesh topology provider configured",
				},
			},
		}, nil, nil
	}

	zap.S().Infow("get_mesh_overview requested")
	result, err := s.meshOverview.Execute(ctx)
	if err != nil {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{
				&mcp.TextContent{
					Text: fmt.Sprintf("get_mesh_overview error: %v", err),
				},
			},
		}, nil, nil
	}

	data, _ := json.Marshal(result)
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{
				Text: string(data),
			},
		},
	}, nil, nil
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

func (s *Server) handleSystemIntroduction(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	desc := `You are connected to the Cortex Mesh via the MCP Gateway.
The Cortex Mesh is a decentralized fleet of nodes. You have four tools available:

1. 'get_tool_list' -> Returns the list of available tools across the entire mesh. Call this FIRST.
2. 'get_tool_help' -> Returns the exact JSON schema required to call a specific tool.
3. 'call_tool' -> Invokes a tool on a specific node, a group, or all nodes.
4. 'get_mesh_overview' -> Returns a complete cluster topology with every node's role (SFA/MGS/MDS/OSS/Client), connectivity graph, and a Mermaid diagram. Use this to understand the fleet before diving into specifics.

When using 'call_tool', you can specify 'node_name'.
- Leave 'node_name' empty to let the mesh auto-route to the best node.
- Use '*' to fan-out and execute the tool on ALL nodes simultaneously.
- Use '@group' (e.g. '@storage') to execute on a specific sub-group of nodes.

Start by running 'get_mesh_overview' for a complete picture, then 'get_tool_list' to see available capabilities.`

	return &mcp.GetPromptResult{
		Description: "Onboarding instruction for Cortex Mesh",
		Messages: []*mcp.PromptMessage{
			{
				Role: "user",
				Content: &mcp.TextContent{
					Text: desc,
				},
			},
		},
	}, nil
}
