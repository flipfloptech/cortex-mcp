package mesh

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"

	"github.com/cortex-mesh/cortex-mesh/tools"
)

// detectTools inspects the local environment to intelligently register tools based on available services.
func detectTools(nodeID string) []toolEntry {
	var detected []toolEntry

	// Tool: Uptime (common system tool)
	if _, err := exec.LookPath("uptime"); err == nil {
		detected = append(detected, toolEntry{
			def: tools.ToolDefinition{
				Name:        "uptime",
				Description: "Get system uptime",
				Category:    "system",
			},
			handler: func(ctx context.Context, _ json.RawMessage) (*tools.ToolResult, error) {
				out, err := exec.CommandContext(ctx, "uptime", "-p").CombinedOutput()
				if err != nil {
					// Fallback to basic uptime if -p isn't supported (e.g., macOS/BSD)
					if runtime.GOOS == "darwin" || runtime.GOOS == "freebsd" {
						out, err = exec.CommandContext(ctx, "uptime").CombinedOutput()
					}
					if err != nil {
						return tools.NewErrorResult(string(out)), nil
					}
				}
				return tools.NewTextResult(fmt.Sprintf("[%s] %s", nodeID, string(out))), nil
			},
		})
	}

	// Tool: PostgreSQL Status
	if _, err := exec.LookPath("pg_isready"); err == nil {
		detected = append(detected, toolEntry{
			def: tools.ToolDefinition{
				Name:        "pg_isready",
				Description: "Check PostgreSQL server status",
				Category:    "database",
			},
			handler: func(ctx context.Context, _ json.RawMessage) (*tools.ToolResult, error) {
				out, err := exec.CommandContext(ctx, "pg_isready").CombinedOutput()
				if err != nil {
					return tools.NewErrorResult(string(out)), nil
				}
				return tools.NewTextResult(string(out)), nil
			},
		})
	}

	// Tool: MySQL/MariaDB Status
	if _, err := exec.LookPath("mysqladmin"); err == nil {
		detected = append(detected, toolEntry{
			def: tools.ToolDefinition{
				Name:        "mysql_ping",
				Description: "Check MySQL/MariaDB server status",
				Category:    "database",
			},
			handler: func(ctx context.Context, _ json.RawMessage) (*tools.ToolResult, error) {
				out, err := exec.CommandContext(ctx, "mysqladmin", "ping").CombinedOutput()
				if err != nil {
					return tools.NewErrorResult(string(out)), nil
				}
				return tools.NewTextResult(string(out)), nil
			},
		})
	}

	// Tool: Lustre Devices
	if _, err := exec.LookPath("lctl"); err == nil {
		detected = append(detected, toolEntry{
			def: tools.ToolDefinition{
				Name:        "lctl_dl",
				Description: "Show configured Lustre devices",
				Category:    "storage",
			},
			handler: func(ctx context.Context, _ json.RawMessage) (*tools.ToolResult, error) {
				out, err := exec.CommandContext(ctx, "lctl", "dl").CombinedOutput()
				if err != nil {
					return tools.NewErrorResult(string(out)), nil
				}
				return tools.NewTextResult(string(out)), nil
			},
		})
	}

	// Tool: NVIDIA SMI Status
	if _, err := exec.LookPath("nvidia-smi"); err == nil {
		detected = append(detected, toolEntry{
			def: tools.ToolDefinition{
				Name:        "gpu_status",
				Description: "Show NVIDIA GPU status",
				Category:    "compute",
			},
			handler: func(ctx context.Context, _ json.RawMessage) (*tools.ToolResult, error) {
				out, err := exec.CommandContext(ctx, "nvidia-smi", "-q", "-d", "MEMORY").CombinedOutput()
				if err != nil {
					return tools.NewErrorResult(string(out)), nil
				}
				return tools.NewTextResult(string(out)), nil
			},
		})
	}

	return detected
}
