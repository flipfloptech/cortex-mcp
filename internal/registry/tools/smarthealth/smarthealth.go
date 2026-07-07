// Package smarthealth implements the get_smart_health diagnostic tool.
//
// It audits SATA/SAS drive health via smartctl JSON output, mirroring the
// get_nvme_smart_log execution contract: drives are discovered natively
// from /sys/class/block, commands are wrapped in non-interactive sudo when
// running unprivileged, and permission lockouts surface as an explicit
// Unauthorized error result instead of silent partial data.
package smarthealth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// sataDeviceRe matches whole-disk SATA/SAS device names (sda, sdb, ..., sdaa)
// and rejects partitions (sda1), NVMe namespaces, and other device classes.
var sataDeviceRe = regexp.MustCompile(`^sd[a-z]+$`)

// Drive is the health snapshot of one SATA/SAS drive.
type Drive struct {
	Device               string   `json:"device"`
	Model                string   `json:"model"`
	SmartPassed          bool     `json:"smart_passed"`
	TemperatureC         int      `json:"temperature_c"`
	PowerOnHours         int64    `json:"power_on_hours"`
	ReallocatedSectors   int64    `json:"reallocated_sectors"`
	PendingSectors       int64    `json:"pending_sectors"`
	UncorrectableSectors int64    `json:"uncorrectable_sectors"`
	CRCErrors            int64    `json:"crc_errors"`
	Status               string   `json:"status"`
	WarningReasons       []string `json:"warning_reasons"`
}

// SystemSummary aggregates counts across all audited drives.
type SystemSummary struct {
	DrivesAudited    int   `json:"drives_audited"`
	DrivesFailing    int   `json:"drives_failing"`
	TotalReallocated int64 `json:"total_reallocated"`
}

// Output is the tool's data payload.
type Output struct {
	SystemSummary SystemSummary `json:"system_summary"`
	Drives        []Drive       `json:"drives"`
}

// Args is the tool's parameter schema.
type Args struct {
	TargetDevice string `json:"target_device"`
}

// driveIdent is a discovered drive's sysfs identity.
type driveIdent struct {
	Name  string
	Model string
}

// smartctlReport is the subset of `smartctl -a -j` output this tool consumes.
type smartctlReport struct {
	ModelName   string `json:"model_name"`
	SmartStatus *struct {
		Passed bool `json:"passed"`
	} `json:"smart_status"`
	Temperature struct {
		Current int `json:"current"`
	} `json:"temperature"`
	PowerOnTime struct {
		Hours int64 `json:"hours"`
	} `json:"power_on_time"`
	ATASmartAttributes struct {
		Table []struct {
			ID  int `json:"id"`
			Raw struct {
				Value int64 `json:"value"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
}

// Tool implements registry.Tool for SATA/SAS SMART health auditing.
type Tool struct {
	sysfsRoot   string
	execCommand func(ctx context.Context, name string, args ...string) ([]byte, error)
	lookPath    func(file string) (string, error)
	geteuid     func() int
}

// New returns a Tool wired to the real sysfs root and executables.
func New() *Tool {
	return &Tool{
		sysfsRoot: "/sys",
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		},
		lookPath: exec.LookPath,
		geteuid:  os.Geteuid,
	}
}

func init() {
	registry.Register(New())
}

// Name returns the unique tool identifier.
func (t *Tool) Name() string {
	return "get_smart_health"
}

// Description returns a one-line summary for get_tool_list output.
func (t *Tool) Description() string {
	return "Audit SATA/SAS drive SMART health: self-assessment, defect counters, temperature, and link errors."
}

// Help returns the full tool help text.
func (t *Tool) Help() string {
	return `Audits the SMART health of SATA/SAS drives (sd*) to catch failing spinning disks and SSDs before data loss.

Drives are discovered natively from /sys/class/block (whole disks with a physical device/ entry only; partitions, virtual devices, and NVMe are excluded — NVMe is covered by get_nvme_smart_log). Each drive is queried with smartctl -a -j and the JSON is reduced to the failure-predictive core: overall self-assessment, temperature, power-on hours, and ATA attributes 5 (Reallocated_Sector_Ct), 197 (Current_Pending_Sector), 198 (Offline_Uncorrectable), and 199 (UDMA_CRC_Error_Count).

Data Sources:
- /sys/class/block/sd*/device/{model,vendor} (native discovery + identity fallback)
- smartctl -a -j /dev/<dev> (wrapped in non-interactive 'sudo -n' when running unprivileged)

Requires root or passwordless sudo; a permission lockout returns an explicit Unauthorized error.`
}

// Category classifies this tool as a storage diagnostic.
func (t *Tool) Category() registry.Category {
	return registry.CategoryStorage
}

// Parameters returns the parameter schema.
func (t *Tool) Parameters() []registry.ToolParam {
	return []registry.ToolParam{
		{
			Name:        "target_device",
			Type:        "string",
			Description: "Optional: Specific drive to audit (e.g., 'sda'). If omitted, audits all SATA/SAS drives.",
			Required:    false,
		},
	}
}

// Hidden reports whether the tool is hidden from LLM discovery.
func (t *Tool) Hidden() bool {
	return false
}

// IsSupported reports whether smartctl is available on this node.
func (t *Tool) IsSupported() (bool, string) {
	if _, err := t.lookPath("smartctl"); err != nil {
		return false, "'smartctl' binary not found in $PATH"
	}
	return true, ""
}

// Execute audits every discovered SATA/SAS drive via smartctl.
func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	var parsedArgs Args
	if len(args) > 0 {
		if err := json.Unmarshal(args, &parsedArgs); err != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("invalid arguments: %v", err)), nil
		}
	}

	if err := ctx.Err(); err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("execution cancelled: %v", err)), nil
	}

	drives := []Drive{}
	anyFailedAssessment := false

	for _, ident := range discoverSATADrives(t.sysfsRoot) {
		if parsedArgs.TargetDevice != "" && ident.Name != parsedArgs.TargetDevice {
			continue
		}
		if err := ctx.Err(); err != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("execution cancelled: %v", err)), nil
		}

		out, err := t.runSmartctl(ctx, ident.Name)
		// smartctl exits non-zero when SMART checks fail (exit bitmask), so
		// parse whatever came back and only treat unparseable output as failure.
		drive, perr := parseSmartctl(out, ident.Name, ident.Model)
		if perr != nil {
			if isPermissionDenied(err, out) {
				return registry.NewErrorResult(t.Name(),
					"Unauthorized: Root or passwordless sudo privileges required to execute SMART ioctls."), nil
			}
			// Per-drive failure (unsupported bridge, dead device, garbage
			// output): skip this drive and keep auditing the rest.
			continue
		}

		applyHealthRules(&drive)
		if !drive.SmartPassed {
			anyFailedAssessment = true
		}
		drives = append(drives, drive)
	}

	out := Output{Drives: drives}
	out.SystemSummary.DrivesAudited = len(drives)
	for _, d := range drives {
		if d.Status == "critical" {
			out.SystemSummary.DrivesFailing++
		}
		out.SystemSummary.TotalReallocated += d.ReallocatedSectors
	}

	status := registry.StatusOK
	switch {
	case anyFailedAssessment:
		status = registry.StatusError
	case out.SystemSummary.DrivesFailing > 0:
		status = registry.StatusWarning
	}
	summary := fmt.Sprintf("SMART: audited %d SATA/SAS drives, %d failing",
		out.SystemSummary.DrivesAudited, out.SystemSummary.DrivesFailing)

	res := registry.NewResult(t.Name(), status, summary, out)
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	res.Metadata.FilteringMethod = "deterministic"
	return res, nil
}

// runSmartctl executes smartctl -a -j for a device, wrapping the command in
// non-interactive sudo when running unprivileged (and sudo is available).
func (t *Tool) runSmartctl(ctx context.Context, dev string) ([]byte, error) {
	name := "smartctl"
	cmdArgs := []string{"-a", "-j", "/dev/" + dev}
	if t.geteuid() != 0 {
		if _, err := t.lookPath("sudo"); err == nil {
			name = "sudo"
			cmdArgs = append([]string{"-n", "smartctl"}, cmdArgs...)
		}
	}
	return t.execCommand(ctx, name, cmdArgs...)
}

// discoverSATADrives enumerates whole-disk sd* devices that are backed by a
// physical device/ entry in sysfs, sorted by name.
func discoverSATADrives(sysfsRoot string) []driveIdent {
	classDir := filepath.Join(sysfsRoot, "class", "block")
	entries, err := os.ReadDir(classDir)
	if err != nil {
		return nil
	}

	var drives []driveIdent
	for _, entry := range entries {
		name := entry.Name()
		if !sataDeviceRe.MatchString(name) {
			continue
		}
		devDir := filepath.Join(classDir, name, "device")
		info, err := os.Stat(devDir)
		if err != nil || !info.IsDir() {
			// No physical device entry: virtual block device, skip.
			continue
		}

		vendor := ""
		if data, err := os.ReadFile(filepath.Join(devDir, "vendor")); err == nil {
			vendor = strings.TrimSpace(string(data))
		}
		model := ""
		if data, err := os.ReadFile(filepath.Join(devDir, "model")); err == nil {
			model = strings.TrimSpace(string(data))
		}
		drives = append(drives, driveIdent{
			Name:  name,
			Model: strings.Join(strings.Fields(vendor+" "+model), " "),
		})
	}

	sort.Slice(drives, func(i, j int) bool { return drives[i].Name < drives[j].Name })
	return drives
}

// parseSmartctl reduces smartctl -a -j JSON output to a Drive. Output that
// is not valid JSON or lacks the smart_status block is an error (the caller
// skips the drive).
func parseSmartctl(out []byte, dev, fallbackModel string) (Drive, error) {
	var report smartctlReport
	if err := json.Unmarshal(out, &report); err != nil {
		return Drive{}, fmt.Errorf("smartctl output for %s is not valid JSON: %w", dev, err)
	}
	if report.SmartStatus == nil {
		return Drive{}, fmt.Errorf("smartctl output for %s is missing smart_status", dev)
	}

	d := Drive{
		Device:         dev,
		Model:          report.ModelName,
		SmartPassed:    report.SmartStatus.Passed,
		TemperatureC:   report.Temperature.Current,
		PowerOnHours:   report.PowerOnTime.Hours,
		WarningReasons: []string{},
	}
	if d.Model == "" {
		d.Model = fallbackModel
	}

	for _, attr := range report.ATASmartAttributes.Table {
		switch attr.ID {
		case 5:
			d.ReallocatedSectors = attr.Raw.Value
		case 197:
			d.PendingSectors = attr.Raw.Value
		case 198:
			d.UncorrectableSectors = attr.Raw.Value
		case 199:
			d.CRCErrors = attr.Raw.Value
		}
	}

	return d, nil
}

// applyHealthRules populates warning_reasons and the healthy/critical status
// from the drive's parsed SMART values.
func applyHealthRules(d *Drive) {
	if !d.SmartPassed {
		d.WarningReasons = append(d.WarningReasons, "SMART overall health self-assessment: FAILED")
	}
	if d.ReallocatedSectors > 0 {
		d.WarningReasons = append(d.WarningReasons,
			fmt.Sprintf("reallocated_sectors=%d (grown defects — media is degrading)", d.ReallocatedSectors))
	}
	if d.PendingSectors > 0 {
		d.WarningReasons = append(d.WarningReasons,
			fmt.Sprintf("pending_sectors=%d (unstable sectors awaiting reallocation)", d.PendingSectors))
	}
	if d.UncorrectableSectors > 0 {
		d.WarningReasons = append(d.WarningReasons,
			fmt.Sprintf("uncorrectable_sectors=%d (unrecoverable media errors)", d.UncorrectableSectors))
	}
	if d.CRCErrors > 0 {
		d.WarningReasons = append(d.WarningReasons,
			fmt.Sprintf("crc_errors=%d (UDMA CRC errors — check cabling/backplane link)", d.CRCErrors))
	}
	if d.TemperatureC > 60 {
		d.WarningReasons = append(d.WarningReasons,
			fmt.Sprintf("temperature %d°C exceeds 60°C threshold", d.TemperatureC))
	}

	if len(d.WarningReasons) > 0 {
		d.Status = "critical"
	} else {
		d.Status = "healthy"
	}
}

// isPermissionDenied reports whether a failed smartctl invocation was
// refused for lack of privileges (direct EPERM/EACCES or sudo demanding a
// password). A successful invocation is never a lockout.
func isPermissionDenied(err error, out []byte) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	outStr := strings.ToLower(string(out))
	return strings.Contains(errStr, "permission denied") ||
		strings.Contains(errStr, "operation not permitted") ||
		strings.Contains(outStr, "permission denied") ||
		strings.Contains(outStr, "operation not permitted") ||
		strings.Contains(outStr, "password is required")
}
