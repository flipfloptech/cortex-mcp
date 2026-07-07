package slabinfo

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"math"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

var slabInfoPath = "/proc/slabinfo"

func init() {
	registry.Register(registry.WithCache(5*time.Second, &SlabInfoTool{}))
}

// SlabInfoTool reads and parses /proc/slabinfo to find the top kernel memory consumers.
type SlabInfoTool struct{}

func (t *SlabInfoTool) Name() string { return "get_slab_info" }

func (t *SlabInfoTool) Description() string {
	return "Get kernel object cache allocations to diagnose memory leaks and metadata exhaustion"
}

func (t *SlabInfoTool) Help() string {
	return `get_slab_info — Get Kernel Slab Info

Returns a precise map of kernel object cache allocations by calculating the true memory footprint of each slab. Returns only the top 15 consumers to help diagnose metadata exhaustion, dentry storms, or driver memory leaks.

Data Sources:
  - /proc/slabinfo (Requires Root)

Mathematical Models / Formatting:
  - True Footprint: Calculates (num_objs * objsize) and standardizes to Megabytes.
  - Fragmentation: Calculates ((num_objs - active_objs) / num_objs) * 100 to identify wasted RAM.

Degradation Profile:
  - If the process lacks root privileges (EACCES), the tool gracefully falls back to an 'error' status with a clear summary and null data array.

Parameters: None
Supported on: Linux`
}

func (t *SlabInfoTool) Category() registry.Category { return registry.CategoryMemory }

func (t *SlabInfoTool) Parameters() []registry.ToolParam { return nil }

func (t *SlabInfoTool) Hidden() bool { return false }

func (t *SlabInfoTool) IsSupported() (bool, string) {
	if !registry.IsLinux() {
		return false, "requires Linux (running on " + runtime.GOOS + ")"
	}
	// We only check if the file exists, not if we can read it, because we want to return a graceful unauthorized payload.
	if _, err := os.Stat(slabInfoPath); err != nil && os.IsNotExist(err) {
		return false, "requires " + slabInfoPath
	}
	return true, ""
}

type SystemSummary struct {
	TotalSlabCachesDetected int     `json:"total_slab_caches_detected"`
	TotalTrackedSizeMB      float64 `json:"total_tracked_size_mb"`
}

type SlabEntry struct {
	Name             string  `json:"name"`
	ActiveObjects    int64   `json:"active_objects"`
	TotalObjects     int64   `json:"total_objects"`
	ObjectSizeBytes  int64   `json:"object_size_bytes"`
	TotalSizeMB      float64 `json:"total_size_mb"`
	FragmentationPct float64 `json:"fragmentation_pct"`
}

type SlabInfoData struct {
	SystemSummary SystemSummary `json:"system_summary"`
	TopSlabs      []SlabEntry   `json:"top_slabs"`
}

func (t *SlabInfoTool) Execute(_ context.Context, _ json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	f, err := os.Open(slabInfoPath)
	if err != nil {
		if os.IsPermission(err) {
			result := registry.NewResult(
				t.Name(),
				registry.StatusError,
				"Unauthorized: Root privileges required to read /proc/slabinfo",
				nil,
			)
			result.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
			return result, nil
		}
		result := registry.NewErrorResult(t.Name(), "failed to open "+slabInfoPath+": "+err.Error())
		result.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
		return result, nil
	}
	defer func() { _ = f.Close() }()

	data, err := ParseSlabInfo(f)
	if err != nil {
		result := registry.NewErrorResult(t.Name(), "failed to parse slabinfo: "+err.Error())
		result.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
		return result, nil
	}

	result := registry.NewResult(
		t.Name(), registry.StatusOK, "Analyzed "+strconv.Itoa(data.SystemSummary.TotalSlabCachesDetected)+" slab caches",
		data,
	)
	result.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	result.Metadata.FilteringMethod = "deterministic"

	return result, nil
}

func ParseSlabInfo(file io.Reader) (*SlabInfoData, error) {
	var entries []SlabEntry
	var totalSizeMB float64

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		line = strings.TrimSpace(line)

		// Skip header lines or empty lines
		if len(line) == 0 || strings.HasPrefix(line, "slabinfo") || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.Fields(line)
		if len(parts) < 4 {
			continue
		}

		name := parts[0]
		activeObjs, err1 := strconv.ParseInt(parts[1], 10, 64)
		totalObjs, err2 := strconv.ParseInt(parts[2], 10, 64)
		objSize, err3 := strconv.ParseInt(parts[3], 10, 64)

		if err1 != nil || err2 != nil || err3 != nil {
			continue // skip invalid lines safely
		}

		totalSizeBytes := float64(totalObjs * objSize)
		sizeMB := totalSizeBytes / (1024.0 * 1024.0)
		sizeMB = math.Round(sizeMB*10) / 10.0

		var fragPct float64
		if totalObjs > 0 {
			fragPct = float64(totalObjs-activeObjs) / float64(totalObjs) * 100.0
			fragPct = math.Round(fragPct*100) / 100.0
		}

		totalSizeMB += sizeMB

		entries = append(entries, SlabEntry{
			Name:             name,
			ActiveObjects:    activeObjs,
			TotalObjects:     totalObjs,
			ObjectSizeBytes:  objSize,
			TotalSizeMB:      sizeMB,
			FragmentationPct: fragPct,
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	totalCaches := len(entries)

	// Sort descending by TotalSizeMB
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].TotalSizeMB > entries[j].TotalSizeMB
	})

	// Truncate to top 15
	if len(entries) > 15 {
		entries = entries[:15]
	}

	// If no entries, ensure it's an empty slice, not nil
	if entries == nil {
		entries = []SlabEntry{}
	}

	totalSizeMB = math.Round(totalSizeMB*10) / 10.0

	return &SlabInfoData{
		SystemSummary: SystemSummary{
			TotalSlabCachesDetected: totalCaches,
			TotalTrackedSizeMB:      totalSizeMB,
		},
		TopSlabs: entries,
	}, nil
}
