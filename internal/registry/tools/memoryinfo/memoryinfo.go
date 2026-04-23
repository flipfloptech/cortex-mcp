package memoryinfo

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// Allow test override of the data source
var meminfoPath = "/proc/meminfo"

func init() {
	registry.Register(registry.WithCache(5*time.Second, &MemoryInfoTool{}))
}

// MemoryInfoTool gathers heavily curated, mathematical memory usage information.
type MemoryInfoTool struct{}

// Name returns the unique tool identifier.
func (t *MemoryInfoTool) Name() string { return "get_memory_info" }

// Description returns a short summary for get_tool_list output.
func (t *MemoryInfoTool) Description() string {
	return "Get a highly structured, mathematical reading of system memory and swap usage"
}

// Help returns the full tool help text.
func (t *MemoryInfoTool) Help() string {
	return `get_memory_info — Get System Memory Information

Returns a highly curated, mathematical summary of system memory.
This is a math-free alternative to the 'free' command designed for LLMs,
standardizing all units to Megabytes (MB) as raw integers for accurate arithmetic.

Data Sources:
  - /proc/meminfo

Mathematical Models / Formatting:
  - True Used: Calculates MemTotal - MemAvailable (or MemTotal - MemFree - Buffers - Cached on older kernels) to give the exact amount of memory actively consumed by applications. This prevents hallucinating that Linux page caches are "wasted" memory.
  - Swap Ratio: Calculates (SwapTotal - SwapFree) / SwapTotal * 100 as a float rounded to two decimal places.
  - Swap Active: A boolean flag instantly drawing attention to potential paging/thrashing.

Degradation Profile:
  Older kernels (pre-3.14) do not expose MemAvailable. The tool handles this
  gracefully by falling back to legacy math (Free + Buffers + Cached) and flags
  the payload with "estimation_mode": "legacy". Otherwise, "standard".

Output format:
  {
    "total_mb": 257744,
    "true_used_mb": 184320,
    "available_mb": 73424,
    "swap_total_mb": 8192,
    "swap_ratio_pct": 12.54,
    "swap_active": true,
    "estimation_mode": "standard"
  }

Parameters: None
Supported on: Linux`
}

// Category returns the tool category.
func (t *MemoryInfoTool) Category() string { return "memory" }

// Parameters returns the parameter schema (parameterless).
func (t *MemoryInfoTool) Parameters() []registry.ToolParam { return nil }

// Hidden returns false; it is a public tool.
func (t *MemoryInfoTool) Hidden() bool { return false }

// IsSupported checks if this tool can operate on the current node.
func (t *MemoryInfoTool) IsSupported() (bool, string) {
	if !registry.IsLinux() {
		return false, "requires Linux (running on " + runtime.GOOS + ")"
	}
	// We also ensure the data source exists.
	if _, err := os.Stat(meminfoPath); err != nil {
		return false, "requires " + meminfoPath
	}
	return true, ""
}

// memoryInfoData is the standardized JSON payload.
type memoryInfoData struct {
	TotalMB        int64   `json:"total_mb"`
	TrueUsedMB     int64   `json:"true_used_mb"`
	AvailableMB    int64   `json:"available_mb"`
	SwapTotalMB    int64   `json:"swap_total_mb"`
	SwapRatioPct   float64 `json:"swap_ratio_pct"`
	SwapActive     bool    `json:"swap_active"`
	EstimationMode string  `json:"estimation_mode"`
}

// Execute gathers the memory information and returns a standardized ToolResult.
func (t *MemoryInfoTool) Execute(_ context.Context, _ json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()
	hostname, _ := os.Hostname()

	f, err := os.Open(meminfoPath)
	if err != nil {
		// Encapsulate error cleanly into ToolResult without halting MCP.
		result := registry.NewErrorResult(t.Name(), hostname, "failed to open "+meminfoPath+": "+err.Error())
		result.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
		return result, nil
	}
	defer func() { _ = f.Close() }()

	data, err := ParseMemInfo(f)
	if err != nil {
		result := registry.NewErrorResult(t.Name(), hostname, "failed to parse meminfo: "+err.Error())
		result.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
		return result, nil
	}

	result := registry.NewResult(
		t.Name(),
		hostname,
		registry.StatusOK,
		"Memory usage: "+strconv.FormatInt(data.TrueUsedMB, 10)+"MB / "+strconv.FormatInt(data.TotalMB, 10)+"MB",
		data,
	)
	result.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	result.Metadata.FilteringMethod = "deterministic"

	return result, nil
}

// ParseMemInfo reads the meminfo file and calculates the standardized metrics.
// Exposed for testing so we don't rely on JSON parsing in the tests for logic validation.
func ParseMemInfo(file io.Reader) (memoryInfoData, error) {
	var totalKB, availableKB, freeKB, buffersKB, cachedKB, swapTotalKB, swapFreeKB int64
	var hasAvailable bool

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) == 0 {
			continue
		}

		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		key := strings.TrimRight(parts[0], ":")
		val, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			continue // skip invalid lines safely
		}

		switch key {
		case "MemTotal":
			totalKB = val
		case "MemAvailable":
			availableKB = val
			hasAvailable = true
		case "MemFree":
			freeKB = val
		case "Buffers":
			buffersKB = val
		case "Cached":
			cachedKB = val
		case "SwapTotal":
			swapTotalKB = val
		case "SwapFree":
			swapFreeKB = val
		}
	}

	if err := scanner.Err(); err != nil {
		return memoryInfoData{}, err
	}

	var trueUsedKB int64
	var mode string

	if hasAvailable {
		trueUsedKB = totalKB - availableKB
		mode = "standard"
	} else {
		availableKB = freeKB + buffersKB + cachedKB // Best effort fallback
		trueUsedKB = totalKB - availableKB
		mode = "legacy"
	}

	if trueUsedKB < 0 {
		trueUsedKB = 0
	}
	if availableKB < 0 {
		availableKB = 0
	}

	// Calculate swap
	var swapRatio float64
	swapUsedKB := swapTotalKB - swapFreeKB
	if swapTotalKB > 0 && swapUsedKB > 0 {
		swapRatio = float64(swapUsedKB) / float64(swapTotalKB) * 100.0
		// Round to 2 decimal places
		swapRatio = math.Round(swapRatio*100) / 100
	}

	return memoryInfoData{
		TotalMB:        totalKB / 1024,
		TrueUsedMB:     trueUsedKB / 1024,
		AvailableMB:    availableKB / 1024,
		SwapTotalMB:    swapTotalKB / 1024,
		SwapRatioPct:   swapRatio,
		SwapActive:     swapRatio > 0,
		EstimationMode: mode,
	}, nil
}
