package bondstatus

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

const bond8023adContent = `Ethernet Channel Bonding Driver: v5.15.0-generic

Bonding Mode: IEEE 802.3ad Dynamic link aggregation
Transmit Hash Policy: layer3+4 (1)
MII Status: up
MII Polling Interval (ms): 100
Up Delay (ms): 0
Down Delay (ms): 0
Peer Notification Delay (ms): 0

802.3ad info
LACP active: on
LACP rate: fast
Min links: 0
Aggregator selection policy (ad_select): stable
System priority: 65535
System MAC address: 52:54:00:12:34:56
Active Aggregator Info:
	Aggregator ID: 1
	Number of ports: 2
	Actor Key: 15
	Partner Key: 32768
	Partner Mac Address: 00:1b:21:aa:bb:cc

Slave Interface: ens1f0
MII Status: up
Speed: 25000 Mbps
Duplex: full
Link Failure Count: 0
Permanent HW addr: 3c:fd:fe:aa:bb:01
Slave queue ID: 0
Aggregator ID: 1
Actor Churn State: none
Partner Churn State: none
Actor Churned Count: 0
Partner Churned Count: 0
details actor lacp pdu:
    system priority: 65535
    system mac address: 52:54:00:12:34:56
    port key: 15
    port priority: 255
    port number: 1
    port state: 63
details partner lacp pdu:
    system priority: 65535
    system mac address: 00:1b:21:aa:bb:cc
    oper key: 32768
    port priority: 255
    port number: 1
    port state: 63

Slave Interface: ens1f1
MII Status: up
Speed: 25000 Mbps
Duplex: full
Link Failure Count: 0
Permanent HW addr: 3c:fd:fe:aa:bb:02
Slave queue ID: 0
Aggregator ID: 1
Actor Churn State: none
Partner Churn State: none
Actor Churned Count: 0
Partner Churned Count: 0
`

const bondActiveBackupContent = `Ethernet Channel Bonding Driver: v5.15.0-generic

Bonding Mode: fault-tolerance (active-backup)
Primary Slave: None
Currently Active Slave: eth0
MII Status: up
MII Polling Interval (ms): 100
Up Delay (ms): 0
Down Delay (ms): 0

Slave Interface: eth0
MII Status: up
Speed: 1000 Mbps
Duplex: full
Link Failure Count: 0
Permanent HW addr: aa:bb:cc:dd:ee:01
Slave queue ID: 0

Slave Interface: eth1
MII Status: down
Speed: Unknown
Duplex: Unknown
Link Failure Count: 3
Permanent HW addr: aa:bb:cc:dd:ee:02
Slave queue ID: 0
`

// buildFakeBondingTree writes bond proc files into a fake procfs root and
// returns the root path.
func buildFakeBondingTree(t testing.TB, bonds map[string]string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "net", "bonding")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	for name, content := range bonds {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write bond file %s: %v", name, err)
		}
	}
	return root
}

func newWithRoot(root string) *Tool {
	tool := New()
	tool.procfsRoot = root
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

	if tool.Name() != "get_bond_status" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "get_bond_status")
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
	if !strings.Contains(tool.Help(), "/proc/net/bonding") {
		t.Errorf("Help() must reference the /proc/net/bonding data source, got %q", tool.Help())
	}
}

func TestIsSupported(t *testing.T) {
	t.Parallel()

	t.Run("supported when bonding dir exists", func(t *testing.T) {
		t.Parallel()
		root := buildFakeBondingTree(t, nil)
		tool := newWithRoot(root)
		if ok, reason := tool.IsSupported(); !ok {
			t.Errorf("IsSupported() = false (%q), want true", reason)
		}
	})

	t.Run("unsupported when bonding module not loaded", func(t *testing.T) {
		t.Parallel()
		tool := newWithRoot(t.TempDir())
		ok, reason := tool.IsSupported()
		if ok {
			t.Error("IsSupported() = true, want false")
		}
		if !strings.Contains(reason, "bonding") {
			t.Errorf("reason = %q, want mention of bonding module", reason)
		}
	})
}

func TestExecuteHappyPath(t *testing.T) {
	t.Parallel()
	root := buildFakeBondingTree(t, map[string]string{
		"bond0": bond8023adContent,
		"bond1": bondActiveBackupContent,
	})
	tool := newWithRoot(root)

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil (errors must be encapsulated)", err)
	}
	if res.Status != registry.StatusWarning {
		t.Errorf("Status = %q, want warning (bond1 has a down slave with link failures)", res.Status)
	}

	out := decodeOutput(t, res)
	if len(out.Bonds) != 2 {
		t.Fatalf("len(bonds) = %d, want 2", len(out.Bonds))
	}

	// ReadDir order is lexical: bond0 first.
	b0 := out.Bonds[0]
	if b0.Name != "bond0" {
		t.Errorf("bonds[0].name = %q, want bond0", b0.Name)
	}
	if b0.Mode != "IEEE 802.3ad Dynamic link aggregation" {
		t.Errorf("bonds[0].mode = %q", b0.Mode)
	}
	if b0.MIIStatus != "up" {
		t.Errorf("bonds[0].mii_status = %q, want up", b0.MIIStatus)
	}
	if b0.ActiveSlave != "" {
		t.Errorf("bonds[0].active_slave = %q, want empty (802.3ad has no active slave line)", b0.ActiveSlave)
	}
	if b0.LACPRate != "fast" {
		t.Errorf("bonds[0].lacp_rate = %q, want fast", b0.LACPRate)
	}
	if b0.PartnerMAC != "00:1b:21:aa:bb:cc" {
		t.Errorf("bonds[0].partner_mac = %q, want 00:1b:21:aa:bb:cc", b0.PartnerMAC)
	}
	if len(b0.Slaves) != 2 {
		t.Fatalf("bonds[0] slaves = %d, want 2", len(b0.Slaves))
	}
	s0 := b0.Slaves[0]
	if s0.Name != "ens1f0" || s0.MIIStatus != "up" || s0.SpeedMbps != 25000 || s0.Duplex != "full" || s0.LinkFailureCount != 0 {
		t.Errorf("unexpected slave 0: %+v", s0)
	}
	if len(b0.WarningReasons) != 0 {
		t.Errorf("bonds[0].warning_reasons = %v, want none", b0.WarningReasons)
	}

	b1 := out.Bonds[1]
	if b1.Name != "bond1" {
		t.Errorf("bonds[1].name = %q, want bond1", b1.Name)
	}
	if b1.Mode != "fault-tolerance (active-backup)" {
		t.Errorf("bonds[1].mode = %q", b1.Mode)
	}
	if b1.ActiveSlave != "eth0" {
		t.Errorf("bonds[1].active_slave = %q, want eth0", b1.ActiveSlave)
	}
	if b1.LACPRate != "" || b1.PartnerMAC != "" {
		t.Errorf("bonds[1] must not carry 802.3ad fields, got lacp_rate=%q partner_mac=%q", b1.LACPRate, b1.PartnerMAC)
	}
	if len(b1.Slaves) != 2 {
		t.Fatalf("bonds[1] slaves = %d, want 2", len(b1.Slaves))
	}
	s1 := b1.Slaves[1]
	if s1.Name != "eth1" || s1.MIIStatus != "down" || s1.SpeedMbps != 0 || s1.Duplex != "Unknown" || s1.LinkFailureCount != 3 {
		t.Errorf("unexpected slave eth1: %+v", s1)
	}
	// down slave + nonzero link failures = 2 warnings
	if len(b1.WarningReasons) != 2 {
		t.Errorf("bonds[1].warning_reasons = %v, want 2 reasons", b1.WarningReasons)
	}
	joined := strings.Join(b1.WarningReasons, "; ")
	if !strings.Contains(joined, "eth1") {
		t.Errorf("warning_reasons %q must name the failing slave eth1", joined)
	}

	sum := out.SystemSummary
	if sum.TotalBonds != 2 || sum.BondsUp != 2 || sum.TotalSlaves != 4 || sum.SlavesUp != 3 || sum.BondsWithWarnings != 1 {
		t.Errorf("unexpected system_summary: %+v", sum)
	}
}

func TestExecuteAllHealthyIsOK(t *testing.T) {
	t.Parallel()
	root := buildFakeBondingTree(t, map[string]string{"bond0": bond8023adContent})
	tool := newWithRoot(root)

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Errorf("Status = %q, want ok", res.Status)
	}
}

func TestExecuteBondMIIDownWarns(t *testing.T) {
	t.Parallel()
	content := strings.Replace(bondActiveBackupContent, "Currently Active Slave: eth0\nMII Status: up", "Currently Active Slave: None\nMII Status: down", 1)
	root := buildFakeBondingTree(t, map[string]string{"bond0": content})
	tool := newWithRoot(root)

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if res.Status != registry.StatusWarning {
		t.Errorf("Status = %q, want warning for bond-level MII down", res.Status)
	}
	out := decodeOutput(t, res)
	if out.Bonds[0].MIIStatus != "down" {
		t.Errorf("bond mii_status = %q, want down", out.Bonds[0].MIIStatus)
	}
	if out.SystemSummary.BondsUp != 0 {
		t.Errorf("bonds_up = %d, want 0", out.SystemSummary.BondsUp)
	}
	joined := strings.Join(out.Bonds[0].WarningReasons, "; ")
	if !strings.Contains(joined, "MII") {
		t.Errorf("warning_reasons %q must mention bond MII status", joined)
	}
}

func TestExecuteEmptyBondingDir(t *testing.T) {
	t.Parallel()
	root := buildFakeBondingTree(t, nil)
	tool := newWithRoot(root)

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Errorf("Status = %q, want ok for empty bonding dir", res.Status)
	}
	out := decodeOutput(t, res)
	if out.Bonds == nil || len(out.Bonds) != 0 {
		t.Errorf("bonds = %#v, want empty (non-nil) slice", out.Bonds)
	}
	if out.SystemSummary.TotalBonds != 0 {
		t.Errorf("total_bonds = %d, want 0", out.SystemSummary.TotalBonds)
	}
}

func TestExecuteMissingBondingDirIsEncapsulatedError(t *testing.T) {
	t.Parallel()
	tool := newWithRoot(t.TempDir())

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
	root := buildFakeBondingTree(t, map[string]string{"bond0": bond8023adContent})
	tool := newWithRoot(root)

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

func TestParseBondFile(t *testing.T) {
	t.Parallel()

	t.Run("802.3ad bond", func(t *testing.T) {
		t.Parallel()
		bond := parseBondFile("bond0", []byte(bond8023adContent))
		if bond.Name != "bond0" {
			t.Errorf("name = %q", bond.Name)
		}
		if bond.Mode != "IEEE 802.3ad Dynamic link aggregation" {
			t.Errorf("mode = %q", bond.Mode)
		}
		if bond.MIIStatus != "up" {
			t.Errorf("mii_status = %q", bond.MIIStatus)
		}
		if bond.LACPRate != "fast" {
			t.Errorf("lacp_rate = %q", bond.LACPRate)
		}
		if bond.PartnerMAC != "00:1b:21:aa:bb:cc" {
			t.Errorf("partner_mac = %q", bond.PartnerMAC)
		}
		if len(bond.Slaves) != 2 {
			t.Fatalf("slaves = %d, want 2", len(bond.Slaves))
		}
		// The per-slave "system mac address" lines inside LACP PDU details
		// must NOT leak into slave or bond fields.
		if bond.Slaves[0].Name != "ens1f0" || bond.Slaves[1].Name != "ens1f1" {
			t.Errorf("slave names = %q, %q", bond.Slaves[0].Name, bond.Slaves[1].Name)
		}
		if len(bond.WarningReasons) != 0 {
			t.Errorf("warning_reasons = %v, want none", bond.WarningReasons)
		}
	})

	t.Run("active-backup bond with degraded slave", func(t *testing.T) {
		t.Parallel()
		bond := parseBondFile("bond1", []byte(bondActiveBackupContent))
		if bond.ActiveSlave != "eth0" {
			t.Errorf("active_slave = %q, want eth0", bond.ActiveSlave)
		}
		if len(bond.Slaves) != 2 {
			t.Fatalf("slaves = %d, want 2", len(bond.Slaves))
		}
		if bond.Slaves[0].SpeedMbps != 1000 {
			t.Errorf("slave 0 speed = %d, want 1000", bond.Slaves[0].SpeedMbps)
		}
		if bond.Slaves[1].SpeedMbps != 0 {
			t.Errorf("slave 1 speed = %d, want 0 for Unknown", bond.Slaves[1].SpeedMbps)
		}
		if bond.Slaves[1].LinkFailureCount != 3 {
			t.Errorf("slave 1 link_failure_count = %d, want 3", bond.Slaves[1].LinkFailureCount)
		}
		if len(bond.WarningReasons) != 2 {
			t.Errorf("warning_reasons = %v, want 2", bond.WarningReasons)
		}
	})

	t.Run("garbage content degrades to zero values without failing", func(t *testing.T) {
		t.Parallel()
		bond := parseBondFile("bondX", []byte("total garbage\nwith: random colons\n\x00\xff"))
		if bond.Name != "bondX" {
			t.Errorf("name = %q, want bondX", bond.Name)
		}
		if bond.Mode != "" || bond.ActiveSlave != "" || len(bond.Slaves) != 0 {
			t.Errorf("expected zero-valued bond, got %+v", bond)
		}
	})

	t.Run("empty content", func(t *testing.T) {
		t.Parallel()
		bond := parseBondFile("bondY", nil)
		if bond.Name != "bondY" || len(bond.Slaves) != 0 {
			t.Errorf("unexpected bond: %+v", bond)
		}
	})
}

func TestParseSpeedMbps(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want int64
	}{
		{"25000 Mbps", 25000},
		{"1000 Mbps", 1000},
		{"100", 100},
		{"Unknown", 0},
		{"", 0},
		{"-1 Mbps", 0},
	}
	for _, c := range cases {
		if got := parseSpeedMbps(c.in); got != c.want {
			t.Errorf("parseSpeedMbps(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
