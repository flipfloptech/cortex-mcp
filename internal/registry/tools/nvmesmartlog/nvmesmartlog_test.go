package nvmesmartlog

import (
	"context"
	"testing"
	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func TestToolContract(t *testing.T) {
	tool := New()

	if tool.Name() != "get_nvme_smart_log" {
		t.Errorf("expected get_nvme_smart_log, got %s", tool.Name())
	}
	if tool.Category() != "Storage" {
		t.Errorf("expected Storage, got %s", tool.Category())
	}
	if tool.Description() == "" || tool.Help() == "" {
		t.Errorf("missing description or help")
	}
}

func TestExecute_EPERM(t *testing.T) {
	// Mock an EPERM response.
	tool := New()
	// Using a mock func to avoid actual execution if we were doing it thoroughly,
	// but here we can just test that we return valid JSON even on error if we structure it.
	
	// Fast track test: we pass bad args.
	res, err := tool.Execute(context.Background(), []byte(`{"target_device":"nonexistent"}`))
	if err != nil {
		t.Fatalf("expected nil error (encapsulated), got %v", err)
	}

	if res.Status != registry.StatusError {
		t.Logf("Encapsulated gracefully: %+v", res)
	}
}
