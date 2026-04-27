package gateway

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/tools"
)

// --- NewGateway ---

func TestNewGateway(t *testing.T) {
	t.Parallel()

	reg := tools.NewRegistry(nil)
	gw := New(reg, nil)
	if gw == nil {
		t.Fatal("New returned nil")
	}
}

// --- Meta-tool definitions ---

func TestGateway_MetaTools(t *testing.T) {
	t.Parallel()

	reg := tools.NewRegistry(nil)
	gw := New(reg, nil)

	metaTools := gw.MetaTools()

	expected := map[string]bool{
		"list_tools": false,
		"tool_help":  false,
		"call_tool":  false,
	}

	for _, mt := range metaTools {
		if _, ok := expected[mt.Name]; ok {
			expected[mt.Name] = true
		}
	}

	for name, found := range expected {
		if !found {
			t.Fatalf("missing meta-tool: %q", name)
		}
	}

	if len(metaTools) != 3 {
		t.Fatalf("expected 3 meta-tools, got %d", len(metaTools))
	}
}

func TestGateway_MetaTools_HaveSchemas(t *testing.T) {
	t.Parallel()

	reg := tools.NewRegistry(nil)
	gw := New(reg, nil)

	for _, mt := range gw.MetaTools() {
		schema := mt.EffectiveSchema()
		if len(schema) == 0 {
			t.Fatalf("meta-tool %q has no schema", mt.Name)
		}

		// Should be valid JSON.
		var parsed map[string]interface{}
		if err := json.Unmarshal(schema, &parsed); err != nil {
			t.Fatalf("meta-tool %q schema is invalid JSON: %v", mt.Name, err)
		}
	}
}

// --- HandleListTools ---

func TestGateway_HandleListTools(t *testing.T) {
	t.Parallel()

	reg := tools.NewRegistry(nil)
	reg.Register(tools.ToolDefinition{Name: "lustre_health", Category: "lustre", Description: "Check Lustre"}, noopHandler)
	reg.Register(tools.ToolDefinition{Name: "lnet_status", Category: "network", Description: "Check LNet"}, noopHandler)

	gw := New(reg, nil)
	result, err := gw.HandleListTools(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("HandleListTools: %v", err)
	}
	if result.IsError {
		t.Fatal("result should not be error")
	}

	// Parse result content.
	var response ListToolsResponse
	if err := json.Unmarshal(result.Content, &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(response.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(response.Tools))
	}
}

func TestGateway_HandleListTools_CategoryFilter(t *testing.T) {
	t.Parallel()

	reg := tools.NewRegistry(nil)
	reg.Register(tools.ToolDefinition{Name: "lustre_health", Category: "lustre"}, noopHandler)
	reg.Register(tools.ToolDefinition{Name: "lnet_status", Category: "network"}, noopHandler)
	reg.Register(tools.ToolDefinition{Name: "lustre_df", Category: "lustre"}, noopHandler)

	gw := New(reg, nil)
	result, err := gw.HandleListTools(context.Background(), json.RawMessage(`{"category":"lustre"}`))
	if err != nil {
		t.Fatalf("HandleListTools: %v", err)
	}

	var response ListToolsResponse
	if err := json.Unmarshal(result.Content, &response); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(response.Tools) != 2 {
		t.Fatalf("expected 2 lustre tools, got %d", len(response.Tools))
	}
}

// --- HandleToolHelp ---

func TestGateway_HandleToolHelp(t *testing.T) {
	t.Parallel()

	reg := tools.NewRegistry(nil)
	reg.Register(tools.ToolDefinition{
		Name:            "lustre_health",
		Description:     "Check Lustre filesystem health",
		LongDescription: "Detailed Lustre health check...",
		Category:        "lustre",
		Parameters: []tools.ToolParam{
			{Name: "verbose", Type: "boolean", Description: "Verbose output", Required: false, Default: "false"},
		},
	}, noopHandler)

	gw := New(reg, nil)
	result, err := gw.HandleToolHelp(context.Background(), json.RawMessage(`{"tool_name":"lustre_health"}`))
	if err != nil {
		t.Fatalf("HandleToolHelp: %v", err)
	}
	if result.IsError {
		t.Fatal("result should not be error")
	}

	var response ToolHelpResponse
	if err := json.Unmarshal(result.Content, &response); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if response.Name != "lustre_health" {
		t.Fatalf("name = %q, want %q", response.Name, "lustre_health")
	}
	if response.LongDescription != "Detailed Lustre health check..." {
		t.Fatalf("long_description mismatch")
	}
	if len(response.Parameters) != 1 {
		t.Fatalf("expected 1 param, got %d", len(response.Parameters))
	}
}

func TestGateway_HandleToolHelp_NotFound(t *testing.T) {
	t.Parallel()

	reg := tools.NewRegistry(nil)
	gw := New(reg, nil)

	result, err := gw.HandleToolHelp(context.Background(), json.RawMessage(`{"tool_name":"nonexistent"}`))
	if err != nil {
		t.Fatalf("HandleToolHelp: %v", err)
	}
	if !result.IsError {
		t.Fatal("should return tool error for unknown tool")
	}
}

func TestGateway_HandleToolHelp_MissingToolName(t *testing.T) {
	t.Parallel()

	reg := tools.NewRegistry(nil)
	gw := New(reg, nil)

	result, err := gw.HandleToolHelp(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("HandleToolHelp: %v", err)
	}
	if !result.IsError {
		t.Fatal("should return error for missing tool_name")
	}
}

// --- HandleCallTool: Local ---

func TestGateway_HandleCallTool_Local(t *testing.T) {
	t.Parallel()

	reg := tools.NewRegistry(nil)
	reg.Register(tools.ToolDefinition{Name: "echo"}, func(ctx context.Context, args json.RawMessage) (*tools.ToolResult, error) {
		return tools.NewJSONResult(map[string]string{"echo": string(args)}), nil
	})

	gw := New(reg, nil)
	result, err := gw.HandleCallTool(context.Background(), json.RawMessage(`{"tool_name":"echo","args":{"msg":"hello"}}`))
	if err != nil {
		t.Fatalf("HandleCallTool: %v", err)
	}
	if result.IsError {
		t.Fatal("result should not be error")
	}
}

func TestGateway_HandleCallTool_MissingToolName(t *testing.T) {
	t.Parallel()

	reg := tools.NewRegistry(nil)
	gw := New(reg, nil)

	result, err := gw.HandleCallTool(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("HandleCallTool: %v", err)
	}
	if !result.IsError {
		t.Fatal("should return error for missing tool_name")
	}
}

func TestGateway_HandleCallTool_NotFound(t *testing.T) {
	t.Parallel()

	reg := tools.NewRegistry(nil)
	gw := New(reg, nil)

	result, err := gw.HandleCallTool(context.Background(), json.RawMessage(`{"tool_name":"nonexistent"}`))
	if err != nil {
		t.Fatalf("HandleCallTool: %v", err)
	}
	if !result.IsError {
		t.Fatal("should return error for unknown tool")
	}
}

// --- HandleCallTool: Unicast ---

func TestGateway_HandleCallTool_Unicast(t *testing.T) {
	t.Parallel()

	mesh := &mockMesh{
		invokeResult: tools.NewTextResult("remote ok"),
	}
	reg := tools.NewRegistry(nil)
	gw := New(reg, mesh)

	result, err := gw.HandleCallTool(context.Background(), json.RawMessage(`{"tool_name":"lustre_health","node_name":"mds-01"}`))
	if err != nil {
		t.Fatalf("HandleCallTool: %v", err)
	}
	if result.IsError {
		t.Fatal("result should not be error")
	}

	// Verify the mesh was called with the right node.
	if mesh.lastNodeID != "mds-01" {
		t.Fatalf("invoked on %q, want %q", mesh.lastNodeID, "mds-01")
	}

	// Verify response shape — should be a CallToolResponse with 1 result.
	var response CallToolResponse
	if err := json.Unmarshal(result.Content, &response); err != nil {
		t.Fatalf("unmarshal CallToolResponse: %v", err)
	}
	if len(response.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(response.Results))
	}
	if response.Results[0].NodeID != "mds-01" {
		t.Fatalf("result node_id = %q, want %q", response.Results[0].NodeID, "mds-01")
	}
}

func TestGateway_HandleCallTool_Unicast_NoMesh(t *testing.T) {
	t.Parallel()

	reg := tools.NewRegistry(nil)
	gw := New(reg, nil) // no mesh

	result, err := gw.HandleCallTool(context.Background(), json.RawMessage(`{"tool_name":"lustre_health","node_name":"mds-01"}`))
	if err != nil {
		t.Fatalf("HandleCallTool: %v", err)
	}
	if !result.IsError {
		t.Fatal("should return error when no mesh available for remote invocation")
	}
}

// --- HandleCallTool: Auto-route ---

func TestGateway_HandleCallTool_AutoRoute_PrefersLocal(t *testing.T) {
	t.Parallel()

	// Tool exists locally AND mesh is available. Auto-route should
	// invoke locally without touching the mesh.
	mesh := &mockMesh{
		invokeResult: tools.NewTextResult("should not be used"),
	}
	reg := tools.NewRegistry(nil)
	reg.Register(tools.ToolDefinition{Name: "echo"}, func(ctx context.Context, args json.RawMessage) (*tools.ToolResult, error) {
		return tools.NewTextResult("local invocation"), nil
	})

	gw := New(reg, mesh)

	result, err := gw.HandleCallTool(context.Background(), json.RawMessage(`{"tool_name":"echo"}`))
	if err != nil {
		t.Fatalf("HandleCallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("result should not be error: %s", result.Content)
	}

	// Verify the mesh was NOT called.
	mesh.mu.Lock()
	invoked := mesh.lastToolName
	mesh.mu.Unlock()
	if invoked != "" {
		t.Fatalf("mesh was called with tool %q, but local should have been preferred", invoked)
	}

	// Verify the local handler was called.
	if string(result.Content) != `{"text":"local invocation"}` {
		t.Fatalf("unexpected result: %s", result.Content)
	}
}

func TestGateway_HandleCallTool_AutoRoute(t *testing.T) {
	t.Parallel()

	mesh := &mockMesh{
		invokeResult: tools.NewTextResult("auto-routed ok"),
		discoverNodes: []tools.AgentInfo{
			{NodeID: "discovered-node", Impedance: 1.0},
		},
	}
	reg := tools.NewRegistry(nil)
	gw := New(reg, mesh)

	// No node_name → auto-route.
	result, err := gw.HandleCallTool(context.Background(), json.RawMessage(`{"tool_name":"lustre_health"}`))
	if err != nil {
		t.Fatalf("HandleCallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("result should not be error: %s", result.Content)
	}

	// Auto-route should discover nodes and pick the first one.
	if mesh.lastNodeID != "discovered-node" {
		t.Fatalf("auto-route should pass discovered nodeID, got %q", mesh.lastNodeID)
	}
}

// --- HandleCallTool: Fan-out ---

func TestGateway_HandleCallTool_FanOut_Wildcard(t *testing.T) {
	t.Parallel()

	mesh := &mockMesh{
		invokeResult: tools.NewTextResult("ok"),
		discoverNodes: []tools.AgentInfo{
			{NodeID: "mds-01", Impedance: 5.0},
			{NodeID: "mds-02", Impedance: 10.0},
			{NodeID: "oss-01", Impedance: 3.0},
		},
	}
	reg := tools.NewRegistry(nil)
	gw := New(reg, mesh)

	// "*" → fan-out to all nodes.
	result, err := gw.HandleCallTool(context.Background(), json.RawMessage(`{"tool_name":"lustre_health","node_name":"*"}`))
	if err != nil {
		t.Fatalf("HandleCallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("result should not be error: %s", result.Content)
	}

	var response CallToolResponse
	if err := json.Unmarshal(result.Content, &response); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(response.Results) != 3 {
		t.Fatalf("expected 3 results (all nodes), got %d", len(response.Results))
	}
}

func TestGateway_HandleCallTool_FanOut_GlobPattern(t *testing.T) {
	t.Parallel()

	mesh := &mockMesh{
		invokeResult: tools.NewTextResult("ok"),
		discoverNodes: []tools.AgentInfo{
			{NodeID: "mds-01", Impedance: 5.0},
			{NodeID: "mds-02", Impedance: 10.0},
			{NodeID: "oss-01", Impedance: 3.0},
			{NodeID: "oss-02", Impedance: 20.0},
		},
	}
	reg := tools.NewRegistry(nil)
	gw := New(reg, mesh)

	// "mds-*" → fan-out to only mds nodes.
	result, err := gw.HandleCallTool(context.Background(), json.RawMessage(`{"tool_name":"lustre_health","node_name":"mds-*"}`))
	if err != nil {
		t.Fatalf("HandleCallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("result should not be error: %s", result.Content)
	}

	var response CallToolResponse
	if err := json.Unmarshal(result.Content, &response); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(response.Results) != 2 {
		t.Fatalf("expected 2 results (mds-* only), got %d", len(response.Results))
	}

	// Verify only mds nodes matched.
	for _, r := range response.Results {
		if r.NodeID != "mds-01" && r.NodeID != "mds-02" {
			t.Fatalf("unexpected node in results: %q", r.NodeID)
		}
	}
}

func TestGateway_HandleCallTool_FanOut_NoMesh(t *testing.T) {
	t.Parallel()

	reg := tools.NewRegistry(nil)
	gw := New(reg, nil) // no mesh

	result, err := gw.HandleCallTool(context.Background(), json.RawMessage(`{"tool_name":"lustre_health","node_name":"*"}`))
	if err != nil {
		t.Fatalf("HandleCallTool: %v", err)
	}
	if !result.IsError {
		t.Fatal("should return error when no mesh available for fan-out")
	}
}

func TestGateway_HandleCallTool_FanOut_NoMatches(t *testing.T) {
	t.Parallel()

	mesh := &mockMesh{
		discoverNodes: []tools.AgentInfo{
			{NodeID: "oss-01", Impedance: 3.0},
			{NodeID: "oss-02", Impedance: 20.0},
		},
	}
	reg := tools.NewRegistry(nil)
	gw := New(reg, mesh)

	// "mds-*" with no mds nodes.
	result, err := gw.HandleCallTool(context.Background(), json.RawMessage(`{"tool_name":"lustre_health","node_name":"mds-*"}`))
	if err != nil {
		t.Fatalf("HandleCallTool: %v", err)
	}
	if !result.IsError {
		t.Fatal("should return error when no nodes match glob pattern")
	}
}

// --- Dispatch ---

func TestGateway_Dispatch(t *testing.T) {
	t.Parallel()

	reg := tools.NewRegistry(nil)
	reg.Register(tools.ToolDefinition{Name: "echo"}, func(ctx context.Context, args json.RawMessage) (*tools.ToolResult, error) {
		return tools.NewTextResult("dispatched"), nil
	})

	gw := New(reg, nil)

	// Dispatch to a known meta-tool.
	result, err := gw.Dispatch(context.Background(), "list_tools", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if result.IsError {
		t.Fatal("result should not be error")
	}

	// Dispatch to unknown tool.
	result, err = gw.Dispatch(context.Background(), "unknown_tool", nil)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !result.IsError {
		t.Fatal("unknown tool should return error")
	}

	// cluster_sweep should no longer be recognized.
	result, err = gw.Dispatch(context.Background(), "cluster_sweep", nil)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !result.IsError {
		t.Fatal("cluster_sweep should return unknown meta-tool error")
	}
}

// TestGateway_HandleCallTool_FanOut_NodesetPattern verifies that nodeset
// bracket patterns (e.g., "node[1-3]") correctly route to matching nodes.
func TestGateway_HandleCallTool_FanOut_NodesetPattern(t *testing.T) {
	t.Parallel()

	reg := tools.NewRegistry(nil)
	reg.Register(tools.ToolDefinition{
		Name:     "test_tool",
		Category: "test",
	}, noopHandler)

	mesh := &mockMesh{
		invokeResult: tools.NewTextResult("ok"),
		discoverNodes: []tools.AgentInfo{
			{NodeID: "node1"},
			{NodeID: "node2"},
			{NodeID: "node3"},
			{NodeID: "node4"},
			{NodeID: "node5"},
		},
	}

	gw := New(reg, mesh)

	args, _ := json.Marshal(map[string]interface{}{
		"tool_name": "test_tool",
		"node_name": "node[1-3]",
	})

	result, err := gw.HandleCallTool(context.Background(), args)
	if err != nil {
		t.Fatalf("HandleCallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", string(result.Content))
	}

	var resp CallToolResponse
	if err := json.Unmarshal(result.Content, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Should match node1, node2, node3 — not node4 or node5.
	if len(resp.Results) != 3 {
		t.Fatalf("Results = %d, want 3", len(resp.Results))
	}

	matched := make(map[string]bool)
	for _, r := range resp.Results {
		matched[r.NodeID] = true
	}
	for _, want := range []string{"node1", "node2", "node3"} {
		if !matched[want] {
			t.Errorf("missing expected node %q in results", want)
		}
	}
	for _, notWant := range []string{"node4", "node5"} {
		if matched[notWant] {
			t.Errorf("unexpected node %q in results", notWant)
		}
	}
}

// TestGateway_HandleCallTool_FanOut_DifferencePattern verifies that nodeset
// difference patterns (e.g., "node[1-5]!node[3-4]") correctly exclude nodes.
func TestGateway_HandleCallTool_FanOut_DifferencePattern(t *testing.T) {
	t.Parallel()

	reg := tools.NewRegistry(nil)
	reg.Register(tools.ToolDefinition{
		Name:     "test_tool",
		Category: "test",
	}, noopHandler)

	mesh := &mockMesh{
		invokeResult: tools.NewTextResult("ok"),
		discoverNodes: []tools.AgentInfo{
			{NodeID: "node1"},
			{NodeID: "node2"},
			{NodeID: "node3"},
			{NodeID: "node4"},
			{NodeID: "node5"},
		},
	}

	gw := New(reg, mesh)

	args, _ := json.Marshal(map[string]interface{}{
		"tool_name": "test_tool",
		"node_name": "node[1-5]!node[3-4]",
	})

	result, err := gw.HandleCallTool(context.Background(), args)
	if err != nil {
		t.Fatalf("HandleCallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", string(result.Content))
	}

	var resp CallToolResponse
	if err := json.Unmarshal(result.Content, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// node[1-5] minus node[3-4] = node1, node2, node5
	if len(resp.Results) != 3 {
		t.Fatalf("Results = %d, want 3", len(resp.Results))
	}

	matched := make(map[string]bool)
	for _, r := range resp.Results {
		matched[r.NodeID] = true
	}
	for _, want := range []string{"node1", "node2", "node5"} {
		if !matched[want] {
			t.Errorf("missing expected node %q in results", want)
		}
	}
	for _, notWant := range []string{"node3", "node4"} {
		if matched[notWant] {
			t.Errorf("unexpected node %q in results (should be excluded by !)", notWant)
		}
	}
}

// --- isNodesetPattern ---

func TestIsNodesetPattern(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  bool
	}{
		{"mds-01", false},
		{"node-target", false},
		{"*", true},
		{"mds-*", true},
		{"oss-?", true},
		{"node-*-backup", true},
		{"", false},
		// Nodeset patterns.
		{"node[1-3]", true},
		{"mds[01-16]", true},
		{"node[1-10]!node[5-7]", true},
		{"node[1-5]&node[3-8]", true},
		{"node[1-5]^node[3-8]", true},
		{"mds[1-3],oss[1-5]", true},
	}

	for _, tc := range tests {
		got := isNodesetPattern(tc.input)
		if got != tc.want {
			t.Errorf("isNodesetPattern(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

// --- Test helpers ---

var noopHandler tools.ToolHandler = func(ctx context.Context, args json.RawMessage) (*tools.ToolResult, error) {
	return tools.NewTextResult("ok"), nil
}

// mockMesh satisfies the MeshBridge interface for gateway testing.
type mockMesh struct {
	mu           sync.Mutex
	lastNodeID   string
	lastToolName string

	invokeResult  *tools.ToolResult
	invokeErr     error
	discoverNodes []tools.AgentInfo
	discoverErr   error
}

func (m *mockMesh) InvokeRemote(ctx context.Context, nodeID, toolName string, args json.RawMessage) (*tools.ToolResult, error) {
	m.mu.Lock()
	m.lastNodeID = nodeID
	m.lastToolName = toolName
	m.mu.Unlock()
	if m.invokeErr != nil {
		return nil, m.invokeErr
	}
	return m.invokeResult, nil
}

func (m *mockMesh) DiscoverNodes(ctx context.Context, toolName string) ([]tools.AgentInfo, error) {
	if m.discoverErr != nil {
		return nil, m.discoverErr
	}
	return m.discoverNodes, nil
}
