// Package main provides agent-level lifecycle management for mesh nodes.
//
// Lifecycle operations are modeled as structured operations (lifecycleOp)
// that the binary executes locally. This keeps all systemd interaction
// inside the binary itself — the gateway never constructs shell commands.
//
// Tool handlers:
//   - node_install:   convert ephemeral → persistent (systemd service)
//   - node_uninstall: remove persistent service OR cleanup ephemeral binary
//   - node_restart:   restart the systemd service
//   - node_stop:      stop the systemd service without uninstalling
//   - node_upgrade:   replace the binary and restart
//   - node_deploy:    deploy this binary to another host via the mesh
package mesh

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/cortex-mesh/cortex-mesh/api"
	"github.com/cortex-mesh/cortex-mesh/tools"
	"github.com/cortex-mesh/cortex-mesh/transport"
)

// liveMode is set to true when the binary enters "serve" or "daemon" mode.
// This guards deferred execution (systemctl, os.Exit) so handlers return
// structured responses without side effects during tests.
var liveMode bool

// lifecycleOp represents a single lifecycle operation that the binary
// performs on the local system. Operations are structured data, not
// shell commands — this is the key abstraction over raw SSH exec.
type lifecycleOp struct {
	Action  string `json:"action"`            // "systemctl", "write_file", "remove_file", "copy_binary", "copy_file"
	Args    string `json:"args,omitempty"`    // e.g. "daemon-reload", "restart cortex-mesh"
	Path    string `json:"path,omitempty"`    // file path for write/remove/copy
	Src     string `json:"src,omitempty"`     // source path for copy_file
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

// ephemeralCleanupOps returns operations to self-destruct an ephemeral
// node running from a temporary path (e.g., /tmp/cortex-mesh-abc123).
func ephemeralCleanupOps(binaryPath string) []lifecycleOp {
	return []lifecycleOp{
		{Action: "remove_file", Path: binaryPath},
	}
}

// nodeUpgradeOps returns the sequence of operations to upgrade an
// installed node: copy the new binary from src to the install path,
// then restart the service.
func nodeUpgradeOps(srcPath string) []lifecycleOp {
	return []lifecycleOp{
		{Action: "copy_file", Src: srcPath, Path: defaultInstallPath},
		{Action: "systemctl", Args: "daemon-reload"},
		{Action: "systemctl", Args: fmt.Sprintf("restart %s", serviceName)},
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

	case "copy_file":
		slog.Info("lifecycle", "action", "copy_file", "src", op.Src, "dest", op.Path)
		data, err := os.ReadFile(op.Src)
		if err != nil {
			return fmt.Errorf("read source: %w", err)
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
// fires when running on a deployed node (liveMode == true).

// handleNodeRestart restarts the cortex-mesh systemd service locally.
func handleNodeRestart(_ context.Context, _ json.RawMessage) (*tools.ToolResult, error) {
	cmd := fmt.Sprintf("restart %s", serviceName)
	resp := map[string]string{
		"command": cmd,
		"status":  "scheduled",
	}
	data, _ := json.Marshal(resp)

	if liveMode {
		go func() {
			time.Sleep(200 * time.Millisecond)
			_ = executeOp(lifecycleOp{Action: "systemctl", Args: cmd})
		}()
	}

	return &tools.ToolResult{Content: data}, nil
}

// handleNodeStop stops the cortex-mesh systemd service without uninstalling.
func handleNodeStop(_ context.Context, _ json.RawMessage) (*tools.ToolResult, error) {
	cmd := fmt.Sprintf("stop %s", serviceName)
	resp := map[string]string{
		"command": cmd,
		"status":  "scheduled",
	}
	data, _ := json.Marshal(resp)

	if liveMode {
		go func() {
			time.Sleep(200 * time.Millisecond)
			_ = executeOp(lifecycleOp{Action: "systemctl", Args: cmd})
		}()
	}

	return &tools.ToolResult{Content: data}, nil
}

// handleNodeUninstall detects whether the node is running as a persistent
// daemon (from /opt/...) or ephemerally (from /tmp/...) and generates
// the appropriate cleanup operations.
func handleNodeUninstall(_ context.Context, _ json.RawMessage) (*tools.ToolResult, error) {
	binaryPath, err := os.Executable()
	if err != nil {
		return tools.NewErrorResult(fmt.Sprintf("resolve executable: %v", err)), nil
	}

	var ops []lifecycleOp
	if strings.HasPrefix(binaryPath, defaultInstallPath) || strings.HasPrefix(binaryPath, "/opt/") {
		// Persistent install — full systemd teardown.
		ops = selfUninstallOps()
	} else {
		// Ephemeral — just remove the binary.
		ops = ephemeralCleanupOps(binaryPath)
	}

	resp := map[string]interface{}{
		"operations": ops,
		"status":     "scheduled",
	}
	data, _ := json.Marshal(resp)

	if liveMode {
		go func() {
			time.Sleep(200 * time.Millisecond)
			if execErr := executeOps(ops); execErr != nil {
				slog.Error("node_uninstall failed", "error", execErr)
			}
			// For ephemeral nodes, exit after cleanup.
			if !strings.HasPrefix(binaryPath, "/opt/") {
				os.Exit(0)
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

	if liveMode {
		go func() {
			time.Sleep(200 * time.Millisecond)
			if execErr := executeOps(ops); execErr != nil {
				slog.Error("node_install failed", "error", execErr)
			}
		}()
	}

	return &tools.ToolResult{Content: data}, nil
}

// handleNodeUpgrade accepts a path to a new binary, generates operations
// to copy it over the installed binary and restart the service.
func handleNodeUpgrade(_ context.Context, args json.RawMessage) (*tools.ToolResult, error) {
	var params struct {
		Path string `json:"path"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &params); err != nil {
			return tools.NewErrorResult(fmt.Sprintf("parse args: %v", err)), nil
		}
	}

	if params.Path == "" {
		return tools.NewErrorResult("path is required: provide the path to the new binary"), nil
	}

	ops := nodeUpgradeOps(params.Path)
	resp := map[string]interface{}{
		"operations": ops,
		"status":     "scheduled",
	}
	data, _ := json.Marshal(resp)

	if liveMode {
		go func() {
			time.Sleep(200 * time.Millisecond)
			if err := executeOps(ops); err != nil {
				slog.Error("node_upgrade failed", "error", err)
			}
		}()
	}

	return &tools.ToolResult{Content: data}, nil
}

// buildNodeDeployHandler returns a tool handler that accepts a target host
// and deploys this binary to it via the mesh using SelfDeployer.
// This allows any mesh node to act as a "jumphost" for deploying deeper
// nodes that the gateway cannot directly SSH to.
func buildNodeDeployHandler(node *api.Node) tools.ToolHandler {
	return func(ctx context.Context, args json.RawMessage) (*tools.ToolResult, error) {
		var params struct {
			Target string `json:"target"`
		}
		if len(args) > 0 {
			if err := json.Unmarshal(args, &params); err != nil {
				return tools.NewErrorResult(fmt.Sprintf("parse args: %v", err)), nil
			}
		}

		if params.Target == "" {
			return tools.NewErrorResult("target is required: provide the host to deploy to"), nil
		}

		if node == nil {
			return tools.NewErrorResult("node_deploy requires an active mesh node"), nil
		}

		if !liveMode {
			return tools.NewErrorResult("node_deploy cannot run outside of live serve/daemon mode"), nil
		}

		// 1. Request credentials for the target from the mesh
		cred, err := node.RequestCredential(ctx, params.Target)
		if err != nil {
			return tools.NewErrorResult(fmt.Sprintf("failed to get credentials: %v", err)), nil
		}

		deployCred, err := toDeployCredential(*cred)
		if err != nil {
			return tools.NewErrorResult(fmt.Sprintf("failed to parse credential: %v", err)), nil
		}

		// 2. Deploy using SelfDeployer
		deployer := &transport.SelfDeployer{
			ExecArgs:   []string{"serve"},
			SkipUpload: false,
		}

		stream, err := deployer.Deploy(ctx, params.Target, deployCred, nil)
		if err != nil {
			return tools.NewErrorResult(fmt.Sprintf("deployment failed: %v", err)), nil
		}

		conn := transport.NewStdioConn(stream, stream)

		// 3. Add the resulting stream as a new mesh peer
		if err := node.AddPeer(ctx, conn, false); err != nil {
			_ = conn.Close()
			return tools.NewErrorResult(fmt.Sprintf("failed to add peer: %v", err)), nil
		}

		resp := map[string]interface{}{
			"target": params.Target,
			"status": "deployed",
		}
		data, _ := json.Marshal(resp)

		return &tools.ToolResult{Content: data}, nil
	}
}
