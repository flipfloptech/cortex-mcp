package ethhardwarestats

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

type RingParams struct {
	RxMax      int `json:"rx_max"`
	RxMiniMax  int `json:"rx_mini_max"`
	RxJumboMax int `json:"rx_jumbo_max"`
	TxMax      int `json:"tx_max"`
	RxCurrent  int `json:"rx_current"`
	RxMiniCur  int `json:"rx_mini_current"`
	RxJumboCur int `json:"rx_jumbo_current"`
	TxCurrent  int `json:"tx_current"`
}

type CoalesceParams struct {
	AdaptiveRx      bool `json:"adaptive_rx"`
	AdaptiveTx      bool `json:"adaptive_tx"`
	RxUsecs         int  `json:"rx_usecs"`
	RxFrames        int  `json:"rx_frames"`
	RxUsecsIrq      int  `json:"rx_usecs_irq"`
	RxFramesIrq     int  `json:"rx_frames_irq"`
	TxUsecs         int  `json:"tx_usecs"`
	TxFrames        int  `json:"tx_frames"`
	TxUsecsIrq      int  `json:"tx_usecs_irq"`
	TxFramesIrq     int  `json:"tx_frames_irq"`
	PktRateLow      int  `json:"pkt_rate_low"`
	PktRateHigh     int  `json:"pkt_rate_high"`
	SampleInterval  int  `json:"sample_interval"`
	StatsBlockUsecs int  `json:"stats_block_usecs"`
}

type EthHardwareStatsEntry struct {
	Interface      string            `json:"interface"`
	RxDropped      int64             `json:"rx_dropped"`
	RxErrors      int64             `json:"rx_errors"`
	RxFifoErrors  int64             `json:"rx_fifo_errors"`
	RxMissed      int64             `json:"rx_missed_errors"`
	TxDropped     int64             `json:"tx_dropped"`
	TxErrors      int64             `json:"tx_errors"`
	TxFifoErrors  int64             `json:"tx_fifo_errors"`
	RingParams    *RingParams       `json:"ring_params,omitempty"`
	CoalesceParams *CoalesceParams  `json:"coalesce_params,omitempty"`
	DriverStats   map[string]string `json:"driver_stats,omitempty"`
}

type EthHardwareStatsData struct {
	Interfaces []EthHardwareStatsEntry `json:"interfaces"`
}

type EthHardwareStatsTool struct {
	sysfsRoot   string
	execCommand func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func New() *EthHardwareStatsTool {
	return &EthHardwareStatsTool{
		sysfsRoot: "/sys/class/net",
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		},
	}
}

func init() {
	registry.Register(registry.WithCache(5*time.Second, New()))
}

func (t *EthHardwareStatsTool) Name() string {
	return "get_eth_hardware_stats"
}

func (t *EthHardwareStatsTool) Category() registry.Category {
	return registry.CategoryNetwork
}

func (t *EthHardwareStatsTool) Help() string {
	return `Deep audit of ethernet hardware configurations, queue limits, coalescing states, and driver stats.

Exposes ring buffer configurations, adaptive coalescing delay timers, frame metrics, and driver-specific drops and errors.

Data Sources:
- /sys/class/net/<iface>/statistics/ (natively read drops/errors)
- ethtool -g (ring buffer configurations fallback)
- ethtool -c (interrupt coalescing configurations fallback)
- ethtool -S (granular driver/PHY metrics fallback)`
}

func (t *EthHardwareStatsTool) Description() string {
	return "Deep audit of ethtool metrics: RX/TX ring sizes, interrupt coalescing, and hardware drops"
}

type Args struct {
	Interface  string   `json:"interface"`
	Interfaces []string `json:"interfaces"`
}

func (t *EthHardwareStatsTool) Parameters() []registry.ToolParam {
	return []registry.ToolParam{
		{
			Name:        "interfaces",
			Type:        "array",
			Description: "Optional: List of interfaces to filter by (e.g., ['eth0', 'eth1']).",
			Required:    false,
		},
		{
			Name:        "interface",
			Type:        "string",
			Description: "Optional: A single interface name or comma-separated list to filter by (e.g., 'eth0' or 'eth0,eth1').",
			Required:    false,
		},
	}
}

func (t *EthHardwareStatsTool) Hidden() bool { return false }

func (t *EthHardwareStatsTool) IsSupported() (bool, string) {
	if !registry.PathExists(t.sysfsRoot) {
		_, err := exec.LookPath("ethtool")
		if err != nil {
			return false, "Ethernet hardware statistics not supported (missing sysfs class net and ethtool binary)"
		}
	}
	return true, ""
}

func (t *EthHardwareStatsTool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	var parsedArgs Args
	if len(args) > 0 {
		_ = json.Unmarshal(args, &parsedArgs)
	}

	filterSet := make(map[string]bool)
	if parsedArgs.Interface != "" {
		parts := strings.Split(parsedArgs.Interface, ",")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				filterSet[p] = true
			}
		}
	}
	for _, p := range parsedArgs.Interfaces {
		p = strings.TrimSpace(p)
		if p != "" {
			filterSet[p] = true
		}
	}

	var entries []EthHardwareStatsEntry

	interfaces, err := os.ReadDir(t.sysfsRoot)
	if err == nil {
		for _, iface := range interfaces {
			name := iface.Name()
			if name == "lo" {
				continue
			}
			if len(filterSet) > 0 && !filterSet[name] {
				continue
			}

			statsDir := filepath.Join(t.sysfsRoot, name, "statistics")
			if !registry.PathExists(statsDir) {
				continue
			}

			// Read standard stats natively
			rxDropped := readInt64FromFile(filepath.Join(statsDir, "rx_dropped"))
			rxErrors := readInt64FromFile(filepath.Join(statsDir, "rx_errors"))
			rxFifo := readInt64FromFile(filepath.Join(statsDir, "rx_fifo_errors"))
			rxMissed := readInt64FromFile(filepath.Join(statsDir, "rx_missed_errors"))
			txDropped := readInt64FromFile(filepath.Join(statsDir, "tx_dropped"))
			txErrors := readInt64FromFile(filepath.Join(statsDir, "tx_errors"))
			txFifo := readInt64FromFile(filepath.Join(statsDir, "tx_fifo_errors"))

			entry := EthHardwareStatsEntry{
				Interface:    name,
				RxDropped:    rxDropped,
				RxErrors:     rxErrors,
				RxFifoErrors: rxFifo,
				RxMissed:     rxMissed,
				TxDropped:    txDropped,
				TxErrors:     txErrors,
				TxFifoErrors: txFifo,
			}

			// Try to augment with ethtool ring configurations
			if ringOut, ringErr := t.execCommand(ctx, "ethtool", "-g", name); ringErr == nil {
				entry.RingParams = parseRingParams(ringOut)
			}

			// Try to augment with ethtool coalescing configurations
			if coalesceOut, coalesceErr := t.execCommand(ctx, "ethtool", "-c", name); coalesceErr == nil {
				entry.CoalesceParams = parseCoalesceParams(coalesceOut)
			}

			// Try to augment with ethtool driver statistics
			if driverOut, driverErr := t.execCommand(ctx, "ethtool", "-S", name); driverErr == nil {
				entry.DriverStats = parseDriverStats(driverOut)
			}

			entries = append(entries, entry)
		}
	}

	var totalRxDropped, totalTxDropped int64
	for _, entry := range entries {
		totalRxDropped += entry.RxDropped
		totalTxDropped += entry.TxDropped
	}

	summaryStr := fmt.Sprintf("Ethernet Hardware Statistics: %d interfaces audited, total RX drops: %d, total TX drops: %d",
		len(entries),
		totalRxDropped,
		totalTxDropped)

	data := EthHardwareStatsData{Interfaces: entries}
	res := registry.NewResult(t.Name(), registry.StatusOK, summaryStr, data)
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()

	return res, nil
}

func readInt64FromFile(path string) int64 {
	content, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	val, err := strconv.ParseInt(strings.TrimSpace(string(content)), 10, 64)
	if err != nil {
		return 0
	}
	return val
}

func parseRingParams(output []byte) *RingParams {
	lines := strings.Split(string(output), "\n")
	var rxMax, rxMiniMax, rxJumboMax, txMax int
	var rxCur, rxMiniCur, rxJumboCur, txCur int

	isMaxSection := false
	isCurSection := false

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.Contains(line, "Pre-set maximums:") {
			isMaxSection = true
			isCurSection = false
			continue
		}
		if strings.Contains(line, "Current hardware settings:") {
			isMaxSection = false
			isCurSection = true
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		valStr := strings.TrimSpace(parts[1])
		val, err := strconv.Atoi(valStr)
		if err != nil {
			continue
		}

		if isMaxSection {
			switch key {
			case "RX":
				rxMax = val
			case "RX Mini":
				rxMiniMax = val
			case "RX Jumbo":
				rxJumboMax = val
			case "TX":
				txMax = val
			}
		} else if isCurSection {
			switch key {
			case "RX":
				rxCur = val
			case "RX Mini":
				rxMiniCur = val
			case "RX Jumbo":
				rxJumboCur = val
			case "TX":
				txCur = val
			}
		}
	}

	if rxMax > 0 || txMax > 0 || rxCur > 0 || txCur > 0 {
		return &RingParams{
			RxMax:      rxMax,
			RxMiniMax:  rxMiniMax,
			RxJumboMax: rxJumboMax,
			TxMax:      txMax,
			RxCurrent:  rxCur,
			RxMiniCur:  rxMiniCur,
			RxJumboCur: rxJumboCur,
			TxCurrent:  txCur,
		}
	}
	return nil
}

func parseCoalesceParams(output []byte) *CoalesceParams {
	lines := strings.Split(string(output), "\n")
	var p CoalesceParams
	hasParsed := false

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if strings.Contains(line, "Adaptive RX:") {
			parts := strings.Fields(line)
			if len(parts) >= 5 {
				p.AdaptiveRx = (parts[2] == "on")
				p.AdaptiveTx = (parts[4] == "on")
				hasParsed = true
			}
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		valStr := strings.TrimSpace(parts[1])
		val, err := strconv.Atoi(valStr)
		if err != nil {
			continue
		}

		hasParsed = true
		switch key {
		case "rx-usecs":
			p.RxUsecs = val
		case "rx-frames":
			p.RxFrames = val
		case "rx-usecs-irq":
			p.RxUsecsIrq = val
		case "rx-frames-irq":
			p.RxFramesIrq = val
		case "tx-usecs":
			p.TxUsecs = val
		case "tx-frames":
			p.TxFrames = val
		case "tx-usecs-irq":
			p.TxUsecsIrq = val
		case "tx-frames-irq":
			p.TxFramesIrq = val
		case "pkt-rate-low":
			p.PktRateLow = val
		case "pkt-rate-high":
			p.PktRateHigh = val
		case "sample-interval":
			p.SampleInterval = val
		case "stats-block-usecs":
			p.StatsBlockUsecs = val
		}
	}

	if hasParsed {
		return &p
	}
	return nil
}

func parseDriverStats(output []byte) map[string]string {
	stats := make(map[string]string)
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "NIC statistics:") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			stats[key] = val
		}
	}
	if len(stats) > 0 {
		return stats
	}
	return nil
}
