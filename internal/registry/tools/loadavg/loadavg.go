package loadavg

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// LoadAvgData represents the structured output of the loadavg tool.
type LoadAvgData struct {
	Load1            float64 `json:"load_1"`
	Load5            float64 `json:"load_5"`
	Load15           float64 `json:"load_15"`
	RunnableEntities int     `json:"runnable_entities"`
	TotalEntities    int     `json:"total_entities"`
	LastPID          int     `json:"last_pid"`
}

// LoadAvgTool implements registry.Tool to read and parse /proc/loadavg.
type LoadAvgTool struct{}

// New returns a new instance of the LoadAvgTool.
func New() *LoadAvgTool {
	return &LoadAvgTool{}
}

func init() {
	registry.Register(registry.WithCache(15*time.Second, New()))
}

// Name returns the unique identifier for this tool.
func (t *LoadAvgTool) Name() string {
	return "loadavg"
}

// Category returns the tool category.
func (t *LoadAvgTool) Category() string {
	return "system"
}

// Help returns usage instructions and documentation for LLM consumption.
func (t *LoadAvgTool) Help() string {
	return `Reads the system load averages from /proc/loadavg.

Provides the 1-minute, 5-minute, and 15-minute load averages, as well as the
ratio of currently runnable scheduling entities to the total number of scheduling
entities, and the PID of the most recently created process.

Data Source: /proc/loadavg`
}

// Description returns a brief one-line summary.
func (t *LoadAvgTool) Description() string {
	return "Read system load averages (1m, 5m, 15m) and scheduling entities"
}

// Parameters returns the parameter schema (none for loadavg).
func (t *LoadAvgTool) Parameters() []registry.ToolParam {
	return nil
}

// Hidden returns false; loadavg is a public tool.
func (t *LoadAvgTool) Hidden() bool { return false }

// IsSupported checks if the node can execute this tool.
func (t *LoadAvgTool) IsSupported() (bool, string) {
	if !registry.IsLinux() {
		return false, "requires Linux"
	}
	if !registry.PathExists("/proc/loadavg") {
		return false, "/proc/loadavg is missing"
	}
	return true, ""
}

// Execute performs the tool's operation.
func (t *LoadAvgTool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}

	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return registry.NewErrorResult(t.Name(), hostname, fmt.Sprintf("failed to read /proc/loadavg: %v", err)), nil
	}

	out, err := parseLoadAvg(data)
	if err != nil {
		return registry.NewErrorResult(t.Name(), hostname, fmt.Sprintf("failed to parse loadavg: %v", err)), nil
	}

	summary := fmt.Sprintf("Load: %.2f %.2f %.2f (Entities: %d/%d)", out.Load1, out.Load5, out.Load15, out.RunnableEntities, out.TotalEntities)

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

// parseLoadAvg parses the raw bytes from /proc/loadavg into a LoadAvgData struct.
// Example format: 0.68 0.69 0.74 1/863 12345
func parseLoadAvg(data []byte) (LoadAvgData, error) {
	parts := strings.Fields(strings.TrimSpace(string(data)))
	if len(parts) != 5 {
		return LoadAvgData{}, fmt.Errorf("invalid format in /proc/loadavg")
	}

	var out LoadAvgData
	var err error

	out.Load1, err = strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return LoadAvgData{}, fmt.Errorf("failed to parse load1: %w", err)
	}

	out.Load5, err = strconv.ParseFloat(parts[1], 64)
	if err != nil {
		return LoadAvgData{}, fmt.Errorf("failed to parse load5: %w", err)
	}

	out.Load15, err = strconv.ParseFloat(parts[2], 64)
	if err != nil {
		return LoadAvgData{}, fmt.Errorf("failed to parse load15: %w", err)
	}

	entities := strings.Split(parts[3], "/")
	if len(entities) != 2 {
		return LoadAvgData{}, fmt.Errorf("invalid entities format: %s", parts[3])
	}

	out.RunnableEntities, err = strconv.Atoi(entities[0])
	if err != nil {
		return LoadAvgData{}, fmt.Errorf("failed to parse runnable entities: %w", err)
	}

	out.TotalEntities, err = strconv.Atoi(entities[1])
	if err != nil {
		return LoadAvgData{}, fmt.Errorf("failed to parse total entities: %w", err)
	}

	out.LastPID, err = strconv.Atoi(parts[4])
	if err != nil {
		return LoadAvgData{}, fmt.Errorf("failed to parse last pid: %w", err)
	}

	return out, nil
}
