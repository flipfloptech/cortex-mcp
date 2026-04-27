package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/api"
	"github.com/flipfloptech/cortex-mcp/pkg/mesh/tools"
)

// mockTopologyProvider implements TopologyProvider for testing.
type mockTopologyProvider struct {
	snapshot api.TopologySnapshot
}

func (m *mockTopologyProvider) MeshTopology() api.TopologySnapshot {
	return m.snapshot
}

// buildMockDispatcher creates a dispatcher that returns canned fan-out results
// for get_system_info and get_mesh_topology calls.
func buildMockDispatcher(sysInfoResults, topoResults map[string]json.RawMessage) *mockDispatcher {
	return &mockDispatcher{
		dispatchFunc: func(ctx context.Context, toolName string, args json.RawMessage) (*tools.ToolResult, error) {
			var callArgs struct {
				ToolName string `json:"tool_name"`
				NodeName string `json:"node_name"`
			}
			_ = json.Unmarshal(args, &callArgs)

			var results []json.RawMessage

			switch callArgs.ToolName {
			case "get_system_info":
				for nodeID, content := range sysInfoResults {
					nr, _ := json.Marshal(map[string]interface{}{
						"node_id":  nodeID,
						"content":  json.RawMessage(content),
						"is_error": false,
					})
					results = append(results, nr)
				}
			case "get_mesh_topology":
				for nodeID, content := range topoResults {
					nr, _ := json.Marshal(map[string]interface{}{
						"node_id":  nodeID,
						"content":  json.RawMessage(content),
						"is_error": false,
					})
					results = append(results, nr)
				}
			default:
				return &tools.ToolResult{Content: json.RawMessage(`"unknown"`)}, nil
			}

			resp, _ := json.Marshal(map[string]interface{}{
				"results": results,
			})
			return &tools.ToolResult{Content: resp}, nil
		},
	}
}

func TestMeshOverview_RoleCountAggregation(t *testing.T) {
	t.Parallel()

	sysInfo := map[string]json.RawMessage{
		"oss1": json.RawMessage(`{"tool_name":"get_system_info","node_id":"oss1","status":"ok","summary":"test","data":{"hostname":"oss1","os":"linux","arch":"amd64","cpus":64,"kernel":"5.14","distro":"Rocky","roles":["oss","sfa"],"role_info":{"is_sfa":true,"is_oss":true}}}`),
		"oss2": json.RawMessage(`{"tool_name":"get_system_info","node_id":"oss2","status":"ok","summary":"test","data":{"hostname":"oss2","os":"linux","arch":"amd64","cpus":64,"kernel":"5.14","distro":"Rocky","roles":["oss"],"role_info":{"is_oss":true}}}`),
		"mds1": json.RawMessage(`{"tool_name":"get_system_info","node_id":"mds1","status":"ok","summary":"test","data":{"hostname":"mds1","os":"linux","arch":"amd64","cpus":32,"kernel":"5.14","distro":"Rocky","roles":["mds","mgs"],"role_info":{"is_mds":true,"is_mgs":true}}}`),
	}

	topo := map[string]json.RawMessage{
		"oss1": json.RawMessage(`{"node_id":"oss1","direct_peers":2,"known_nodes":3,"node_details":[{"node_id":"gateway","impedance":0.1,"next_hop":"gateway","is_direct":true},{"node_id":"oss2","impedance":0.05,"next_hop":"oss2","is_direct":true}]}`),
		"oss2": json.RawMessage(`{"node_id":"oss2","direct_peers":1,"known_nodes":3,"node_details":[{"node_id":"oss1","impedance":0.05,"next_hop":"oss1","is_direct":true}]}`),
		"mds1": json.RawMessage(`{"node_id":"mds1","direct_peers":1,"known_nodes":3,"node_details":[{"node_id":"gateway","impedance":0.1,"next_hop":"gateway","is_direct":true}]}`),
	}

	dispatcher := buildMockDispatcher(sysInfo, topo)
	gwTopo := &mockTopologyProvider{
		snapshot: api.TopologySnapshot{
			NodeID:      "gateway",
			DirectPeers: 2,
			KnownNodes:  3,
			NodeDetails: []api.NodeSummary{
				{NodeID: "oss1", Impedance: 0.1, NextHop: "oss1", IsDirect: true},
				{NodeID: "oss2", Impedance: 0.15, NextHop: "oss1", IsDirect: false},
				{NodeID: "mds1", Impedance: 0.1, NextHop: "mds1", IsDirect: true},
			},
		},
	}

	handler := NewMeshOverviewHandler(dispatcher, gwTopo)
	result, err := handler.Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	// Role counts
	if result.RoleCounts["oss"] != 2 {
		t.Errorf("expected 2 OSS nodes, got %d", result.RoleCounts["oss"])
	}
	if result.RoleCounts["sfa"] != 1 {
		t.Errorf("expected 1 SFA node, got %d", result.RoleCounts["sfa"])
	}
	if result.RoleCounts["mds"] != 1 {
		t.Errorf("expected 1 MDS node, got %d", result.RoleCounts["mds"])
	}
	if result.RoleCounts["mgs"] != 1 {
		t.Errorf("expected 1 MGS node, got %d", result.RoleCounts["mgs"])
	}
}

func TestMeshOverview_EdgeDeduplication(t *testing.T) {
	t.Parallel()

	// Empty sysinfo — we only care about edges here.
	sysInfo := map[string]json.RawMessage{
		"nodeA": json.RawMessage(`{"tool_name":"get_system_info","node_id":"nodeA","status":"ok","summary":"test","data":{"hostname":"nodeA","os":"linux","arch":"amd64","cpus":4,"kernel":"5.14","roles":["generic"],"role_info":{}}}`),
		"nodeB": json.RawMessage(`{"tool_name":"get_system_info","node_id":"nodeB","status":"ok","summary":"test","data":{"hostname":"nodeB","os":"linux","arch":"amd64","cpus":4,"kernel":"5.14","roles":["generic"],"role_info":{}}}`),
	}

	// A reports B as direct, B reports A as direct — should produce ONE edge.
	topo := map[string]json.RawMessage{
		"nodeA": json.RawMessage(`{"node_id":"nodeA","direct_peers":1,"known_nodes":2,"node_details":[{"node_id":"nodeB","impedance":0.1,"next_hop":"nodeB","is_direct":true}]}`),
		"nodeB": json.RawMessage(`{"node_id":"nodeB","direct_peers":1,"known_nodes":2,"node_details":[{"node_id":"nodeA","impedance":0.1,"next_hop":"nodeA","is_direct":true}]}`),
	}

	dispatcher := buildMockDispatcher(sysInfo, topo)
	gwTopo := &mockTopologyProvider{
		snapshot: api.TopologySnapshot{
			NodeID:      "gateway",
			DirectPeers: 2,
			KnownNodes:  2,
			NodeDetails: []api.NodeSummary{
				{NodeID: "nodeA", Impedance: 0.1, NextHop: "nodeA", IsDirect: true},
				{NodeID: "nodeB", Impedance: 0.1, NextHop: "nodeB", IsDirect: true},
			},
		},
	}

	handler := NewMeshOverviewHandler(dispatcher, gwTopo)
	result, err := handler.Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	// nodeA↔nodeB should appear exactly once, plus gateway↔nodeA and gateway↔nodeB.
	edgeCount := len(result.Edges)
	if edgeCount != 3 {
		t.Errorf("expected 3 edges (gw↔A, gw↔B, A↔B), got %d: %+v", edgeCount, result.Edges)
	}
}

func TestMeshOverview_MermaidContainsNodes(t *testing.T) {
	t.Parallel()

	sysInfo := map[string]json.RawMessage{
		"oss1": json.RawMessage(`{"tool_name":"get_system_info","node_id":"oss1","status":"ok","summary":"test","data":{"hostname":"oss1","os":"linux","arch":"amd64","cpus":64,"kernel":"5.14","roles":["oss"],"role_info":{"is_oss":true}}}`),
	}
	topo := map[string]json.RawMessage{
		"oss1": json.RawMessage(`{"node_id":"oss1","direct_peers":1,"known_nodes":1,"node_details":[{"node_id":"gateway","impedance":0.1,"next_hop":"gateway","is_direct":true}]}`),
	}

	dispatcher := buildMockDispatcher(sysInfo, topo)
	gwTopo := &mockTopologyProvider{
		snapshot: api.TopologySnapshot{
			NodeID:      "gateway",
			DirectPeers: 1,
			KnownNodes:  1,
			NodeDetails: []api.NodeSummary{
				{NodeID: "oss1", Impedance: 0.1, NextHop: "oss1", IsDirect: true},
			},
		},
	}

	handler := NewMeshOverviewHandler(dispatcher, gwTopo)
	result, err := handler.Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.MermaidGraph == "" {
		t.Fatal("expected non-empty Mermaid graph")
	}
	if !strings.Contains(result.MermaidGraph, "oss1") {
		t.Error("Mermaid graph should contain oss1")
	}
	if !strings.Contains(result.MermaidGraph, "gateway") {
		t.Error("Mermaid graph should contain gateway")
	}
	if !strings.Contains(result.MermaidGraph, "graph TD") {
		t.Error("Mermaid graph should start with graph TD")
	}
}

func TestMeshOverview_EmptyMesh(t *testing.T) {
	t.Parallel()

	dispatcher := buildMockDispatcher(nil, nil)
	gwTopo := &mockTopologyProvider{
		snapshot: api.TopologySnapshot{
			NodeID:      "gateway",
			DirectPeers: 0,
			KnownNodes:  0,
		},
	}

	handler := NewMeshOverviewHandler(dispatcher, gwTopo)
	result, err := handler.Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.TotalNodes != 0 {
		t.Errorf("expected 0 total nodes, got %d", result.TotalNodes)
	}
	if result.GatewayNodeID != "gateway" {
		t.Errorf("expected gateway node ID, got %q", result.GatewayNodeID)
	}
}
