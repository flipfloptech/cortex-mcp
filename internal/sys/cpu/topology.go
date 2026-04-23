package cpu

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// SystemSummary provides top-level metrics and abstraction flags.
type SystemSummary struct {
	TotalLogicalCPUs     int  `json:"total_logical_cpus"`
	TotalNumaNodes       int  `json:"total_numa_nodes"`
	IsNuma               bool `json:"is_numa"`
	SMTEnabled           bool `json:"smt_enabled"`
	L3TopologyAbstracted bool `json:"l3_topology_abstracted,omitempty"`
}

// PhysicalCore represents a physical core containing logical threads (SMT/Hyperthreading).
type PhysicalCore struct {
	CoreID         int   `json:"core_id"`
	LogicalThreads []int `json:"logical_threads"`
}

// L3CacheDomain represents a group of physical cores sharing an L3 cache.
type L3CacheDomain struct {
	SharedLogicalCPUs []int          `json:"shared_logical_cpus"`
	PhysicalCores     []PhysicalCore `json:"physical_cores"`
}

// NumaNode represents a physical CPU socket and its memory resources.
type NumaNode struct {
	SocketID       int                      `json:"socket_id"`
	L3CacheDomains map[string]L3CacheDomain `json:"l3_cache_domains"`
}

// SystemTopology is the root container for the hardware layout.
type SystemTopology struct {
	SystemSummary SystemSummary       `json:"system_summary"`
	Topology      map[string]NumaNode `json:"topology"`
}

// readFileWithContext reads a file but respects the context deadline/cancellation to prevent hangs on virtual filesystems.
func readFileWithContext(ctx context.Context, path string) (string, error) {
	// A simple way to enforce context on file reading is using a goroutine.
	// We optimize for the common case (it's fast) but don't hang if it's dead.

	type result struct {
		data string
		err  error
	}

	ch := make(chan result, 1)
	go func() {
		data, err := os.ReadFile(path)
		if err != nil {
			ch <- result{"", err}
			return
		}
		ch <- result{string(bytes.TrimSpace(data)), nil}
	}()

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case res := <-ch:
		return res.data, res.err
	}
}

// ParseCPUList converts a sysfs cpu list string (e.g. "0-7,64-71") into a sorted slice of integers.
func ParseCPUList(listStr string) []int {
	var cpus []int
	if listStr == "" {
		return cpus
	}
	parts := strings.Split(listStr, ",")
	for _, part := range parts {
		if strings.Contains(part, "-") {
			rangeParts := strings.Split(part, "-")
			if len(rangeParts) == 2 {
				start, err1 := strconv.Atoi(rangeParts[0])
				end, err2 := strconv.Atoi(rangeParts[1])
				if err1 == nil && err2 == nil && start <= end {
					for i := start; i <= end; i++ {
						cpus = append(cpus, i)
					}
				}
			}
		} else {
			if val, err := strconv.Atoi(part); err == nil {
				cpus = append(cpus, val)
			}
		}
	}
	sort.Ints(cpus)
	return cpus
}

// GetTopology traverses the given sysfsBase to construct the SystemTopology mapping.
func GetTopology(ctx context.Context, sysfsBase string) (*SystemTopology, error) {
	topology := &SystemTopology{
		Topology: make(map[string]NumaNode),
	}

	// 1. Determine NUMA nodes
	nodeBase := filepath.Join(sysfsBase, "devices/system/node")
	cpuBase := filepath.Join(sysfsBase, "devices/system/cpu")

	var isNuma bool
	var l3Abstracted bool
	var hasSMT bool
	numaNodes := make(map[int][]int) // map[nodeID][]logicalCPUs

	nodeEntries, err := os.ReadDir(nodeBase)
	if err == nil {
		for _, entry := range nodeEntries {
			if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "node") {
				continue
			}
			nodeIDStr := strings.TrimPrefix(entry.Name(), "node")
			nodeID, err := strconv.Atoi(nodeIDStr)
			if err != nil {
				continue
			}
			cpulistPath := filepath.Join(nodeBase, entry.Name(), "cpulist")
			listStr, err := readFileWithContext(ctx, cpulistPath)
			if err != nil {
				continue
			}
			cpus := ParseCPUList(listStr)
			if len(cpus) > 0 {
				numaNodes[nodeID] = cpus
			}
		}
	}

	// UMA Fallback
	if len(numaNodes) <= 1 {
		isNuma = false
		if len(numaNodes) == 0 {
			// Find all CPUs
			cpuEntries, err := os.ReadDir(cpuBase)
			var cpus []int
			if err == nil {
				for _, entry := range cpuEntries {
					if entry.IsDir() && strings.HasPrefix(entry.Name(), "cpu") {
						idStr := strings.TrimPrefix(entry.Name(), "cpu")
						if id, err := strconv.Atoi(idStr); err == nil {
							cpus = append(cpus, id)
						}
					}
				}
				sort.Ints(cpus)
			}
			numaNodes[0] = cpus
		} else {
			// Still rename to 0 to maintain consistency if the only node was not 0
			var singleCpus []int
			for _, v := range numaNodes {
				singleCpus = v
			}
			numaNodes = map[int][]int{0: singleCpus}
		}
	} else {
		isNuma = true
	}

	totalLogicalCPUs := 0

	// Track seen L3 and Cores to build the hierarchy
	// Node -> L3 -> Core -> CPUs
	type coreInfo struct {
		coreID   int
		siblings []int
	}
	type l3Info struct {
		sharedCpus []int
		cores      map[int]coreInfo
	}

	for nodeID, cpus := range numaNodes {
		totalLogicalCPUs += len(cpus)
		socketID := 0 // default

		l3Domains := make(map[int]l3Info)

		for _, cpuID := range cpus {
			cpuDir := filepath.Join(cpuBase, fmt.Sprintf("cpu%d", cpuID))

			// Socket ID
			pkgPath := filepath.Join(cpuDir, "topology/physical_package_id")
			pkgStr, err := readFileWithContext(ctx, pkgPath)
			if err == nil {
				if id, err := strconv.Atoi(pkgStr); err == nil {
					socketID = id
				}
			}

			// Core ID
			corePath := filepath.Join(cpuDir, "topology/core_id")
			coreStr, err := readFileWithContext(ctx, corePath)
			coreID := 0
			if err == nil {
				if id, err := strconv.Atoi(coreStr); err == nil {
					coreID = id
				}
			}

			// Siblings (SMT)
			sibPath := filepath.Join(cpuDir, "topology/thread_siblings_list")
			sibStr, err := readFileWithContext(ctx, sibPath)
			var siblings []int
			if err == nil {
				siblings = ParseCPUList(sibStr)
				if len(siblings) > 1 {
					hasSMT = true
				}
			} else {
				siblings = []int{cpuID}
			}

			// L3 Cache
			l3Path := filepath.Join(cpuDir, "cache/index3/shared_cpu_list")
			l3Str, err := readFileWithContext(ctx, l3Path)
			var l3Cpus []int
			l3ID := 0 // fallback
			if err == nil {
				l3Cpus = ParseCPUList(l3Str)
				if len(l3Cpus) > 0 {
					l3ID = l3Cpus[0] // Use first CPU in domain as domain ID
				}
			} else {
				l3Abstracted = true
				// Virtualization fallback: all CPUs in the node share l3_domain_0
				l3Cpus = cpus
			}

			domain, exists := l3Domains[l3ID]
			if !exists {
				domain = l3Info{
					sharedCpus: l3Cpus,
					cores:      make(map[int]coreInfo),
				}
			}

			// Filter out siblings that are not in this L3 cache domain or not in this NUMA node.
			// Actually, thread_siblings_list should always be in the same core and node.
			if _, coreExists := domain.cores[coreID]; !coreExists {
				domain.cores[coreID] = coreInfo{
					coreID:   coreID,
					siblings: siblings,
				}
			}
			l3Domains[l3ID] = domain
		}

		// Build the NumaNode struct
		numaNode := NumaNode{
			SocketID:       socketID,
			L3CacheDomains: make(map[string]L3CacheDomain),
		}

		l3Idx := 0
		// Need to sort L3 IDs to ensure deterministic map naming
		var l3IDs []int
		for l3ID := range l3Domains {
			l3IDs = append(l3IDs, l3ID)
		}
		sort.Ints(l3IDs)

		for _, l3ID := range l3IDs {
			domain := l3Domains[l3ID]

			var pCores []PhysicalCore
			// Sort core IDs for determinism
			var coreIDs []int
			for cid := range domain.cores {
				coreIDs = append(coreIDs, cid)
			}
			sort.Ints(coreIDs)

			for _, cid := range coreIDs {
				cInfo := domain.cores[cid]
				pCores = append(pCores, PhysicalCore{
					CoreID:         cInfo.coreID,
					LogicalThreads: cInfo.siblings,
				})
			}

			l3DomainName := fmt.Sprintf("l3_domain_%d", l3Idx)
			numaNode.L3CacheDomains[l3DomainName] = L3CacheDomain{
				SharedLogicalCPUs: domain.sharedCpus,
				PhysicalCores:     pCores,
			}
			l3Idx++
		}

		numaNodeName := fmt.Sprintf("numa_node_%d", nodeID)
		topology.Topology[numaNodeName] = numaNode
	}

	topology.SystemSummary = SystemSummary{
		TotalLogicalCPUs:     totalLogicalCPUs,
		TotalNumaNodes:       len(numaNodes),
		IsNuma:               isNuma,
		SMTEnabled:           hasSMT,
		L3TopologyAbstracted: l3Abstracted,
	}

	return topology, nil
}
