package buddyinfo

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func TestBuddyInfo_Contract(t *testing.T) {
	tool := New()

	if tool.Name() != "get_buddy_info" {
		t.Errorf("expected get_buddy_info, got %s", tool.Name())
	}

	if tool.Category() != "memory" {
		t.Errorf("expected memory, got %s", tool.Category())
	}

	if tool.Description() == "" || tool.Help() == "" {
		t.Errorf("expected non-empty description and help")
	}

	params := tool.Parameters()
	if len(params) != 0 {
		t.Fatalf("expected 0 parameters, got %d", len(params))
	}
}

func TestBuddyInfo_Execute(t *testing.T) {
	tool := New()
	supported, _ := tool.IsSupported()
	if !supported {
		t.Skip("Tool not supported on this environment")
	}

	ctx := context.Background()
	args := json.RawMessage(`{}`)

	res, err := tool.Execute(ctx, args)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if res.Status != registry.StatusOK {
		t.Errorf("expected OK status, got %s", res.Status)
	}

	// Verify the schema can be unmarshaled
	dataBytes, err := json.Marshal(res.Data)
	if err != nil {
		t.Fatalf("failed to marshal res.Data: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(dataBytes, &parsed); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}

	if _, ok := parsed["system_summary"]; !ok {
		t.Errorf("missing system_summary in output")
	}
	if _, ok := parsed["zones"]; !ok {
		t.Errorf("missing zones in output")
	}
}
