package tools

import (
	"context"
	"encoding/json"
	"net"
	"testing"
)

// --- NeuronBridge tests ---

// mockMeshNode implements the MeshNode interface for testing.
type mockMeshNode struct {
	nodeID       string
	sonarResults []AgentInfo
	sonarErr     error
	dialConn     net.Conn
	dialErr      error
	capEntries   []NodeCapEntry // returned by LookupCapability
}

func (m *mockMeshNode) NodeID() string { return m.nodeID }

func (m *mockMeshNode) Sonar(_ context.Context, _ string) ([]AgentInfo, error) {
	return m.sonarResults, m.sonarErr
}

func (m *mockMeshNode) GrpcDialer(_ context.Context, _ string) (net.Conn, error) {
	return m.dialConn, m.dialErr
}

func (m *mockMeshNode) LookupCapability(_ string) []NodeCapEntry {
	return m.capEntries
}

func TestNeuronBridge_DiscoverTool_PrefersIndex(t *testing.T) {
	t.Parallel()

	// Capability index has entries — should use them (zero Sonar traffic).
	mock := &mockMeshNode{
		nodeID: "gateway-01",
		capEntries: []NodeCapEntry{
			{NodeID: "oss-01", Impedance: 3.0},
			{NodeID: "oss-02", Impedance: 8.0},
		},
		sonarResults: []AgentInfo{
			{NodeID: "should-not-be-used", Impedance: 99.0},
		},
	}

	bridge := NewNeuronBridge(mock)

	agents, err := bridge.DiscoverTool(context.Background(), "lustre_health")
	if err != nil {
		t.Fatalf("DiscoverTool error: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("expected 2 agents from index, got %d", len(agents))
	}
	if agents[0].NodeID != "oss-01" {
		t.Fatalf("first agent should be oss-01 (from index), got %s", agents[0].NodeID)
	}
}

func TestNeuronBridge_DiscoverTool_FallsBackToSonar(t *testing.T) {
	t.Parallel()

	// Capability index is empty — should fall back to Sonar.
	mock := &mockMeshNode{
		nodeID:     "gateway-01",
		capEntries: nil, // empty index
		sonarResults: []AgentInfo{
			{NodeID: "oss-01", Impedance: 5.0},
			{NodeID: "oss-02", Impedance: 10.0},
		},
	}

	bridge := NewNeuronBridge(mock)

	agents, err := bridge.DiscoverTool(context.Background(), "lustre_health")
	if err != nil {
		t.Fatalf("DiscoverTool error: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("expected 2 agents from Sonar, got %d", len(agents))
	}
	if agents[0].NodeID != "oss-01" {
		t.Fatalf("first agent should be oss-01 (from Sonar), got %s", agents[0].NodeID)
	}
}

func TestNeuronBridge_InvokeRemote(t *testing.T) {
	t.Parallel()

	// Set up a tool server on the "remote" end of a pipe.
	client, server := net.Pipe()

	remoteRegistry := NewRegistry(nil)
	remoteRegistry.Register(ToolDefinition{
		Name:     "remote_echo",
		Category: "test",
	}, func(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
		return &ToolResult{Content: args}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = ServeToolConn(ctx, server, remoteRegistry) }()

	// The bridge's GrpcDialer returns the client end of the pipe.
	mock := &mockMeshNode{
		nodeID:   "gateway-01",
		dialConn: client,
	}

	bridge := NewNeuronBridge(mock)

	result, err := bridge.InvokeRemote(ctx, "oss-01", "remote_echo", json.RawMessage(`{"key":"value"}`))
	if err != nil {
		t.Fatalf("InvokeRemote error: %v", err)
	}

	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.Content)
	}

	var got map[string]string
	if err := json.Unmarshal(result.Content, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["key"] != "value" {
		t.Fatalf("got %q, want %q", got["key"], "value")
	}
}

func TestNeuronBridge_ImplementsMeshTransport(t *testing.T) {
	var _ MeshTransport = &NeuronBridge{}
}

func TestNeuronBridge_DiscoverNodes_DelegatesToSonar(t *testing.T) {
	t.Parallel()

	mock := &mockMeshNode{
		nodeID: "gateway-01",
		sonarResults: []AgentInfo{
			{NodeID: "oss-01", Impedance: 5.0},
		},
	}

	bridge := NewNeuronBridge(mock)

	// DiscoverNodes is the gateway.MeshBridge-compatible alias.
	agents, err := bridge.DiscoverNodes(context.Background(), "system_info")
	if err != nil {
		t.Fatalf("DiscoverNodes error: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].NodeID != "oss-01" {
		t.Fatalf("agent should be oss-01, got %s", agents[0].NodeID)
	}
}
