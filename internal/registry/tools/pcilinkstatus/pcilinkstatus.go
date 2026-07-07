// Package pcilinkstatus implements the get_pci_link_status diagnostic tool.
//
// It audits PCIe link training (negotiated speed/width vs. hardware maximum)
// and Advanced Error Reporting (AER) counters straight from sysfs. A
// downtrained link — e.g. an HDR InfiniBand HCA negotiating x4 instead of
// x16 — silently caps throughput, and rising AER counters are an early
// indicator of failing risers, retimers, or seating problems.
package pcilinkstatus

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func init() {
	registry.Register(New())
}

// maxDevices caps the devices list to keep LLM payloads bounded on
// large systems (a dense GPU server can expose several hundred functions).
const maxDevices = 64

// Tool implements the registry.Tool interface for get_pci_link_status.
type Tool struct {
	// sysfsRoot is the sysfs mount point, injectable for tests.
	sysfsRoot string
}

// New returns a new instance of the PCI link status tool.
func New() *Tool {
	return &Tool{sysfsRoot: "/sys"}
}

// Name returns the unique identifier for this tool.
func (t *Tool) Name() string { return "get_pci_link_status" }

// Description provides a brief summary for the LLM.
func (t *Tool) Description() string {
	return "Audit PCIe link training (speed/width vs maximum) and AER error counters to find downtrained or flaky devices."
}

// Help returns detailed documentation for the tool.
func (t *Tool) Help() string {
	return `get_pci_link_status — PCIe Link Training & AER Health Audit

Detects PCIe devices running below their negotiated maximum (downtrained
links) and devices accumulating Advanced Error Reporting (AER) errors.
A x16 GPU or HCA silently retrained to x4 loses 75% of its bandwidth;
non-zero fatal/nonfatal AER counters indicate signal-integrity problems
(bad risers, retimers, poorly seated cards).

Data Sources (pure sysfs, no binaries):
  - Device Discovery: /sys/bus/pci/devices/ directory iteration
  - Link Training: current_link_speed, current_link_width,
    max_link_speed, max_link_width per device
  - Identity: class, vendor, device, numa_node per device
  - AER Counters (when the kernel exposes them): aer_dev_correctable,
    aer_dev_nonfatal, aer_dev_fatal — individual "KEY N" counters are
    summed, TOTAL_ERR_* aggregate lines are excluded to avoid double
    counting.

Analysis (deterministic):
  - is_downtrained: numeric GT/s of current speed < max speed, or
    current width < max width.
  - Interesting classes surfaced by default: NVMe (0x0108), network
    (0x02, incl. InfiniBand 0x0207), display/GPU (0x03), and fabric
    (0x0c04).

Parameters:
  - all (boolean, default false): when false, only devices that are
    downtrained, have AER errors, or belong to an interesting class are
    returned. When true, every device exposing link files is returned.

Output: devices[] with address, decoded class_name, vendor/device IDs,
numa_node (omitted when -1), link {current/max speed and width},
is_downtrained, aer counter sums (omitted when not exposed), and
per-device warning_reasons. Summary counts devices_scanned,
downtrained_count, and aer_error_count.

Caveats: virtual functions and host bridges without link capability
files are skipped silently. The devices list is capped at 64 entries
(summary.truncated flags when the cap is hit).`
}

// Category returns the functional group for this tool.
func (t *Tool) Category() registry.Category {
	return registry.CategoryHardware
}

// Parameters returns the parameter schema for this tool.
func (t *Tool) Parameters() []registry.ToolParam {
	return []registry.ToolParam{
		{
			Name:        "all",
			Type:        "boolean",
			Description: "Return every PCI device exposing link files, not just downtrained/erroring/interesting ones.",
			Required:    false,
			Default:     "false",
		},
	}
}

// Hidden indicates whether this tool is excluded from LLM discovery.
func (t *Tool) Hidden() bool { return false }

// IsSupported reports whether the PCI sysfs tree exists on this node.
func (t *Tool) IsSupported() (bool, string) {
	devDir := filepath.Join(t.sysfsRoot, "bus", "pci", "devices")
	if info, err := os.Stat(devDir); err != nil || !info.IsDir() {
		return false, "/sys/bus/pci/devices not present (no PCI bus exposed)"
	}
	return true, ""
}

// Args is the parameter payload accepted by Execute.
type Args struct {
	All bool `json:"all"`
}

// Link describes the negotiated vs maximum PCIe link parameters.
type Link struct {
	CurrentSpeed string `json:"current_speed"`
	CurrentWidth int    `json:"current_width"`
	MaxSpeed     string `json:"max_speed"`
	MaxWidth     int    `json:"max_width"`
}

// AER holds summed Advanced Error Reporting counters for a device.
type AER struct {
	Correctable int64 `json:"correctable"`
	NonFatal    int64 `json:"nonfatal"`
	Fatal       int64 `json:"fatal"`
}

// Device is one PCI device entry in the tool output.
type Device struct {
	Address        string   `json:"address"`
	ClassName      string   `json:"class_name"`
	VendorID       string   `json:"vendor_id"`
	DeviceID       string   `json:"device_id"`
	NUMANode       *int     `json:"numa_node,omitempty"`
	Link           Link     `json:"link"`
	IsDowntrained  bool     `json:"is_downtrained"`
	AER            *AER     `json:"aer,omitempty"`
	WarningReasons []string `json:"warning_reasons,omitempty"`

	// class is the raw sysfs class hex, kept internal for filtering.
	class string
}

// Summary aggregates fleet-level counts for the scan.
type Summary struct {
	DevicesScanned   int  `json:"devices_scanned"`
	DowntrainedCount int  `json:"downtrained_count"`
	AERErrorCount    int  `json:"aer_error_count"`
	Truncated        bool `json:"truncated,omitempty"`
}

// Output is the data payload of the tool result.
type Output struct {
	Devices []Device `json:"devices"`
	Summary Summary  `json:"summary"`
}

// readTrimmed reads a sysfs attribute file and returns its
// whitespace-trimmed content. ok is false when the file is unreadable.
func readTrimmed(path string) (string, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(raw)), true
}

// parseGTs extracts the numeric GT/s prefix from a sysfs link speed
// string such as "8.0 GT/s PCIe" or "2.5 GT/s". ok is false when no
// leading number can be parsed.
func parseGTs(s string) (float64, bool) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// decodeClassName maps a sysfs PCI class hex string (e.g. "0x010802")
// to a human-readable device family. Unrecognized classes render as
// "other (<hex>)" so the LLM still sees the raw code.
func decodeClassName(classHex string) string {
	code := strings.TrimPrefix(classHex, "0x")
	switch {
	case strings.HasPrefix(code, "0108"):
		return "nvme"
	case strings.HasPrefix(code, "0207"), strings.HasPrefix(code, "0c04"):
		return "infiniband"
	case strings.HasPrefix(code, "02"):
		return "ethernet"
	case strings.HasPrefix(code, "03"):
		return "gpu"
	case strings.HasPrefix(code, "01"):
		return "storage"
	default:
		return fmt.Sprintf("other (%s)", classHex)
	}
}

// isInterestingClass reports whether a PCI class is surfaced by default
// (without all=true): NVMe controllers, network devices (incl.
// InfiniBand), display/GPU devices, and fabric controllers.
func isInterestingClass(classHex string) bool {
	code := strings.TrimPrefix(classHex, "0x")
	if code == "" {
		return false
	}
	for _, prefix := range []string{"0108", "02", "03", "0c04", "0207"} {
		if strings.HasPrefix(code, prefix) {
			return true
		}
	}
	return false
}

// readAERSum parses a sysfs AER counter file consisting of "KEY N"
// lines and returns the sum of the individual counters. Aggregate
// TOTAL_ERR_* lines are excluded to avoid double counting, and
// malformed lines are skipped. ok is false when the file is unreadable.
func readAERSum(path string) (int64, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	var sum int64
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if strings.HasPrefix(strings.ToUpper(fields[0]), "TOTAL") {
			continue
		}
		v, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		sum += v
	}
	return sum, true
}

// collectDevice reads one PCI device directory and builds its Device
// entry. ok is false when the device does not expose link training
// files (virtual functions, host bridges) and must be skipped silently.
func collectDevice(devPath, addr string) (*Device, bool) {
	curSpeed, okSpeed := readTrimmed(filepath.Join(devPath, "current_link_speed"))
	curWidthStr, okWidth := readTrimmed(filepath.Join(devPath, "current_link_width"))
	if !okSpeed || !okWidth {
		return nil, false
	}
	maxSpeed, _ := readTrimmed(filepath.Join(devPath, "max_link_speed"))
	maxWidthStr, _ := readTrimmed(filepath.Join(devPath, "max_link_width"))

	classHex, _ := readTrimmed(filepath.Join(devPath, "class"))
	vendorID, _ := readTrimmed(filepath.Join(devPath, "vendor"))
	deviceID, _ := readTrimmed(filepath.Join(devPath, "device"))

	dev := &Device{
		Address:   addr,
		ClassName: decodeClassName(classHex),
		VendorID:  vendorID,
		DeviceID:  deviceID,
		class:     classHex,
	}

	curWidth, _ := strconv.Atoi(curWidthStr)
	maxWidth, _ := strconv.Atoi(maxWidthStr)
	dev.Link = Link{
		CurrentSpeed: curSpeed,
		CurrentWidth: curWidth,
		MaxSpeed:     maxSpeed,
		MaxWidth:     maxWidth,
	}

	// numa_node: -1 means "no NUMA affinity reported" — omit entirely.
	if numaStr, ok := readTrimmed(filepath.Join(devPath, "numa_node")); ok {
		if n, err := strconv.Atoi(numaStr); err == nil && n >= 0 {
			dev.NUMANode = &n
		}
	}

	// Downtraining: numeric GT/s comparison for speed, integer for width.
	curGTs, okCur := parseGTs(curSpeed)
	maxGTs, okMax := parseGTs(maxSpeed)
	speedDown := okCur && okMax && curGTs < maxGTs
	widthDown := curWidth > 0 && maxWidth > 0 && curWidth < maxWidth
	dev.IsDowntrained = speedDown || widthDown

	if widthDown {
		dev.WarningReasons = append(dev.WarningReasons,
			fmt.Sprintf("link downtrained: running x%d at max x%d", curWidth, maxWidth))
	}
	if speedDown {
		dev.WarningReasons = append(dev.WarningReasons,
			fmt.Sprintf("link downtrained: running at %s, max %s", curSpeed, maxSpeed))
	}

	// AER counters are only exposed when the kernel AER driver claims
	// the device; omit the block entirely when absent.
	corr, okCorr := readAERSum(filepath.Join(devPath, "aer_dev_correctable"))
	nonfatal, okNF := readAERSum(filepath.Join(devPath, "aer_dev_nonfatal"))
	fatal, okF := readAERSum(filepath.Join(devPath, "aer_dev_fatal"))
	if okCorr || okNF || okF {
		dev.AER = &AER{Correctable: corr, NonFatal: nonfatal, Fatal: fatal}
		if nonfatal > 0 {
			dev.WarningReasons = append(dev.WarningReasons,
				fmt.Sprintf("%d nonfatal AER errors recorded", nonfatal))
		}
		if fatal > 0 {
			dev.WarningReasons = append(dev.WarningReasons,
				fmt.Sprintf("%d fatal AER errors recorded", fatal))
		}
	}

	return dev, true
}

// Execute scans the PCI sysfs tree and returns the link/AER audit.
func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	var parsed Args
	if len(args) > 0 {
		if err := json.Unmarshal(args, &parsed); err != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("invalid arguments: %v", err)), nil
		}
	}

	devDir := filepath.Join(t.sysfsRoot, "bus", "pci", "devices")
	entries, err := os.ReadDir(devDir)
	if err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("failed to read %s: %v", devDir, err)), nil
	}

	addrs := make([]string, 0, len(entries))
	for _, e := range entries {
		addrs = append(addrs, e.Name())
	}
	sort.Strings(addrs)

	out := Output{Devices: []Device{}}
	for _, addr := range addrs {
		if err := ctx.Err(); err != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("execution cancelled: %v", err)), nil
		}

		dev, ok := collectDevice(filepath.Join(devDir, addr), addr)
		if !ok {
			// No link capability files (virtual function, host bridge):
			// skip silently per degradation profile.
			continue
		}
		out.Summary.DevicesScanned++

		aerTotal := int64(0)
		if dev.AER != nil {
			aerTotal = dev.AER.Correctable + dev.AER.NonFatal + dev.AER.Fatal
		}
		if dev.IsDowntrained {
			out.Summary.DowntrainedCount++
		}
		if aerTotal > 0 {
			out.Summary.AERErrorCount++
		}

		if !parsed.All && !dev.IsDowntrained && aerTotal == 0 && !isInterestingClass(dev.class) {
			continue
		}
		if len(out.Devices) >= maxDevices {
			out.Summary.Truncated = true
			continue
		}
		out.Devices = append(out.Devices, *dev)
	}

	status := registry.StatusOK
	summary := fmt.Sprintf("Scanned %d PCI devices with link data: all links at full training, no AER errors.",
		out.Summary.DevicesScanned)

	seriousAER := false
	for _, d := range out.Devices {
		if d.AER != nil && (d.AER.NonFatal > 0 || d.AER.Fatal > 0) {
			seriousAER = true
			break
		}
	}
	if out.Summary.DowntrainedCount > 0 || seriousAER {
		status = registry.StatusWarning
		summary = fmt.Sprintf("Scanned %d PCI devices: %d downtrained link(s), %d device(s) with AER errors.",
			out.Summary.DevicesScanned, out.Summary.DowntrainedCount, out.Summary.AERErrorCount)
	}

	return registry.NewResult(t.Name(), status, summary, out), nil
}
