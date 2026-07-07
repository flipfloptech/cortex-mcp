package pcilinkstatus

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// writeDevice creates a fake PCI device directory under
// <root>/bus/pci/devices/<addr> with the given file contents.
func writeDevice(tb testing.TB, root, addr string, files map[string]string) {
	tb.Helper()
	dir := filepath.Join(root, "bus", "pci", "devices", addr)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		tb.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			tb.Fatal(err)
		}
	}
}

// newFakeSysfs builds a representative fake PCI sysfs tree and returns its root.
func newFakeSysfs(tb testing.TB) string {
	tb.Helper()
	root := tb.TempDir()

	// Healthy NVMe drive on NUMA node 0 with zeroed AER counters.
	writeDevice(tb, root, "0000:01:00.0", map[string]string{
		"current_link_speed":  "16.0 GT/s PCIe\n",
		"current_link_width":  "8\n",
		"max_link_speed":      "16.0 GT/s PCIe\n",
		"max_link_width":      "8\n",
		"class":               "0x010802\n",
		"vendor":              "0x8086\n",
		"device":              "0x0a54\n",
		"numa_node":           "0\n",
		"aer_dev_correctable": "RxErr 0\nBadTLP 0\nTOTAL_ERR_COR 0\n",
		"aer_dev_nonfatal":    "Undefined 0\nTOTAL_ERR_NONFATAL 0\n",
		"aer_dev_fatal":       "Undefined 0\nTOTAL_ERR_FATAL 0\n",
	})

	// Downtrained Ethernet NIC: x4 at 2.5 GT/s vs a x8 8.0 GT/s maximum.
	// numa_node is -1 (must be omitted from output). No AER files.
	writeDevice(tb, root, "0000:02:00.0", map[string]string{
		"current_link_speed": "2.5 GT/s PCIe\n",
		"current_link_width": "4\n",
		"max_link_speed":     "8.0 GT/s PCIe\n",
		"max_link_width":     "8\n",
		"class":              "0x020000\n",
		"vendor":             "0x15b3\n",
		"device":             "0x1017\n",
		"numa_node":          "-1\n",
	})

	// Healthy PCI bridge: boring class, no errors — hidden unless all=true.
	writeDevice(tb, root, "0000:03:00.0", map[string]string{
		"current_link_speed": "8.0 GT/s PCIe\n",
		"current_link_width": "16\n",
		"max_link_speed":     "8.0 GT/s PCIe\n",
		"max_link_width":     "16\n",
		"class":              "0x060400\n",
		"vendor":             "0x1000\n",
		"device":             "0x00b2\n",
		"numa_node":          "0\n",
	})

	// Virtual function without link files — must be skipped silently.
	writeDevice(tb, root, "0000:04:00.1", map[string]string{
		"class":  "0x020000\n",
		"vendor": "0x15b3\n",
		"device": "0x101a\n",
	})

	// Boring-class peripheral accumulating AER errors (correctable + fatal).
	// TOTAL_ERR_* lines must NOT be double counted.
	writeDevice(tb, root, "0000:05:00.0", map[string]string{
		"current_link_speed":  "8.0 GT/s PCIe\n",
		"current_link_width":  "8\n",
		"max_link_speed":      "8.0 GT/s PCIe\n",
		"max_link_width":      "8\n",
		"class":               "0x088000\n",
		"vendor":              "0x8086\n",
		"device":              "0x2021\n",
		"aer_dev_correctable": "RxErr 3\nBadTLP 2\nBadDLLP 0\nTOTAL_ERR_COR 5\n",
		"aer_dev_nonfatal":    "Undefined 0\nTOTAL_ERR_NONFATAL 0\n",
		"aer_dev_fatal":       "Undefined 0\nDLP 1\nTLP 1\nTOTAL_ERR_FATAL 2\n",
	})

	return root
}

func executeOutput(tb testing.TB, tool *Tool, args string) (*registry.ToolResult, Output) {
	tb.Helper()
	res, err := tool.Execute(context.Background(), json.RawMessage(args))
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
	if tool.Name() != "get_pci_link_status" {
		t.Errorf("Name = %q, want %q", tool.Name(), "get_pci_link_status")
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

	params := tool.Parameters()
	if len(params) != 1 {
		t.Fatalf("Parameters length = %d, want 1", len(params))
	}
	if params[0].Name != "all" || params[0].Type != "boolean" || params[0].Required {
		t.Errorf("unexpected 'all' parameter schema: %+v", params[0])
	}

	help := tool.Help()
	if help == "" {
		t.Fatal("Help must not be empty")
	}
	// Help must reference its data sources per workflow contract.
	for _, source := range []string{
		"/sys/bus/pci/devices",
		"current_link_speed",
		"current_link_width",
		"max_link_speed",
		"aer_dev_correctable",
		"aer_dev_nonfatal",
		"aer_dev_fatal",
	} {
		if !strings.Contains(help, source) {
			t.Errorf("Help missing data source reference: %q", source)
		}
	}
}

func TestIsSupported(t *testing.T) {
	t.Parallel()

	t.Run("missing_pci_tree", func(t *testing.T) {
		t.Parallel()
		tool := New()
		tool.sysfsRoot = t.TempDir()
		supported, reason := tool.IsSupported()
		if supported {
			t.Error("expected unsupported when bus/pci/devices is missing")
		}
		if reason == "" {
			t.Error("expected a reason for unsupported")
		}
	})

	t.Run("present_pci_tree", func(t *testing.T) {
		t.Parallel()
		tool := New()
		tool.sysfsRoot = newFakeSysfs(t)
		supported, reason := tool.IsSupported()
		if !supported {
			t.Errorf("expected supported, got reason: %q", reason)
		}
	})
}

func TestExecuteDefaultFiltering(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.sysfsRoot = newFakeSysfs(t)

	res, out := executeOutput(t, tool, `{}`)

	if res.Status != registry.StatusWarning {
		t.Errorf("Status = %q, want warning (downtrained + fatal AER present)", res.Status)
	}
	if out.Summary.DevicesScanned != 4 {
		t.Errorf("devices_scanned = %d, want 4 (virtual fn without link files skipped)", out.Summary.DevicesScanned)
	}
	if out.Summary.DowntrainedCount != 1 {
		t.Errorf("downtrained_count = %d, want 1", out.Summary.DowntrainedCount)
	}
	if out.Summary.AERErrorCount != 1 {
		t.Errorf("aer_error_count = %d, want 1", out.Summary.AERErrorCount)
	}

	if len(out.Devices) != 3 {
		t.Fatalf("devices returned = %d, want 3 (bridge filtered out)", len(out.Devices))
	}

	wantAddrs := []string{"0000:01:00.0", "0000:02:00.0", "0000:05:00.0"}
	for i, want := range wantAddrs {
		if out.Devices[i].Address != want {
			t.Errorf("devices[%d].address = %q, want %q", i, out.Devices[i].Address, want)
		}
	}

	nvme := out.Devices[0]
	if nvme.ClassName != "nvme" {
		t.Errorf("nvme class_name = %q, want nvme", nvme.ClassName)
	}
	if nvme.VendorID != "0x8086" || nvme.DeviceID != "0x0a54" {
		t.Errorf("nvme ids = %q/%q, want 0x8086/0x0a54", nvme.VendorID, nvme.DeviceID)
	}
	if nvme.NUMANode == nil || *nvme.NUMANode != 0 {
		t.Errorf("nvme numa_node = %v, want 0", nvme.NUMANode)
	}
	if nvme.IsDowntrained {
		t.Error("nvme should not be downtrained")
	}
	if nvme.AER == nil || nvme.AER.Correctable != 0 || nvme.AER.NonFatal != 0 || nvme.AER.Fatal != 0 {
		t.Errorf("nvme aer = %+v, want zeroed counters present", nvme.AER)
	}
	if nvme.Link.CurrentWidth != 8 || nvme.Link.MaxWidth != 8 {
		t.Errorf("nvme link widths = %d/%d, want 8/8", nvme.Link.CurrentWidth, nvme.Link.MaxWidth)
	}
	if len(nvme.WarningReasons) != 0 {
		t.Errorf("nvme warning_reasons = %v, want none", nvme.WarningReasons)
	}

	nic := out.Devices[1]
	if nic.ClassName != "ethernet" {
		t.Errorf("nic class_name = %q, want ethernet", nic.ClassName)
	}
	if nic.NUMANode != nil {
		t.Errorf("nic numa_node = %v, want omitted (-1 in sysfs)", *nic.NUMANode)
	}
	if !nic.IsDowntrained {
		t.Error("nic must be flagged downtrained (x4@2.5 vs x8@8.0)")
	}
	if nic.AER != nil {
		t.Errorf("nic aer = %+v, want omitted (no AER files)", nic.AER)
	}
	joined := strings.Join(nic.WarningReasons, " | ")
	if !strings.Contains(joined, "x4") || !strings.Contains(joined, "x8") {
		t.Errorf("nic warning_reasons %q must mention x4 and x8", joined)
	}

	periph := out.Devices[2]
	if !strings.Contains(periph.ClassName, "other") {
		t.Errorf("peripheral class_name = %q, want other+hex", periph.ClassName)
	}
	if periph.AER == nil {
		t.Fatal("peripheral aer must be present")
	}
	if periph.AER.Correctable != 5 {
		t.Errorf("peripheral aer.correctable = %d, want 5 (TOTAL_ERR_COR excluded)", periph.AER.Correctable)
	}
	if periph.AER.Fatal != 2 {
		t.Errorf("peripheral aer.fatal = %d, want 2 (TOTAL_ERR_FATAL excluded)", periph.AER.Fatal)
	}
	if !strings.Contains(strings.Join(periph.WarningReasons, " "), "fatal") {
		t.Errorf("peripheral warning_reasons %v must mention fatal AER errors", periph.WarningReasons)
	}
}

func TestExecuteAllParam(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.sysfsRoot = newFakeSysfs(t)

	_, out := executeOutput(t, tool, `{"all": true}`)
	if len(out.Devices) != 4 {
		t.Fatalf("devices returned = %d, want 4 with all=true", len(out.Devices))
	}
	found := false
	for _, d := range out.Devices {
		if d.Address == "0000:03:00.0" {
			found = true
		}
	}
	if !found {
		t.Error("all=true must include the healthy PCI bridge")
	}
}

func TestExecuteHealthyTreeStatusOK(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeDevice(t, root, "0000:01:00.0", map[string]string{
		"current_link_speed": "16.0 GT/s PCIe\n",
		"current_link_width": "4\n",
		"max_link_speed":     "16.0 GT/s PCIe\n",
		"max_link_width":     "4\n",
		"class":              "0x010802\n",
		"vendor":             "0x144d\n",
		"device":             "0xa808\n",
		"numa_node":          "1\n",
	})
	tool := New()
	tool.sysfsRoot = root

	res, out := executeOutput(t, tool, `{}`)
	if res.Status != registry.StatusOK {
		t.Errorf("Status = %q, want ok for healthy tree", res.Status)
	}
	if len(out.Devices) != 1 || out.Devices[0].IsDowntrained {
		t.Errorf("unexpected devices: %+v", out.Devices)
	}
	if out.Devices[0].NUMANode == nil || *out.Devices[0].NUMANode != 1 {
		t.Errorf("numa_node = %v, want 1", out.Devices[0].NUMANode)
	}
}

func TestExecuteInvalidArgs(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.sysfsRoot = newFakeSysfs(t)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"all": "definitely"}`))
	if err != nil {
		t.Fatalf("expected encapsulated error, got hard error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %q, want error for invalid args", res.Status)
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
		t.Errorf("Status = %q, want error when bus/pci/devices is missing", res.Status)
	}
}

func TestExecuteContextCancelled(t *testing.T) {
	t.Parallel()

	tool := New()
	tool.sysfsRoot = newFakeSysfs(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := tool.Execute(ctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("expected encapsulated error, got hard error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %q, want error on cancelled context", res.Status)
	}
}

func TestParseGTs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in     string
		want   float64
		wantOK bool
	}{
		{"8.0 GT/s PCIe", 8.0, true},
		{"2.5 GT/s", 2.5, true},
		{"16.0 GT/s PCIe", 16.0, true},
		{"32.0 GT/s PCIe", 32.0, true},
		{"5 GT/s", 5.0, true},
		{"Unknown speed", 0, false},
		{"", 0, false},
		{"GT/s", 0, false},
	}
	for _, tc := range tests {
		got, ok := parseGTs(tc.in)
		if ok != tc.wantOK || (ok && got != tc.want) {
			t.Errorf("parseGTs(%q) = (%v, %v), want (%v, %v)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestDecodeClassName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want string
	}{
		{"0x010802", "nvme"},
		{"0x020000", "ethernet"},
		{"0x020700", "infiniband"},
		{"0x0c0400", "infiniband"},
		{"0x030000", "gpu"},
		{"0x030200", "gpu"},
		{"0x010601", "storage"},
		{"0x060400", "other (0x060400)"},
		{"0x088000", "other (0x088000)"},
		{"garbage", "other (garbage)"},
	}
	for _, tc := range tests {
		if got := decodeClassName(tc.in); got != tc.want {
			t.Errorf("decodeClassName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIsInterestingClass(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want bool
	}{
		{"0x010802", true},  // NVMe
		{"0x020000", true},  // Ethernet
		{"0x020700", true},  // InfiniBand
		{"0x0c0400", true},  // Fibre Channel / fabric
		{"0x030000", true},  // GPU
		{"0x010601", false}, // AHCI SATA — storage but not flagged interesting
		{"0x060400", false}, // PCI bridge
		{"0x088000", false}, // System peripheral
		{"", false},
	}
	for _, tc := range tests {
		if got := isInterestingClass(tc.in); got != tc.want {
			t.Errorf("isInterestingClass(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestReadAERSum(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	full := filepath.Join(dir, "aer_dev_correctable")
	content := "RxErr 3\nBadTLP 2\nBadDLLP 0\nnot-a-counter\nTOTAL_ERR_COR 5\n"
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	sum, ok := readAERSum(full)
	if !ok {
		t.Fatal("readAERSum ok = false, want true")
	}
	if sum != 5 {
		t.Errorf("readAERSum = %d, want 5 (TOTAL excluded, malformed skipped)", sum)
	}

	if _, ok := readAERSum(filepath.Join(dir, "missing")); ok {
		t.Error("readAERSum on missing file must return ok=false")
	}
}

func TestReadTrimmed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "value")
	if err := os.WriteFile(path, []byte("  8.0 GT/s PCIe \n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok := readTrimmed(path)
	if !ok || got != "8.0 GT/s PCIe" {
		t.Errorf("readTrimmed = (%q, %v), want (\"8.0 GT/s PCIe\", true)", got, ok)
	}
	if _, ok := readTrimmed(filepath.Join(dir, "missing")); ok {
		t.Error("readTrimmed on missing file must return ok=false")
	}
}

func TestCollectDevice(t *testing.T) {
	t.Parallel()

	root := newFakeSysfs(t)

	t.Run("with_link_files", func(t *testing.T) {
		t.Parallel()
		dev, ok := collectDevice(filepath.Join(root, "bus", "pci", "devices", "0000:02:00.0"), "0000:02:00.0")
		if !ok || dev == nil {
			t.Fatal("expected device with link files to be collected")
		}
		if !dev.IsDowntrained {
			t.Error("expected downtrained device")
		}
	})

	t.Run("without_link_files", func(t *testing.T) {
		t.Parallel()
		_, ok := collectDevice(filepath.Join(root, "bus", "pci", "devices", "0000:04:00.1"), "0000:04:00.1")
		if ok {
			t.Error("device without link files must be skipped")
		}
	})
}
