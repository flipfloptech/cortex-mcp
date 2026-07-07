package socketstats

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func TestSocketStatsTool_ContractCompliance(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "get_socket_stats" {
		t.Errorf("expected Name() == 'get_socket_stats', got %q", tool.Name())
	}
	if tool.Category() != "network" {
		t.Errorf("expected Category() == 'network', got %q", tool.Category())
	}
	if tool.Parameters() != nil {
		t.Errorf("expected Parameters() to be nil")
	}
}

func TestParseIPv4HexIPPort(t *testing.T) {
	t.Parallel()

	cases := []struct {
		hexStr   string
		wantIP   string
		wantPort int
	}{
		{"01FAA8C0:0035", "192.168.250.1", 53},
		{"0100007F:8121", "127.0.0.1", 33057},
		{"3500007F:0035", "127.0.0.53", 53},
		{"00000000:08AE", "0.0.0.0", 2222},
	}

	for _, c := range cases {
		ip, port, err := parseIPv4HexIPPort(c.hexStr)
		if err != nil {
			t.Errorf("parseIPv4HexIPPort(%q) error: %v", c.hexStr, err)
			continue
		}
		if ip != c.wantIP || port != c.wantPort {
			t.Errorf("parseIPv4HexIPPort(%q) = (%q, %d), want (%q, %d)", c.hexStr, ip, port, c.wantIP, c.wantPort)
		}
	}
}

func TestParseIPv6HexIPPort(t *testing.T) {
	t.Parallel()

	cases := []struct {
		hexStr   string
		wantIP   string
		wantPort int
	}{
		{"00000000000000000000000001000000:0277", "::1", 631},
		{"00000000000000000000000000000000:08AE", "::", 2222},
		{"fe80000000000000b623a2fffe6d03a7:0050", "fe80::b623:a2ff:fe6d:3a7", 80},
	}

	for _, c := range cases {
		ip, port, err := parseIPv6HexIPPort(c.hexStr)
		if err != nil {
			t.Errorf("parseIPv6HexIPPort(%q) error: %v", c.hexStr, err)
			continue
		}
		if ip != c.wantIP || port != c.wantPort {
			t.Errorf("parseIPv6HexIPPort(%q) = (%q, %d), want (%q, %d)", c.hexStr, ip, port, c.wantIP, c.wantPort)
		}
	}
}

func TestParseProcSockets(t *testing.T) {
	t.Parallel()

	tcpInput := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 01FAA8C0:0035 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 13851639 1 0000000000000000 100 0 0 10 5
   1: 0100007F:8121 0200007F:0050 01 00000000:00000000 00:00000000 00000000  1000        0 2545376 2 0000000000000000 100 0 0 10 0
`
	sockets, err := parseProcSockets([]byte(tcpInput), "tcp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sockets) != 2 {
		t.Fatalf("expected 2 sockets, got %d", len(sockets))
	}

	s0 := sockets[0]
	if s0.Protocol != "tcp" || s0.LocalIP != "192.168.250.1" || s0.LocalPort != 53 || s0.RemoteIP != "0.0.0.0" || s0.RemotePort != 0 || s0.State != "LISTEN" || s0.RecvQueue != 0 || s0.SendQueue != 0 || s0.Inode != 13851639 || s0.Uid != 0 {
		t.Errorf("unexpected socket 0: %+v", s0)
	}

	s1 := sockets[1]
	if s1.Protocol != "tcp" || s1.LocalIP != "127.0.0.1" || s1.LocalPort != 33057 || s1.RemoteIP != "127.0.0.2" || s1.RemotePort != 80 || s1.State != "ESTABLISHED" || s1.RecvQueue != 0 || s1.SendQueue != 0 || s1.Inode != 2545376 || s1.Uid != 1000 {
		t.Errorf("unexpected socket 1: %+v", s1)
	}
}

func TestParseSsFallback(t *testing.T) {
	t.Parallel()

	input := `udp   ESTAB     0      0            192.168.86.52:46812  64.233.180.101:443
tcp   LISTEN    0      128                [::1]:631                    [::]:*
`
	sockets, err := parseSsFallback([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sockets) != 2 {
		t.Fatalf("expected 2 sockets, got %d", len(sockets))
	}

	s0 := sockets[0]
	if s0.Protocol != "udp" || s0.LocalIP != "192.168.86.52" || s0.LocalPort != 46812 || s0.RemoteIP != "64.233.180.101" || s0.RemotePort != 443 || s0.State != "ESTAB" || s0.RecvQueue != 0 || s0.SendQueue != 0 {
		t.Errorf("unexpected socket 0: %+v", s0)
	}

	s1 := sockets[1]
	if s1.Protocol != "tcp" || s1.LocalIP != "::1" || s1.LocalPort != 631 || s1.RemoteIP != "::" || s1.RemotePort != 0 || s1.State != "LISTEN" || s1.RecvQueue != 0 || s1.SendQueue != 128 {
		t.Errorf("unexpected socket 1: %+v", s1)
	}
}

func TestSocketStatsTool_Execute(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	procnet := filepath.Join(tmpDir, "proc", "net")
	if err := os.MkdirAll(procnet, 0755); err != nil {
		t.Fatal(err)
	}

	tcpData := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 01FAA8C0:0035 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 13851639 1 0000000000000000 100 0 0 10 5
`
	if err := os.WriteFile(filepath.Join(procnet, "tcp"), []byte(tcpData), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &SocketStatsTool{
		procnetRoot: procnet,
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return nil, os.ErrNotExist
		},
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.Status != registry.StatusOK {
		t.Fatalf("expected status OK, got %s. Summary: %s", res.Status, res.Summary)
	}

	var data SocketStatsData
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("failed to unmarshal output: %v", err)
	}

	if len(data.Sockets) != 1 {
		t.Fatalf("expected 1 socket, got %d", len(data.Sockets))
	}

	if data.Sockets[0].LocalIP != "192.168.250.1" || data.Sockets[0].State != "LISTEN" {
		t.Errorf("unexpected socket: %+v", data.Sockets[0])
	}
}
