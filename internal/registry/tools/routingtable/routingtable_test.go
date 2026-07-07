package routingtable

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func TestRoutingTableTool_ContractCompliance(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "get_routing_table" {
		t.Errorf("expected Name() == 'get_routing_table', got %q", tool.Name())
	}
	if tool.Category() != registry.CategoryNetwork {
		t.Errorf("expected Category() == 'network', got %q", tool.Category())
	}
	if tool.Parameters() != nil {
		t.Errorf("expected Parameters() to be nil")
	}
}

func TestParseIPv4Hex(t *testing.T) {
	t.Parallel()

	cases := []struct {
		hexStr string
		want   string
	}{
		{"00000000", "0.0.0.0"},
		{"0156A8C0", "192.168.86.1"},
		{"0056A8C0", "192.168.86.0"},
		{"00FFFFFF", "255.255.255.0"},
	}

	for _, c := range cases {
		got, err := parseIPv4Hex(c.hexStr)
		if err != nil {
			t.Errorf("parseIPv4Hex(%q) error: %v", c.hexStr, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseIPv4Hex(%q) = %q, want %q", c.hexStr, got, c.want)
		}
	}
}

func TestParseIPv6Hex(t *testing.T) {
	t.Parallel()

	cases := []struct {
		hexStr string
		want   string
	}{
		{"00000000000000000000000000000000", "::"},
		{"fdbad78e093e76b80000000000000000", "fdba:d78e:93e:76b8::"},
		{"fe80000000000000b623a2fffe6d03a7", "fe80::b623:a2ff:fe6d:3a7"},
	}

	for _, c := range cases {
		got, err := parseIPv6Hex(c.hexStr)
		if err != nil {
			t.Errorf("parseIPv6Hex(%q) error: %v", c.hexStr, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseIPv6Hex(%q) = %q, want %q", c.hexStr, got, c.want)
		}
	}
}

func TestParseIPv4Routes(t *testing.T) {
	t.Parallel()

	input := `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
wlan0	00000000	0156A8C0	0003	0	0	600	00000000	0	0	0
wlan0	0056A8C0	00000000	0001	0	0	600	00FFFFFF	0	0	0
`
	routes, err := parseIPv4Routes([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(routes) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(routes))
	}

	r0 := routes[0]
	if r0.Interface != "wlan0" || r0.Destination != "0.0.0.0" || r0.Gateway != "192.168.86.1" || r0.Mask != "0.0.0.0" || r0.PrefixLen != 0 || r0.Metric != 600 || r0.Flags != "UP|GATEWAY" || r0.Family != "ipv4" {
		t.Errorf("unexpected route 0: %+v", r0)
	}

	r1 := routes[1]
	if r1.Interface != "wlan0" || r1.Destination != "192.168.86.0" || r1.Gateway != "0.0.0.0" || r1.Mask != "255.255.255.0" || r1.PrefixLen != 24 || r1.Metric != 600 || r1.Flags != "UP" || r1.Family != "ipv4" {
		t.Errorf("unexpected route 1: %+v", r1)
	}
}

func TestParseIPv6Routes(t *testing.T) {
	t.Parallel()

	input := `fdbad78e093e76b80000000000000000 40 00000000000000000000000000000000 00 00000000000000000000000000000000 00000258 00000002 00000000 00000001    wlan0
fdc3bf47eaee00010000000000000000 40 00000000000000000000000000000000 00 fe80000000000000b623a2fffe6d03a7 0000025d 00000001 00000000 00000003    wlan0
`
	routes, err := parseIPv6Routes([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(routes) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(routes))
	}

	r0 := routes[0]
	if r0.Interface != "wlan0" || r0.Destination != "fdba:d78e:93e:76b8::" || r0.Gateway != "::" || r0.PrefixLen != 64 || r0.Metric != 600 || r0.Flags != "UP" || r0.Family != "ipv6" {
		t.Errorf("unexpected route 0: %+v", r0)
	}

	r1 := routes[1]
	if r1.Interface != "wlan0" || r1.Destination != "fdc3:bf47:eaee:1::" || r1.Gateway != "fe80::b623:a2ff:fe6d:3a7" || r1.PrefixLen != 64 || r1.Metric != 605 || r1.Flags != "UP|GATEWAY" || r1.Family != "ipv6" {
		t.Errorf("unexpected route 1: %+v", r1)
	}
}

func TestParseIpRouteJSON(t *testing.T) {
	t.Parallel()

	inputv4 := `[
		{"dst":"default","gateway":"192.168.86.1","dev":"wlan0","metric":600},
		{"dst":"192.168.86.0/24","dev":"wlan0","metric":600}
	]`
	routesv4, err := parseIpRouteJSON([]byte(inputv4), "ipv4")
	if err != nil {
		t.Fatalf("unexpected error parsing IPv4 JSON: %v", err)
	}
	if len(routesv4) != 2 {
		t.Fatalf("expected 2 IPv4 routes, got %d", len(routesv4))
	}
	if routesv4[0].Destination != "0.0.0.0" || routesv4[0].Gateway != "192.168.86.1" || routesv4[0].Interface != "wlan0" || routesv4[0].PrefixLen != 0 {
		t.Errorf("unexpected IPv4 route 0: %+v", routesv4[0])
	}
	if routesv4[1].Destination != "192.168.86.0" || routesv4[1].Gateway != "" || routesv4[1].Interface != "wlan0" || routesv4[1].PrefixLen != 24 {
		t.Errorf("unexpected IPv4 route 1: %+v", routesv4[1])
	}

	inputv6 := `[
		{"dst":"fdc3:bf47:eaee:1::/64","metric":605,"nexthops":[{"gateway":"fe80::1","dev":"wlan0"}]}
	]`
	routesv6, err := parseIpRouteJSON([]byte(inputv6), "ipv6")
	if err != nil {
		t.Fatalf("unexpected error parsing IPv6 JSON: %v", err)
	}
	if len(routesv6) != 1 {
		t.Fatalf("expected 1 IPv6 route, got %d", len(routesv6))
	}
	if routesv6[0].Destination != "fdc3:bf47:eaee:1::" || routesv6[0].Gateway != "fe80::1" || routesv6[0].Interface != "wlan0" || routesv6[0].PrefixLen != 64 {
		t.Errorf("unexpected IPv6 route 0: %+v", routesv6[0])
	}
}

func TestRoutingTableTool_Execute(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	procnet := filepath.Join(tmpDir, "proc", "net")
	if err := os.MkdirAll(procnet, 0755); err != nil {
		t.Fatal(err)
	}

	routeData := `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
wlan0	00000000	0156A8C0	0003	0	0	600	00000000	0	0	0
`
	ipv6RouteData := `fdbad78e093e76b80000000000000000 40 00000000000000000000000000000000 00 00000000000000000000000000000000 00000258 00000002 00000000 00000001    wlan0
`
	if err := os.WriteFile(filepath.Join(procnet, "route"), []byte(routeData), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(procnet, "ipv6_route"), []byte(ipv6RouteData), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &RoutingTableTool{
		procnetRoot: procnet,
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return nil, os.ErrNotExist // fail command execution to verify primary logic
		},
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.Status != registry.StatusOK {
		t.Fatalf("expected status OK, got %s. Summary: %s", res.Status, res.Summary)
	}

	var data RoutingTableData
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("failed to unmarshal output: %v", err)
	}

	if len(data.IPv4Routes) != 1 || len(data.IPv6Routes) != 1 {
		t.Fatalf("unexpected route count: %+v", data)
	}

	if data.IPv4Routes[0].Gateway != "192.168.86.1" {
		t.Errorf("expected IPv4 gateway 192.168.86.1, got %s", data.IPv4Routes[0].Gateway)
	}
	if data.IPv6Routes[0].Destination != "fdba:d78e:93e:76b8::" {
		t.Errorf("expected IPv6 destination fdba:d78e:93e:76b8::, got %s", data.IPv6Routes[0].Destination)
	}
}
