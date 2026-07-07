package registry_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// mockTool is a minimal Tool implementation for testing the contract.
type mockTool struct {
	name        string
	description string
	help        string
	category    registry.Category
	params      []registry.ToolParam
	supported   bool
	reason      string
	execFn      func(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error)
}

func (t *mockTool) Name() string                     { return t.name }
func (t *mockTool) Description() string              { return t.description }
func (t *mockTool) Help() string                     { return t.help }
func (t *mockTool) Category() registry.Category      { return t.category }
func (t *mockTool) Parameters() []registry.ToolParam { return t.params }
func (t *mockTool) IsSupported() (bool, string)      { return t.supported, t.reason }
func (t *mockTool) Hidden() bool                     { return false }

func (t *mockTool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	if t.execFn != nil {
		return t.execFn(ctx, args)
	}
	return registry.NewResult(t.name, registry.StatusOK, "mock result", nil), nil
}

// TestToolInterface_ContractCompliance verifies that a Tool implementation
// satisfies all interface methods and returns sensible values.
func TestToolInterface_ContractCompliance(t *testing.T) {
	t.Parallel()

	tool := &mockTool{
		name:        "test_tool",
		description: "A test tool",
		help:        "Detailed help for test_tool.\n\nFiltering: deterministic.",
		category:    registry.CategorySystem,
		params: []registry.ToolParam{
			{Name: "verbose", Type: "boolean", Description: "Enable verbose output", Required: false, Default: "false"},
		},
		supported: true,
	}

	// Verify all interface methods return expected values.
	if tool.Name() != "test_tool" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "test_tool")
	}
	if tool.Description() != "A test tool" {
		t.Errorf("Description() = %q, want %q", tool.Description(), "A test tool")
	}
	if tool.Help() == "" {
		t.Error("Help() must not be empty")
	}
	if tool.Category() != registry.CategorySystem {
		t.Errorf("Category() = %q, want %q", tool.Category(), registry.CategorySystem)
	}

	params := tool.Parameters()
	if len(params) != 1 {
		t.Fatalf("Parameters() returned %d params, want 1", len(params))
	}
	if params[0].Name != "verbose" {
		t.Errorf("Parameters()[0].Name = %q, want %q", params[0].Name, "verbose")
	}

	supported, reason := tool.IsSupported()
	if !supported {
		t.Errorf("IsSupported() = false, want true")
	}
	if reason != "" {
		t.Errorf("IsSupported() reason = %q, want empty", reason)
	}
}

// TestToolInterface_UnsupportedReturnsReason verifies that unsupported tools
// provide a clear reason string for logging.
func TestToolInterface_UnsupportedReturnsReason(t *testing.T) {
	t.Parallel()

	tool := &mockTool{
		name:      "lustre_health",
		supported: false,
		reason:    "lctl not found in PATH",
	}

	supported, reason := tool.IsSupported()
	if supported {
		t.Error("IsSupported() = true, want false")
	}
	if reason == "" {
		t.Error("unsupported tool must provide a reason")
	}
	if reason != "lctl not found in PATH" {
		t.Errorf("IsSupported() reason = %q, want %q", reason, "lctl not found in PATH")
	}
}

// TestToolInterface_ExecuteRespectsContext verifies that Execute honors
// context cancellation.
func TestToolInterface_ExecuteRespectsContext(t *testing.T) {
	t.Parallel()

	tool := &mockTool{
		name:      "slow_tool",
		supported: true,
		execFn: func(ctx context.Context, _ json.RawMessage) (*registry.ToolResult, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
				return registry.NewResult("slow_tool", registry.StatusOK, "done", nil), nil
			}
		},
	}

	// Cancelled context should cause Execute to return context error.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	_, err := tool.Execute(ctx, nil)
	if err == nil {
		t.Error("Execute with cancelled context should return error")
	}
}

// TestToolInterface_ExecuteWithArguments verifies that Execute receives
// and can parse JSON arguments.
func TestToolInterface_ExecuteWithArguments(t *testing.T) {
	t.Parallel()

	tool := &mockTool{
		name:      "grep_tool",
		supported: true,
		execFn: func(_ context.Context, args json.RawMessage) (*registry.ToolResult, error) {
			var params struct {
				Pattern string `json:"pattern"`
			}
			if err := json.Unmarshal(args, &params); err != nil {
				return registry.NewErrorResult("grep_tool", err.Error()), nil
			}
			return registry.NewResult("grep_tool", registry.StatusOK, "found: "+params.Pattern, nil), nil
		},
	}

	args, _ := json.Marshal(map[string]string{"pattern": "ERROR"})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Summary != "found: ERROR" {
		t.Errorf("Summary = %q, want %q", result.Summary, "found: ERROR")
	}
}
