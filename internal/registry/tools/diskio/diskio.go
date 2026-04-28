package diskio

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
	"github.com/flipfloptech/cortex-mcp/internal/sys/storage"
)

type Tool struct {
	procfsRoot    string
	sysfsRoot     string
	sleepInterval time.Duration
}

func New() *Tool {
	return &Tool{
		procfsRoot:    "/proc",
		sysfsRoot:     "/sys",
		sleepInterval: 500 * time.Millisecond,
	}
}

func init() {
	registry.Register(New())
}

func (t *Tool) Name() string {
	return "get_disk_io_stats"
}

func (t *Tool) Description() string {
	return "Get high-resolution disk I/O performance metrics to identify storage bottlenecks."
}

func (t *Tool) Help() string {
	return `Provides high-resolution I/O metrics by sampling /proc/diskstats and calculating precise deltas for throughput, IOPS, and latency. Automatically filters out virtual loopbacks and RAM disks.`
}

func (t *Tool) Category() string {
	return "Storage"
}

func (t *Tool) Hidden() bool {
	return false
}

func (t *Tool) Parameters() []registry.ToolParam {
	return []registry.ToolParam{
		{
			Name:        "latency_threshold_ms",
			Type:        "number",
			Description: "Threshold in milliseconds to consider a device 'high latency'. Defaults to 20.0.",
			Required:    false,
		},
	}
}

func (t *Tool) IsSupported() (bool, string) {
	_, err := os.Stat(t.procfsRoot + "/diskstats")
	if err != nil {
		return false, "/proc/diskstats is missing"
	}
	return true, ""
}

type toolArgs struct {
	LatencyThresholdMs *float64 `json:"latency_threshold_ms"`
}

func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	var parsedArgs toolArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &parsedArgs); err != nil {
			return registry.NewErrorResult(t.Name(), "failed to parse arguments"), nil
		}
	}

	threshold := 20.0
	if parsedArgs.LatencyThresholdMs != nil {
		threshold = *parsedArgs.LatencyThresholdMs
	}

	startStats, err := storage.ParseDiskStats(t.procfsRoot + "/diskstats")
	if err != nil {
		return registry.NewErrorResult(t.Name(), "failed to parse diskstats: "+err.Error()), nil
	}

	// Sleep context-aware
	select {
	case <-ctx.Done():
		return registry.NewErrorResult(t.Name(), "context canceled during sampling"), nil
	case <-time.After(t.sleepInterval):
	}

	endStats, err := storage.ParseDiskStats(t.procfsRoot + "/diskstats")
	if err != nil {
		return registry.NewErrorResult(t.Name(), "failed to parse diskstats: "+err.Error()), nil
	}

	metrics := storage.CalculateMetrics(startStats, endStats, t.sleepInterval)

	// Decorate with queue depth maximums
	for i := range metrics {
		depth, _ := storage.GetQueueDepthMax(t.sysfsRoot, metrics[i].DeviceName)
		metrics[i].QueueDepthMax = depth
	}

	summary := storage.AggregateSummary(metrics, threshold)

	payload := storage.DiskIOPayload{
		SystemSummary: summary,
		Devices:       metrics,
	}

	return registry.NewResult(t.Name(), registry.StatusOK, "Gathered disk I/O metrics", payload), nil
}
