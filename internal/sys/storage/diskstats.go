package storage

import (
	"bufio"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DiskStat represents a single line from /proc/diskstats
type DiskStat struct {
	Major           int
	Minor           int
	DeviceName      string
	ReadsCompleted  uint64
	SectorsRead     uint64
	WritesCompleted uint64
	SectorsWritten  uint64
	QueueDepth      int
	TimeInIOMs      uint64
}

// DeviceIOMetrics represents the calculated metrics for a device
type DeviceIOMetrics struct {
	DeviceName        string  `json:"device_name"`
	ReadIOPS          float64 `json:"read_iops"`
	WriteIOPS         float64 `json:"write_iops"`
	ReadMBs           float64 `json:"read_mbs"`
	WriteMBs          float64 `json:"write_mbs"`
	AvgLatencyMs      float64 `json:"avg_latency_ms"`
	QueueDepthCurrent int     `json:"queue_depth_current"`
	QueueDepthMax     *int    `json:"queue_depth_max,omitempty"`
	UtilizationPct    float64 `json:"utilization_pct"`
}

// SystemSummary represents the aggregated summary
type SystemSummary struct {
	TotalReadMBs       float64  `json:"total_read_mbs"`
	TotalWriteMBs      float64  `json:"total_write_mbs"`
	HighLatencyDevices []string `json:"high_latency_devices"`
}

// DiskIOPayload represents the final JSON payload
type DiskIOPayload struct {
	SystemSummary SystemSummary     `json:"system_summary"`
	Devices       []DeviceIOMetrics `json:"devices"`
}

// ParseDiskStats parses the given /proc/diskstats file
func ParseDiskStats(path string) ([]DiskStat, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	var stats []DiskStat
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 14 {
			continue // Valid diskstats line has at least 14 fields
		}

		major, _ := strconv.Atoi(fields[0])
		minor, _ := strconv.Atoi(fields[1])
		reads, _ := strconv.ParseUint(fields[3], 10, 64)
		sectorsRead, _ := strconv.ParseUint(fields[5], 10, 64)
		writes, _ := strconv.ParseUint(fields[7], 10, 64)
		sectorsWritten, _ := strconv.ParseUint(fields[9], 10, 64)
		queue, _ := strconv.Atoi(fields[11])
		timeMs, _ := strconv.ParseUint(fields[12], 10, 64)

		stats = append(stats, DiskStat{
			Major:           major,
			Minor:           minor,
			DeviceName:      fields[2],
			ReadsCompleted:  reads,
			SectorsRead:     sectorsRead,
			WritesCompleted: writes,
			SectorsWritten:  sectorsWritten,
			QueueDepth:      queue,
			TimeInIOMs:      timeMs,
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return stats, nil
}

// GetQueueDepthMax reads /sys/block/<dev>/queue/nr_requests
func GetQueueDepthMax(sysfsRoot, devName string) (*int, error) {
	path := filepath.Join(sysfsRoot, "block", devName, "queue", "nr_requests")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // Gracefully return nil if not present
		}
		return nil, err
	}

	val, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, err
	}
	return &val, nil
}

// isIgnoredDevice checks if the device should be filtered out
func isIgnoredDevice(name string) bool {
	prefixes := []string{"loop", "ram", "zram", "nbd"}
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// CalculateMetrics computes the deltas and translates them to metrics
func CalculateMetrics(start, end []DiskStat, interval time.Duration) []DeviceIOMetrics {
	startMap := make(map[string]DiskStat)
	for _, s := range start {
		startMap[s.DeviceName] = s
	}

	var metrics []DeviceIOMetrics
	intervalSecs := interval.Seconds()
	intervalMs := float64(interval.Milliseconds())

	if intervalSecs <= 0 {
		return metrics
	}

	for _, e := range end {
		if isIgnoredDevice(e.DeviceName) {
			continue
		}

		s, ok := startMap[e.DeviceName]
		if !ok {
			continue // New device appeared during interval
		}

		readOps := float64(e.ReadsCompleted - s.ReadsCompleted)
		writeOps := float64(e.WritesCompleted - s.WritesCompleted)
		totalOps := readOps + writeOps

		readBytes := float64(e.SectorsRead-s.SectorsRead) * 512.0
		writeBytes := float64(e.SectorsWritten-s.SectorsWritten) * 512.0

		deltaTimeMs := float64(e.TimeInIOMs - s.TimeInIOMs)

		var avgLatencyMs float64
		if totalOps > 0 {
			avgLatencyMs = deltaTimeMs / totalOps
		}

		utilPct := 0.0
		if intervalMs > 0 {
			utilPct = math.Min(100.0, (deltaTimeMs/intervalMs)*100.0)
		}

		metrics = append(metrics, DeviceIOMetrics{
			DeviceName:        e.DeviceName,
			ReadIOPS:          readOps / intervalSecs,
			WriteIOPS:         writeOps / intervalSecs,
			ReadMBs:           readBytes / 1000000.0 / intervalSecs, // Using decimal MBs
			WriteMBs:          writeBytes / 1000000.0 / intervalSecs,
			AvgLatencyMs:      avgLatencyMs,
			QueueDepthCurrent: e.QueueDepth,
			UtilizationPct:    utilPct,
		})
	}

	return metrics
}

// AggregateSummary creates the system-wide summary
func AggregateSummary(metrics []DeviceIOMetrics, latencyThresholdMs float64) SystemSummary {
	var summary SystemSummary
	summary.HighLatencyDevices = []string{}

	for _, m := range metrics {
		summary.TotalReadMBs += m.ReadMBs
		summary.TotalWriteMBs += m.WriteMBs

		if m.AvgLatencyMs > latencyThresholdMs {
			summary.HighLatencyDevices = append(summary.HighLatencyDevices, m.DeviceName)
		}
	}

	return summary
}
