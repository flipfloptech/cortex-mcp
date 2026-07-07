package systemdstatus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

type ServiceStatus struct {
	ID            string `json:"id"`
	LoadState     string `json:"load_state"`
	ActiveState   string `json:"active_state"`
	SubState      string `json:"sub_state"`
	UnitFileState string `json:"unit_file_state,omitempty"`
	Description   string `json:"description,omitempty"`
	MainPID       int    `json:"main_pid,omitempty"`
}

type SystemdStatusData struct {
	Services []ServiceStatus `json:"services,omitempty"`
	Failed   []ServiceStatus `json:"failed_services,omitempty"`
}

type Args struct {
	Services []string `json:"services"`
	ListAll  bool     `json:"list_all"`
}

type QuerySystemdStatusTool struct {
	execCommand func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func New() *QuerySystemdStatusTool {
	return &QuerySystemdStatusTool{
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		},
	}
}

func init() {
	registry.Register(New())
}

func (t *QuerySystemdStatusTool) Name() string {
	return "get_systemd_status"
}

func (t *QuerySystemdStatusTool) Category() string {
	return "system"
}

func (t *QuerySystemdStatusTool) Help() string {
	return `Get status information for systemd services or query failed system services.

Queries systemd unit status and lists failures.

Parameters:
- services: Optional list of strings (service names, e.g. ["ssh", "docker"]).
- list_all: Optional boolean. If true, lists the state of all services in the system.`
}

func (t *QuerySystemdStatusTool) Description() string {
	return "Status of specific daemons or identification of failed services"
}

func (t *QuerySystemdStatusTool) Parameters() []registry.ToolParam {
	return []registry.ToolParam{
		{
			Name:        "services",
			Type:        "array",
			Description: "Optional: List of specific systemd service names to inspect (e.g. ['ssh', 'docker']).",
			Required:    false,
		},
		{
			Name:        "list_all",
			Type:        "boolean",
			Description: "Optional: If true, lists status for all systemd services on the host.",
			Required:    false,
		},
	}
}

func (t *QuerySystemdStatusTool) Hidden() bool { return false }

func (t *QuerySystemdStatusTool) IsSupported() (bool, string) {
	_, err := exec.LookPath("systemctl")
	if err != nil {
		return false, "systemctl status query not supported (missing systemctl binary)"
	}
	return true, ""
}

func (t *QuerySystemdStatusTool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	var parsedArgs Args
	if len(args) > 0 {
		if err := json.Unmarshal(args, &parsedArgs); err != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("invalid arguments: %v", err)), nil
		}
	}

	var data SystemdStatusData

	// 1. Query failed services (always queried as diagnostic information)
	failedOutput, err := t.execCommand(ctx, "systemctl", "list-units", "--state=failed", "--no-legend", "--no-pager")
	if err == nil {
		data.Failed = parseListUnits(failedOutput)
	}

	// 2. Query specific services if requested
	if len(parsedArgs.Services) > 0 {
		var showArgs []string
		showArgs = append(showArgs, "show")
		showArgs = append(showArgs, parsedArgs.Services...)
		showOutput, err := t.execCommand(ctx, "systemctl", showArgs...)
		if err != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("failed to query services: %v", err)), nil
		}
		data.Services = parseSystemdShow(showOutput)
	} else if parsedArgs.ListAll {
		// 3. Query all services if requested
		allOutput, err := t.execCommand(ctx, "systemctl", "list-units", "--type=service", "--no-legend", "--no-pager")
		if err != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("failed to list all services: %v", err)), nil
		}
		data.Services = parseListUnits(allOutput)
	}

	summaryStr := fmt.Sprintf("Systemd Status: queried %d services, %d failed services found", len(data.Services), len(data.Failed))
	res := registry.NewResult(t.Name(), registry.StatusOK, summaryStr, data)
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()

	return res, nil
}

func parseSystemdShow(output []byte) []ServiceStatus {
	var services []ServiceStatus
	blocks := bytes.Split(output, []byte("\n\n"))

	for _, block := range blocks {
		block = bytes.TrimSpace(block)
		if len(block) == 0 {
			continue
		}

		var svc ServiceStatus
		lines := bytes.Split(block, []byte("\n"))
		hasData := false

		for _, line := range lines {
			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				continue
			}

			parts := bytes.SplitN(line, []byte("="), 2)
			if len(parts) != 2 {
				continue
			}

			key := string(bytes.TrimSpace(parts[0]))
			val := string(bytes.TrimSpace(parts[1]))

			switch key {
			case "Id":
				svc.ID = val
				hasData = true
			case "LoadState":
				svc.LoadState = val
			case "ActiveState":
				svc.ActiveState = val
			case "SubState":
				svc.SubState = val
			case "UnitFileState":
				svc.UnitFileState = val
			case "Description":
				svc.Description = val
			case "MainPID":
				if pid, err := strconv.Atoi(val); err == nil {
					svc.MainPID = pid
				}
			}
		}

		if hasData {
			services = append(services, svc)
		}
	}

	return services
}

func parseListUnits(output []byte) []ServiceStatus {
	var services []ServiceStatus
	lines := bytes.Split(output, []byte("\n"))

	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}

		tokens := strings.Fields(string(line))
		if len(tokens) == 0 {
			continue
		}

		// Strip bullet status symbol if present
		if tokens[0] == "●" || tokens[0] == "*" {
			tokens = tokens[1:]
		}

		if len(tokens) < 4 {
			continue
		}

		var svc ServiceStatus
		svc.ID = tokens[0]
		svc.LoadState = tokens[1]
		svc.ActiveState = tokens[2]
		svc.SubState = tokens[3]

		if len(tokens) > 4 {
			svc.Description = strings.Join(tokens[4:], " ")
		}

		services = append(services, svc)
	}

	return services
}
