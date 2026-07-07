package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
	"github.com/flipfloptech/cortex-mcp/pkg/mesh/api"
	"github.com/flipfloptech/cortex-mcp/pkg/mesh/tools"
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
	category    registry.Category
	hidden      bool
	params      []registry.ToolParam
}

func (m *mockTool) Name() string                     { return m.name }
func (m *mockTool) Description() string              { return m.description }
func (m *mockTool) Help() string                     { return "long description" }
func (m *mockTool) Category() registry.Category      { return m.category }
func (m *mockTool) Parameters() []registry.ToolParam { return m.params }
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
		&mockTool{name: "get_uptime", description: "Get uptime", category: registry.CategorySystem, hidden: false},
		&mockTool{name: "get_system_info", description: "Get info", category: registry.CategorySystem, hidden: false},
		&mockTool{name: "secret_tool", description: "Hidden tool", category: registry.CategorySystem, hidden: true},
	})

	srv := NewServer(&mockDispatcher{}, topology, plugins)
	res, _, err := srv.handleListTools(context.Background(), &mcp.CallToolRequest{}, ListToolsInput{})
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

// listToolsTestServer builds a server whose topology advertises one system
// tool, one storage tool, and one hidden lifecycle tool.
func listToolsTestServer() *Server {
	topology := &mockTopologyProvider{
		snapshot: api.TopologySnapshot{
			NodeDetails: []api.NodeSummary{
				{NodeID: "node1", Capabilities: []string{"tool:get_uptime", "tool:get_disk_io_stats", "tool:node_stop"}},
			},
		},
	}
	plugins := registry.NewPluginRegistryFrom("test-node", []registry.Tool{
		&mockTool{name: "get_uptime", description: "Get uptime", category: registry.CategorySystem},
		&mockTool{name: "get_disk_io_stats", description: "Disk IO", category: registry.CategoryStorage},
		&mockTool{name: "node_stop", description: "Stop node", category: registry.CategoryLifecycle, hidden: true},
	})
	return NewServer(&mockDispatcher{}, topology, plugins)
}

// TestServer_ListToolsCategoryFilter verifies get_tool_list honors the
// optional category argument.
func TestServer_ListToolsCategoryFilter(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		category    string
		wantTool    string
		excludeTool string
	}{
		{"filter storage", "storage", `"get_disk_io_stats"`, `"get_uptime"`},
		{"filter system", "system", `"get_uptime"`, `"get_disk_io_stats"`},
		// Case-insensitive: the exact bug class that motivated the enum.
		{"filter mixed case", "Storage", `"get_disk_io_stats"`, `"get_uptime"`},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := listToolsTestServer()
			res, _, err := srv.handleListTools(context.Background(), &mcp.CallToolRequest{}, ListToolsInput{Category: tc.category})
			if err != nil {
				t.Fatalf("handleListTools failed: %v", err)
			}
			if res.IsError {
				t.Fatalf("expected success, got error: %v", res.Content)
			}
			content := res.Content[0].(*mcp.TextContent).Text
			if !strings.Contains(content, tc.wantTool) {
				t.Errorf("category %q: expected %s in result, got: %s", tc.category, tc.wantTool, content)
			}
			if strings.Contains(content, tc.excludeTool) {
				t.Errorf("category %q: did not expect %s in result, got: %s", tc.category, tc.excludeTool, content)
			}
		})
	}
}

// TestServer_ListToolsCategoryFilter_Empty verifies the no-filter behavior is
// unchanged: all visible tools, hidden excluded.
func TestServer_ListToolsCategoryFilter_Empty(t *testing.T) {
	t.Parallel()

	srv := listToolsTestServer()
	res, _, err := srv.handleListTools(context.Background(), &mcp.CallToolRequest{}, ListToolsInput{})
	if err != nil {
		t.Fatalf("handleListTools failed: %v", err)
	}
	content := res.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{`"get_uptime"`, `"get_disk_io_stats"`} {
		if !strings.Contains(content, want) {
			t.Errorf("expected %s in unfiltered result", want)
		}
	}
	if strings.Contains(content, `"node_stop"`) {
		t.Error("hidden tool must not appear in unfiltered result")
	}
}

// TestServer_ListToolsCategoryFilter_Invalid verifies an unknown category
// returns a self-correcting error that lists every valid category.
func TestServer_ListToolsCategoryFilter_Invalid(t *testing.T) {
	t.Parallel()

	srv := listToolsTestServer()
	res, _, err := srv.handleListTools(context.Background(), &mcp.CallToolRequest{}, ListToolsInput{Category: "lustre"})
	if err != nil {
		t.Fatalf("handleListTools failed: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected error result for invalid category")
	}
	content := res.Content[0].(*mcp.TextContent).Text
	for _, name := range registry.CategoryNames() {
		if !strings.Contains(content, name) {
			t.Errorf("invalid-category error must list %q, got: %s", name, content)
		}
	}
}

// TestServer_ListToolsFallbackDispatch verifies the local-fallback path
// (no plugins/topology) dispatches the gateway's canonical meta-tool name
// "list_tools" — not "get_tool_list", which gateway.Dispatch rejects —
// and passes the category filter through.
func TestServer_ListToolsFallbackDispatch(t *testing.T) {
	t.Parallel()

	var gotName string
	var gotArgs json.RawMessage
	disp := &mockDispatcher{
		dispatchFunc: func(_ context.Context, toolName string, args json.RawMessage) (*tools.ToolResult, error) {
			gotName = toolName
			gotArgs = args
			return &tools.ToolResult{Content: json.RawMessage(`{"tools":[]}`)}, nil
		},
	}

	srv := NewServer(disp, nil, nil)
	_, _, err := srv.handleListTools(context.Background(), &mcp.CallToolRequest{}, ListToolsInput{Category: "storage"})
	if err != nil {
		t.Fatalf("handleListTools failed: %v", err)
	}
	if gotName != "list_tools" {
		t.Errorf("fallback dispatched %q, want %q", gotName, "list_tools")
	}
	if !strings.Contains(string(gotArgs), `"storage"`) {
		t.Errorf("fallback args %s must carry the category filter", gotArgs)
	}
}

// TestServer_ToolHelpFallbackDispatch verifies the same name-mismatch fix
// for get_tool_help → tool_help.
func TestServer_ToolHelpFallbackDispatch(t *testing.T) {
	t.Parallel()

	var gotName string
	disp := &mockDispatcher{
		dispatchFunc: func(_ context.Context, toolName string, _ json.RawMessage) (*tools.ToolResult, error) {
			gotName = toolName
			return &tools.ToolResult{Content: json.RawMessage(`{}`)}, nil
		},
	}

	srv := NewServer(disp, nil, nil)
	_, _, err := srv.handleToolHelp(context.Background(), &mcp.CallToolRequest{}, ToolHelpInput{ToolName: "get_uptime"})
	if err != nil {
		t.Fatalf("handleToolHelp failed: %v", err)
	}
	if gotName != "tool_help" {
		t.Errorf("fallback dispatched %q, want %q", gotName, "tool_help")
	}
}

// TestListToolsDescription_MentionsAllCategories keeps the LLM-facing help
// text in lockstep with the Category enum: adding a category without
// updating discoverability documentation must fail the build.
func TestListToolsDescription_MentionsAllCategories(t *testing.T) {
	t.Parallel()

	desc := listToolsDescription()
	for _, name := range registry.CategoryNames() {
		if !strings.Contains(desc, name) {
			t.Errorf("get_tool_list description must mention category %q, got: %s", name, desc)
		}
	}
}

func TestServer_ToolHelpDynamic(t *testing.T) {
	t.Parallel()

	plugins := registry.NewPluginRegistryFrom("test-node", []registry.Tool{
		&mockTool{name: "get_uptime", description: "Get uptime", category: registry.CategorySystem},
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

func TestServer_CallToolValidation(t *testing.T) {
	t.Parallel()

	plugins := registry.NewPluginRegistryFrom("test-node", []registry.Tool{
		&mockTool{
			name: "test_tool",
			params: []registry.ToolParam{
				{Name: "req_str", Type: "string", Required: true},
				{Name: "req_int", Type: "integer", Required: true},
				{Name: "opt_bool", Type: "boolean", Required: false},
			},
		},
	})

	srv := NewServer(&mockDispatcher{}, nil, plugins)

	tests := []struct {
		name          string
		input         CallToolInput
		expectErr     bool
		expectErrText string
	}{
		{
			name: "Happy Path (all correct)",
			input: CallToolInput{
				ToolName: "test_tool",
				Args: map[string]interface{}{
					"req_str":  "hello",
					"req_int":  float64(42), // JSON unmarshals to float64
					"opt_bool": true,
				},
			},
			expectErr: false,
		},
		{
			name: "Missing Required",
			input: CallToolInput{
				ToolName: "test_tool",
				Args: map[string]interface{}{
					"req_str": "hello",
				},
			},
			expectErr:     true,
			expectErrText: "missing required argument 'req_int'",
		},
		{
			name: "Type Mismatch (int provided as string)",
			input: CallToolInput{
				ToolName: "test_tool",
				Args: map[string]interface{}{
					"req_str": "hello",
					"req_int": "42",
				},
			},
			expectErr:     true,
			expectErrText: "must be of type 'integer'",
		},
		{
			name: "Unknown Argument",
			input: CallToolInput{
				ToolName: "test_tool",
				Args: map[string]interface{}{
					"req_str": "hello",
					"req_int": float64(42),
					"unknown": "bad",
				},
			},
			expectErr:     true,
			expectErrText: "unknown argument 'unknown'",
		},
		{
			name: "Unknown Tool (passes through to mesh)",
			input: CallToolInput{
				ToolName: "does_not_exist",
			},
			expectErr: false, // The dispatcher handles unknown tool error
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res, _, err := srv.handleCallTool(context.Background(), &mcp.CallToolRequest{}, tt.input)
			if err != nil {
				t.Fatalf("handleCallTool failed: %v", err)
			}

			if res.IsError != tt.expectErr {
				t.Errorf("expected error=%v, got %v", tt.expectErr, res.IsError)
			}

			if tt.expectErr {
				content := res.Content[0].(*mcp.TextContent).Text
				if !strings.Contains(content, tt.expectErrText) {
					t.Errorf("expected error text to contain %q, got: %q", tt.expectErrText, content)
				}
				if !strings.Contains(content, "Please call 'get_tool_help'") {
					t.Errorf("expected error text to direct to get_tool_help, got: %q", content)
				}
			}
		})
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
