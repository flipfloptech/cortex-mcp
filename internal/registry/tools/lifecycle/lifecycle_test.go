package lifecycle

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func TestInstallTool_ContractCompliance(t *testing.T) {
	tool := NewInstallTool()

	if tool.Name() != "node_install" {
		t.Errorf("expected node_install, got %s", tool.Name())
	}
	if tool.Category() != "lifecycle" {
		t.Errorf("expected lifecycle, got %s", tool.Category())
	}
	if tool.Hidden() != true {
		t.Errorf("expected Hidden to be true")
	}
	if tool.Parameters() != nil {
		t.Errorf("expected no parameters")
	}

	supported, reason := tool.IsSupported()
	if !registry.IsLinux() {
		if supported {
			t.Errorf("expected IsSupported to be false on non-Linux")
		}
		if reason == "" {
			t.Errorf("expected a reason for being unsupported")
		}
	} else {
		if !supported {
			t.Errorf("expected IsSupported to be true on Linux")
		}
	}
}

func TestInstallTool_Execute_DryRun(t *testing.T) {
	SetLiveMode(false) // explicitly dry run
	tool := NewInstallTool()

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.Status != registry.StatusOK {
		t.Errorf("expected OK status, got %s", res.Status)
	}

	// We expect scheduled operations
	var data map[string]interface{}
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("failed to unmarshal data: %v", err)
	}

	if data["status"] != "dry_run" {
		t.Errorf("expected status dry_run, got %v", data["status"])
	}

	ops, ok := data["operations"].([]interface{})
	if !ok || len(ops) == 0 {
		t.Errorf("expected operations array, got %v", data["operations"])
	}
}

func TestUpgradeTool_ContractCompliance(t *testing.T) {
	tool := NewUpgradeTool()

	if tool.Name() != "node_upgrade" {
		t.Errorf("expected node_upgrade, got %s", tool.Name())
	}

	params := tool.Parameters()
	if len(params) != 1 || params[0].Name != "path" {
		t.Errorf("expected 1 parameter named 'path'")
	}
}

func TestUpgradeTool_Execute_MissingPath(t *testing.T) {
	tool := NewUpgradeTool()

	// Empty args
	res, _ := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if res.Status != registry.StatusError {
		t.Errorf("expected error status for missing path")
	}

	// No args
	res, _ = tool.Execute(context.Background(), nil)
	if res.Status != registry.StatusError {
		t.Errorf("expected error status for missing args")
	}
}

func TestUpgradeTool_Execute_DryRun(t *testing.T) {
	SetLiveMode(false)
	tool := NewUpgradeTool()

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"path": "/tmp/new-bin"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.Status != registry.StatusOK {
		t.Errorf("expected OK status, got %s", res.Status)
	}
}
