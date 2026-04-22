package lifecycle

import (
	"context"
	"fmt"
	"go.uber.org/zap"
	"os"
	"os/exec"
)

// liveMode is set to true when the binary enters "serve" or "daemon" mode.
// This guards deferred execution (systemctl, os.Exit) so handlers return
// structured responses without side effects during tests.
var liveMode bool

// SetLiveMode allows the application entrypoint to enable destructive
// operations. When false, operations are planned and returned but not executed.
func SetLiveMode(live bool) {
	liveMode = live
}

// LifecycleOp represents a single lifecycle operation that the binary
// performs on the local system. Operations are structured data, not
// shell commands — this is the key abstraction over raw SSH exec.
type LifecycleOp struct {
	Action  string `json:"action"`            // "systemctl", "write_file", "remove_file", "copy_binary", "copy_file"
	Args    string `json:"args,omitempty"`    // e.g. "daemon-reload", "restart cortex-mesh"
	Path    string `json:"path,omitempty"`    // file path for write/remove/copy
	Src     string `json:"src,omitempty"`     // source path for copy_file
	Content string `json:"content,omitempty"` // file content for write_file
}

// SelfInstallOps returns the sequence of operations to install the
// running binary as a persistent systemd service.
func SelfInstallOps() []LifecycleOp {
	return []LifecycleOp{
		{Action: "copy_binary", Path: defaultInstallPath},
		{Action: "write_file", Path: serviceUnitPath(), Content: generateServiceUnit(defaultInstallPath)},
		{Action: "systemctl", Args: "daemon-reload"},
		{Action: "systemctl", Args: fmt.Sprintf("enable %s", ServiceName)},
		{Action: "systemctl", Args: fmt.Sprintf("restart %s", ServiceName)},
	}
}

// SelfUninstallOps returns the sequence of operations to fully remove
// the cortex-mesh systemd service, unit file, and binary.
func SelfUninstallOps() []LifecycleOp {
	return []LifecycleOp{
		{Action: "systemctl", Args: fmt.Sprintf("stop %s", ServiceName)},
		{Action: "systemctl", Args: fmt.Sprintf("disable %s", ServiceName)},
		{Action: "remove_file", Path: serviceUnitPath()},
		{Action: "systemctl", Args: "daemon-reload"},
		// Kill any lingering legacy processes via abstract socket lock
		{Action: "kill_abstract_socket", Path: "@cortex-mcp-lock"},
		{Action: "remove_file", Path: defaultInstallPath},
	}
}

// ephemeralCleanupOps returns operations to self-destruct an ephemeral
// node running from a temporary path (e.g., /tmp/cortex-mesh-abc123).
func ephemeralCleanupOps(binaryPath string) []LifecycleOp {
	return []LifecycleOp{
		{Action: "remove_file", Path: binaryPath},
	}
}

// nodeUpgradeOps returns the sequence of operations to upgrade an
// installed node: copy the new binary from src to the install path,
// then restart the service.
func nodeUpgradeOps(srcPath string) []LifecycleOp {
	return []LifecycleOp{
		{Action: "copy_file", Src: srcPath, Path: defaultInstallPath},
		{Action: "systemctl", Args: "daemon-reload"},
		{Action: "systemctl", Args: fmt.Sprintf("restart %s", ServiceName)},
	}
}

// ExecuteOps runs a sequence of lifecycle operations on the local system.
// Returns the first error encountered.
func ExecuteOps(ops []LifecycleOp) error {
	for _, op := range ops {
		if err := ExecuteOp(op); err != nil {
			return fmt.Errorf("%s %s: %w", op.Action, op.Args, err)
		}
	}
	return nil
}

// ExecuteOp runs a single lifecycle operation.
func ExecuteOp(op LifecycleOp) error {
	switch op.Action {
	case "systemctl":
		zap.S().Infow("lifecycle", "action", "systemctl", "args", op.Args)
		//nolint:gosec // Args are constructed internally, not from user input.
		cmd := exec.Command("systemctl", splitArgs(op.Args)...)
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		return cmd.Run()

	case "write_file":
		zap.S().Infow("lifecycle", "action", "write_file", "path", op.Path)
		return os.WriteFile(op.Path, []byte(op.Content), 0644)

	case "remove_file":
		zap.S().Infow("lifecycle", "action", "remove_file", "path", op.Path)
		return os.Remove(op.Path)

	case "copy_binary":
		zap.S().Infow("lifecycle", "action", "copy_binary", "dest", op.Path)
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
		zap.S().Infow("lifecycle", "action", "copy_file", "src", op.Src, "dest", op.Path)
		data, err := os.ReadFile(op.Src)
		if err != nil {
			return fmt.Errorf("read source: %w", err)
		}
		return os.WriteFile(op.Path, data, 0755)

	case "kill_abstract_socket":
		zap.S().Infow("lifecycle", "action", "kill_abstract_socket", "socket", op.Path)
		if err := KillLockedProcess(context.Background(), op.Path); err != nil {
			zap.S().Warnw("failed to kill process via abstract socket", "socket", op.Path, "error", err)
		} else {
			zap.S().Infow("kill_abstract_socket complete", "socket", op.Path)
		}
		return nil

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
