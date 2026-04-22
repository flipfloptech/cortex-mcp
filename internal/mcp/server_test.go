package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cortex-mesh/cortex-mesh/api"
	"github.com/cortex-mesh/cortex-mesh/tools"
	"github.com/flipfloptech/cortex-mcp/internal/registry"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mockDispatcher implements a simple Dispatcher for testing.
type mockDispatcher struct {
	dispatchFunc func(ctx context.Context, toolName string, args json.RawMessage) (*tools.ToolResult, error)
}

func (m *mockDispatcher) Dispatch(ctx context.Context, toolName string, args json.RawMessage) (*tools.ToolResult, error) {
	if m.dispatchFunc != nil {
		return m.dispatchFunc(ctx, toolName, args)
	}
	return &tools.ToolResult{Content: json.RawMessage(`"mock success"`)}, nil
}

// mockTool implements registry.Tool for testing.
type mockTool struct {
	name        string
	description string
	category    string
	hidden      bool
}

func (m *mockTool) Name() string                     { return m.name }
func (m *mockTool) Description() string              { return m.description }
func (m *mockTool) Help() string                     { return "long description" }
func (m *mockTool) Category() string                 { return m.category }
func (m *mockTool) Parameters() []registry.ToolParam { return nil }
func (m *mockTool) Hidden() bool                     { return m.hidden }
func (m *mockTool) IsSupported() (bool, string)      { return true, "" }
func (m *mockTool) Execute(context.Context, json.RawMessage) (*registry.ToolResult, error) {
	return nil, nil
}

func TestServer_ListToolsDynamic(t *testing.T) {
	t.Parallel()

	// 1. Setup mock topology with some tools
	topology := &mockTopologyProvider{
		snapshot: api.TopologySnapshot{
			NodeDetails: []api.NodeSummary{
				{NodeID: "node1", Capabilities: []string{"tool:get_uptime", "tool:get_system_info", "tool:secret_tool"}},
				{NodeID: "node2", Capabilities: []string{"tool:get_uptime", "tool:other"}}, // "other" is not in registry
			},
		},
	}

	// 2. Setup plugin registry with definitions
	plugins := registry.NewPluginRegistryFrom("test-node", []registry.Tool{
		&mockTool{name: "get_uptime", description: "Get uptime", category: "system", hidden: false},
		&mockTool{name: "get_system_info", description: "Get info", category: "system", hidden: false},
		&mockTool{name: "secret_tool", description: "Hidden tool", category: "system", hidden: true},
	})

	srv := NewServer(&mockDispatcher{}, topology, plugins)
	res, _, err := srv.handleListTools(context.Background(), &mcp.CallToolRequest{}, EmptyInput{})
	if err != nil {
		t.Fatalf("handleListTools failed: %v", err)
	}

	if res.IsError {
		t.Error("expected success result")
	}

	// Expect exactly 2 tools (uptime and system_info)
	content := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(content, `"get_uptime"`) {
		t.Error("expected uptime tool in result")
	}
	if !strings.Contains(content, `"get_system_info"`) {
		t.Error("expected system_info tool in result")
	}
	if strings.Contains(content, `"other"`) {
		t.Error("did not expect 'other' tool in result (not in registry)")
	}
	if strings.Contains(content, `"secret_tool"`) {
		t.Error("did not expect 'secret_tool' tool in result (it is hidden)")
	}
}

func TestServer_ToolHelpDynamic(t *testing.T) {
	t.Parallel()

	plugins := registry.NewPluginRegistryFrom("test-node", []registry.Tool{
		&mockTool{name: "get_uptime", description: "Get uptime", category: "system"},
	})

	srv := NewServer(&mockDispatcher{}, nil, plugins)
	res, _, err := srv.handleToolHelp(context.Background(), &mcp.CallToolRequest{}, ToolHelpInput{ToolName: "get_uptime"})
	if err != nil {
		t.Fatalf("handleToolHelp failed: %v", err)
	}

	if res.IsError {
		t.Error("expected success result")
	}

	content := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(content, `"get_uptime"`) {
		t.Error("expected uptime in help output")
	}

	// Test missing tool
	res, _, _ = srv.handleToolHelp(context.Background(), &mcp.CallToolRequest{}, ToolHelpInput{ToolName: "not_exist"})
	if !res.IsError {
		t.Error("expected error for missing tool")
	}
}

func TestServer_SystemIntroductionPrompt(t *testing.T) {
	t.Parallel()

	srv := NewServer(&mockDispatcher{}, nil, nil)
	res, err := srv.handleSystemIntroduction(context.Background(), &mcp.GetPromptRequest{})
	if err != nil {
		t.Fatalf("handleSystemIntroduction failed: %v", err)
	}

	if len(res.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(res.Messages))
	}

	textContent, ok := res.Messages[0].Content.(*mcp.TextContent)
	if !ok {
		t.Fatal("expected TextContent")
	}

	if textContent.Text == "" {
		t.Error("expected non-empty text content in prompt")
	}
}
