package nicethtoolstats

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func TestNICEthtoolStatsTool_ContractCompliance(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "get_nic_ethtool_stats" {
		t.Errorf("expected Name() == 'get_nic_ethtool_stats', got %q", tool.Name())
	}
	if tool.Category() != registry.CategoryNetwork {
		t.Errorf("expected Category() == 'network', got %q", tool.Category())
	}
	params := tool.Parameters()
	if len(params) != 2 {
		t.Errorf("expected 2 parameters, got %d", len(params))
	}
}

func TestParseRingParams(t *testing.T) {
	t.Parallel()

	input := `Ring parameters for eth0:
Pre-set maximums:
RX:		4096
RX Mini:	0
RX Jumbo:	0
TX:		4096
Current hardware settings:
RX:		512
RX Mini:	0
RX Jumbo:	0
TX:		512
`
	ring := parseRingParams([]byte(input))
	if ring == nil {
		t.Fatalf("expected parsed ring params, got nil")
	}

	if ring.RxMax != 4096 || ring.TxMax != 4096 || ring.RxCurrent != 512 || ring.TxCurrent != 512 {
		t.Errorf("unexpected ring parameters: %+v", ring)
	}
}

func TestParseChannelParams(t *testing.T) {
	t.Parallel()

	input := `Channel parameters for eth0:
Pre-set maximums:
RX:		0
TX:		0
Other:		0
Combined:	8
Current hardware settings:
RX:		0
TX:		0
Other:		0
Combined:	4
`
	channel := parseChannelParams([]byte(input))
	if channel == nil {
		t.Fatalf("expected parsed channel params, got nil")
	}

	if channel.CombinedMax != 8 || channel.CombinedCur != 4 || channel.RxMax != 0 || channel.RxCurrent != 0 {
		t.Errorf("unexpected channel parameters: %+v", channel)
	}
}

func TestParseDriverStats(t *testing.T) {
	t.Parallel()

	input := `NIC statistics:
     rx_packets: 4300229
     rx_bytes: 4894518924
     rx_dropped: 167
`
	stats := parseDriverStats([]byte(input))
	if stats == nil {
		t.Fatalf("expected parsed driver stats, got nil")
	}

	if stats["rx_packets"] != "4300229" || stats["rx_dropped"] != "167" {
		t.Errorf("unexpected driver stats: %+v", stats)
	}
}

func TestNICEthtoolStatsTool_Execute(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	netDir := filepath.Join(tmpDir, "sys", "class", "net")
	eth0Dir := filepath.Join(netDir, "eth0", "statistics")
	if err := os.MkdirAll(eth0Dir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write mock statistics files
	files := map[string][]byte{
		"rx_dropped":       []byte("15\n"),
		"rx_errors":        []byte("2\n"),
		"rx_fifo_errors":   []byte("1\n"),
		"rx_missed_errors": []byte("0\n"),
		"tx_dropped":       []byte("0\n"),
		"tx_errors":        []byte("0\n"),
		"tx_fifo_errors":   []byte("0\n"),
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(eth0Dir, name), content, 0644); err != nil {
			t.Fatal(err)
		}
	}

	tool := &NICEthtoolStatsTool{
		sysfsRoot: netDir,
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if len(args) >= 2 && args[0] == "-g" {
				return []byte(`Pre-set maximums:
RX:		4096
TX:		4096
Current hardware settings:
RX:		512
TX:		512
`), nil
			}
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

	var data NicStatsData
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("failed to unmarshal output: %v", err)
	}

	if len(data.Interfaces) != 1 {
		t.Fatalf("expected 1 interface, got %d", len(data.Interfaces))
	}

	iface := data.Interfaces[0]
	if iface.Interface != "eth0" || iface.RxDropped != 15 || iface.RxFifoErrors != 1 || iface.RingParams == nil {
		t.Errorf("unexpected interface statistics: %+v", iface)
	}
}

func TestNICEthtoolStatsTool_Execute_Filtering(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	netDir := filepath.Join(tmpDir, "sys", "class", "net")

	// Set up two mock interfaces: eth0 and eth1
	for _, iface := range []string{"eth0", "eth1"} {
		ethDir := filepath.Join(netDir, iface, "statistics")
		if err := os.MkdirAll(ethDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ethDir, "rx_dropped"), []byte("10\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	tool := &NICEthtoolStatsTool{
		sysfsRoot: netDir,
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return nil, os.ErrNotExist
		},
	}

	// 1. Filter by interfaces array: ["eth0"]
	{
		args, _ := json.Marshal(map[string]interface{}{
			"interfaces": []string{"eth0"},
		})
		res, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatal(err)
		}
		var data NicStatsData
		_ = json.Unmarshal(res.Data, &data)
		if len(data.Interfaces) != 1 || data.Interfaces[0].Interface != "eth0" {
			t.Errorf("expected only eth0 interface when filtering by array, got %v", data.Interfaces)
		}
	}

	// 2. Filter by single interface string: "eth1"
	{
		args, _ := json.Marshal(map[string]interface{}{
			"interface": "eth1",
		})
		res, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatal(err)
		}
		var data NicStatsData
		_ = json.Unmarshal(res.Data, &data)
		if len(data.Interfaces) != 1 || data.Interfaces[0].Interface != "eth1" {
			t.Errorf("expected only eth1 interface when filtering by single string, got %v", data.Interfaces)
		}
	}

	// 3. Filter by comma-separated string: "eth0,eth1"
	{
		args, _ := json.Marshal(map[string]interface{}{
			"interface": "eth0,eth1",
		})
		res, err := tool.Execute(context.Background(), args)
		if err != nil {
			t.Fatal(err)
		}
		var data NicStatsData
		_ = json.Unmarshal(res.Data, &data)
		if len(data.Interfaces) != 2 {
			t.Errorf("expected both interfaces when filtering by comma-separated string, got %v", data.Interfaces)
		}
	}
}
