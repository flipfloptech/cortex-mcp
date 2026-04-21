package registry

import (
	"context"
	"encoding/json"
	"testing"

	ctools "github.com/cortex-mesh/cortex-mesh/tools"
)

// Dummy tool for testing
type benchTool struct {
	name      string
	supported bool
}

func (t benchTool) Name() string                { return t.name }
func (t benchTool) Description() string         { return "bench tool" }
func (t benchTool) Help() string                { return "help" }
func (t benchTool) Category() string            { return "bench" }
func (t benchTool) Parameters() []ToolParam     { return nil }
func (t benchTool) Hidden() bool                { return false }
func (t benchTool) IsSupported() (bool, string) { return t.supported, "" }
func (t benchTool) Execute(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	return NewResult("tool", "node", StatusOK, "summary", nil), nil
}

type dummyTracker struct{}

func (d dummyTracker) RegisterCapability(cap string)                 {}
func (d dummyTracker) RemoveCapability(nodeID string, name string)   {}
func (d dummyTracker) HasCapability(nodeID string, name string) bool { return true }
func (d dummyTracker) FindNodesWithCapability(name string) []string  { return []string{} }

func BenchmarkRegister(b *testing.B) {
	t := benchTool{name: "register_bench_tool", supported: true}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Register(t)
	}
}

func BenchmarkNewPluginRegistry(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewPluginRegistry("bench-node")
	}
}

func BenchmarkNewPluginRegistryFrom(b *testing.B) {
	tools := []Tool{
		benchTool{name: "tool1", supported: true},
		benchTool{name: "tool2", supported: false},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewPluginRegistryFrom("bench-node", tools)
	}
}

func BenchmarkSupported(b *testing.B) {
	tools := []Tool{benchTool{name: "tool1", supported: true}}
	pr := NewPluginRegistryFrom("bench-node", tools)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = pr.Supported()
	}
}

func BenchmarkUnsupported(b *testing.B) {
	tools := []Tool{benchTool{name: "tool2", supported: false}}
	pr := NewPluginRegistryFrom("bench-node", tools)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = pr.Unsupported()
	}
}

func BenchmarkGetTool(b *testing.B) {
	tools := []Tool{benchTool{name: "tool1", supported: true}}
	pr := NewPluginRegistryFrom("bench-node", tools)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = pr.GetTool("tool1")
	}
}

func BenchmarkNodeID(b *testing.B) {
	pr := NewPluginRegistryFrom("bench-node", nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = pr.NodeID()
	}
}

func BenchmarkBridgeToMesh(b *testing.B) {
	tools := []Tool{benchTool{name: "tool1", supported: true}}
	pr := NewPluginRegistryFrom("bench-node", tools)
	mReg := ctools.NewRegistry(dummyTracker{})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pr.BridgeToMesh(mReg)
	}
}

func BenchmarkDetectNodeRoles(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = DetectNodeRoles()
	}
}

func BenchmarkRoles(b *testing.B) {
	roles := DetectNodeRoles()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = roles.Roles()
	}
}

func BenchmarkHasStorageRole(b *testing.B) {
	roles := DetectNodeRoles()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = roles.HasStorageRole()
	}
}

func BenchmarkReadOSRelease(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ReadOSRelease()
	}
}

func BenchmarkIsDistro(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = IsDistro("ubuntu")
	}
}

func BenchmarkIsLinux(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = IsLinux()
	}
}

func BenchmarkPathExists(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = PathExists("/tmp")
	}
}

func BenchmarkHasBinary(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = HasBinary("ls")
	}
}

func BenchmarkListSubdirs(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = listSubdirs("/tmp")
	}
}

func BenchmarkNewResult(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewResult("tool", "node", StatusOK, "test data", nil)
	}
}

func BenchmarkNewErrorResult(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewErrorResult("tool", "node", "error message")
	}
}
