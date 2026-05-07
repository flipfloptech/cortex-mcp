package nvmesmartlog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
	"github.com/flipfloptech/cortex-mcp/internal/sys/storage"
)

type Tool struct{}

func New() *Tool {
	return &Tool{}
}

func (t *Tool) Name() string {
	return "get_nvme_smart_log"
}

func (t *Tool) Description() string {
	return "Audit the physical health, thermal state, and silicon degradation of NVMe storage media."
}

func (t *Tool) Help() string {
	return "Executes nvme smart-log against NVMe controllers (e.g. /dev/nvme0) to diagnose thermal throttling or imminent drive failure. Data sourced via nvme-cli or smartctl."
}

func (t *Tool) Category() string {
	return "Storage"
}

func (t *Tool) Parameters() []registry.ToolParam {
	return []registry.ToolParam{
		{
			Name:        "target_device",
			Type:        "string",
			Description: "Optional: Specific NVMe controller to audit (e.g., 'nvme1'). If omitted, audits all.",
			Required:    false,
		},
	}
}

func (t *Tool) IsSupported() (bool, string) {
	_, err1 := exec.LookPath("nvme")
	_, err2 := exec.LookPath("smartctl")
	if err1 != nil && err2 != nil {
		return false, "Neither 'nvme' nor 'smartctl' binary found in $PATH"
	}
	return true, ""
}

type Args struct {
	TargetDevice string `json:"target_device"`
}

type SystemSummary struct {
	DrivesAudited            int `json:"drives_audited"`
	CriticalWarningsDetected int `json:"critical_warnings_detected"`
	MediaErrorsDetected      int `json:"media_errors_detected"`
}

type Output struct {
	SystemSummary SystemSummary        `json:"system_summary"`
	Drives        []*storage.NVMeDrive `json:"drives"`
}

func (t *Tool) Hidden() bool {
	return false
}

func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	var parsedArgs Args
	if len(args) > 0 {
		if err := json.Unmarshal(args, &parsedArgs); err != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("invalid arguments: %v", err)), nil
		}
	}

	devices, err := storage.DiscoverNVMeDevices("/sys")
	if err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("failed to discover devices: %v", err)), nil
	}

	var drives []*storage.NVMeDrive
	drivesAudited := 0
	critDetect := 0
	mediaDetect := 0

	for _, dev := range devices {
		if parsedArgs.TargetDevice != "" && dev != parsedArgs.TargetDevice {
			continue
		}

		cmdArgs := []string{"smart-log", "/dev/" + dev, "-o", "json"}
		var cmd *exec.Cmd

		// If not running as root, attempt to use non-interactive sudo if available
		if os.Geteuid() != 0 {
			if _, err := exec.LookPath("sudo"); err == nil {
				cmdArgs = append([]string{"-n", "nvme"}, cmdArgs...)
				cmd = exec.CommandContext(ctx, "sudo", cmdArgs...)
			} else {
				cmd = exec.CommandContext(ctx, "nvme", cmdArgs...)
			}
		} else {
			cmd = exec.CommandContext(ctx, "nvme", cmdArgs...)
		}

		out, err := cmd.CombinedOutput()
		if err != nil {
			errStr := strings.ToLower(err.Error())
			outStr := strings.ToLower(string(out))

			if strings.Contains(errStr, "permission denied") || strings.Contains(outStr, "permission denied") ||
				strings.Contains(errStr, "operation not permitted") || strings.Contains(outStr, "operation not permitted") ||
				strings.Contains(outStr, "password is required") {
				return registry.NewErrorResult(t.Name(), "Unauthorized: Root or passwordless sudo privileges required to execute NVMe ioctls."), nil
			}
			continue
		}

		drive, err := storage.ParseNVMeOutput(out, dev)
		if err == nil {
			drives = append(drives, drive)
			drivesAudited++
			if drive.CriticalWarning > 0 {
				critDetect++
			}
			if drive.MediaErrors > 0 {
				mediaDetect += drive.MediaErrors
			}
		}
	}

	outData := Output{
		SystemSummary: SystemSummary{
			DrivesAudited:            drivesAudited,
			CriticalWarningsDetected: critDetect,
			MediaErrorsDetected:      mediaDetect,
		},
		Drives: drives,
	}

	return registry.NewResult(t.Name(), registry.StatusOK, fmt.Sprintf("Audited %d drives", drivesAudited), outData), nil
}

func init() {
	registry.Register(New())
}
