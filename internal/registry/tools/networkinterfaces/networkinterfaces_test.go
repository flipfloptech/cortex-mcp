package networkinterfaces

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func TestNetworkInterfacesTool_ContractCompliance(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "get_network_interfaces" {
		t.Errorf("expected Name() == 'get_network_interfaces', got %q", tool.Name())
	}
	if tool.Category() != registry.CategoryNetwork {
		t.Errorf("expected Category() == 'network', got %q", tool.Category())
	}
	if tool.Parameters() != nil {
		t.Errorf("expected Parameters() to be nil")
	}
}

func TestParseSysfsInterfaces(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	sysfs := filepath.Join(tmpDir, "sys", "class", "net")

	// Create lo (should be excluded)
	if err := os.MkdirAll(filepath.Join(sysfs, "lo"), 0755); err != nil {
		t.Fatal(err)
	}
	// Create eth0
	eth0 := filepath.Join(sysfs, "eth0")
	if err := os.MkdirAll(eth0, 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(eth0, "address"), []byte("00:11:22:33:44:55\n"), 0644)
	_ = os.WriteFile(filepath.Join(eth0, "mtu"), []byte("1500\n"), 0644)
	_ = os.WriteFile(filepath.Join(eth0, "operstate"), []byte("up\n"), 0644)
	_ = os.WriteFile(filepath.Join(eth0, "carrier"), []byte("1\n"), 0644)
	_ = os.WriteFile(filepath.Join(eth0, "speed"), []byte("1000\n"), 0644)
	_ = os.WriteFile(filepath.Join(eth0, "duplex"), []byte("full\n"), 0644)

	// Create virbr0
	virbr0 := filepath.Join(sysfs, "virbr0")
	if err := os.MkdirAll(virbr0, 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(virbr0, "address"), []byte("52:54:00:db:d8:6b\n"), 0644)
	_ = os.WriteFile(filepath.Join(virbr0, "mtu"), []byte("1500\n"), 0644)
	_ = os.WriteFile(filepath.Join(virbr0, "operstate"), []byte("down\n"), 0644)
	_ = os.WriteFile(filepath.Join(virbr0, "duplex"), []byte("unknown\n"), 0644)

	interfaces, err := parseSysfsInterfaces(sysfs)
	if err != nil {
		t.Fatalf("unexpected error parsing sysfs: %v", err)
	}

	if len(interfaces) != 2 {
		t.Fatalf("expected 2 interfaces, got %d: %+v", len(interfaces), interfaces)
	}

	var eth0Info *NetworkInterface
	var virbr0Info *NetworkInterface
	for i := range interfaces {
		if interfaces[i].Name == "eth0" {
			eth0Info = &interfaces[i]
		} else if interfaces[i].Name == "virbr0" {
			virbr0Info = &interfaces[i]
		}
	}

	if eth0Info == nil {
		t.Fatal("eth0 missing in results")
	}
	if eth0Info.MAC != "00:11:22:33:44:55" || eth0Info.MTU != 1500 || eth0Info.OperState != "up" {
		t.Errorf("unexpected eth0 stats: %+v", eth0Info)
	}
	if eth0Info.Carrier == nil || *eth0Info.Carrier != 1 {
		t.Errorf("expected eth0 carrier to be 1, got %v", eth0Info.Carrier)
	}
	if eth0Info.SpeedMbps == nil || *eth0Info.SpeedMbps != 1000 {
		t.Errorf("expected eth0 speed to be 1000, got %v", eth0Info.SpeedMbps)
	}
	if eth0Info.Duplex != "full" {
		t.Errorf("expected eth0 duplex full, got %s", eth0Info.Duplex)
	}

	if virbr0Info == nil {
		t.Fatal("virbr0 missing in results")
	}
	if virbr0Info.Carrier != nil {
		t.Errorf("expected virbr0 carrier to be nil, got %v", virbr0Info.Carrier)
	}
	if virbr0Info.SpeedMbps != nil {
		t.Errorf("expected virbr0 speed to be nil, got %v", virbr0Info.SpeedMbps)
	}
}

func TestParseIpAddrJSON(t *testing.T) {
	t.Parallel()

	input := `[
		{
			"ifindex": 1,
			"ifname": "lo",
			"flags": ["LOOPBACK", "UP"],
			"mtu": 65536,
			"operstate": "UNKNOWN",
			"address": "00:00:00:00:00:00"
		},
		{
			"ifindex": 2,
			"ifname": "eth0",
			"flags": ["BROADCAST", "MULTICAST", "UP", "LOWER_UP"],
			"mtu": 1500,
			"operstate": "UP",
			"address": "00:11:22:33:44:55",
			"addr_info": [
				{
					"family": "inet",
					"local": "192.168.1.50",
					"prefixlen": 24,
					"scope": "global"
				},
				{
					"family": "inet6",
					"local": "fe80::1",
					"prefixlen": 64,
					"scope": "link"
				}
			]
		}
	]`

	interfaces, err := parseIpAddrJSON([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(interfaces) != 1 {
		t.Fatalf("expected 1 interface (lo excluded), got %d: %+v", len(interfaces), interfaces)
	}

	eth0 := interfaces[0]
	if eth0.Name != "eth0" || eth0.Index != 2 || eth0.MTU != 1500 || eth0.OperState != "up" || eth0.MAC != "00:11:22:33:44:55" {
		t.Errorf("unexpected interface metadata: %+v", eth0)
	}
	if len(eth0.IPs) != 2 {
		t.Fatalf("expected 2 IP addresses, got %d", len(eth0.IPs))
	}
	if eth0.IPs[0].Address != "192.168.1.50" || eth0.IPs[0].PrefixLen != 24 || eth0.IPs[0].Family != "inet" {
		t.Errorf("unexpected IPv4 details: %+v", eth0.IPs[0])
	}
	if eth0.IPs[1].Address != "fe80::1" || eth0.IPs[1].PrefixLen != 64 || eth0.IPs[1].Family != "inet6" {
		t.Errorf("unexpected IPv6 details: %+v", eth0.IPs[1])
	}
}

func TestNetworkInterfacesTool_Execute(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	sysfs := filepath.Join(tmpDir, "sys", "class", "net")
	if err := os.MkdirAll(filepath.Join(sysfs, "eth0"), 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(sysfs, "eth0", "address"), []byte("00:11:22:33:44:55\n"), 0644)
	_ = os.WriteFile(filepath.Join(sysfs, "eth0", "mtu"), []byte("1500\n"), 0644)
	_ = os.WriteFile(filepath.Join(sysfs, "eth0", "operstate"), []byte("up\n"), 0644)

	tool := &NetworkInterfacesTool{
		sysfsRoot: sysfs,
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			mockJSON := `[
				{
					"ifname": "eth0",
					"addr_info": [
						{"family": "inet", "local": "192.168.1.100", "prefixlen": 24}
					]
				}
			]`
			return []byte(mockJSON), nil
		},
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.Status != registry.StatusOK {
		t.Fatalf("expected status OK, got %s", res.Status)
	}

	var data NetworkInterfacesData
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("failed to unmarshal output: %v", err)
	}

	if len(data.Interfaces) != 1 {
		t.Fatalf("expected 1 interface, got %d", len(data.Interfaces))
	}

	eth0 := data.Interfaces[0]
	if eth0.Name != "eth0" || eth0.MAC != "00:11:22:33:44:55" || eth0.MTU != 1500 || eth0.OperState != "up" {
		t.Errorf("unexpected eth0 properties: %+v", eth0)
	}
	if len(eth0.IPs) != 1 || eth0.IPs[0].Address != "192.168.1.100" {
		t.Errorf("unexpected IPs: %+v", eth0.IPs)
	}
}
