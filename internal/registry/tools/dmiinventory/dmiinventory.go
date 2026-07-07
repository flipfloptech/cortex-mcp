// Package dmiinventory implements the get_dmi_inventory diagnostic tool.
//
// It reads the SMBIOS/DMI identity of the machine (vendor, model, serial,
// board, BIOS, chassis) natively from sysfs, and — when dmidecode is
// available — enriches the answer with the physical DIMM population so an
// operator can locate the exact slot of a failing module reported by EDAC.
package dmiinventory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func init() {
	registry.Register(New())
}

// maxDIMMs caps the DIMM list to keep LLM payloads bounded on very
// large multi-socket systems.
const maxDIMMs = 64

// Tool implements the registry.Tool interface for get_dmi_inventory.
type Tool struct {
	// sysfsRoot is the sysfs mount point, injectable for tests.
	sysfsRoot string

	// execCommand runs an external binary; injectable for tests.
	execCommand func(ctx context.Context, name string, args ...string) ([]byte, error)

	// lookPath resolves a binary in $PATH; injectable for tests.
	lookPath func(file string) (string, error)

	// geteuid returns the effective UID; injectable for tests.
	geteuid func() int
}

// New returns a new instance of the DMI inventory tool.
func New() *Tool {
	return &Tool{
		sysfsRoot: "/sys",
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		},
		lookPath: exec.LookPath,
		geteuid:  os.Geteuid,
	}
}

// Name returns the unique identifier for this tool.
func (t *Tool) Name() string { return "get_dmi_inventory" }

// Description provides a brief summary for the LLM.
func (t *Tool) Description() string {
	return "Report SMBIOS/DMI hardware identity (vendor, model, serial, BIOS, chassis) plus DIMM slot population."
}

// Help returns detailed documentation for the tool.
func (t *Tool) Help() string {
	return `get_dmi_inventory — SMBIOS/DMI Hardware Identity & DIMM Inventory

Answers "what physical machine is this?": manufacturer, product, serial
number, mainboard, firmware level, and chassis form factor — plus, when
possible, the exact physical DIMM population (which slot holds which
module). Essential for support cases, firmware audits, and correlating
EDAC memory errors to a physical part to replace.

Data Sources:
  - Native (pure sysfs): /sys/class/dmi/id/ attributes — sys_vendor,
    product_name, product_serial (root-only, mode 0400), product_uuid
    (root-only), board_vendor, board_name, bios_vendor, bios_version,
    bios_date, chassis_type (numeric SMBIOS code).
  - Enrichment (binary fallback): 'dmidecode -t memory', wrapped in
    'sudo -n' when not running as root. Parses "Memory Device" blocks
    for Locator, Size, Speed, Type, Manufacturer, and Part Number;
    slots reporting "No Module Installed" are counted as empty.

Formatting (deterministic):
  - chassis_type is decoded from the SMBIOS 7.4.1 enum (e.g. 3=Desktop,
    17=Main Server Chassis, 23=Rack Mount Chassis); unmapped codes
    render as "type <n>".
  - serial_available reports whether product_serial was readable, so an
    LLM can distinguish "no serial" from "needs root".

Output: system {vendor, product, serial, uuid}, board {vendor, name},
bios {vendor, version, date}, chassis_type, dimms[] (only when
dmidecode succeeded), dimm_summary {populated, empty_slots}, and
serial_available.

Caveats: without root, product_serial/product_uuid are omitted (still
status ok). If dmidecode is absent or fails (no passwordless sudo), the
dimms section is omitted entirely — never an error. The DIMM list is
capped at 64 entries.`
}

// Category returns the functional group for this tool.
func (t *Tool) Category() registry.Category {
	return registry.CategoryHardware
}

// Parameters returns the parameter schema (none for this tool).
func (t *Tool) Parameters() []registry.ToolParam {
	return nil
}

// Hidden indicates whether this tool is excluded from LLM discovery.
func (t *Tool) Hidden() bool { return false }

// IsSupported reports whether the DMI sysfs tree exists on this node.
func (t *Tool) IsSupported() (bool, string) {
	dir := filepath.Join(t.sysfsRoot, "class", "dmi", "id")
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return false, "/sys/class/dmi/id not present (no SMBIOS/DMI exposed by firmware)"
	}
	return true, ""
}

// System identifies the machine itself.
type System struct {
	Vendor  string `json:"vendor"`
	Product string `json:"product"`
	Serial  string `json:"serial,omitempty"`
	UUID    string `json:"uuid,omitempty"`
}

// Board identifies the mainboard.
type Board struct {
	Vendor string `json:"vendor"`
	Name   string `json:"name"`
}

// BIOS identifies the firmware level.
type BIOS struct {
	Vendor  string `json:"vendor"`
	Version string `json:"version"`
	Date    string `json:"date"`
}

// DIMM describes one populated physical memory module.
type DIMM struct {
	Locator      string `json:"locator"`
	Size         string `json:"size"`
	Speed        string `json:"speed,omitempty"`
	Type         string `json:"type,omitempty"`
	Manufacturer string `json:"manufacturer,omitempty"`
	PartNumber   string `json:"part_number,omitempty"`
}

// DIMMSummary counts populated vs empty memory slots.
type DIMMSummary struct {
	Populated  int `json:"populated"`
	EmptySlots int `json:"empty_slots"`
}

// Output is the data payload of the tool result.
type Output struct {
	System          System       `json:"system"`
	Board           Board        `json:"board"`
	BIOS            BIOS         `json:"bios"`
	ChassisType     string       `json:"chassis_type"`
	DIMMs           []DIMM       `json:"dimms,omitempty"`
	DIMMSummary     *DIMMSummary `json:"dimm_summary,omitempty"`
	SerialAvailable bool         `json:"serial_available"`
}

// readID reads one /sys/class/dmi/id attribute, trimmed. ok is false
// when the file is missing or unreadable (e.g. 0400 root-only files
// read by a non-root agent).
func readID(dir, name string) (string, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(raw)), true
}

// chassisTypeNames maps the SMBIOS 7.4.1 chassis type enum (1..36).
var chassisTypeNames = map[int]string{
	1:  "Other",
	2:  "Unknown",
	3:  "Desktop",
	4:  "Low Profile Desktop",
	5:  "Pizza Box",
	6:  "Mini Tower",
	7:  "Tower",
	8:  "Portable",
	9:  "Laptop",
	10: "Notebook",
	11: "Hand Held",
	12: "Docking Station",
	13: "All In One",
	14: "Sub Notebook",
	15: "Space-saving",
	16: "Lunch Box",
	17: "Main Server Chassis",
	18: "Expansion Chassis",
	19: "Sub Chassis",
	20: "Bus Expansion Chassis",
	21: "Peripheral Chassis",
	22: "RAID Chassis",
	23: "Rack Mount Chassis",
	24: "Sealed-case PC",
	25: "Multi-system Chassis",
	26: "Compact PCI",
	27: "Advanced TCA",
	28: "Blade",
	29: "Blade Enclosure",
	30: "Tablet",
	31: "Convertible",
	32: "Detachable",
	33: "IoT Gateway",
	34: "Embedded PC",
	35: "Mini PC",
	36: "Stick PC",
}

// decodeChassisType converts the numeric sysfs chassis_type value into
// its SMBIOS name. Unmapped numeric codes render as "type <n>";
// unparseable input renders as "unknown".
func decodeChassisType(s string) string {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return "unknown"
	}
	if name, ok := chassisTypeNames[n]; ok {
		return name
	}
	return fmt.Sprintf("type %d", n)
}

// parseDMIMemory parses `dmidecode -t memory` output. It returns the
// populated DIMMs and the count of empty slots ("No Module Installed").
// Keys are matched exactly so "Bank Locator", "Type Detail", and
// "Configured Memory Speed" never pollute Locator/Type/Speed.
func parseDMIMemory(out []byte) ([]DIMM, int) {
	var dimms []DIMM
	empty := 0

	var current map[string]string
	flush := func() {
		if current == nil {
			return
		}
		size := current["Size"]
		lowered := strings.ToLower(size)
		if lowered == "no module installed" || lowered == "not installed" {
			empty++
		} else if size != "" || current["Locator"] != "" {
			dimms = append(dimms, DIMM{
				Locator:      current["Locator"],
				Size:         size,
				Speed:        current["Speed"],
				Type:         current["Type"],
				Manufacturer: current["Manufacturer"],
				PartNumber:   current["Part Number"],
			})
		}
		current = nil
	}

	for _, line := range strings.Split(string(out), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(line, "Handle ") || trimmed == "" {
			flush()
			continue
		}
		if trimmed == "Memory Device" {
			flush()
			current = make(map[string]string)
			continue
		}
		if current == nil {
			continue
		}
		parts := strings.SplitN(trimmed, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		switch key {
		case "Locator", "Size", "Speed", "Type", "Manufacturer", "Part Number":
			current[key] = value
		}
	}
	flush()

	if len(dimms) > maxDIMMs {
		dimms = dimms[:maxDIMMs]
	}
	return dimms, empty
}

// runDMIDecode executes `dmidecode -t memory`, wrapping it in
// `sudo -n` when running without root and passwordless sudo is
// plausible (sudo binary present).
func (t *Tool) runDMIDecode(ctx context.Context) ([]byte, error) {
	args := []string{"-t", "memory"}
	if t.geteuid() != 0 {
		if _, err := t.lookPath("sudo"); err == nil {
			return t.execCommand(ctx, "sudo", append([]string{"-n", "dmidecode"}, args...)...)
		}
	}
	return t.execCommand(ctx, "dmidecode", args...)
}

// Execute reads the DMI identity and optionally enriches it with the
// physical DIMM inventory.
func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("execution cancelled: %v", err)), nil
	}

	dir := filepath.Join(t.sysfsRoot, "class", "dmi", "id")
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("DMI sysfs tree not readable: %s", dir)), nil
	}

	// field degrades to "unknown" when the attribute is missing, per the
	// catalog's graceful degradation philosophy.
	field := func(name string) string {
		if v, ok := readID(dir, name); ok && v != "" {
			return v
		}
		return "unknown"
	}

	out := Output{
		System: System{
			Vendor:  field("sys_vendor"),
			Product: field("product_name"),
		},
		Board: Board{
			Vendor: field("board_vendor"),
			Name:   field("board_name"),
		},
		BIOS: BIOS{
			Vendor:  field("bios_vendor"),
			Version: field("bios_version"),
			Date:    field("bios_date"),
		},
		ChassisType: "unknown",
	}

	// Root-only attributes: omit when unreadable, still status ok.
	if serial, ok := readID(dir, "product_serial"); ok {
		out.System.Serial = serial
		out.SerialAvailable = true
	}
	if uuid, ok := readID(dir, "product_uuid"); ok {
		out.System.UUID = uuid
	}
	if chassis, ok := readID(dir, "chassis_type"); ok {
		out.ChassisType = decodeChassisType(chassis)
	}

	// DIMM enrichment is best-effort: absent binary or a failed run
	// (no passwordless sudo, locked /dev/mem) simply omits the section.
	dimmNote := "DIMM inventory unavailable (dmidecode absent or unauthorized)"
	if _, err := t.lookPath("dmidecode"); err == nil {
		if err := ctx.Err(); err != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("execution cancelled: %v", err)), nil
		}
		if raw, err := t.runDMIDecode(ctx); err == nil {
			dimms, emptySlots := parseDMIMemory(raw)
			out.DIMMs = dimms
			out.DIMMSummary = &DIMMSummary{Populated: len(dimms), EmptySlots: emptySlots}
			dimmNote = fmt.Sprintf("%d DIMMs populated, %d slots empty", len(dimms), emptySlots)
		}
	}

	summary := fmt.Sprintf("%s %s (BIOS %s %s, %s); %s.",
		out.System.Vendor, out.System.Product, out.BIOS.Version, out.BIOS.Date, out.ChassisType, dimmNote)

	return registry.NewResult(t.Name(), registry.StatusOK, summary, out), nil
}
