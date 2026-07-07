package arpneighbors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

const procArpContent = `IP address       HW type     Flags       HW address            Mask     Device
192.168.1.1      0x1         0x2         aa:bb:cc:dd:ee:01     *        eth0
192.168.1.50     0x1         0x0         00:00:00:00:00:00     *        eth0
192.168.1.99     0x1         0x6         aa:bb:cc:dd:ee:63     *        eth1
`

const ipNeighJSON = `[
	{"dst":"192.168.1.1","dev":"eth0","lladdr":"aa:bb:cc:dd:ee:01","state":["STALE"]},
	{"dst":"fe80::1","dev":"eth0","lladdr":"aa:bb:cc:dd:ee:99","state":["REACHABLE"]},
	{"dst":"192.168.1.77","dev":"eth0","state":["FAILED"]}
]`

// writeArpFile creates <root>/net/arp with the given content and returns root.
func writeArpFile(t testing.TB, content string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "net")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "arp"), []byte(content), 0o644); err != nil {
		t.Fatalf("write arp file: %v", err)
	}
	return root
}

// newProcfsOnlyTool returns a Tool whose 'ip' binary lookup always fails.
func newProcfsOnlyTool(root string) *Tool {
	tool := New()
	tool.procfsRoot = root
	tool.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	tool.execCommand = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("must not be called when 'ip' is unavailable")
	}
	return tool
}

// newEnrichedTool returns a Tool whose 'ip -j neigh' returns the given JSON.
func newEnrichedTool(root string, ipOutput []byte, execErr error) *Tool {
	tool := New()
	tool.procfsRoot = root
	tool.lookPath = func(string) (string, error) { return "/fake/bin/ip", nil }
	tool.execCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if execErr != nil {
			return nil, execErr
		}
		if name != "ip" || strings.Join(args, " ") != "-j neigh" {
			return nil, fmt.Errorf("unexpected command: %s %v", name, args)
		}
		return ipOutput, nil
	}
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

	if tool.Name() != "get_arp_neighbors" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "get_arp_neighbors")
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
	for _, ref := range []string{"/proc/net/arp", "ip -j neigh"} {
		if !strings.Contains(tool.Help(), ref) {
			t.Errorf("Help() must reference data source %q", ref)
		}
	}
}

func TestIsSupported(t *testing.T) {
	t.Parallel()

	t.Run("supported via procfs", func(t *testing.T) {
		t.Parallel()
		tool := newProcfsOnlyTool(writeArpFile(t, procArpContent))
		if ok, reason := tool.IsSupported(); !ok {
			t.Errorf("IsSupported() = false (%q), want true", reason)
		}
	})

	t.Run("supported via ip binary when procfs missing", func(t *testing.T) {
		t.Parallel()
		tool := newEnrichedTool(t.TempDir(), []byte("[]"), nil)
		if ok, reason := tool.IsSupported(); !ok {
			t.Errorf("IsSupported() = false (%q), want true", reason)
		}
	})

	t.Run("unsupported when both sources missing", func(t *testing.T) {
		t.Parallel()
		tool := newProcfsOnlyTool(t.TempDir())
		ok, reason := tool.IsSupported()
		if ok {
			t.Error("IsSupported() = true, want false")
		}
		if !strings.Contains(reason, "/proc/net/arp") || !strings.Contains(reason, "ip") {
			t.Errorf("reason = %q, want mention of both missing sources", reason)
		}
	})
}

func TestExecuteProcfsOnly(t *testing.T) {
	t.Parallel()
	tool := newProcfsOnlyTool(writeArpFile(t, procArpContent))

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil (errors must be encapsulated)", err)
	}
	if res.Status != registry.StatusOK {
		t.Errorf("Status = %q, want ok (no FAILED entries)", res.Status)
	}

	out := decodeOutput(t, res)
	if out.TotalEntries != 3 {
		t.Errorf("total_entries = %d, want 3", out.TotalEntries)
	}
	if out.IPv6Included {
		t.Error("ipv6_included = true, want false without the 'ip' binary")
	}
	if out.Note == "" {
		t.Error("note must explain the IPv4-only degradation")
	}
	if out.CountsByState["reachable"] != 1 || out.CountsByState["incomplete"] != 1 || out.CountsByState["permanent"] != 1 {
		t.Errorf("counts_by_state = %v, want 1 reachable / 1 incomplete / 1 permanent", out.CountsByState)
	}
	if len(out.Entries) != 3 {
		t.Fatalf("len(entries) = %d, want 3", len(out.Entries))
	}
	// Incomplete entries are prioritized ahead of healthy states.
	if out.Entries[0].IP != "192.168.1.50" || out.Entries[0].State != "incomplete" {
		t.Errorf("entries[0] = %+v, want the incomplete 192.168.1.50 first", out.Entries[0])
	}
	if out.Entries[0].MAC != "" {
		t.Errorf("incomplete entry MAC = %q, want empty (all-zero MAC decoded away)", out.Entries[0].MAC)
	}
	for _, e := range out.Entries {
		if e.Family != "ipv4" {
			t.Errorf("entry %s family = %q, want ipv4", e.IP, e.Family)
		}
	}
	if len(out.Failed) != 0 || len(out.WarningReasons) != 0 {
		t.Errorf("expected no failures, got failed=%v warnings=%v", out.Failed, out.WarningReasons)
	}
}

func TestExecuteWithIPEnrichment(t *testing.T) {
	t.Parallel()
	tool := newEnrichedTool(writeArpFile(t, procArpContent), []byte(ipNeighJSON), nil)

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if res.Status != registry.StatusWarning {
		t.Errorf("Status = %q, want warning (one FAILED neighbor)", res.Status)
	}

	out := decodeOutput(t, res)
	// 3 procfs entries + fe80::1 + 192.168.1.77 (192.168.1.1 merges).
	if out.TotalEntries != 5 {
		t.Errorf("total_entries = %d, want 5", out.TotalEntries)
	}
	if !out.IPv6Included {
		t.Error("ipv6_included = false, want true when 'ip -j neigh' succeeds")
	}

	byIP := map[string]Neighbor{}
	for _, e := range out.Entries {
		byIP[e.IP] = e
	}
	if got := byIP["192.168.1.1"]; got.State != "stale" {
		t.Errorf("192.168.1.1 state = %q, want granular 'stale' from ip neigh", got.State)
	}
	if got := byIP["fe80::1"]; got.Family != "ipv6" || got.State != "reachable" {
		t.Errorf("fe80::1 = %+v, want ipv6/reachable", got)
	}
	if out.Entries[0].State != "failed" {
		t.Errorf("entries[0].state = %q, want failed entries prioritized first", out.Entries[0].State)
	}
	if len(out.Failed) != 1 || out.Failed[0].IP != "192.168.1.77" {
		t.Errorf("failed = %+v, want exactly 192.168.1.77", out.Failed)
	}
	if len(out.WarningReasons) == 0 || !strings.Contains(strings.ToUpper(strings.Join(out.WarningReasons, " ")), "FAILED") {
		t.Errorf("warning_reasons = %v, want FAILED mentioned", out.WarningReasons)
	}
	if out.CountsByState["failed"] != 1 || out.CountsByState["stale"] != 1 {
		t.Errorf("counts_by_state = %v, want failed=1 stale=1", out.CountsByState)
	}
}

func TestExecuteIPCommandFailureDegradesToProcfs(t *testing.T) {
	t.Parallel()
	tool := newEnrichedTool(writeArpFile(t, procArpContent), nil, errors.New("exec blew up"))

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Errorf("Status = %q, want ok", res.Status)
	}
	out := decodeOutput(t, res)
	if out.IPv6Included {
		t.Error("ipv6_included = true, want false when ip neigh fails")
	}
	if out.TotalEntries != 3 {
		t.Errorf("total_entries = %d, want 3 procfs entries", out.TotalEntries)
	}
	if out.Note == "" {
		t.Error("note must record the ip neigh degradation")
	}
}

func TestExecuteCapsEntriesAtFifty(t *testing.T) {
	t.Parallel()

	var sb strings.Builder
	sb.WriteString("IP address       HW type     Flags       HW address            Mask     Device\n")
	for i := 0; i < 60; i++ {
		sb.WriteString(fmt.Sprintf("10.0.%d.%d      0x1         0x2         aa:bb:cc:dd:%02x:%02x     *        eth0\n", i/256, i%256, i/256, i%256))
	}
	failedJSON := `[
		{"dst":"10.9.9.1","dev":"eth0","state":["FAILED"]},
		{"dst":"10.9.9.2","dev":"eth0","state":["FAILED"]}
	]`
	tool := newEnrichedTool(writeArpFile(t, sb.String()), []byte(failedJSON), nil)

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	out := decodeOutput(t, res)
	if out.TotalEntries != 62 {
		t.Errorf("total_entries = %d, want 62", out.TotalEntries)
	}
	if len(out.Entries) != 50 {
		t.Errorf("len(entries) = %d, want capped at 50", len(out.Entries))
	}
	if !out.Truncated {
		t.Error("truncated = false, want true")
	}
	if out.Entries[0].State != "failed" || out.Entries[1].State != "failed" {
		t.Errorf("entries[0..1] = %+v, %+v — failed entries must come first", out.Entries[0], out.Entries[1])
	}
	// The failed list is never truncated.
	if len(out.Failed) != 2 {
		t.Errorf("len(failed) = %d, want 2 (listed fully)", len(out.Failed))
	}
	if res.Status != registry.StatusWarning {
		t.Errorf("Status = %q, want warning", res.Status)
	}
}

func TestExecuteNoSourcesIsEncapsulatedError(t *testing.T) {
	t.Parallel()
	tool := newProcfsOnlyTool(t.TempDir())

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil (errors must be encapsulated)", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %q, want error when no data source is usable", res.Status)
	}
}

func TestExecuteContextCancellation(t *testing.T) {
	t.Parallel()
	tool := newProcfsOnlyTool(writeArpFile(t, procArpContent))

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

func TestFlagsToState(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want string
	}{
		{"0x0", "incomplete"},
		{"0x2", "reachable"},
		{"0x6", "permanent"},
		{"0x4", "permanent"},
		{"0x02", "reachable"},
		{"junk", "unknown"},
		{"", "unknown"},
	}
	for _, c := range cases {
		if got := flagsToState(c.in); got != c.want {
			t.Errorf("flagsToState(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseProcArp(t *testing.T) {
	t.Parallel()

	t.Run("parses entries and skips header", func(t *testing.T) {
		t.Parallel()
		got := parseProcArp([]byte(procArpContent))
		if len(got) != 3 {
			t.Fatalf("len = %d, want 3", len(got))
		}
		want := Neighbor{IP: "192.168.1.1", Device: "eth0", MAC: "aa:bb:cc:dd:ee:01", State: "reachable", Family: "ipv4"}
		if got[0] != want {
			t.Errorf("entry[0] = %+v, want %+v", got[0], want)
		}
		if got[1].MAC != "" {
			t.Errorf("incomplete entry MAC = %q, want blanked", got[1].MAC)
		}
	})

	t.Run("skips malformed lines", func(t *testing.T) {
		t.Parallel()
		got := parseProcArp([]byte("IP address  HW type Flags\nshort line\n\n192.168.1.2 0x1 0x2 aa:bb:cc:dd:ee:02 * eth0\n"))
		if len(got) != 1 || got[0].IP != "192.168.1.2" {
			t.Errorf("got %+v, want single 192.168.1.2 entry", got)
		}
	})

	t.Run("empty content", func(t *testing.T) {
		t.Parallel()
		if got := parseProcArp(nil); len(got) != 0 {
			t.Errorf("got %+v, want empty", got)
		}
	})
}

func TestParseIPNeighJSON(t *testing.T) {
	t.Parallel()

	t.Run("valid payload", func(t *testing.T) {
		t.Parallel()
		got, err := parseIPNeighJSON([]byte(ipNeighJSON))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("len = %d, want 3", len(got))
		}
		if got[0].State != "stale" || got[1].Family != "ipv6" || got[2].State != "failed" {
			t.Errorf("unexpected entries: %+v", got)
		}
	})

	t.Run("entry without state decodes to unknown", func(t *testing.T) {
		t.Parallel()
		got, err := parseIPNeighJSON([]byte(`[{"dst":"10.0.0.1","dev":"eth0"}]`))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 || got[0].State != "unknown" {
			t.Errorf("got %+v, want single entry with state unknown", got)
		}
	})

	t.Run("malformed JSON returns error", func(t *testing.T) {
		t.Parallel()
		if _, err := parseIPNeighJSON([]byte("not json")); err == nil {
			t.Error("expected error for malformed JSON")
		}
	})

	t.Run("entries without dst are skipped", func(t *testing.T) {
		t.Parallel()
		got, err := parseIPNeighJSON([]byte(`[{"dev":"eth0","state":["STALE"]}]`))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got %+v, want empty", got)
		}
	})
}

func TestMergeNeighbors(t *testing.T) {
	t.Parallel()

	base := []Neighbor{
		{IP: "10.0.0.1", Device: "eth0", MAC: "aa:aa:aa:aa:aa:01", State: "reachable", Family: "ipv4"},
		{IP: "10.0.0.2", Device: "eth0", MAC: "", State: "incomplete", Family: "ipv4"},
	}
	extra := []Neighbor{
		{IP: "10.0.0.1", Device: "eth0", MAC: "aa:aa:aa:aa:aa:01", State: "stale", Family: "ipv4"},
		{IP: "10.0.0.2", Device: "eth0", MAC: "aa:aa:aa:aa:aa:02", State: "delay", Family: "ipv4"},
		{IP: "fe80::9", Device: "eth1", MAC: "aa:aa:aa:aa:aa:09", State: "reachable", Family: "ipv6"},
	}

	got := mergeNeighbors(base, extra)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].State != "stale" {
		t.Errorf("merged state = %q, want granular 'stale' to win", got[0].State)
	}
	if got[1].State != "delay" || got[1].MAC != "aa:aa:aa:aa:aa:02" {
		t.Errorf("merged entry = %+v, want state delay and MAC filled in", got[1])
	}
	if got[2].IP != "fe80::9" {
		t.Errorf("appended entry = %+v, want fe80::9", got[2])
	}

	t.Run("nil base", func(t *testing.T) {
		t.Parallel()
		got := mergeNeighbors(nil, extra)
		if len(got) != 3 {
			t.Errorf("len = %d, want 3", len(got))
		}
	})
}

func TestStatePriority(t *testing.T) {
	t.Parallel()

	if !(statePriority("failed") < statePriority("incomplete")) {
		t.Error("failed must sort before incomplete")
	}
	if !(statePriority("incomplete") < statePriority("reachable")) {
		t.Error("incomplete must sort before reachable")
	}
	if statePriority("reachable") != statePriority("stale") {
		t.Error("healthy states must share the same priority")
	}
}
