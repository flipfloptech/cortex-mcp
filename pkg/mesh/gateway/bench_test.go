package gateway

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/tools"
)

type benchCapabilityTracker struct{}

func (b *benchCapabilityTracker) RegisterCapability(capability string) {}

func BenchmarkWithGroupResolver(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = WithGroupResolver(nil)
	}
}

func BenchmarkNew(b *testing.B) {
	tracker := &benchCapabilityTracker{}
	reg := tools.NewRegistry(tracker)
	mesh := &mockMesh{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = New(reg, mesh)
	}
}

func BenchmarkIsNodesetPattern(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = isNodesetPattern("foo,bar")
	}
}

func BenchmarkHandleListTools(b *testing.B) {
	tracker := &benchCapabilityTracker{}
	reg := tools.NewRegistry(tracker)
	reg.Register(tools.ToolDefinition{Name: "test"}, nil)
	gw := New(reg, nil)
	ctx := context.Background()
	args := json.RawMessage(`{}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = gw.HandleListTools(ctx, args)
	}
}

func BenchmarkHandleToolHelp(b *testing.B) {
	tracker := &benchCapabilityTracker{}
	reg := tools.NewRegistry(tracker)
	reg.Register(tools.ToolDefinition{Name: "test"}, nil)
	gw := New(reg, nil)
	ctx := context.Background()
	args := json.RawMessage(`{"tool_name": "test"}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = gw.HandleToolHelp(ctx, args)
	}
}

func BenchmarkHandleCallTool(b *testing.B) {
	tracker := &benchCapabilityTracker{}
	reg := tools.NewRegistry(tracker)
	reg.Register(tools.ToolDefinition{Name: "test"}, func(ctx context.Context, args json.RawMessage) (*tools.ToolResult, error) {
		return tools.NewTextResult("ok"), nil
	})
	gw := New(reg, nil)
	ctx := context.Background()
	args := json.RawMessage(`{"tool_name": "test"}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = gw.HandleCallTool(ctx, args)
	}
}

func BenchmarkHandleAutoRoute(b *testing.B) {
	tracker := &benchCapabilityTracker{}
	reg := tools.NewRegistry(tracker)
	reg.Register(tools.ToolDefinition{Name: "test"}, func(ctx context.Context, args json.RawMessage) (*tools.ToolResult, error) {
		return tools.NewTextResult("ok"), nil
	})
	gw := New(reg, nil)
	ctx := context.Background()
	params := callToolArgs{ToolName: "test"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = gw.handleAutoRoute(ctx, params)
	}
}

func BenchmarkHandleUnicast(b *testing.B) {
	tracker := &benchCapabilityTracker{}
	reg := tools.NewRegistry(tracker)
	mesh := &mockMesh{
		invokeResult: tools.NewTextResult("ok"),
	}
	gw := New(reg, mesh)
	ctx := context.Background()
	params := callToolArgs{ToolName: "test", NodeName: "node-b"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = gw.handleUnicast(ctx, params)
	}
}

func BenchmarkHandleFanOut(b *testing.B) {
	tracker := &benchCapabilityTracker{}
	reg := tools.NewRegistry(tracker)
	mesh := &mockMesh{
		discoverNodes: []tools.AgentInfo{{NodeID: "node-b"}},
		invokeResult:  tools.NewTextResult("ok"),
	}
	gw := New(reg, mesh)
	ctx := context.Background()
	params := callToolArgs{ToolName: "test", NodeName: "node-b"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = gw.handleFanOut(ctx, params)
	}
}

func BenchmarkDispatch(b *testing.B) {
	tracker := &benchCapabilityTracker{}
	reg := tools.NewRegistry(tracker)
	reg.Register(tools.ToolDefinition{Name: "test"}, func(ctx context.Context, args json.RawMessage) (*tools.ToolResult, error) {
		return tools.NewTextResult("ok"), nil
	})
	gw := New(reg, nil)
	ctx := context.Background()
	args := json.RawMessage(`{}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = gw.Dispatch(ctx, "test", args)
	}
}

func BenchmarkMetaTools(b *testing.B) {
	tracker := &benchCapabilityTracker{}
	reg := tools.NewRegistry(tracker)
	gw := New(reg, nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = gw.MetaTools()
	}
}
