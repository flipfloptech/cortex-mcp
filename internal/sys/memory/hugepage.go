package memory

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// HugePageSystemSummary represents the global state of HugePages.
type HugePageSystemSummary struct {
	StaticHugepagesSupported bool   `json:"static_hugepages_supported"`
	THPEnabledMode           string `json:"thp_enabled_mode"`
	THPDefragPolicy          string `json:"thp_defrag_policy"`
}

// StaticHugepages represents the configuration and utilization of static HugePages.
type StaticHugepages struct {
	PageSizeKB    uint64  `json:"page_size_kb"`
	TotalPages    uint64  `json:"total_pages"`
	FreePages     uint64  `json:"free_pages"`
	ReservedPages uint64  `json:"reserved_pages"`
	TotalSizeMB   float64 `json:"total_size_mb"`
	UsagePct      float64 `json:"usage_pct"`
}

// THPStats represents Transparent HugePage telemetry.
type THPStats struct {
	FaultAllocations    uint64 `json:"fault_allocations"`
	FaultFallbacks      uint64 `json:"fault_fallbacks"`
	CollapseAllocations uint64 `json:"collapse_allocations"`
	IsStallingRisk      bool   `json:"is_stalling_risk"`
}

// HugePageInfo aggregates all HugePage telemetry.
type HugePageInfo struct {
	SystemSummary   HugePageSystemSummary `json:"system_summary"`
	StaticHugepages StaticHugepages       `json:"static_hugepages"`
	THPStats        *THPStats             `json:"thp_stats,omitempty"`
}

// GetHugePageInfo returns the current HugePage diagnostic info reading from standard sysfs/procfs paths.
func GetHugePageInfo() (*HugePageInfo, error) {
	return getHugePageInfoFromPaths(
		"/proc/meminfo",
		"/sys/kernel/mm/transparent_hugepage/enabled",
		"/sys/kernel/mm/transparent_hugepage/defrag",
		"/proc/vmstat",
	)
}

func getHugePageInfoFromPaths(meminfoPath, thpEnabledPath, thpDefragPath, vmstatPath string) (*HugePageInfo, error) {
	info := &HugePageInfo{}

	// 1. Parse /proc/meminfo
	memFile, err := os.Open(meminfoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open meminfo: %w", err)
	}
	defer func() {
		_ = memFile.Close()
	}()

	scanner := bufio.NewScanner(memFile)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		key := fields[0]
		val, _ := strconv.ParseUint(fields[1], 10, 64)

		switch key {
		case "HugePages_Total:":
			info.StaticHugepages.TotalPages = val
		case "HugePages_Free:":
			info.StaticHugepages.FreePages = val
		case "HugePages_Rsvd:":
			info.StaticHugepages.ReservedPages = val
		case "Hugepagesize:":
			info.StaticHugepages.PageSizeKB = val
		}
	}

	info.SystemSummary.StaticHugepagesSupported = true
	info.StaticHugepages.TotalSizeMB = float64(info.StaticHugepages.TotalPages*info.StaticHugepages.PageSizeKB) / 1024.0

	if info.StaticHugepages.TotalPages == 0 {
		info.StaticHugepages.UsagePct = 0.0
	} else {
		used := info.StaticHugepages.TotalPages - info.StaticHugepages.FreePages
		info.StaticHugepages.UsagePct = (float64(used) / float64(info.StaticHugepages.TotalPages)) * 100.0
	}

	// 2. Parse THP Paths
	enabledMode, err := parseTHPMode(thpEnabledPath)
	if err != nil {
		info.SystemSummary.THPEnabledMode = "unsupported"
		info.SystemSummary.THPDefragPolicy = "unsupported"
		// If THP is unsupported, we return just the static hugepage info
		return info, nil
	}
	info.SystemSummary.THPEnabledMode = enabledMode

	defragPolicy, err := parseTHPMode(thpDefragPath)
	if err != nil {
		defragPolicy = "unknown"
	}
	info.SystemSummary.THPDefragPolicy = defragPolicy

	// 3. Parse /proc/vmstat for THP stats
	thpStats := &THPStats{}
	vmstatFile, err := os.Open(vmstatPath)
	if err == nil {
		defer func() {
			_ = vmstatFile.Close()
		}()
		vScanner := bufio.NewScanner(vmstatFile)
		for vScanner.Scan() {
			fields := strings.Fields(vScanner.Text())
			if len(fields) < 2 {
				continue
			}
			val, _ := strconv.ParseUint(fields[1], 10, 64)
			switch fields[0] {
			case "thp_fault_alloc":
				thpStats.FaultAllocations = val
			case "thp_fault_fallback":
				thpStats.FaultFallbacks = val
			case "thp_collapse_alloc":
				thpStats.CollapseAllocations = val
			}
		}
	}

	// Evaluate stalling risk
	isStallingRisk := false
	if enabledMode == "always" && defragPolicy == "always" {
		isStallingRisk = true
	} else if thpStats.FaultFallbacks > 0 {
		isStallingRisk = true
	}
	thpStats.IsStallingRisk = isStallingRisk

	info.THPStats = thpStats

	return info, nil
}

// parseTHPMode reads a file like "always [madvise] never" and extracts the selected mode.
func parseTHPMode(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	// Data looks like "always [madvise] never\n" or similar
	content := strings.TrimSpace(string(data))
	start := strings.IndexByte(content, '[')
	end := strings.IndexByte(content, ']')

	if start != -1 && end != -1 && end > start {
		return content[start+1 : end], nil
	}

	return "", fmt.Errorf("failed to parse mode from %s", content)
}
