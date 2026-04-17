package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// --- selfInstall / selfUninstall tests ---
// These test the local lifecycle operations that the binary performs
// on itself. They produce operation lists (not shell commands) so
// we can verify the logic without root.

func TestSelfInstallOps(t *testing.T) {
	t.Parallel()

	ops := selfInstallOps("/usr/local/bin/mesh-example")

	// Must write a service unit file.
	var hasWriteUnit bool
	var hasReload bool
	var hasEnable bool
	var hasStart bool

	for _, op := range ops {
		switch op.Action {
		case "write_file":
			if strings.Contains(op.Path, "cortex-mesh.service") {
				hasWriteUnit = true
				if !strings.Contains(op.Content, "ExecStart=") {
					t.Error("service unit missing ExecStart")
				}
			}
		case "systemctl":
			switch {
			case strings.Contains(op.Args, "daemon-reload"):
				hasReload = true
			case strings.Contains(op.Args, "enable"):
				hasEnable = true
			case strings.Contains(op.Args, "start") || strings.Contains(op.Args, "restart"):
				hasStart = true
			}
		case "copy_binary":
			if op.Path == "" {
				t.Error("copy_binary missing destination path")
			}
		}
	}

	if !hasWriteUnit {
		t.Error("selfInstallOps missing service unit write")
	}
	if !hasReload {
		t.Error("selfInstallOps missing daemon-reload")
	}
	if !hasEnable {
		t.Error("selfInstallOps missing enable")
	}
	if !hasStart {
		t.Error("selfInstallOps missing start/restart")
	}
}

func TestSelfUninstallOps(t *testing.T) {
	t.Parallel()

	ops := selfUninstallOps()

	var hasStop bool
	var hasDisable bool
	var hasRemoveUnit bool
	var hasReload bool
	var hasRemoveBinary bool

	for _, op := range ops {
		switch op.Action {
		case "systemctl":
			switch {
			case strings.Contains(op.Args, "stop"):
				hasStop = true
			case strings.Contains(op.Args, "disable"):
				hasDisable = true
			case strings.Contains(op.Args, "daemon-reload"):
				hasReload = true
			}
		case "remove_file":
			if strings.Contains(op.Path, ".service") {
				hasRemoveUnit = true
			}
			if strings.Contains(op.Path, defaultInstallPath) {
				hasRemoveBinary = true
			}
		}
	}

	if !hasStop {
		t.Error("selfUninstallOps missing stop")
	}
	if !hasDisable {
		t.Error("selfUninstallOps missing disable")
	}
	if !hasRemoveUnit {
		t.Error("selfUninstallOps missing unit file removal")
	}
	if !hasReload {
		t.Error("selfUninstallOps missing daemon-reload")
	}
	if !hasRemoveBinary {
		t.Error("selfUninstallOps missing binary removal")
	}
}

// --- mesh lifecycle tool handler tests ---
// The tool handlers produce the same operations as the self-* flags
// but are invocable over the mesh.

func TestNodeRestartToolHandler(t *testing.T) {
	t.Parallel()

	result, err := handleNodeRestart(context.Background(), nil)
	if err != nil {
		t.Fatalf("handleNodeRestart returned error: %v", err)
	}
	if result.IsError {
		t.Error("handleNodeRestart should not return IsError")
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(result.Content, &resp); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	cmd, ok := resp["command"].(string)
	if !ok || !strings.Contains(cmd, "restart") {
		t.Errorf("expected restart command, got %v", resp["command"])
	}
}

func TestNodeUninstallToolHandler(t *testing.T) {
	t.Parallel()

	result, err := handleNodeUninstall(context.Background(), nil)
	if err != nil {
		t.Fatalf("handleNodeUninstall returned error: %v", err)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(result.Content, &resp); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	ops, ok := resp["operations"]
	if !ok {
		t.Error("expected operations in response")
	}

	opsSlice, ok := ops.([]interface{})
	if !ok || len(opsSlice) == 0 {
		t.Error("expected non-empty operations list")
	}
}

// --- New tool handler tests ---
// These cover the new lifecycle tools added in the subcommand refactor.

func TestNodeStopToolHandler(t *testing.T) {
	t.Parallel()

	result, err := handleNodeStop(context.Background(), nil)
	if err != nil {
		t.Fatalf("handleNodeStop returned error: %v", err)
	}
	if result.IsError {
		t.Error("handleNodeStop should not return IsError")
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(result.Content, &resp); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	cmd, ok := resp["command"].(string)
	if !ok || !strings.Contains(cmd, "stop") {
		t.Errorf("expected stop command, got %v", resp["command"])
	}

	status, ok := resp["status"].(string)
	if !ok || status != "scheduled" {
		t.Errorf("expected status=scheduled, got %v", resp["status"])
	}
}

func TestNodeUpgradeToolHandler_MissingPath(t *testing.T) {
	t.Parallel()

	// Invoking node_upgrade without a path arg should return an error result.
	result, err := handleNodeUpgrade(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("handleNodeUpgrade returned Go error: %v", err)
	}
	if !result.IsError {
		t.Error("handleNodeUpgrade with empty path should return IsError=true")
	}
}

func TestNodeUpgradeToolHandler_WithPath(t *testing.T) {
	t.Parallel()

	args := json.RawMessage(`{"path": "/tmp/cortex-mesh-new"}`)
	result, err := handleNodeUpgrade(context.Background(), args)
	if err != nil {
		t.Fatalf("handleNodeUpgrade returned Go error: %v", err)
	}
	if result.IsError {
		t.Error("handleNodeUpgrade should not return IsError with valid path")
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(result.Content, &resp); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	// Should produce operations: copy_binary + restart.
	ops, ok := resp["operations"]
	if !ok {
		t.Fatal("expected operations in response")
	}

	opsSlice, ok := ops.([]interface{})
	if !ok || len(opsSlice) < 2 {
		t.Errorf("expected at least 2 operations (copy + restart), got %d", len(opsSlice))
	}

	// Verify first op is copy from the provided path.
	firstOp, ok := opsSlice[0].(map[string]interface{})
	if !ok {
		t.Fatal("could not parse first operation")
	}
	if firstOp["action"] != "copy_file" {
		t.Errorf("expected first action=copy_file, got %v", firstOp["action"])
	}
}

func TestNodeDeployToolHandler_MissingTarget(t *testing.T) {
	t.Parallel()

	// Invoking node_deploy without a target should return an error result.
	handler := buildNodeDeployHandler(nil)
	result, err := handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("handleNodeDeploy returned Go error: %v", err)
	}
	if !result.IsError {
		t.Error("handleNodeDeploy with empty target should return IsError=true")
	}
}

func TestNodeDeployToolHandler_WithTarget(t *testing.T) {
	t.Parallel()

	args := json.RawMessage(`{"target": "10.0.1.5"}`)
	handler := buildNodeDeployHandler(nil)
	result, err := handler(context.Background(), args)
	if err != nil {
		t.Fatalf("handleNodeDeploy returned Go error: %v", err)
	}
	if result == nil {
		t.Fatal("handleNodeDeploy returned nil result")
	}

	// Because we passed a nil node to buildNodeDeployHandler, it should
	// return a tool error indicating a live mesh node is required.
	if !result.IsError {
		t.Error("expected IsError=true when node is nil")
	}
}

// --- Ephemeral cleanup ops ---
// When a node is running ephemerally (from /tmp), uninstall should
// self-destruct rather than try systemd operations.

func TestEphemeralCleanupOps(t *testing.T) {
	t.Parallel()

	ops := ephemeralCleanupOps("/tmp/cortex-mesh-abc123")

	if len(ops) == 0 {
		t.Fatal("expected at least one cleanup op")
	}

	var hasRemove bool
	for _, op := range ops {
		if op.Action == "remove_file" && op.Path == "/tmp/cortex-mesh-abc123" {
			hasRemove = true
		}
	}

	if !hasRemove {
		t.Error("ephemeralCleanupOps should remove the binary path")
	}
}
