package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// --- RemoteInvoker ---

func TestRemoteInvoker_InvokeByNodeID(t *testing.T) {
	t.Parallel()

	// Mock mesh that succeeds on invoke.
	mesh := &mockMeshTransport{
		invokeResult: &ToolResult{Content: json.RawMessage(`{"status":"ok"}`), IsError: false},
	}
	invoker := NewRemoteInvoker(mesh)

	result, err := invoker.Invoke(context.Background(), "node-target", "lustre_health", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if result.IsError {
		t.Fatal("result should not be error")
	}
	if mesh.lastTargetNode != "node-target" {
		t.Fatalf("invoked on %q, want %q", mesh.lastTargetNode, "node-target")
	}
	if mesh.lastToolName != "lustre_health" {
		t.Fatalf("tool = %q, want %q", mesh.lastToolName, "lustre_health")
	}
}

func TestRemoteInvoker_InvokeAutoRoute(t *testing.T) {
	t.Parallel()

	// Mock mesh that returns sonar results for auto-routing.
	mesh := &mockMeshTransport{
		sonarResults: []AgentInfo{
			{NodeID: "node-heavy", Impedance: 80.0},
			{NodeID: "node-idle", Impedance: 2.0},
			{NodeID: "node-mid", Impedance: 30.0},
		},
		invokeResult: &ToolResult{Content: json.RawMessage(`{"routed":"ok"}`), IsError: false},
	}
	invoker := NewRemoteInvoker(mesh)

	// Empty nodeID → auto-route via Sonar to lowest-impedance node.
	result, err := invoker.Invoke(context.Background(), "", "lustre_health", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if result.IsError {
		t.Fatal("result should not be error")
	}
	// Should have routed to "node-idle" (lowest impedance).
	if mesh.lastTargetNode != "node-idle" {
		t.Fatalf("auto-routed to %q, want %q (lowest impedance)", mesh.lastTargetNode, "node-idle")
	}
}

func TestRemoteInvoker_InvokeAutoRoute_NoNodes(t *testing.T) {
	t.Parallel()

	// Sonar returns empty — no nodes with this capability.
	mesh := &mockMeshTransport{
		sonarResults: []AgentInfo{},
	}
	invoker := NewRemoteInvoker(mesh)

	_, err := invoker.Invoke(context.Background(), "", "nonexistent_tool", nil)
	if err == nil {
		t.Fatal("expected error when no nodes found")
	}
	if !errors.Is(err, ErrNoNodesAvailable) {
		t.Fatalf("expected ErrNoNodesAvailable, got %v", err)
	}
}

func TestRemoteInvoker_InvokeAutoRoute_SonarError(t *testing.T) {
	t.Parallel()

	mesh := &mockMeshTransport{
		sonarErr: errors.New("network partitioned"),
	}
	invoker := NewRemoteInvoker(mesh)

	_, err := invoker.Invoke(context.Background(), "", "lustre_health", nil)
	if err == nil {
		t.Fatal("expected sonar error to propagate")
	}
}

func TestRemoteInvoker_InvokeTransportError(t *testing.T) {
	t.Parallel()

	mesh := &mockMeshTransport{
		invokeErr: errors.New("connection refused"),
	}
	invoker := NewRemoteInvoker(mesh)

	_, err := invoker.Invoke(context.Background(), "node-target", "lustre_health", nil)
	if err == nil {
		t.Fatal("expected transport error to propagate")
	}
}

// --- FanOut ---

func TestRemoteInvoker_FanOut(t *testing.T) {
	t.Parallel()

	mesh := &mockMeshTransport{
		invokeResult: &ToolResult{Content: json.RawMessage(`{"ok":true}`), IsError: false},
	}
	invoker := NewRemoteInvoker(mesh)

	nodeIDs := []string{"node-1", "node-2", "node-3"}
	results, err := invoker.FanOut(context.Background(), nodeIDs, "lustre_health", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("FanOut: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// All should succeed.
	for _, r := range results {
		if r.Error != nil {
			t.Fatalf("node %q error: %v", r.NodeID, r.Error)
		}
	}
}

func TestRemoteInvoker_FanOut_PartialFailure(t *testing.T) {
	t.Parallel()

	// Fail on specific node.
	mesh := &mockMeshTransport{
		invokeFunc: func(ctx context.Context, nodeID, toolName string, args json.RawMessage) (*ToolResult, error) {
			if nodeID == "node-bad" {
				return nil, errors.New("connection failed")
			}
			return NewTextResult("ok"), nil
		},
	}
	invoker := NewRemoteInvoker(mesh)

	nodeIDs := []string{"node-1", "node-bad", "node-3"}
	results, err := invoker.FanOut(context.Background(), nodeIDs, "tool", nil)
	if err != nil {
		t.Fatalf("FanOut should not return top-level error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// Find the failed node.
	var failed int
	for _, r := range results {
		if r.Error != nil {
			failed++
			if r.NodeID != "node-bad" {
				t.Fatalf("expected failure on node-bad, got %q", r.NodeID)
			}
		}
	}
	if failed != 1 {
		t.Fatalf("expected 1 failure, got %d", failed)
	}
}

func TestRemoteInvoker_FanOut_Empty(t *testing.T) {
	t.Parallel()

	mesh := &mockMeshTransport{}
	invoker := NewRemoteInvoker(mesh)

	results, err := invoker.FanOut(context.Background(), nil, "tool", nil)
	if err != nil {
		t.Fatalf("FanOut empty: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
}

// --- ListAll (mesh-wide discovery) ---

func TestRemoteInvoker_ListAll(t *testing.T) {
	t.Parallel()

	mesh := &mockMeshTransport{
		listAllResults: []ToolInfo{
			{
				Definition: ToolDefinition{Name: "lustre_health", Category: "lustre"},
				Nodes:      []AgentInfo{{NodeID: "node-1", Impedance: 5.0}},
			},
			{
				Definition: ToolDefinition{Name: "lnet_status", Category: "network"},
				Nodes:      []AgentInfo{{NodeID: "node-2", Impedance: 10.0}},
			},
		},
	}
	invoker := NewRemoteInvoker(mesh)

	tools, err := invoker.ListAll(context.Background())
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
}

// --- Goroutine safety ---

func TestRemoteInvoker_ConcurrentInvoke(t *testing.T) {
	t.Parallel()

	mesh := &mockMeshTransport{
		invokeResult: NewTextResult("ok"),
	}
	invoker := NewRemoteInvoker(mesh)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			_, _ = invoker.Invoke(context.Background(), fmt.Sprintf("node-%d", id), "tool", nil)
		}(i)
	}
	wg.Wait()
}

// --- Test helpers ---

// mockMeshTransport simulates the mesh's Sonar and Laser for testing.
type mockMeshTransport struct {
	mu             sync.Mutex
	lastTargetNode string
	lastToolName   string

	// Sonar mock.
	sonarResults []AgentInfo
	sonarErr     error

	// Invoke mock.
	invokeResult *ToolResult
	invokeErr    error
	invokeFunc   func(ctx context.Context, nodeID, toolName string, args json.RawMessage) (*ToolResult, error)

	// ListAll mock.
	listAllResults []ToolInfo
	listAllErr     error
}

func (m *mockMeshTransport) DiscoverTool(ctx context.Context, toolName string) ([]AgentInfo, error) {
	if m.sonarErr != nil {
		return nil, m.sonarErr
	}
	return m.sonarResults, nil
}

func (m *mockMeshTransport) InvokeRemote(ctx context.Context, nodeID, toolName string, args json.RawMessage) (*ToolResult, error) {
	m.mu.Lock()
	m.lastTargetNode = nodeID
	m.lastToolName = toolName
	m.mu.Unlock()

	if m.invokeFunc != nil {
		return m.invokeFunc(ctx, nodeID, toolName, args)
	}
	if m.invokeErr != nil {
		return nil, m.invokeErr
	}
	return m.invokeResult, nil
}

func (m *mockMeshTransport) DiscoverAllTools(ctx context.Context) ([]ToolInfo, error) {
	if m.listAllErr != nil {
		return nil, m.listAllErr
	}
	return m.listAllResults, nil
}
