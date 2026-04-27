package lifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"go.uber.org/zap"
	"os"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func init() {
	registry.Register(NewInstallTool())
	registry.Register(NewUninstallTool())
	registry.Register(NewRestartTool())
	registry.Register(NewStopTool())
	registry.Register(NewUpgradeTool())
}

// baseLifecycleTool provides common methods for lifecycle tools.
type baseLifecycleTool struct {
	name        string
	description string
	longDesc    string
}

func (b *baseLifecycleTool) Name() string                     { return b.name }
func (b *baseLifecycleTool) Category() string                 { return "lifecycle" }
func (b *baseLifecycleTool) Description() string              { return b.description }
func (b *baseLifecycleTool) Help() string                     { return b.longDesc }
func (b *baseLifecycleTool) Hidden() bool                     { return true }
func (b *baseLifecycleTool) Parameters() []registry.ToolParam { return nil }
func (b *baseLifecycleTool) IsSupported() (bool, string) {
	if !registry.IsLinux() {
		return false, "lifecycle operations require linux"
	}
	return true, ""
}

// -- InstallTool --
type InstallTool struct{ baseLifecycleTool }

func NewInstallTool() *InstallTool {
	return &InstallTool{
		baseLifecycleTool: baseLifecycleTool{
			name:        "node_install",
			description: "Install this node as a persistent systemd service",
			longDesc:    "Copies the binary to /opt/cortex-mcp/bin/, writes a systemd unit, and enables/starts the service. Converts an ephemeral node into persistent infrastructure.",
		},
	}
}

func (t *InstallTool) Execute(ctx context.Context, _ json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()
	hostname, _ := os.Hostname()

	if _, err := os.Executable(); err != nil {
		return registry.NewErrorResult(t.Name(), hostname, fmt.Sprintf("resolve executable: %v", err)), nil
	}

	ops := SelfInstallOps()
	resp := map[string]interface{}{
		"operations": ops,
		"status":     "scheduled",
	}

	if liveMode {
		go func() {
			time.Sleep(200 * time.Millisecond)
			if execErr := ExecuteOps(ops); execErr != nil {
				zap.S().Errorw("node_install failed", "error", execErr)
			}
		}()
	} else {
		resp["status"] = "dry_run"
	}

	result := registry.NewResult(t.Name(), hostname, registry.StatusOK, "scheduled install operations", resp)
	result.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	return result, nil
}

// -- UninstallTool --
type UninstallTool struct{ baseLifecycleTool }

func NewUninstallTool() *UninstallTool {
	return &UninstallTool{
		baseLifecycleTool: baseLifecycleTool{
			name:        "node_uninstall",
			description: "Remove this node — handles both persistent (systemd) and ephemeral (/tmp) nodes",
			longDesc:    "Detects whether the node is persistent or ephemeral. Persistent: stops/disables service, removes unit + binary. Ephemeral: removes the /tmp binary and exits.",
		},
	}
}

func (t *UninstallTool) Execute(ctx context.Context, _ json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()
	hostname, _ := os.Hostname()

	binaryPath, err := os.Executable()
	if err != nil {
		return registry.NewErrorResult(t.Name(), hostname, fmt.Sprintf("resolve executable: %v", err)), nil
	}

	var ops []LifecycleOp
	if strings.HasPrefix(binaryPath, defaultInstallPath) || strings.HasPrefix(binaryPath, "/opt/") {
		ops = SelfUninstallOps()
	} else {
		ops = []LifecycleOp{
			{Action: "kill_abstract_socket", Path: "@cortex-mcp-lock"},
		}
	}

	resp := map[string]interface{}{
		"operations": ops,
		"status":     "scheduled",
	}

	if liveMode {
		go func() {
			time.Sleep(200 * time.Millisecond)
			if execErr := ExecuteOps(ops); execErr != nil {
				zap.S().Errorw("node_uninstall failed", "error", execErr)
			}
			if !strings.HasPrefix(binaryPath, "/opt/") {
				os.Exit(0)
			}
		}()
	} else {
		resp["status"] = "dry_run"
	}

	result := registry.NewResult(t.Name(), hostname, registry.StatusOK, "scheduled uninstall operations", resp)
	result.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	return result, nil
}

// -- RestartTool --
type RestartTool struct{ baseLifecycleTool }

func NewRestartTool() *RestartTool {
	return &RestartTool{
		baseLifecycleTool: baseLifecycleTool{
			name:        "node_restart",
			description: "Restart the local cortex-mcp systemd service",
			longDesc:    "Runs systemctl restart cortex-mcp. Use after binary upgrades or configuration changes.",
		},
	}
}

func (t *RestartTool) Execute(ctx context.Context, _ json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()
	hostname, _ := os.Hostname()

	cmd := fmt.Sprintf("restart %s", ServiceName)
	resp := map[string]string{
		"command": cmd,
		"status":  "scheduled",
	}

	if liveMode {
		go func() {
			time.Sleep(200 * time.Millisecond)
			_ = ExecuteOp(LifecycleOp{Action: "systemctl", Args: cmd})
		}()
	} else {
		resp["status"] = "dry_run"
	}

	result := registry.NewResult(t.Name(), hostname, registry.StatusOK, "scheduled restart operation", resp)
	result.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	return result, nil
}

// -- StopTool --
type StopTool struct{ baseLifecycleTool }

func NewStopTool() *StopTool {
	return &StopTool{
		baseLifecycleTool: baseLifecycleTool{
			name:        "node_stop",
			description: "Stop the cortex-mcp systemd service without uninstalling",
			longDesc:    "Gracefully stops the service. The node remains installed and can be restarted. Use for maintenance windows.",
		},
	}
}

func (t *StopTool) Execute(ctx context.Context, _ json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()
	hostname, _ := os.Hostname()

	cmd := fmt.Sprintf("stop %s", ServiceName)
	resp := map[string]string{
		"command": cmd,
		"status":  "scheduled",
	}

	if liveMode {
		go func() {
			time.Sleep(200 * time.Millisecond)
			_ = ExecuteOp(LifecycleOp{Action: "systemctl", Args: cmd})
		}()
	} else {
		resp["status"] = "dry_run"
	}

	result := registry.NewResult(t.Name(), hostname, registry.StatusOK, "scheduled stop operation", resp)
	result.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	return result, nil
}

// -- UpgradeTool --
type UpgradeTool struct{ baseLifecycleTool }

func NewUpgradeTool() *UpgradeTool {
	return &UpgradeTool{
		baseLifecycleTool: baseLifecycleTool{
			name:        "node_upgrade",
			description: "Upgrade the node binary and restart the service",
			longDesc:    "Copies a new binary from the specified path over the installed binary, reloads systemd, and restarts the service.",
		},
	}
}

func (t *UpgradeTool) Parameters() []registry.ToolParam {
	return []registry.ToolParam{
		{Name: "path", Type: "string", Description: "Path to the new binary (e.g., /tmp/cortex-mcp-new)", Required: true},
	}
}

func (t *UpgradeTool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()
	hostname, _ := os.Hostname()

	var params struct {
		Path string `json:"path"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &params); err != nil {
			return registry.NewErrorResult(t.Name(), hostname, fmt.Sprintf("parse args: %v", err)), nil
		}
	}

	if params.Path == "" {
		return registry.NewErrorResult(t.Name(), hostname, "path is required: provide the path to the new binary"), nil
	}

	ops := nodeUpgradeOps(params.Path)
	resp := map[string]interface{}{
		"operations": ops,
		"status":     "scheduled",
	}

	if liveMode {
		go func() {
			time.Sleep(200 * time.Millisecond)
			if err := ExecuteOps(ops); err != nil {
				zap.S().Errorw("node_upgrade failed", "error", err)
			}
		}()
	} else {
		resp["status"] = "dry_run"
	}

	result := registry.NewResult(t.Name(), hostname, registry.StatusOK, "scheduled upgrade operations", resp)
	result.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	return result, nil
}
