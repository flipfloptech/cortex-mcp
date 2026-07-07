package smarthealth

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

const healthySmartctlJSON = `{
	"model_name": "MOCK SSD 860 EVO 1TB",
	"smart_status": {"passed": true},
	"temperature": {"current": 33},
	"power_on_time": {"hours": 12345},
	"ata_smart_attributes": {
		"table": [
			{"id": 5, "name": "Reallocated_Sector_Ct", "raw": {"value": 0, "string": "0"}},
			{"id": 194, "name": "Temperature_Celsius", "raw": {"value": 33, "string": "33"}},
			{"id": 197, "name": "Current_Pending_Sector", "raw": {"value": 0, "string": "0"}},
			{"id": 198, "name": "Offline_Uncorrectable", "raw": {"value": 0, "string": "0"}},
			{"id": 199, "name": "UDMA_CRC_Error_Count", "raw": {"value": 0, "string": "0"}}
		]
	}
}`

const failingSmartctlJSON = `{
	"model_name": "MOCK HDD ST4000",
	"smart_status": {"passed": false},
	"temperature": {"current": 65},
	"power_on_time": {"hours": 60000},
	"ata_smart_attributes": {
		"table": [
			{"id": 5, "name": "Reallocated_Sector_Ct", "raw": {"value": 12, "string": "12"}},
			{"id": 197, "name": "Current_Pending_Sector", "raw": {"value": 3, "string": "3"}},
			{"id": 198, "name": "Offline_Uncorrectable", "raw": {"value": 2, "string": "2"}},
			{"id": 199, "name": "UDMA_CRC_Error_Count", "raw": {"value": 7, "string": "7"}}
		]
	}
}`

// writeFile is a test helper that creates a file (and parents) with content.
func writeFile(t testing.TB, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// addDisk adds a SATA/SAS disk with a device/ subdir to the fake sysfs tree.
func addDisk(t testing.TB, sysfsRoot, name, vendor, model string) {
	t.Helper()
	writeFile(t, filepath.Join(sysfsRoot, "class", "block", name, "device", "vendor"), vendor+"\n")
	writeFile(t, filepath.Join(sysfsRoot, "class", "block", name, "device", "model"), model+"\n")
}

// newFixtureTool builds a tool wired to a fake sysfs tree with one SATA disk
// (sda) plus excluded noise entries, running as root with a mocked smartctl
// that always reports a healthy drive.
func newFixtureTool(t testing.TB) *Tool {
	t.Helper()
	sysRoot := filepath.Join(t.TempDir(), "sys")

	addDisk(t, sysRoot, "sda", "ATA", "Samsung SSD 860")
	// Excluded: partition (regex), virtual device (no device/ dir), NVMe,
	// optical, device-mapper.
	writeFile(t, filepath.Join(sysRoot, "class", "block", "sda1", "partition"), "1\n")
	if err := os.MkdirAll(filepath.Join(sysRoot, "class", "block", "sdv"), 0o755); err != nil {
		t.Fatal(err)
	}
	addDisk(t, sysRoot, "nvme0n1", "", "NVMe Drive")
	addDisk(t, sysRoot, "sr0", "ATA", "DVD")
	if err := os.MkdirAll(filepath.Join(sysRoot, "class", "block", "dm-0"), 0o755); err != nil {
		t.Fatal(err)
	}

	tool := New()
	tool.sysfsRoot = sysRoot
	tool.geteuid = func() int { return 0 }
	tool.lookPath = func(file string) (string, error) {
		if file == "smartctl" {
			return "/mock/sbin/smartctl", nil
		}
		return "", errors.New("not found")
	}
	tool.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "smartctl" {
			return nil, errors.New("unexpected command: " + name)
		}
		return []byte(healthySmartctlJSON), nil
	}
	return tool
}

func decodeOutput(t *testing.T, res *registry.ToolResult) Output {
	t.Helper()
	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("failed to unmarshal output: %v (raw: %s)", err, string(res.Data))
	}
	return out
}

func TestSmartHealth_Contract(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "get_smart_health" {
		t.Errorf("expected get_smart_health, got %s", tool.Name())
	}
	if tool.Category() != registry.CategoryStorage {
		t.Errorf("expected storage, got %s", tool.Category())
	}
	if tool.Description() == "" {
		t.Error("expected non-empty description")
	}
	if tool.Hidden() {
		t.Error("expected Hidden() == false")
	}

	params := tool.Parameters()
	if len(params) != 1 {
		t.Fatalf("expected 1 parameter, got %d", len(params))
	}
	if params[0].Name != "target_device" || params[0].Type != "string" || params[0].Required {
		t.Errorf("unexpected target_device parameter schema: %+v", params[0])
	}

	help := tool.Help()
	for _, src := range []string{"smartctl", "/sys/class/block"} {
		if !strings.Contains(help, src) {
			t.Errorf("Help() must reference data source %q", src)
		}
	}
}

func TestSmartHealth_IsSupported(t *testing.T) {
	t.Parallel()

	t.Run("smartctl present", func(t *testing.T) {
		t.Parallel()
		tool := newFixtureTool(t)
		supported, reason := tool.IsSupported()
		if !supported {
			t.Errorf("expected supported, got false (%s)", reason)
		}
	})

	t.Run("smartctl absent", func(t *testing.T) {
		t.Parallel()
		tool := New()
		tool.lookPath = func(string) (string, error) { return "", errors.New("not found") }
		supported, reason := tool.IsSupported()
		if supported {
			t.Error("expected unsupported without smartctl")
		}
		if !strings.Contains(reason, "smartctl") {
			t.Errorf("reason should mention smartctl, got %q", reason)
		}
	})
}

func TestSmartHealth_Execute_Healthy(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected encapsulated errors, got hard error: %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("expected status ok, got %s (%s)", res.Status, res.Summary)
	}

	out := decodeOutput(t, res)
	if len(out.Drives) != 1 {
		t.Fatalf("expected exactly 1 drive (noise excluded), got %d: %+v", len(out.Drives), out.Drives)
	}

	d := out.Drives[0]
	if d.Device != "sda" {
		t.Errorf("expected device sda, got %s", d.Device)
	}
	if d.Model != "MOCK SSD 860 EVO 1TB" {
		t.Errorf("expected model from smartctl JSON, got %q", d.Model)
	}
	if !d.SmartPassed {
		t.Error("expected smart_passed=true")
	}
	if d.TemperatureC != 33 {
		t.Errorf("expected temperature 33, got %d", d.TemperatureC)
	}
	if d.PowerOnHours != 12345 {
		t.Errorf("expected power_on_hours 12345, got %d", d.PowerOnHours)
	}
	if d.ReallocatedSectors != 0 || d.PendingSectors != 0 || d.UncorrectableSectors != 0 || d.CRCErrors != 0 {
		t.Errorf("expected zero defect counters, got %+v", d)
	}
	if d.Status != "healthy" {
		t.Errorf("expected status healthy, got %s", d.Status)
	}
	if len(d.WarningReasons) != 0 {
		t.Errorf("expected no warnings, got %v", d.WarningReasons)
	}

	s := out.SystemSummary
	if s.DrivesAudited != 1 || s.DrivesFailing != 0 || s.TotalReallocated != 0 {
		t.Errorf("unexpected system_summary: %+v", s)
	}
}

func TestSmartHealth_Execute_SudoWrapping(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)
	tool.geteuid = func() int { return 1000 }
	tool.lookPath = func(file string) (string, error) {
		if file == "smartctl" || file == "sudo" {
			return "/mock/bin/" + file, nil
		}
		return "", errors.New("not found")
	}

	var gotName string
	var gotArgs []string
	tool.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		gotName = name
		gotArgs = args
		return []byte(healthySmartctlJSON), nil
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("expected status ok, got %s (%s)", res.Status, res.Summary)
	}

	if gotName != "sudo" {
		t.Errorf("expected sudo wrapper when euid != 0, got %q", gotName)
	}
	want := []string{"-n", "smartctl", "-a", "-j", "/dev/sda"}
	if len(gotArgs) != len(want) {
		t.Fatalf("expected args %v, got %v", want, gotArgs)
	}
	for i := range want {
		if gotArgs[i] != want[i] {
			t.Fatalf("expected args %v, got %v", want, gotArgs)
		}
	}
}

func TestSmartHealth_Execute_NoSudoBinaryRunsDirect(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)
	tool.geteuid = func() int { return 1000 }
	tool.lookPath = func(file string) (string, error) {
		if file == "smartctl" {
			return "/mock/sbin/smartctl", nil
		}
		return "", errors.New("not found")
	}

	var gotName string
	tool.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		gotName = name
		return []byte(healthySmartctlJSON), nil
	}

	if _, err := tool.Execute(context.Background(), nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotName != "smartctl" {
		t.Errorf("expected direct smartctl without sudo binary, got %q", gotName)
	}
}

func TestSmartHealth_Execute_PermissionLockout(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)
	tool.geteuid = func() int { return 1000 }
	tool.lookPath = func(file string) (string, error) { return "/mock/bin/" + file, nil }
	tool.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("sudo: a password is required\n"), errors.New("exit status 1")
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("lockout must be encapsulated, got hard error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Fatalf("expected status error, got %s", res.Status)
	}
	if !strings.Contains(res.Summary, "Unauthorized") {
		t.Errorf("expected Unauthorized lockout summary, got %q", res.Summary)
	}
}

func TestSmartHealth_Execute_FailingDriveAttributes(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)
	tool.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		// smartctl exits non-zero when SMART checks fail; output is still valid.
		return []byte(failingSmartctlJSON), errors.New("exit status 64")
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Fatalf("expected status error for failed self-assessment, got %s", res.Status)
	}

	out := decodeOutput(t, res)
	if len(out.Drives) != 1 {
		t.Fatalf("expected 1 drive, got %d", len(out.Drives))
	}

	d := out.Drives[0]
	if d.SmartPassed {
		t.Error("expected smart_passed=false")
	}
	if d.ReallocatedSectors != 12 || d.PendingSectors != 3 || d.UncorrectableSectors != 2 || d.CRCErrors != 7 {
		t.Errorf("unexpected defect counters: %+v", d)
	}
	if d.TemperatureC != 65 {
		t.Errorf("expected temperature 65, got %d", d.TemperatureC)
	}
	if d.Status != "critical" {
		t.Errorf("expected status critical, got %s", d.Status)
	}

	joined := strings.ToLower(strings.Join(d.WarningReasons, "; "))
	for _, want := range []string{"failed", "reallocated", "pending", "uncorrectable", "crc", "temperature"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warning_reasons should mention %q, got %v", want, d.WarningReasons)
		}
	}

	s := out.SystemSummary
	if s.DrivesAudited != 1 || s.DrivesFailing != 1 || s.TotalReallocated != 12 {
		t.Errorf("unexpected system_summary: %+v", s)
	}
}

func TestSmartHealth_Execute_AttributeWarningsOnlyIsWarning(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)
	// Passed self-assessment but pending sectors present.
	tool.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte(`{
			"model_name": "MOCK",
			"smart_status": {"passed": true},
			"temperature": {"current": 40},
			"power_on_time": {"hours": 100},
			"ata_smart_attributes": {"table": [
				{"id": 197, "name": "Current_Pending_Sector", "raw": {"value": 1, "string": "1"}}
			]}
		}`), nil
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != registry.StatusWarning {
		t.Fatalf("expected status warning, got %s", res.Status)
	}

	out := decodeOutput(t, res)
	if out.Drives[0].Status != "critical" {
		t.Errorf("expected drive status critical with pending sectors, got %s", out.Drives[0].Status)
	}
	if out.SystemSummary.DrivesFailing != 1 {
		t.Errorf("expected drives_failing 1, got %d", out.SystemSummary.DrivesFailing)
	}
}

func TestSmartHealth_Execute_TargetDeviceFilter(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)
	addDisk(t, tool.sysfsRoot, "sdb", "SEAGATE", "ST4000NM0023")

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"target_device":"sdb"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := decodeOutput(t, res)
	if len(out.Drives) != 1 {
		t.Fatalf("expected 1 drive with target filter, got %d", len(out.Drives))
	}
	if out.Drives[0].Device != "sdb" {
		t.Errorf("expected device sdb, got %s", out.Drives[0].Device)
	}
}

func TestSmartHealth_Execute_InvalidArgs(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"target_device": 5}`))
	if err != nil {
		t.Fatalf("invalid args must be encapsulated, got hard error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("expected status error for invalid args, got %s", res.Status)
	}
}

func TestSmartHealth_Execute_ParseFailureSkipsDrive(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)
	addDisk(t, tool.sysfsRoot, "sdb", "SEAGATE", "ST4000NM0023")
	tool.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		for _, a := range args {
			if a == "/dev/sda" {
				return []byte("Smartctl open device: /dev/sda failed: INQUIRY failed"), errors.New("exit status 2")
			}
		}
		return []byte(healthySmartctlJSON), nil
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("expected status ok (bad drive skipped), got %s (%s)", res.Status, res.Summary)
	}

	out := decodeOutput(t, res)
	if len(out.Drives) != 1 || out.Drives[0].Device != "sdb" {
		t.Errorf("expected only sdb audited, got %+v", out.Drives)
	}
}

func TestSmartHealth_Execute_NoDrives(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)
	tool.sysfsRoot = t.TempDir() // empty tree: no SATA/SAS drives

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("expected status ok for zero drives, got %s", res.Status)
	}
	if !strings.Contains(string(res.Data), `"drives":[]`) {
		t.Errorf("expected empty drives to serialize as [], got: %s", string(res.Data))
	}

	out := decodeOutput(t, res)
	if out.SystemSummary.DrivesAudited != 0 {
		t.Errorf("expected 0 drives audited, got %d", out.SystemSummary.DrivesAudited)
	}
}

func TestSmartHealth_Execute_ContextCancelled(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := tool.Execute(ctx, nil)
	if err != nil {
		t.Fatalf("cancellation must be encapsulated, got hard error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("expected status error on cancelled context, got %s", res.Status)
	}
}

func TestDiscoverSATADrives(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)
	addDisk(t, tool.sysfsRoot, "sdb", "SEAGATE ", " ST4000NM0023 ")

	drives := discoverSATADrives(tool.sysfsRoot)
	if len(drives) != 2 {
		t.Fatalf("expected [sda sdb], got %+v", drives)
	}
	if drives[0].Name != "sda" || drives[1].Name != "sdb" {
		t.Errorf("expected sorted [sda sdb], got %+v", drives)
	}
	if drives[1].Model != "SEAGATE ST4000NM0023" {
		t.Errorf("expected trimmed 'SEAGATE ST4000NM0023', got %q", drives[1].Model)
	}

	if got := discoverSATADrives(t.TempDir()); len(got) != 0 {
		t.Errorf("expected no drives in empty tree, got %+v", got)
	}
}

func TestParseSmartctl(t *testing.T) {
	t.Parallel()

	t.Run("healthy report", func(t *testing.T) {
		t.Parallel()
		d, err := parseSmartctl([]byte(healthySmartctlJSON), "sda", "FALLBACK")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if d.Model != "MOCK SSD 860 EVO 1TB" || !d.SmartPassed || d.TemperatureC != 33 || d.PowerOnHours != 12345 {
			t.Errorf("unexpected drive: %+v", d)
		}
	})

	t.Run("model falls back to sysfs identity", func(t *testing.T) {
		t.Parallel()
		d, err := parseSmartctl([]byte(`{"smart_status":{"passed":true}}`), "sda", "SEAGATE ST4000")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if d.Model != "SEAGATE ST4000" {
			t.Errorf("expected fallback model, got %q", d.Model)
		}
	})

	t.Run("invalid JSON errors", func(t *testing.T) {
		t.Parallel()
		if _, err := parseSmartctl([]byte("not json"), "sda", ""); err == nil {
			t.Error("expected error for invalid JSON")
		}
	})

	t.Run("missing smart_status errors", func(t *testing.T) {
		t.Parallel()
		if _, err := parseSmartctl([]byte(`{"model_name":"x"}`), "sda", ""); err == nil {
			t.Error("expected error when smart_status block is absent")
		}
	})
}

func TestIsPermissionDenied(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		out  string
		want bool
	}{
		{"permission denied in output", errors.New("exit status 1"), "smartctl: Permission denied", true},
		{"operation not permitted in output", errors.New("exit status 1"), "Operation not permitted", true},
		{"sudo password required", errors.New("exit status 1"), "sudo: a password is required", true},
		{"permission denied in error", errors.New("fork/exec: permission denied"), "", true},
		{"benign failure", errors.New("exit status 2"), "device open failed", false},
		{"no error", nil, "clean output", false},
		{"empty", nil, "", false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isPermissionDenied(tc.err, []byte(tc.out)); got != tc.want {
				t.Errorf("isPermissionDenied(%v, %q) = %v, want %v", tc.err, tc.out, got, tc.want)
			}
		})
	}
}
