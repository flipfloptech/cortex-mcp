package dmiinventory

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// dmidecodeMemoryFixture is a realistic `dmidecode -t memory` payload:
// two populated DIMMs and two empty slots. It intentionally contains
// "Bank Locator", "Type Detail", and "Configured Memory Speed" lines
// that a sloppy parser would confuse with Locator/Type/Speed.
const dmidecodeMemoryFixture = `# dmidecode 3.3
Getting SMBIOS data from sysfs.
SMBIOS 3.2.0 present.

Handle 0x1100, DMI type 17, 40 bytes
Memory Device
	Array Handle: 0x1000
	Error Information Handle: Not Provided
	Total Width: 72 bits
	Data Width: 64 bits
	Size: 16 GB
	Form Factor: DIMM
	Set: None
	Locator: DIMM_A1
	Bank Locator: Bank 0
	Type: DDR4
	Type Detail: Synchronous Registered (Buffered)
	Speed: 2666 MT/s
	Manufacturer: Samsung
	Serial Number: 038A6E52
	Asset Tag: Not Specified
	Part Number: M393A2K40CB2-CTD
	Rank: 1
	Configured Memory Speed: 2400 MT/s

Handle 0x1101, DMI type 17, 40 bytes
Memory Device
	Array Handle: 0x1000
	Size: No Module Installed
	Locator: DIMM_A2
	Bank Locator: Bank 0
	Type: Unknown
	Type Detail: None

Handle 0x1102, DMI type 17, 40 bytes
Memory Device
	Array Handle: 0x1000
	Size: 16 GB
	Locator: DIMM_B1
	Bank Locator: Bank 1
	Type: DDR4
	Speed: 2666 MT/s
	Manufacturer: Hynix
	Part Number: HMA82GR7CJR8N-VK

Handle 0x1103, DMI type 17, 40 bytes
Memory Device
	Size: No Module Installed
	Locator: DIMM_B2
`

// writeDMI creates a fake <root>/class/dmi/id tree with the given files.
func writeDMI(tb testing.TB, files map[string]string) string {
	tb.Helper()
	root := tb.TempDir()
	dir := filepath.Join(root, "class", "dmi", "id")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		tb.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			tb.Fatal(err)
		}
	}
	return root
}

// fullDMIFiles returns a complete DMI id tree (root-readable scenario).
func fullDMIFiles() map[string]string {
	return map[string]string{
		"sys_vendor":     "Dell Inc.\n",
		"product_name":   "PowerEdge R750\n",
		"product_serial": "ABC1234\n",
		"product_uuid":   "4c4c4544-0042-4310-8054-b9c04f313233\n",
		"board_name":     "0PJ80M\n",
		"board_vendor":   "Dell Inc.\n",
		"bios_version":   "1.8.2\n",
		"bios_date":      "09/14/2022\n",
		"bios_vendor":    "Dell Inc.\n",
		"chassis_type":   "23\n",
	}
}

// mockTool builds a Tool against the fake tree with fully mocked
// externals. The returned pointers observe the last exec invocation.
func mockTool(root string, euid int, hasDmidecode, hasSudo bool, out string, execErr error) (*Tool, *string, *[]string) {
	tool := New()
	tool.sysfsRoot = root
	tool.geteuid = func() int { return euid }
	tool.lookPath = func(file string) (string, error) {
		switch file {
		case "dmidecode":
			if hasDmidecode {
				return "/mock/bin/dmidecode", nil
			}
		case "sudo":
			if hasSudo {
				return "/mock/bin/sudo", nil
			}
		}
		return "", errors.New("not found")
	}
	gotName := new(string)
	gotArgs := new([]string)
	tool.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		*gotName = name
		*gotArgs = args
		if execErr != nil {
			return nil, execErr
		}
		return []byte(out), nil
	}
	return tool, gotName, gotArgs
}

func executeOutput(tb testing.TB, tool *Tool) (*registry.ToolResult, Output) {
	tb.Helper()
	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		tb.Fatalf("Execute returned hard error: %v", err)
	}
	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		tb.Fatalf("failed to unmarshal output: %v", err)
	}
	return res, out
}

func TestContract(t *testing.T) {
	t.Parallel()

	tool := New()
	if tool.Name() != "get_dmi_inventory" {
		t.Errorf("Name = %q, want %q", tool.Name(), "get_dmi_inventory")
	}
	if tool.Category() != registry.CategoryHardware {
		t.Errorf("Category = %q, want hardware", tool.Category())
	}
	if tool.Description() == "" {
		t.Error("Description must not be empty")
	}
	if tool.Hidden() {
		t.Error("tool must not be hidden")
	}
	if params := tool.Parameters(); params != nil {
		t.Errorf("Parameters = %v, want nil", params)
	}

	help := tool.Help()
	if help == "" {
		t.Fatal("Help must not be empty")
	}
	// Help must reference its data sources per workflow contract.
	for _, source := range []string{
		"/sys/class/dmi/id",
		"sys_vendor",
		"product_serial",
		"chassis_type",
		"dmidecode",
	} {
		if !strings.Contains(help, source) {
			t.Errorf("Help missing data source reference: %q", source)
		}
	}
}

func TestIsSupported(t *testing.T) {
	t.Parallel()

	t.Run("missing_dmi_tree", func(t *testing.T) {
		t.Parallel()
		tool := New()
		tool.sysfsRoot = t.TempDir()
		supported, reason := tool.IsSupported()
		if supported {
			t.Error("expected unsupported when class/dmi/id missing")
		}
		if reason == "" {
			t.Error("expected a reason for unsupported")
		}
	})

	t.Run("present_dmi_tree", func(t *testing.T) {
		t.Parallel()
		tool := New()
		tool.sysfsRoot = writeDMI(t, fullDMIFiles())
		supported, reason := tool.IsSupported()
		if !supported {
			t.Errorf("expected supported, got reason: %q", reason)
		}
	})
}

func TestExecuteHappyPathAsRoot(t *testing.T) {
	t.Parallel()

	root := writeDMI(t, fullDMIFiles())
	tool, gotName, gotArgs := mockTool(root, 0, true, true, dmidecodeMemoryFixture, nil)

	res, out := executeOutput(t, tool)

	if res.Status != registry.StatusOK {
		t.Fatalf("Status = %q, want ok (summary: %s)", res.Status, res.Summary)
	}

	if out.System.Vendor != "Dell Inc." || out.System.Product != "PowerEdge R750" {
		t.Errorf("system = %+v, want Dell Inc. / PowerEdge R750", out.System)
	}
	if out.System.Serial != "ABC1234" {
		t.Errorf("system.serial = %q, want ABC1234", out.System.Serial)
	}
	if out.System.UUID != "4c4c4544-0042-4310-8054-b9c04f313233" {
		t.Errorf("system.uuid = %q, want fixture UUID", out.System.UUID)
	}
	if !out.SerialAvailable {
		t.Error("serial_available = false, want true")
	}
	if out.Board.Vendor != "Dell Inc." || out.Board.Name != "0PJ80M" {
		t.Errorf("board = %+v, want Dell Inc. / 0PJ80M", out.Board)
	}
	if out.BIOS.Vendor != "Dell Inc." || out.BIOS.Version != "1.8.2" || out.BIOS.Date != "09/14/2022" {
		t.Errorf("bios = %+v, want Dell Inc. / 1.8.2 / 09/14/2022", out.BIOS)
	}
	if out.ChassisType != "Rack Mount Chassis" {
		t.Errorf("chassis_type = %q, want Rack Mount Chassis (23)", out.ChassisType)
	}

	// As root, dmidecode must run directly (no sudo wrapping).
	if *gotName != "dmidecode" {
		t.Errorf("exec name = %q, want dmidecode", *gotName)
	}
	if strings.Join(*gotArgs, " ") != "-t memory" {
		t.Errorf("exec args = %v, want [-t memory]", *gotArgs)
	}

	if len(out.DIMMs) != 2 {
		t.Fatalf("dimms = %d, want 2 populated", len(out.DIMMs))
	}
	first := out.DIMMs[0]
	if first.Locator != "DIMM_A1" || first.Size != "16 GB" || first.Speed != "2666 MT/s" ||
		first.Type != "DDR4" || first.Manufacturer != "Samsung" || first.PartNumber != "M393A2K40CB2-CTD" {
		t.Errorf("dimms[0] = %+v, want DIMM_A1 fixture values", first)
	}
	if out.DIMMSummary == nil || out.DIMMSummary.Populated != 2 || out.DIMMSummary.EmptySlots != 2 {
		t.Errorf("dimm_summary = %+v, want populated=2 empty_slots=2", out.DIMMSummary)
	}
}

func TestExecuteNonRootUsesSudoDashN(t *testing.T) {
	t.Parallel()

	root := writeDMI(t, fullDMIFiles())
	tool, gotName, gotArgs := mockTool(root, 1000, true, true, dmidecodeMemoryFixture, nil)

	_, out := executeOutput(t, tool)

	if *gotName != "sudo" {
		t.Errorf("exec name = %q, want sudo when euid != 0", *gotName)
	}
	if strings.Join(*gotArgs, " ") != "-n dmidecode -t memory" {
		t.Errorf("exec args = %v, want [-n dmidecode -t memory]", *gotArgs)
	}
	if len(out.DIMMs) != 2 {
		t.Errorf("dimms = %d, want 2", len(out.DIMMs))
	}
}

func TestExecuteNonRootWithoutSudoRunsDirect(t *testing.T) {
	t.Parallel()

	root := writeDMI(t, fullDMIFiles())
	tool, gotName, gotArgs := mockTool(root, 1000, true, false, dmidecodeMemoryFixture, nil)

	_, _ = executeOutput(t, tool)

	if *gotName != "dmidecode" {
		t.Errorf("exec name = %q, want dmidecode when sudo absent", *gotName)
	}
	if strings.Join(*gotArgs, " ") != "-t memory" {
		t.Errorf("exec args = %v, want [-t memory]", *gotArgs)
	}
}

func TestExecuteSerialUnreadableNonRoot(t *testing.T) {
	t.Parallel()

	// product_serial and product_uuid absent — the non-root visibility
	// scenario (files are 0400 root:root on real systems).
	files := fullDMIFiles()
	delete(files, "product_serial")
	delete(files, "product_uuid")
	root := writeDMI(t, files)
	tool, _, _ := mockTool(root, 1000, true, true, dmidecodeMemoryFixture, nil)

	res, out := executeOutput(t, tool)

	if res.Status != registry.StatusOK {
		t.Errorf("Status = %q, want ok despite unreadable serial", res.Status)
	}
	if out.SerialAvailable {
		t.Error("serial_available = true, want false")
	}

	// serial/uuid keys must be omitted from the JSON entirely.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(res.Data, &raw); err != nil {
		t.Fatal(err)
	}
	var system map[string]json.RawMessage
	if err := json.Unmarshal(raw["system"], &system); err != nil {
		t.Fatal(err)
	}
	if _, ok := system["serial"]; ok {
		t.Error("system.serial must be omitted when unreadable")
	}
	if _, ok := system["uuid"]; ok {
		t.Error("system.uuid must be omitted when unreadable")
	}
}

func TestExecuteDmidecodeAbsent(t *testing.T) {
	t.Parallel()

	root := writeDMI(t, fullDMIFiles())
	tool, _, _ := mockTool(root, 0, false, false, "", nil)

	res, out := executeOutput(t, tool)

	if res.Status != registry.StatusOK {
		t.Errorf("Status = %q, want ok when dmidecode is absent", res.Status)
	}
	if out.DIMMs != nil {
		t.Errorf("dimms = %+v, want omitted when dmidecode absent", out.DIMMs)
	}
	if out.DIMMSummary != nil {
		t.Errorf("dimm_summary = %+v, want omitted when dmidecode absent", out.DIMMSummary)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(res.Data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["dimms"]; ok {
		t.Error("dimms key must be omitted entirely when dmidecode absent")
	}
}

func TestExecuteDmidecodeFails(t *testing.T) {
	t.Parallel()

	root := writeDMI(t, fullDMIFiles())
	tool, _, _ := mockTool(root, 1000, true, true, "", errors.New("sudo: a password is required"))

	res, out := executeOutput(t, tool)

	if res.Status != registry.StatusOK {
		t.Errorf("Status = %q, want ok when dmidecode fails (graceful degradation)", res.Status)
	}
	if out.DIMMs != nil || out.DIMMSummary != nil {
		t.Errorf("dimms/dimm_summary = %+v/%+v, want both omitted on dmidecode failure", out.DIMMs, out.DIMMSummary)
	}
}

func TestExecuteMissingFieldsDegrade(t *testing.T) {
	t.Parallel()

	// Only a couple of files present — everything else must degrade to
	// "unknown" without erroring.
	root := writeDMI(t, map[string]string{"sys_vendor": "QEMU\n"})
	tool, _, _ := mockTool(root, 0, false, false, "", nil)

	res, out := executeOutput(t, tool)
	if res.Status != registry.StatusOK {
		t.Errorf("Status = %q, want ok", res.Status)
	}
	if out.System.Vendor != "QEMU" {
		t.Errorf("system.vendor = %q, want QEMU", out.System.Vendor)
	}
	if out.System.Product != "unknown" || out.Board.Name != "unknown" || out.BIOS.Version != "unknown" {
		t.Errorf("missing fields must degrade to unknown, got %+v %+v %+v", out.System, out.Board, out.BIOS)
	}
	if out.ChassisType != "unknown" {
		t.Errorf("chassis_type = %q, want unknown when file missing", out.ChassisType)
	}
}

func TestExecuteMissingTree(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.sysfsRoot = t.TempDir()

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected encapsulated error, got hard error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %q, want error when class/dmi/id missing", res.Status)
	}
}

func TestExecuteContextCancelled(t *testing.T) {
	t.Parallel()

	root := writeDMI(t, fullDMIFiles())
	tool, _, _ := mockTool(root, 0, true, true, dmidecodeMemoryFixture, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := tool.Execute(ctx, nil)
	if err != nil {
		t.Fatalf("expected encapsulated error, got hard error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %q, want error on cancelled context", res.Status)
	}
}

func TestDecodeChassisType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want string
	}{
		{"1", "Other"},
		{"3", "Desktop"},
		{"17", "Main Server Chassis"},
		{"23", "Rack Mount Chassis"},
		{"28", "Blade"},
		{"36", "Stick PC"},
		{"99", "type 99"},
		{"0", "type 0"},
		{"garbage", "unknown"},
		{"", "unknown"},
	}
	for _, tc := range tests {
		if got := decodeChassisType(tc.in); got != tc.want {
			t.Errorf("decodeChassisType(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseDMIMemory(t *testing.T) {
	t.Parallel()

	dimms, empty := parseDMIMemory([]byte(dmidecodeMemoryFixture))

	if len(dimms) != 2 {
		t.Fatalf("populated dimms = %d, want 2", len(dimms))
	}
	if empty != 2 {
		t.Errorf("empty slots = %d, want 2", empty)
	}

	a1 := dimms[0]
	if a1.Locator != "DIMM_A1" {
		t.Errorf("locator = %q, want DIMM_A1 (Bank Locator must not override)", a1.Locator)
	}
	if a1.Type != "DDR4" {
		t.Errorf("type = %q, want DDR4 (Type Detail must not override)", a1.Type)
	}
	if a1.Speed != "2666 MT/s" {
		t.Errorf("speed = %q, want 2666 MT/s (Configured Memory Speed must not override)", a1.Speed)
	}
	if a1.Manufacturer != "Samsung" || a1.PartNumber != "M393A2K40CB2-CTD" || a1.Size != "16 GB" {
		t.Errorf("dimms[0] = %+v, unexpected fields", a1)
	}

	b1 := dimms[1]
	if b1.Locator != "DIMM_B1" || b1.Manufacturer != "Hynix" {
		t.Errorf("dimms[1] = %+v, want DIMM_B1 / Hynix", b1)
	}

	// Degenerate inputs must not panic and yield nothing.
	if d, e := parseDMIMemory(nil); len(d) != 0 || e != 0 {
		t.Errorf("parseDMIMemory(nil) = %v/%d, want empty", d, e)
	}
	if d, e := parseDMIMemory([]byte("# dmidecode 3.3\n\nNo SMBIOS nor DMI entry point found\n")); len(d) != 0 || e != 0 {
		t.Errorf("parseDMIMemory(no-smbios) = %v/%d, want empty", d, e)
	}
}

func TestReadID(t *testing.T) {
	t.Parallel()

	root := writeDMI(t, map[string]string{"sys_vendor": "  Supermicro \n"})
	dir := filepath.Join(root, "class", "dmi", "id")

	got, ok := readID(dir, "sys_vendor")
	if !ok || got != "Supermicro" {
		t.Errorf("readID = (%q, %v), want (Supermicro, true)", got, ok)
	}
	if _, ok := readID(dir, "product_serial"); ok {
		t.Error("readID on missing file must return ok=false")
	}
}

func TestRunDMIDecode(t *testing.T) {
	t.Parallel()

	root := writeDMI(t, fullDMIFiles())
	tool, gotName, _ := mockTool(root, 1000, true, true, "payload", nil)

	out, err := tool.runDMIDecode(context.Background())
	if err != nil {
		t.Fatalf("runDMIDecode error: %v", err)
	}
	if string(out) != "payload" {
		t.Errorf("runDMIDecode out = %q, want payload", out)
	}
	if *gotName != "sudo" {
		t.Errorf("exec name = %q, want sudo", *gotName)
	}
}
