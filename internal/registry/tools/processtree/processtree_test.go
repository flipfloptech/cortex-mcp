package processtree

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func TestProcessTreeTool_ContractCompliance(t *testing.T) {
	tool := New()

	if tool.Name() != "get_process_tree" {
		t.Errorf("expected name 'get_process_tree', got %q", tool.Name())
	}

	if tool.Category() != registry.CategoryCompute {
		t.Errorf("expected category 'compute', got %q", tool.Category())
	}

	params := tool.Parameters()
	if len(params) != 1 {
		t.Errorf("expected exactly 1 parameter, got %d", len(params))
	} else {
		if params[0].Name != "target_pid" {
			t.Errorf("expected parameter name 'target_pid', got %q", params[0].Name)
		}
		if params[0].Type != "integer" {
			t.Errorf("expected parameter type 'integer', got %q", params[0].Type)
		}
		if !params[0].Required {
			t.Error("expected target_pid to be required")
		}
	}
}

func TestProcessTreeTool_Execute(t *testing.T) {
	tool := New()

	supported, _ := tool.IsSupported()
	if !supported {
		t.Skip("skipping execute test because tool is not supported on this host")
	}

	// target_pid=1 should always exist on Linux
	args := []byte(`{"target_pid": 1}`)

	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}

	if res.Status == registry.StatusError {
		t.Fatalf("expected non-error result, got error summary: %s", res.Summary)
	}

	// Ensure the result data can be unmarshaled into a map
	var data map[string]interface{}
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("failed to unmarshal result data: %v", err)
	}

	if _, ok := data["pid"]; !ok {
		t.Error("expected 'pid' in result data")
	}
	if _, ok := data["children"]; !ok {
		t.Error("expected 'children' in result data")
	}
}

func BenchmarkProcessTreeTool_Execute(b *testing.B) {
	tool := New()
	ctx := context.Background()
	args := []byte(`{"target_pid": 1}`)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, args)
	}
}
