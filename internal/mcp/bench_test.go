package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cortex-mesh/cortex-mesh/api"
	"github.com/cortex-mesh/cortex-mesh/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type dummyDispatcher struct{}

func (d dummyDispatcher) Dispatch(ctx context.Context, toolName string, args json.RawMessage) (*tools.ToolResult, error) {
	return &tools.ToolResult{Content: json.RawMessage(`"ok"`)}, nil
}

type dummyTopology struct{}

func (d dummyTopology) MeshTopology() api.TopologySnapshot {
	return api.TopologySnapshot{}
}

func BenchmarkNewServer(b *testing.B) {
	d := dummyDispatcher{}
	t := dummyTopology{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewServer(d, t, nil)
	}
}

func BenchmarkMCPServer(b *testing.B) {
	srv := NewServer(dummyDispatcher{}, dummyTopology{}, nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = srv.MCPServer()
	}
}

func BenchmarkHandleListTools(b *testing.B) {
	srv := NewServer(dummyDispatcher{}, dummyTopology{}, nil)
	ctx := context.Background()
	req := &mcp.CallToolRequest{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = srv.handleListTools(ctx, req, EmptyInput{})
	}
}

func BenchmarkHandleToolHelp(b *testing.B) {
	srv := NewServer(dummyDispatcher{}, dummyTopology{}, nil)
	ctx := context.Background()
	req := &mcp.CallToolRequest{}
	in := ToolHelpInput{ToolName: "system_info"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = srv.handleToolHelp(ctx, req, in)
	}
}

func BenchmarkHandleCallTool(b *testing.B) {
	srv := NewServer(dummyDispatcher{}, dummyTopology{}, nil)
	ctx := context.Background()
	req := &mcp.CallToolRequest{}
	in := CallToolInput{ToolName: "system_info", Args: make(map[string]interface{})}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = srv.handleCallTool(ctx, req, in)
	}
}

func BenchmarkHandleMeshOverview(b *testing.B) {
	srv := NewServer(dummyDispatcher{}, dummyTopology{}, nil)
	ctx := context.Background()
	req := &mcp.CallToolRequest{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = srv.handleMeshOverview(ctx, req, EmptyInput{})
	}
}

func BenchmarkDispatchToMesh(b *testing.B) {
	srv := NewServer(dummyDispatcher{}, dummyTopology{}, nil)
	ctx := context.Background()
	args := json.RawMessage(`{}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = srv.dispatchToMesh(ctx, "system_info", args)
	}
}

func BenchmarkHandleSystemIntroduction(b *testing.B) {
	srv := NewServer(dummyDispatcher{}, dummyTopology{}, nil)
	ctx := context.Background()
	req := &mcp.GetPromptRequest{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = srv.handleSystemIntroduction(ctx, req)
	}
}

func BenchmarkNewMeshOverviewHandler(b *testing.B) {
	d := dummyDispatcher{}
	t := dummyTopology{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewMeshOverviewHandler(d, t)
	}
}

func BenchmarkExecute(b *testing.B) {
	h := NewMeshOverviewHandler(dummyDispatcher{}, dummyTopology{})
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = h.Execute(ctx)
	}
}

func BenchmarkFanOutTool(b *testing.B) {
	h := NewMeshOverviewHandler(dummyDispatcher{}, dummyTopology{})
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = h.fanOutTool(ctx, "system_info")
	}
}

func BenchmarkParseSysInfoResult(b *testing.B) {
	content := json.RawMessage(`{"hostname":"test-node","os":"linux","arch":"amd64","cpus":4,"kernel":"5.14","distro":"ubuntu","roles":["worker"]}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseSysInfoResult("node1", content)
	}
}

func BenchmarkParseTopoEdges(b *testing.B) {
	edges := make(map[edgeKey]MeshEdge)
	content := json.RawMessage(`{"node2": 10.5, "node3": 20.1}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		parseTopoEdges(edges, "node1", content)
	}
}

func BenchmarkParseDirectPeerCount(b *testing.B) {
	content := json.RawMessage(`{"node2": 10.5, "node3": 20.1}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseDirectPeerCount(content)
	}
}

func BenchmarkAddEdge(b *testing.B) {
	edges := make(map[edgeKey]MeshEdge)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		addEdge(edges, "node1", "node2", 10.5)
	}
}

func BenchmarkExtractTools(b *testing.B) {
	caps := []string{"tool:system_info", "tool:loadavg", "network:gateway"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = extractTools(caps)
	}
}

func BenchmarkRenderMermaid(b *testing.B) {
	overview := &MeshOverview{
		Nodes: []NodeOverview{
			{NodeID: "node1", Hostname: "node1", Roles: []string{"worker"}},
		},
		Edges: []MeshEdge{
			{From: "node1", To: "node2"},
		},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = renderMermaid(overview)
	}
}

func BenchmarkPrimaryRole(b *testing.B) {
	roles := []string{"worker", "storage"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = primaryRole(roles)
	}
}

func BenchmarkSanitizeMermaidID(b *testing.B) {
	id := "node-1.domain.com!"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = sanitizeMermaidID(id)
	}
}

func BenchmarkStartHTTPServer(b *testing.B) {
	srv := NewServer(dummyDispatcher{}, dummyTopology{}, nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// we can't really start it in a tight loop without port conflicts,
		// but we can pass an invalid address to fail fast and measure the setup overhead.
		_ = StartHTTPServer("invalid-address", srv)
	}
}
