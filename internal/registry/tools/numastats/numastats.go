package numastats

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

var sysfsNodePath = "/sys/devices/system/node"

func init() {
	registry.Register(registry.WithCache(5*time.Second, &NumaStatsTool{}))
}

// NumaStatsTool translates cumulative NUMA allocation counters into hit/miss ratios.
type NumaStatsTool struct{}

func (t *NumaStatsTool) Name() string { return "get_numa_stats" }

func (t *NumaStatsTool) Description() string {
	return "Instant visibility into memory locality efficiency and NUMA thread-pinning violations"
}

func (t *NumaStatsTool) Help() string {
	return `get_numa_stats — Get System NUMA Stats

Provides instant visibility into memory locality efficiency by translating
cumulative NUMA allocation counters into actionable hit/miss ratios. This helps
detect thread-pinning violations or kernel numad failures.

Data Sources:
  - /sys/devices/system/node/node*/numastat

Mathematical Models / Formatting:
  - node_miss_ratio_pct: (numa_miss / (numa_hit + numa_miss)) * 100
  - system_miss_ratio_pct: Aggregate hits and misses across all nodes.

Degradation Profile:
  If the system only has node0 (UMA), cross-node misses are physically impossible.
  In this case, "is_numa" will be false, and ratios will be 0.0.

Parameters: None
Supported on: Linux`
}

func (t *NumaStatsTool) Category() registry.Category { return registry.CategoryMemory }

func (t *NumaStatsTool) Parameters() []registry.ToolParam { return nil }

func (t *NumaStatsTool) Hidden() bool { return false }

func (t *NumaStatsTool) IsSupported() (bool, string) {
	if !registry.IsLinux() {
		return false, "requires Linux (running on " + runtime.GOOS + ")"
	}
	if _, err := os.Stat(sysfsNodePath); err != nil {
		return false, "requires " + sysfsNodePath
	}
	return true, ""
}

// NodeMetrics holds raw counters and pre-calculated ratio for a single node.
type NodeMetrics struct {
	NumaHit          uint64  `json:"numa_hit"`
	NumaMiss         uint64  `json:"numa_miss"`
	NumaForeign      uint64  `json:"numa_foreign"`
	LocalNode        uint64  `json:"local_node"`
	OtherNode        uint64  `json:"other_node"`
	NodeMissRatioPct float64 `json:"node_miss_ratio_pct"`
}

// SystemSummary holds the aggregated cross-system NUMA stats.
type SystemSummary struct {
	TotalNumaNodes         int     `json:"total_numa_nodes"`
	IsNuma                 bool    `json:"is_numa"`
	SystemMissRatioPct     float64 `json:"system_miss_ratio_pct"`
	TotalSystemAllocations uint64  `json:"total_system_allocations"`
}

// NumaStatsData is the full JSON payload.
type NumaStatsData struct {
	SystemSummary SystemSummary          `json:"system_summary"`
	NodeMetrics   map[string]NodeMetrics `json:"node_metrics"`
}

func (t *NumaStatsTool) Execute(_ context.Context, _ json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	data, err := GetNumaStats()
	if err != nil {
		result := registry.NewErrorResult(t.Name(), "failed to get NUMA stats: "+err.Error())
		result.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
		return result, nil
	}

	result := registry.NewResult(
		t.Name(), registry.StatusOK, fmt.Sprintf("NUMA Miss Ratio: %.2f%% across %d nodes", data.SystemSummary.SystemMissRatioPct, data.SystemSummary.TotalNumaNodes),
		data,
	)
	result.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	result.Metadata.FilteringMethod = "deterministic"

	return result, nil
}

// GetNumaStats reads the sysfs tree and builds the JSON structure.
func GetNumaStats() (*NumaStatsData, error) {
	nodes, err := filepath.Glob(filepath.Join(sysfsNodePath, "node*"))
	if err != nil {
		return nil, err
	}

	if len(nodes) == 0 {
		return nil, fmt.Errorf("no NUMA nodes found in %s", sysfsNodePath)
	}

	// Filter out "node_online" or similar files that aren't directories
	var validNodes []string
	for _, nodePath := range nodes {
		info, err := os.Stat(nodePath)
		if err == nil && info.IsDir() {
			validNodes = append(validNodes, nodePath)
		}
	}

	// Make output deterministic
	sort.Strings(validNodes)

	data := &NumaStatsData{
		NodeMetrics: make(map[string]NodeMetrics),
	}

	var totalHits uint64
	var totalMisses uint64

	for _, nodePath := range validNodes {
		nodeName := filepath.Base(nodePath)                      // e.g. "node0"
		nodeName = strings.Replace(nodeName, "node", "node_", 1) // e.g. "node_0"

		numastatFile := filepath.Join(nodePath, "numastat")
		metrics, err := parseNumaStatFile(numastatFile)
		if err != nil {
			// Skip nodes that don't have a numastat file (could be node_online file etc)
			continue
		}

		data.NodeMetrics[nodeName] = metrics
		totalHits += metrics.NumaHit
		totalMisses += metrics.NumaMiss
	}

	data.SystemSummary.TotalNumaNodes = len(data.NodeMetrics)
	data.SystemSummary.IsNuma = data.SystemSummary.TotalNumaNodes > 1

	if data.SystemSummary.IsNuma {
		sumAllocations := totalHits + totalMisses
		if sumAllocations > 0 {
			ratio := (float64(totalMisses) / float64(sumAllocations)) * 100.0
			data.SystemSummary.SystemMissRatioPct = math.Round(ratio*100) / 100
		}
	} else {
		// UMA fallback
		data.SystemSummary.SystemMissRatioPct = 0.0
	}

	data.SystemSummary.TotalSystemAllocations = totalHits + totalMisses

	return data, nil
}

// parseNumaStatFile parses a single numastat file.
func parseNumaStatFile(path string) (NodeMetrics, error) {
	file, err := os.Open(path)
	if err != nil {
		return NodeMetrics{}, err
	}
	defer func() { _ = file.Close() }()

	var metrics NodeMetrics
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Fields(line)
		if len(parts) != 2 {
			continue
		}

		key := parts[0]
		val, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil {
			continue
		}

		switch key {
		case "numa_hit":
			metrics.NumaHit = val
		case "numa_miss":
			metrics.NumaMiss = val
		case "numa_foreign":
			metrics.NumaForeign = val
		case "local_node":
			metrics.LocalNode = val
		case "other_node":
			metrics.OtherNode = val
		}
	}

	if err := scanner.Err(); err != nil {
		return NodeMetrics{}, err
	}

	total := metrics.NumaHit + metrics.NumaMiss
	if total > 0 {
		ratio := (float64(metrics.NumaMiss) / float64(total)) * 100.0
		metrics.NodeMissRatioPct = math.Round(ratio*100) / 100
	} else {
		metrics.NodeMissRatioPct = 0.0
	}

	return metrics, nil
}
