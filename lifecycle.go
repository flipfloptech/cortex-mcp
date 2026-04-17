// Package main provides agent-level lifecycle management for mesh nodes.
//
// Instead of running raw shell commands over SSH, lifecycle operations
// are modeled as structured operations (lifecycleOp) that the binary
// executes locally. This keeps all systemd interaction inside the
// binary itself — the gateway never constructs shell commands.
//
// Two interfaces:
//   - Binary flags (-self-install, -self-uninstall): for pre-mesh bootstrap
//   - Mesh tools (node_install, node_uninstall, node_restart): for runtime
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"time"

	"github.com/cortex-mesh/cortex-mesh/tools"
	"github.com/cortex-mesh/cortex-mesh/transport"
)

// lifecycleOp represents a single lifecycle operation that the binary
// performs on the local system. Operations are structured data, not
// shell commands — this is the key abstraction over raw SSH exec.
type lifecycleOp struct {
	Action  string `json:"action"`            // "systemctl", "write_file", "remove_file", "copy_binary"
	Args    string `json:"args,omitempty"`    // e.g. "daemon-reload", "restart cortex-mesh"
	Path    string `json:"path,omitempty"`    // file path for write/remove/copy
	Content string `json:"content,omitempty"` // file content for write_file
}

// selfInstallOps returns the sequence of operations to install the
// running binary as a persistent systemd service.
func selfInstallOps(binaryPath string) []lifecycleOp {
	return []lifecycleOp{
		{Action: "copy_binary", Path: defaultInstallPath},
		{Action: "write_file", Path: serviceUnitPath(), Content: generateServiceUnit(defaultInstallPath)},
		{Action: "systemctl", Args: "daemon-reload"},
		{Action: "systemctl", Args: fmt.Sprintf("enable %s", serviceName)},
		{Action: "systemctl", Args: fmt.Sprintf("restart %s", serviceName)},
	}
}

// selfUninstallOps returns the sequence of operations to fully remove
// the cortex-mesh systemd service, unit file, and binary.
func selfUninstallOps() []lifecycleOp {
	return []lifecycleOp{
		{Action: "systemctl", Args: fmt.Sprintf("stop %s", serviceName)},
		{Action: "systemctl", Args: fmt.Sprintf("disable %s", serviceName)},
		{Action: "remove_file", Path: serviceUnitPath()},
		{Action: "systemctl", Args: "daemon-reload"},
		{Action: "remove_file", Path: defaultInstallPath},
	}
}

// executeOps runs a sequence of lifecycle operations on the local system.
// Returns the first error encountered.
func executeOps(ops []lifecycleOp) error {
	for _, op := range ops {
		if err := executeOp(op); err != nil {
			return fmt.Errorf("%s %s: %w", op.Action, op.Args, err)
		}
	}
	return nil
}

// executeOp runs a single lifecycle operation.
func executeOp(op lifecycleOp) error {
	switch op.Action {
	case "systemctl":
		slog.Info("lifecycle", "action", "systemctl", "args", op.Args)
		//nolint:gosec // Args are constructed internally, not from user input.
		cmd := exec.Command("systemctl", splitArgs(op.Args)...)
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		return cmd.Run()

	case "write_file":
		slog.Info("lifecycle", "action", "write_file", "path", op.Path)
		return os.WriteFile(op.Path, []byte(op.Content), 0644)

	case "remove_file":
		slog.Info("lifecycle", "action", "remove_file", "path", op.Path)
		return os.Remove(op.Path)

	case "copy_binary":
		slog.Info("lifecycle", "action", "copy_binary", "dest", op.Path)
		src, err := os.Executable()
		if err != nil {
			return fmt.Errorf("resolve executable: %w", err)
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("read binary: %w", err)
		}
		return os.WriteFile(op.Path, data, 0755)

	default:
		return fmt.Errorf("unknown lifecycle action: %s", op.Action)
	}
}

// splitArgs splits a simple argument string by spaces. This is NOT
// a shell parser — it handles the fixed-format systemctl arguments
// produced by selfInstallOps/selfUninstallOps.
func splitArgs(s string) []string {
	var args []string
	for _, part := range splitSimple(s) {
		if part != "" {
			args = append(args, part)
		}
	}
	return args
}

func splitSimple(s string) []string {
	var result []string
	current := ""
	for _, c := range s {
		if c == ' ' {
			result = append(result, current)
			current = ""
		} else {
			current += string(c)
		}
	}
	if current != "" {
		result = append(result, current)
	}
	return result
}

// --- Mesh tool handlers ---
// These are registered in defineTools() and invocable over the mesh
// via call_tool. They perform lifecycle operations on the local node.
//
// The actual execution is deferred (goroutine + sleep) so the RPC
// response is sent before the node restarts/exits. Execution only
// fires when running on a deployed node (CORTEX_MESH_SPAWNED=1).

// handleNodeRestart restarts the cortex-mesh systemd service locally.
func handleNodeRestart(_ context.Context, _ json.RawMessage) (*tools.ToolResult, error) {
	cmd := fmt.Sprintf("restart %s", serviceName)
	resp := map[string]string{
		"command": cmd,
		"status":  "scheduled",
	}
	data, _ := json.Marshal(resp)

	if transport.WasDeployed() {
		go func() {
			time.Sleep(200 * time.Millisecond)
			_ = executeOp(lifecycleOp{Action: "systemctl", Args: cmd})
		}()
	}

	return &tools.ToolResult{Content: data}, nil
}

// handleNodeUninstall returns the uninstall operations that will be
// executed. The actual uninstall is deferred so the RPC can return.
func handleNodeUninstall(_ context.Context, _ json.RawMessage) (*tools.ToolResult, error) {
	ops := selfUninstallOps()
	resp := map[string]interface{}{
		"operations": ops,
		"status":     "scheduled",
	}
	data, _ := json.Marshal(resp)

	if transport.WasDeployed() {
		go func() {
			time.Sleep(200 * time.Millisecond)
			if err := executeOps(ops); err != nil {
				slog.Error("node_uninstall failed", "error", err)
			}
		}()
	}

	return &tools.ToolResult{Content: data}, nil
}

// handleNodeInstall installs the node as a persistent systemd service.
func handleNodeInstall(_ context.Context, _ json.RawMessage) (*tools.ToolResult, error) {
	binaryPath, err := os.Executable()
	if err != nil {
		return tools.NewErrorResult(fmt.Sprintf("resolve executable: %v", err)), nil
	}

	ops := selfInstallOps(binaryPath)
	resp := map[string]interface{}{
		"operations": ops,
		"status":     "scheduled",
	}
	data, _ := json.Marshal(resp)

	if transport.WasDeployed() {
		go func() {
			time.Sleep(200 * time.Millisecond)
			if err := executeOps(ops); err != nil {
				slog.Error("node_install failed", "error", err)
			}
		}()
	}

	return &tools.ToolResult{Content: data}, nil
}
