package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"testing"

	pb "github.com/flipfloptech/cortex-mcp/pkg/mesh/proto"
)

// --- Types Benchmarks ---

func BenchmarkGenerateSchema(b *testing.B) {
	def := ToolDefinition{
		Name:        "test",
		Description: "test tool",
		Parameters: []ToolParam{
			{Name: "foo", Type: "string", Description: "foo parameter"},
		},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = def.GenerateSchema()
	}
}

func BenchmarkNewErrorResult(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = NewErrorResult("error message")
	}
}

func BenchmarkNewJSONResult(b *testing.B) {
	data := map[string]string{"key": "value"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewJSONResult(data)
	}
}

func BenchmarkNewTextResult(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = NewTextResult("text result")
	}
}

func BenchmarkSortNodesByImpedance(b *testing.B) {
	ti := ToolInfo{
		Nodes: []AgentInfo{
			{NodeID: "a", Impedance: 10},
			{NodeID: "b", Impedance: 5},
			{NodeID: "c", Impedance: 15},
		},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ti.SortNodesByImpedance()
	}
}

type benchCapabilityTracker struct{}

func (b *benchCapabilityTracker) RegisterCapability(capability string) {}

type benchMeshTransport struct{}

func (b *benchMeshTransport) DiscoverTool(ctx context.Context, toolName string) ([]AgentInfo, error) {
	return nil, nil
}
func (b *benchMeshTransport) InvokeRemote(ctx context.Context, nodeID, toolName string, args json.RawMessage) (*ToolResult, error) {
	return NewTextResult("ok"), nil
}
func (b *benchMeshTransport) DiscoverAllTools(ctx context.Context) ([]ToolInfo, error) {
	return nil, nil
}

type benchMeshNode struct{}

func (b *benchMeshNode) NodeID() string { return "bench-node" }
func (b *benchMeshNode) Sonar(ctx context.Context, capability string) ([]AgentInfo, error) {
	return nil, nil
}
func (b *benchMeshNode) GrpcDialer(ctx context.Context, targetNodeID string) (net.Conn, error) {
	c1, c2 := net.Pipe()
	go func() {
		_, _ = readToolFrame(c2)
		resp := &pb.ToolResponse{IsError: false, ContentJson: []byte(`"ok"`)}
		data, _ := json.Marshal(resp)
		_ = writeToolFrame(c2, data)
		_ = c2.Close()
	}()
	return c1, nil
}
func (b *benchMeshNode) LookupCapability(capability string) []NodeCapEntry {
	return nil
}

// --- Registry Benchmarks ---

func BenchmarkNewRegistry(b *testing.B) {
	tracker := &benchCapabilityTracker{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewRegistry(tracker)
	}
}

func BenchmarkRegister(b *testing.B) {
	tracker := &benchCapabilityTracker{}
	reg := NewRegistry(tracker)
	def := ToolDefinition{Name: "test"}
	handler := func(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
		return NewTextResult("ok"), nil
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reg.Register(def, handler)
	}
}

func BenchmarkListLocal(b *testing.B) {
	tracker := &benchCapabilityTracker{}
	reg := NewRegistry(tracker)
	reg.Register(ToolDefinition{Name: "test"}, nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = reg.ListLocal()
	}
}

func BenchmarkListLocalByCategory(b *testing.B) {
	tracker := &benchCapabilityTracker{}
	reg := NewRegistry(tracker)
	reg.Register(ToolDefinition{Name: "test", Category: "cat1"}, nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = reg.ListLocalByCategory("cat1")
	}
}

func BenchmarkInvokeLocal(b *testing.B) {
	tracker := &benchCapabilityTracker{}
	reg := NewRegistry(tracker)
	reg.Register(ToolDefinition{Name: "test"}, func(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
		return NewTextResult("ok"), nil
	})
	ctx := context.Background()
	args := json.RawMessage(`{}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = reg.InvokeLocal(ctx, "test", args)
	}
}

func BenchmarkGetDefinition(b *testing.B) {
	tracker := &benchCapabilityTracker{}
	reg := NewRegistry(tracker)
	reg.Register(ToolDefinition{Name: "test"}, nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = reg.GetDefinition("test")
	}
}

// --- Protocol Benchmarks ---

func BenchmarkWriteToolFrame(b *testing.B) {
	buf := new(bytes.Buffer)
	data := []byte("hello world")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Reset()
		_ = writeToolFrame(buf, data)
	}
}

func BenchmarkReadToolFrame(b *testing.B) {
	data := []byte("hello world")
	buf := new(bytes.Buffer)
	_ = writeToolFrame(buf, data)
	frameData := buf.Bytes()

	readerBuf := bytes.NewReader(nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		readerBuf.Reset(frameData)
		_, _ = readToolFrame(readerBuf)
	}
}

func BenchmarkDispatchLocal(b *testing.B) {
	tracker := &benchCapabilityTracker{}
	reg := NewRegistry(tracker)
	reg.Register(ToolDefinition{Name: "test"}, func(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
		return NewTextResult("ok"), nil
	})
	req := &pb.ToolRequest{
		Name:     "test",
		ArgsJson: []byte("{}"),
	}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = dispatchLocal(ctx, reg, req)
	}
}

func BenchmarkServeToolListener(b *testing.B) {
	tracker := &benchCapabilityTracker{}
	reg := NewRegistry(tracker)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	l, err := net.Listen("unix", "\x00cortex-bench-tools")
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = l.Close() }()

	go ServeToolListener(ctx, l, reg)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		conn, err := net.Dial("unix", "\x00cortex-bench-tools")
		if err == nil {
			_ = conn.Close()
		}
	}
}

func BenchmarkServeToolConn(b *testing.B) {
	tracker := &benchCapabilityTracker{}
	reg := NewRegistry(tracker)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c1, c2 := net.Pipe()

		go func() {
			_ = c1.Close()
		}()
		_ = ServeToolConn(ctx, c2, reg)
	}
}

func BenchmarkDialInvoke(b *testing.B) {
	ctx := context.Background()
	args := json.RawMessage(`{}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c1, c2 := net.Pipe()
		go func() {
			_, _ = readToolFrame(c2) // Read request to unblock DialInvoke write
			// Fake a response
			resp := &pb.ToolResponse{IsError: false, ContentJson: []byte(`"ok"`)}
			data, _ := json.Marshal(resp)
			_ = writeToolFrame(c2, data)
			_ = c2.Close()
		}()

		_, _ = DialInvoke(ctx, c1, "test", args)
	}
}

// --- Remote Invoker Benchmarks ---

func BenchmarkNewRemoteInvoker(b *testing.B) {
	mesh := &benchMeshTransport{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewRemoteInvoker(mesh)
	}
}

func BenchmarkInvoke(b *testing.B) {
	mesh := &benchMeshTransport{}
	ri := NewRemoteInvoker(mesh)
	ctx := context.Background()
	args := json.RawMessage(`{}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ri.Invoke(ctx, "node-b", "test", args)
	}
}

func BenchmarkFanOut(b *testing.B) {
	mesh := &benchMeshTransport{}
	ri := NewRemoteInvoker(mesh)
	ctx := context.Background()
	args := json.RawMessage(`{}`)
	nodes := []string{"node-1", "node-2", "node-3"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ri.FanOut(ctx, nodes, "test", args)
	}
}

func BenchmarkListAll(b *testing.B) {
	mesh := &benchMeshTransport{}
	ri := NewRemoteInvoker(mesh)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ri.ListAll(ctx)
	}
}

// --- Neuron Bridge Benchmarks ---

func BenchmarkNewNeuronBridge(b *testing.B) {
	node := &benchMeshNode{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewNeuronBridge(node)
	}
}

func BenchmarkDiscoverNodes(b *testing.B) {
	node := &benchMeshNode{}
	bridge := NewNeuronBridge(node)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = bridge.DiscoverNodes(ctx, "test")
	}
}

func BenchmarkDiscoverTool(b *testing.B) {
	node := &benchMeshNode{}
	bridge := NewNeuronBridge(node)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = bridge.DiscoverTool(ctx, "test")
	}
}

func BenchmarkInvokeRemote(b *testing.B) {
	node := &benchMeshNode{}
	bridge := NewNeuronBridge(node)
	ctx := context.Background()
	args := json.RawMessage(`{}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = bridge.InvokeRemote(ctx, "node-b", "test", args)
	}
}

func BenchmarkDiscoverAllTools(b *testing.B) {
	node := &benchMeshNode{}
	bridge := NewNeuronBridge(node)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = bridge.DiscoverAllTools(ctx)
	}
}
