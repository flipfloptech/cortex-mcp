package registry_test

import (
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// TestPluginRegistry_FiltersUnsupported verifies that NewPluginRegistry
// only includes tools where IsSupported returns true.
func TestPluginRegistry_FiltersUnsupported(t *testing.T) {
	t.Parallel()

	supported := &mockTool{name: "supported_tool", supported: true, category: registry.CategorySystem}
	unsupported := &mockTool{name: "unsupported_tool", supported: false, reason: "missing binary", category: registry.CategorySystem}

	pr := registry.NewPluginRegistryFrom("test-node", []registry.Tool{supported, unsupported})

	tools := pr.Supported()
	if len(tools) != 1 {
		t.Fatalf("Supported() returned %d tools, want 1", len(tools))
	}
	if tools[0].Name() != "supported_tool" {
		t.Errorf("Supported()[0].Name() = %q, want %q", tools[0].Name(), "supported_tool")
	}

	skipped := pr.Unsupported()
	if len(skipped) != 1 {
		t.Fatalf("Unsupported() returned %d entries, want 1", len(skipped))
	}
	if _, ok := skipped["unsupported_tool"]; !ok {
		t.Error("Unsupported() should contain 'unsupported_tool'")
	}
	if skipped["unsupported_tool"] != "missing binary" {
		t.Errorf("Unsupported reason = %q, want %q", skipped["unsupported_tool"], "missing binary")
	}
}

// TestPluginRegistry_EmptyTools verifies that an empty tool set produces
// an empty registry without errors.
func TestPluginRegistry_EmptyTools(t *testing.T) {
	t.Parallel()

	pr := registry.NewPluginRegistryFrom("test-node", nil)

	if len(pr.Supported()) != 0 {
		t.Errorf("Supported() returned %d tools, want 0", len(pr.Supported()))
	}
	if len(pr.Unsupported()) != 0 {
		t.Errorf("Unsupported() returned %d entries, want 0", len(pr.Unsupported()))
	}
}

// TestPluginRegistry_AllSupported verifies that when all tools are supported,
// none appear in the unsupported map.
func TestPluginRegistry_AllSupported(t *testing.T) {
	t.Parallel()

	tools := []registry.Tool{
		&mockTool{name: "tool_a", supported: true, category: registry.CategoryCompute},
		&mockTool{name: "tool_b", supported: true, category: registry.CategoryMemory},
	}

	pr := registry.NewPluginRegistryFrom("test-node", tools)

	if len(pr.Supported()) != 2 {
		t.Errorf("Supported() returned %d tools, want 2", len(pr.Supported()))
	}
	if len(pr.Unsupported()) != 0 {
		t.Errorf("Unsupported() returned %d entries, want 0", len(pr.Unsupported()))
	}
}

// TestPluginRegistry_AllUnsupported verifies that when no tools are supported,
// all appear in the unsupported map.
func TestPluginRegistry_AllUnsupported(t *testing.T) {
	t.Parallel()

	tools := []registry.Tool{
		&mockTool{name: "tool_a", supported: false, reason: "needs root", category: registry.CategoryCompute},
		&mockTool{name: "tool_b", supported: false, reason: "wrong OS", category: registry.CategoryMemory},
	}

	pr := registry.NewPluginRegistryFrom("test-node", tools)

	if len(pr.Supported()) != 0 {
		t.Errorf("Supported() returned %d tools, want 0", len(pr.Supported()))
	}
	if len(pr.Unsupported()) != 2 {
		t.Errorf("Unsupported() returned %d entries, want 2", len(pr.Unsupported()))
	}
}

// TestPluginRegistry_GetTool_Found verifies that GetTool returns a supported tool by name.
func TestPluginRegistry_GetTool_Found(t *testing.T) {
	t.Parallel()

	tool := &mockTool{name: "target_tool", supported: true, category: registry.CategorySystem}
	pr := registry.NewPluginRegistryFrom("test-node", []registry.Tool{tool})

	found, ok := pr.GetTool("target_tool")
	if !ok {
		t.Fatal("GetTool should find 'target_tool'")
	}
	if found.Name() != "target_tool" {
		t.Errorf("GetTool returned tool with name %q, want %q", found.Name(), "target_tool")
	}
}

// TestPluginRegistry_GetTool_NotFound verifies that GetTool returns false
// for tools that don't exist or aren't supported.
func TestPluginRegistry_GetTool_NotFound(t *testing.T) {
	t.Parallel()

	unsupported := &mockTool{name: "hidden_tool", supported: false, reason: "nope", category: registry.CategorySystem}
	pr := registry.NewPluginRegistryFrom("test-node", []registry.Tool{unsupported})

	_, ok := pr.GetTool("hidden_tool")
	if ok {
		t.Error("GetTool should not find unsupported tool")
	}

	_, ok = pr.GetTool("nonexistent")
	if ok {
		t.Error("GetTool should not find nonexistent tool")
	}
}

// TestPluginRegistry_GetAnyTool_Found verifies that GetAnyTool returns both supported and unsupported tools by name.
func TestPluginRegistry_GetAnyTool_Found(t *testing.T) {
	t.Parallel()

	supported := &mockTool{name: "target_tool", supported: true, category: registry.CategorySystem}
	unsupported := &mockTool{name: "hidden_tool", supported: false, reason: "nope", category: registry.CategorySystem}
	pr := registry.NewPluginRegistryFrom("test-node", []registry.Tool{supported, unsupported})

	// Should find supported tool
	found, ok := pr.GetAnyTool("target_tool")
	if !ok {
		t.Fatal("GetAnyTool should find 'target_tool'")
	}
	if found.Name() != "target_tool" {
		t.Errorf("GetAnyTool returned tool with name %q, want %q", found.Name(), "target_tool")
	}

	// Should also find unsupported tool
	found, ok = pr.GetAnyTool("hidden_tool")
	if !ok {
		t.Fatal("GetAnyTool should find 'hidden_tool'")
	}
	if found.Name() != "hidden_tool" {
		t.Errorf("GetAnyTool returned tool with name %q, want %q", found.Name(), "hidden_tool")
	}
}

// TestPluginRegistry_GetAnyTool_NotFound verifies that GetAnyTool returns false
// for tools that don't exist in the compiled binary at all.
func TestPluginRegistry_GetAnyTool_NotFound(t *testing.T) {
	t.Parallel()

	unsupported := &mockTool{name: "hidden_tool", supported: false, reason: "nope", category: registry.CategorySystem}
	pr := registry.NewPluginRegistryFrom("test-node", []registry.Tool{unsupported})

	_, ok := pr.GetAnyTool("nonexistent")
	if ok {
		t.Error("GetAnyTool should not find nonexistent tool")
	}
}

// TestPluginRegistry_NodeID verifies the registry tracks its node identity.
func TestPluginRegistry_NodeID(t *testing.T) {
	t.Parallel()

	pr := registry.NewPluginRegistryFrom("gateway-01", nil)

	if pr.NodeID() != "gateway-01" {
		t.Errorf("NodeID() = %q, want %q", pr.NodeID(), "gateway-01")
	}
}

// TestGlobalRegister_AddsToGlobalPool verifies that Register() adds tools
// to the global pool and NewPluginRegistry reads from it.
func TestGlobalRegister_AddsToGlobalPool(t *testing.T) {
	// Not parallel — modifies global state.
	// We test the global registration path by directly calling Register
	// and then building a registry from globals.
	tool := &mockTool{name: "global_test_tool", supported: true, category: registry.CategorySystem}

	registry.Register(tool)

	// Build a registry from the global pool.
	pr := registry.NewPluginRegistry("test-node")
	_, ok := pr.GetTool("global_test_tool")
	if !ok {
		t.Error("globally registered tool should be found in NewPluginRegistry")
	}
}
