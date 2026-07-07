package hugepageinfo

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
	"github.com/flipfloptech/cortex-mcp/internal/sys/memory"
)

// sysDevicesSystemNodePath is the sysfs node root; a package-level var so
// tests can point the tool at a fake tree.
var sysDevicesSystemNodePath = "/sys/devices/system/node"

var (
	nodeRegex         = regexp.MustCompile(`^node(\d+)$`)
	hugepageSizeRegex = regexp.MustCompile(`^hugepages-(\d+)kB$`)
)

// NodeHugePages describes one hugepage size pool on one NUMA node.
type NodeHugePages struct {
	Node    int    `json:"node"`
	SizeKB  uint64 `json:"size_kb"`
	Total   uint64 `json:"total"`
	Free    uint64 `json:"free"`
	Surplus uint64 `json:"surplus"`
}

// HugePageData wraps the memory package payload with tool-level additions.
// The embedded pointer keeps the pre-existing top-level JSON keys intact.
type HugePageData struct {
	*memory.HugePageInfo
	PerNode        []NodeHugePages `json:"per_node,omitempty"`
	WarningReasons []string        `json:"warning_reasons,omitempty"`
}

// Tool implements the registry.Tool interface for get_hugepage_info.
type Tool struct{}

// New returns a new instance of the hugepageinfo tool.
func New() *Tool {
	return &Tool{}
}

func init() {
	registry.Register(New())
}

// Name returns the tool name.
func (t *Tool) Name() string {
	return "get_hugepage_info"
}

// Description returns a short summary of the tool.
func (t *Tool) Description() string {
	return "Provide a comprehensive audit of memory paging efficiency by identifying the allocation status of static HugePages and the current operational mode of Transparent HugePages (THP)."
}

// Help returns detailed documentation for the tool.
func (t *Tool) Help() string {
	return `Objective:
Provide a comprehensive audit of memory paging efficiency by identifying the allocation status of static HugePages and the current operational mode of Transparent HugePages (THP) to diagnose memory allocation stalls.

Data Sources:
- Static HugePages: /proc/meminfo
- Transparent HugePages (THP) Settings: /sys/kernel/mm/transparent_hugepage/enabled, /sys/kernel/mm/transparent_hugepage/defrag
- THP Performance Impact: /proc/vmstat
- Per-NUMA-node pools: /sys/devices/system/node/node*/hugepages/hugepages-*kB/{nr,free,surplus}_hugepages

Returns a JSON object detailing static hugepage utilization and THP allocation/fallback statistics.
When per-node hugepage pools are exposed, a "per_node" breakdown is included and allocation
imbalance across nodes (one node exhausted while another has free pages) raises a warning.`
}

// Category returns the tool classification.
func (t *Tool) Category() registry.Category {
	return registry.CategoryMemory
}

// Parameters returns the parameter schema. This tool takes no parameters.
func (t *Tool) Parameters() []registry.ToolParam {
	return nil
}

// Hidden indicates whether this tool should be hidden from general LLM discovery.
func (t *Tool) Hidden() bool {
	return false
}

// IsSupported returns true if the host supports hugepages.
// It checks for the existence of /proc/meminfo as the core dependency.
func (t *Tool) IsSupported() (bool, string) {
	if _, err := os.Stat("/proc/meminfo"); err != nil {
		return false, "failed to access /proc/meminfo"
	}
	return true, ""
}

// Execute performs the diagnostic logic and returns the structured result.
func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	info, err := memory.GetHugePageInfo()
	if err != nil {
		return registry.NewErrorResult(t.Name(), err.Error()), nil
	}

	summary := "HugePage diagnostic telemetry captured"
	if info.THPStats != nil && info.THPStats.IsStallingRisk {
		summary = "THP Stalling Risk Detected"
	}

	data := HugePageData{HugePageInfo: info}
	data.PerNode = collectPerNodeHugePages()
	data.WarningReasons = evaluateNodeImbalance(data.PerNode)

	status := registry.StatusOK
	if len(data.WarningReasons) > 0 {
		status = registry.StatusWarning
		summary = data.WarningReasons[0]
	}

	return registry.NewResult(t.Name(), status, summary, data), nil
}

// collectPerNodeHugePages scans <root>/node<N>/hugepages/hugepages-<size>kB/
// pools and returns the per-node breakdown sorted by node then size. It
// returns nil (block omitted) on UMA systems and old kernels without per-node
// hugepage directories; individual unreadable counter files are reported as 0.
func collectPerNodeHugePages() []NodeHugePages {
	entries, err := os.ReadDir(sysDevicesSystemNodePath)
	if err != nil {
		return nil
	}

	var out []NodeHugePages
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		matches := nodeRegex.FindStringSubmatch(e.Name())
		if len(matches) != 2 {
			continue
		}
		nodeID, err := strconv.Atoi(matches[1])
		if err != nil {
			continue
		}

		hpDir := filepath.Join(sysDevicesSystemNodePath, e.Name(), "hugepages")
		sizeEntries, err := os.ReadDir(hpDir)
		if err != nil {
			continue
		}

		for _, se := range sizeEntries {
			if !se.IsDir() {
				continue
			}
			sizeMatches := hugepageSizeRegex.FindStringSubmatch(se.Name())
			if len(sizeMatches) != 2 {
				continue
			}
			sizeKB, err := strconv.ParseUint(sizeMatches[1], 10, 64)
			if err != nil {
				continue
			}

			sizeDir := filepath.Join(hpDir, se.Name())
			out = append(out, NodeHugePages{
				Node:    nodeID,
				SizeKB:  sizeKB,
				Total:   readHugeCount(filepath.Join(sizeDir, "nr_hugepages")),
				Free:    readHugeCount(filepath.Join(sizeDir, "free_hugepages")),
				Surplus: readHugeCount(filepath.Join(sizeDir, "surplus_hugepages")),
			})
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Node != out[j].Node {
			return out[i].Node < out[j].Node
		}
		return out[i].SizeKB < out[j].SizeKB
	})

	return out
}

// readHugeCount reads a hugepage counter file. Unreadable or malformed files
// are reported as 0 so partial node data still surfaces what is readable.
func readHugeCount(path string) uint64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// evaluateNodeImbalance flags allocation imbalance per hugepage size: a node
// whose pool is exhausted (free == 0 with pages allocated) while another node
// of the same size still has free pages means NUMA-pinned allocations on the
// exhausted node may fail. perNode must be sorted (as produced by
// collectPerNodeHugePages) for deterministic warning ordering.
func evaluateNodeImbalance(perNode []NodeHugePages) []string {
	bySize := make(map[uint64][]NodeHugePages)
	for _, n := range perNode {
		bySize[n.SizeKB] = append(bySize[n.SizeKB], n)
	}

	sizes := make([]uint64, 0, len(bySize))
	for size := range bySize {
		sizes = append(sizes, size)
	}
	sort.Slice(sizes, func(i, j int) bool { return sizes[i] < sizes[j] })

	var warnings []string
	for _, size := range sizes {
		nodes := bySize[size]

		donorIdx := -1
		for i := range nodes {
			if nodes[i].Free > 0 && (donorIdx == -1 || nodes[i].Free > nodes[donorIdx].Free) {
				donorIdx = i
			}
		}
		if donorIdx == -1 {
			continue
		}

		for _, n := range nodes {
			if n.Total > 0 && n.Free == 0 {
				warnings = append(warnings, fmt.Sprintf(
					"HugePages exhausted on node %d while node %d has %d free — NUMA-pinned allocations may fail",
					n.Node, nodes[donorIdx].Node, nodes[donorIdx].Free))
			}
		}
	}

	return warnings
}
