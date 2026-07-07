package threadwchan

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func TestThreadWchan_Contract(t *testing.T) {
	tool := New()

	if tool.Name() != "get_thread_wchan" {
		t.Errorf("expected get_thread_wchan, got %s", tool.Name())
	}

	if tool.Category() != registry.CategoryCompute {
		t.Errorf("expected compute, got %s", tool.Category())
	}

	if tool.Description() == "" || tool.Help() == "" {
		t.Errorf("expected non-empty description and help")
	}

	params := tool.Parameters()
	if len(params) != 1 {
		t.Fatalf("expected 1 parameter, got %d", len(params))
	}

	if params[0].Name != "target_pid" {
		t.Errorf("expected target_pid param")
	}
}

func TestThreadWchan_Execute_Global(t *testing.T) {
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

	// Verify Mode 1 schema
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
	if _, ok := parsed["thread_states"]; !ok {
		t.Errorf("missing thread_states in output")
	}
	if _, ok := parsed["threads"]; ok {
		t.Errorf("Mode 1 should not have threads array in output")
	}
}

func TestThreadWchan_Execute_Targeted(t *testing.T) {
	tool := New()
	supported, _ := tool.IsSupported()
	if !supported {
		t.Skip("Tool not supported on this environment")
	}

	ctx := context.Background()
	args := json.RawMessage(`{"target_pid": 1}`)

	res, err := tool.Execute(ctx, args)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if res.Status != registry.StatusOK {
		t.Errorf("expected OK status, got %s", res.Status)
	}

	// Verify Mode 2 schema
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
	if _, ok := parsed["threads"]; !ok {
		t.Errorf("missing threads array in Mode 2 output")
	}
	if _, ok := parsed["blocked_wchan"]; ok {
		t.Errorf("Mode 2 should not have blocked_wchan map in output")
	}
}
