package uptime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// UptimeTool provides system uptime telemetry parsed and formatted for LLM consumption.
type UptimeTool struct{}

// New returns a new instance of the UptimeTool.
func New() *UptimeTool {
	return &UptimeTool{}
}

func init() {
	registry.Register(New())
}

// Name returns the unique identifier for this tool.
func (t *UptimeTool) Name() string {
	return "uptime"
}

// Category returns the grouping category for this tool.
func (t *UptimeTool) Category() string {
	return "system"
}

// Description provides a short summary for the MCP catalog.
func (t *UptimeTool) Description() string {
	return "Get system uptime and idle statistics."
}

// Help provides detailed usage instructions and schema information.
func (t *UptimeTool) Help() string {
	return `Gathers system uptime metrics from /proc/uptime.

To assist with LLM consumption, this tool automatically parses the raw seconds into
human-readable durations (days, hours, minutes, seconds) and calculates the overall
idle percentage, preventing the need for complex mathematical calculations by the caller.

Returns a structured object containing:
- uptime_seconds: Raw uptime in seconds (float64)
- idle_seconds: Raw idle time in seconds (float64, combined across all cores)
- idle_percent: Idle time as a percentage of total possible core time (float64)
- human_readable: Pre-formatted natural language string of the uptime duration

Data Source: /proc/uptime`
}

// Parameters returns the parameter schema (none for uptime).
func (t *UptimeTool) Parameters() []registry.ToolParam {
	return nil
}

// Hidden returns false; uptime is a public tool.
func (t *UptimeTool) Hidden() bool { return false }

// IsSupported checks if the node can execute this tool.
// Requires the existence of /proc/uptime.
func (t *UptimeTool) IsSupported() (bool, string) {
	if !registry.IsLinux() {
		return false, "requires linux operating system"
	}
	if !registry.PathExists("/proc/uptime") {
		return false, "/proc/uptime is missing"
	}
	return true, ""
}

// UptimeData represents the structured output of the uptime tool.
type UptimeData struct {
	UptimeSeconds float64 `json:"uptime_seconds"`
	IdleSeconds   float64 `json:"idle_seconds"`
	IdlePercent   float64 `json:"idle_percent"`
	HumanReadable string  `json:"human_readable"`
}

// Execute performs the tool's operation.
func (t *UptimeTool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}

	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return registry.NewErrorResult(t.Name(), hostname, fmt.Sprintf("failed to read /proc/uptime: %v", err)), nil
	}

	parts := strings.Fields(string(data))
	if len(parts) < 2 {
		return registry.NewErrorResult(t.Name(), hostname, "invalid format in /proc/uptime"), nil
	}

	uptimeSecs, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return registry.NewErrorResult(t.Name(), hostname, fmt.Sprintf("failed to parse uptime: %v", err)), nil
	}

	idleSecs, err := strconv.ParseFloat(parts[1], 64)
	if err != nil {
		return registry.NewErrorResult(t.Name(), hostname, fmt.Sprintf("failed to parse idle time: %v", err)), nil
	}

	numCPUs := float64(runtime.NumCPU())
	idlePercent := 0.0
	if uptimeSecs > 0 && numCPUs > 0 {
		idlePercent = (idleSecs / (uptimeSecs * numCPUs)) * 100.0
	}

	humanReadable := formatDuration(uptimeSecs)

	out := UptimeData{
		UptimeSeconds: uptimeSecs,
		IdleSeconds:   idleSecs,
		IdlePercent:   idlePercent,
		HumanReadable: humanReadable,
	}

	summary := fmt.Sprintf("%s (idle: %.1f%%)", humanReadable, idlePercent)

	result := registry.NewResult(
		t.Name(),
		hostname,
		registry.StatusOK,
		summary,
		out,
	)
	result.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()

	return result, nil
}

// formatDuration converts seconds into a human-readable string (e.g. "4 days, 2 hours, 15 minutes").
func formatDuration(seconds float64) string {
	d := time.Duration(seconds * float64(time.Second))

	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	secs := int(d.Seconds()) % 60

	var parts []string
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%d days", days))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%d hours", hours))
	}
	if minutes > 0 {
		parts = append(parts, fmt.Sprintf("%d minutes", minutes))
	}
	// Always include seconds if everything else is 0, or just to be precise
	if secs > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%d seconds", secs))
	}

	return strings.Join(parts, ", ")
}
