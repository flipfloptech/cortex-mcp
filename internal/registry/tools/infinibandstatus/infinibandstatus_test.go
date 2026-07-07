package infinibandstatus

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// writeSysfsFile creates a file (and parent dirs) inside a fake sysfs tree.
func writeSysfsFile(t testing.TB, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// buildFakeIBTree builds a fake sysfs root with two HCAs:
//   - mlx5_0 port 1: healthy ACTIVE/LinkUp port with all-zero counters
//   - mlx5_1 port 1: DOWN/Disabled port with nonzero error counters and
//     two counter files absent (link_error_recovery, port_xmit_discards)
func buildFakeIBTree(t testing.TB) string {
	t.Helper()
	root := t.TempDir()

	p0 := filepath.Join(root, "class", "infiniband", "mlx5_0", "ports", "1")
	writeSysfsFile(t, filepath.Join(p0, "state"), "4: ACTIVE\n")
	writeSysfsFile(t, filepath.Join(p0, "phys_state"), "5: LinkUp\n")
	writeSysfsFile(t, filepath.Join(p0, "rate"), "100 Gb/sec (4X EDR)\n")
	writeSysfsFile(t, filepath.Join(p0, "lid"), "0x3\n")
	writeSysfsFile(t, filepath.Join(p0, "link_layer"), "InfiniBand\n")
	for _, c := range []string{"symbol_error", "link_downed", "link_error_recovery", "port_rcv_errors", "port_xmit_discards"} {
		writeSysfsFile(t, filepath.Join(p0, "counters", c), "0\n")
	}
	// Stray non-numeric entry in the ports dir must be ignored.
	writeSysfsFile(t, filepath.Join(root, "class", "infiniband", "mlx5_0", "ports", "README"), "not a port\n")

	p1 := filepath.Join(root, "class", "infiniband", "mlx5_1", "ports", "1")
	writeSysfsFile(t, filepath.Join(p1, "state"), "1: DOWN\n")
	writeSysfsFile(t, filepath.Join(p1, "phys_state"), "3: Disabled\n")
	writeSysfsFile(t, filepath.Join(p1, "rate"), "10 Gb/sec (4X SDR)\n")
	writeSysfsFile(t, filepath.Join(p1, "lid"), "0x0\n")
	writeSysfsFile(t, filepath.Join(p1, "link_layer"), "InfiniBand\n")
	writeSysfsFile(t, filepath.Join(p1, "counters", "symbol_error"), "12\n")
	writeSysfsFile(t, filepath.Join(p1, "counters", "link_downed"), "3\n")
	writeSysfsFile(t, filepath.Join(p1, "counters", "port_rcv_errors"), "7\n")

	return root
}

func newWithRoot(root string) *Tool {
	tool := New()
	tool.sysfsRoot = root
	return tool
}

func decodeOutput(t *testing.T, res *registry.ToolResult) Output {
	t.Helper()
	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("cannot unmarshal result data: %v", err)
	}
	return out
}

func TestContractCompliance(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "get_infiniband_status" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "get_infiniband_status")
	}
	if tool.Category() != registry.CategoryNetwork {
		t.Errorf("Category() = %q, want network", tool.Category())
	}
	if tool.Hidden() {
		t.Error("Hidden() = true, want false")
	}
	if tool.Parameters() != nil {
		t.Errorf("Parameters() = %v, want nil", tool.Parameters())
	}
	if tool.Description() == "" {
		t.Error("Description() must not be empty")
	}
	for _, ref := range []string{"/sys/class/infiniband", "counters"} {
		if !strings.Contains(tool.Help(), ref) {
			t.Errorf("Help() must reference data source %q", ref)
		}
	}
}

func TestIsSupported(t *testing.T) {
	t.Parallel()

	t.Run("supported when class/infiniband exists", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "class", "infiniband"), 0o755); err != nil {
			t.Fatal(err)
		}
		tool := newWithRoot(root)
		ok, reason := tool.IsSupported()
		if !ok {
			t.Errorf("IsSupported() = false (%q), want true", reason)
		}
	})

	t.Run("unsupported when sysfs path missing", func(t *testing.T) {
		t.Parallel()
		tool := newWithRoot(t.TempDir())
		ok, reason := tool.IsSupported()
		if ok {
			t.Error("IsSupported() = true, want false")
		}
		if !strings.Contains(reason, "no InfiniBand devices") {
			t.Errorf("reason = %q, want mention of missing InfiniBand devices", reason)
		}
	})
}

func TestExecuteHappyPath(t *testing.T) {
	t.Parallel()
	tool := newWithRoot(buildFakeIBTree(t))

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil (errors must be encapsulated)", err)
	}
	if res.Status != registry.StatusWarning {
		t.Errorf("Status = %q, want warning (one port is DOWN with errors)", res.Status)
	}

	out := decodeOutput(t, res)

	if out.SystemSummary.TotalPorts != 2 {
		t.Errorf("total_ports = %d, want 2", out.SystemSummary.TotalPorts)
	}
	if out.SystemSummary.ActivePorts != 1 {
		t.Errorf("active_ports = %d, want 1", out.SystemSummary.ActivePorts)
	}
	if out.SystemSummary.PortsWithErrors != 1 {
		t.Errorf("ports_with_errors = %d, want 1", out.SystemSummary.PortsWithErrors)
	}
	if len(out.Ports) != 2 {
		t.Fatalf("len(ports) = %d, want 2", len(out.Ports))
	}

	healthy := out.Ports[0]
	if healthy.Device != "mlx5_0" || healthy.Port != 1 {
		t.Errorf("port[0] identity = %s/%d, want mlx5_0/1", healthy.Device, healthy.Port)
	}
	if healthy.State != "ACTIVE" {
		t.Errorf("port[0].state = %q, want ACTIVE (decoded from '4: ACTIVE')", healthy.State)
	}
	if healthy.PhysState != "LinkUp" {
		t.Errorf("port[0].phys_state = %q, want LinkUp", healthy.PhysState)
	}
	if healthy.Rate != "100 Gb/sec (4X EDR)" {
		t.Errorf("port[0].rate = %q", healthy.Rate)
	}
	if healthy.RateGbps != 100 {
		t.Errorf("port[0].rate_gbps = %v, want 100", healthy.RateGbps)
	}
	if healthy.LID != 3 {
		t.Errorf("port[0].lid = %d, want 3 (decoded from 0x3)", healthy.LID)
	}
	if healthy.LinkLayer != "InfiniBand" {
		t.Errorf("port[0].link_layer = %q, want InfiniBand", healthy.LinkLayer)
	}
	if len(healthy.Counters) != 5 {
		t.Errorf("port[0] counters = %v, want all 5 zero-valued counters present", healthy.Counters)
	}
	if len(healthy.WarningReasons) != 0 {
		t.Errorf("port[0].warning_reasons = %v, want none", healthy.WarningReasons)
	}

	bad := out.Ports[1]
	if bad.Device != "mlx5_1" || bad.Port != 1 {
		t.Errorf("port[1] identity = %s/%d, want mlx5_1/1", bad.Device, bad.Port)
	}
	if bad.State != "DOWN" {
		t.Errorf("port[1].state = %q, want DOWN", bad.State)
	}
	if bad.PhysState != "Disabled" {
		t.Errorf("port[1].phys_state = %q, want Disabled", bad.PhysState)
	}
	if got := bad.Counters["symbol_error"]; got != 12 {
		t.Errorf("port[1].counters[symbol_error] = %d, want 12", got)
	}
	if got := bad.Counters["link_downed"]; got != 3 {
		t.Errorf("port[1].counters[link_downed] = %d, want 3", got)
	}
	if got := bad.Counters["port_rcv_errors"]; got != 7 {
		t.Errorf("port[1].counters[port_rcv_errors] = %d, want 7", got)
	}
	// Absent counter files must be omitted, not zero-filled.
	if _, present := bad.Counters["link_error_recovery"]; present {
		t.Error("port[1].counters must omit absent file link_error_recovery")
	}
	if _, present := bad.Counters["port_xmit_discards"]; present {
		t.Error("port[1].counters must omit absent file port_xmit_discards")
	}
	if len(bad.Counters) != 3 {
		t.Errorf("port[1] counters len = %d, want 3", len(bad.Counters))
	}

	// state DOWN + phys Disabled + 3 nonzero counters = 5 warning reasons.
	if len(bad.WarningReasons) != 5 {
		t.Errorf("port[1].warning_reasons = %v, want 5 reasons", bad.WarningReasons)
	}
	joined := strings.Join(bad.WarningReasons, "; ")
	for _, want := range []string{"ACTIVE", "LinkUp", "symbol_error", "link_downed", "port_rcv_errors"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warning_reasons %q must mention %q", joined, want)
		}
	}
}

func TestExecuteRoCEPortReportedFactually(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	p := filepath.Join(root, "class", "infiniband", "mlx5_bond_0", "ports", "1")
	writeSysfsFile(t, filepath.Join(p, "state"), "4: ACTIVE\n")
	writeSysfsFile(t, filepath.Join(p, "phys_state"), "5: LinkUp\n")
	writeSysfsFile(t, filepath.Join(p, "rate"), "25 Gb/sec (1X EDR)\n")
	writeSysfsFile(t, filepath.Join(p, "lid"), "0x0\n")
	writeSysfsFile(t, filepath.Join(p, "link_layer"), "Ethernet\n")
	// RoCE ports frequently lack the IB error counter files entirely.

	tool := newWithRoot(root)
	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Errorf("Status = %q, want ok (RoCE port is healthy)", res.Status)
	}

	out := decodeOutput(t, res)
	if len(out.Ports) != 1 {
		t.Fatalf("len(ports) = %d, want 1", len(out.Ports))
	}
	port := out.Ports[0]
	if port.LinkLayer != "Ethernet" {
		t.Errorf("link_layer = %q, want Ethernet", port.LinkLayer)
	}
	if port.Counters != nil {
		t.Errorf("counters = %v, want omitted (nil) when counters dir is absent", port.Counters)
	}
	if len(port.WarningReasons) != 0 {
		t.Errorf("warning_reasons = %v, want none for a healthy RoCE port", port.WarningReasons)
	}
	if out.SystemSummary.ActivePorts != 1 || out.SystemSummary.PortsWithErrors != 0 {
		t.Errorf("summary = %+v, want 1 active / 0 with errors", out.SystemSummary)
	}
}

func TestExecuteEmptyClassDir(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "class", "infiniband"), 0o755); err != nil {
		t.Fatal(err)
	}
	tool := newWithRoot(root)

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Errorf("Status = %q, want ok", res.Status)
	}
	out := decodeOutput(t, res)
	if out.SystemSummary.TotalPorts != 0 || len(out.Ports) != 0 {
		t.Errorf("expected empty result, got %+v", out)
	}
}

func TestExecuteMissingSysfsIsEncapsulatedError(t *testing.T) {
	t.Parallel()
	tool := newWithRoot(filepath.Join(t.TempDir(), "does-not-exist"))

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil (errors must be encapsulated)", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %q, want error", res.Status)
	}
}

func TestExecuteContextCancellation(t *testing.T) {
	t.Parallel()
	tool := newWithRoot(buildFakeIBTree(t))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := tool.Execute(ctx, nil)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil (errors must be encapsulated)", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %q, want error for canceled context", res.Status)
	}
	if !strings.Contains(strings.ToLower(res.Summary), "context") {
		t.Errorf("Summary = %q, want mention of context cancellation", res.Summary)
	}
}

func TestParseStateLine(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want string
	}{
		{"4: ACTIVE", "ACTIVE"},
		{"1: DOWN", "DOWN"},
		{"5: LinkUp", "LinkUp"},
		{"3: Disabled", "Disabled"},
		{"ACTIVE", "ACTIVE"},
		{"  4:  ACTIVE  ", "ACTIVE"},
		{"", ""},
	}
	for _, c := range cases {
		if got := parseStateLine(c.in); got != c.want {
			t.Errorf("parseStateLine(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseLID(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want int64
	}{
		{"0x3", 3},
		{"0x0", 0},
		{"0xffff", 65535},
		{"12", 12},
		{" 0x1c \n", 28},
		{"garbage", 0},
		{"", 0},
	}
	for _, c := range cases {
		if got := parseLID(c.in); got != c.want {
			t.Errorf("parseLID(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestParseRateGbps(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want float64
	}{
		{"100 Gb/sec (4X EDR)", 100},
		{"10 Gb/sec (4X SDR)", 10},
		{"2.5 Gb/sec (1X SDR)", 2.5},
		{"400 Gb/sec (4X NDR)", 400},
		{"unknown", 0},
		{"", 0},
	}
	for _, c := range cases {
		if got := parseRateGbps(c.in); got != c.want {
			t.Errorf("parseRateGbps(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestReadCounters(t *testing.T) {
	t.Parallel()

	t.Run("reads present counters and skips malformed ones", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeSysfsFile(t, filepath.Join(dir, "symbol_error"), "42\n")
		writeSysfsFile(t, filepath.Join(dir, "link_downed"), "not-a-number\n")

		got := readCounters(dir)
		if len(got) != 1 {
			t.Fatalf("readCounters() = %v, want exactly 1 entry", got)
		}
		if got["symbol_error"] != 42 {
			t.Errorf("symbol_error = %d, want 42", got["symbol_error"])
		}
	})

	t.Run("missing dir yields nil map", func(t *testing.T) {
		t.Parallel()
		if got := readCounters(filepath.Join(t.TempDir(), "absent")); got != nil {
			t.Errorf("readCounters() = %v, want nil", got)
		}
	})
}

func TestReadFileTrim(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeSysfsFile(t, filepath.Join(dir, "f"), "  value \n")

	got, ok := readFileTrim(filepath.Join(dir, "f"))
	if !ok || got != "value" {
		t.Errorf("readFileTrim() = (%q, %v), want (value, true)", got, ok)
	}
	if _, ok := readFileTrim(filepath.Join(dir, "missing")); ok {
		t.Error("readFileTrim() on missing file must return ok=false")
	}
}
