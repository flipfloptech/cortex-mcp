package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// readAheadWarningThreshold is the read-ahead value (in KB) at or above which
// a tuning warning is emitted. 4096 KB is almost always a misconfiguration on
// modern NVMe drives and causes cache thrashing on Lustre OSS nodes.
const readAheadWarningThreshold = 4096

// SchedulerInfo holds parsed scheduler and read-ahead data for a single block device.
type SchedulerInfo struct {
	DeviceName          string   `json:"device_name"`
	ActiveScheduler     string   `json:"active_scheduler"`
	AvailableSchedulers []string `json:"available_schedulers"`
	ReadAheadKB         int      `json:"read_ahead_kb"`
	TuningWarning       bool     `json:"tuning_warning"`
	WarningReasons      []string `json:"warning_reasons,omitempty"`
}

// SchedulerSummary provides system-level aggregate counts.
type SchedulerSummary struct {
	DevicesAudited         int `json:"devices_audited"`
	TuningWarningsDetected int `json:"tuning_warnings_detected"`
}

// SchedulerPayload is the complete JSON output for the scheduler audit.
type SchedulerPayload struct {
	SystemSummary SchedulerSummary `json:"system_summary"`
	Devices       []SchedulerInfo  `json:"devices"`
}

// GetSchedulerInfo iterates /sys/block/, filters devices, and returns the
// complete scheduler and read-ahead audit payload.
func GetSchedulerInfo(sysfsBase string) (*SchedulerPayload, error) {
	blockDir := filepath.Join(sysfsBase, "block")
	entries, err := os.ReadDir(blockDir)
	if err != nil {
		return nil, fmt.Errorf("read block dir: %w", err)
	}

	var devices []SchedulerInfo
	warningCount := 0

	for _, entry := range entries {
		name := entry.Name()
		if !isSchedulerDevice(sysfsBase, name) {
			continue
		}

		raw := readScheduler(sysfsBase, name)
		active, available := parseSchedulerString(raw)

		info := SchedulerInfo{
			DeviceName:          name,
			ActiveScheduler:     active,
			AvailableSchedulers: available,
			ReadAheadKB:         readReadAheadKB(sysfsBase, name),
		}

		evaluateWarnings(&info)
		if info.TuningWarning {
			warningCount++
		}

		devices = append(devices, info)
	}

	sort.Slice(devices, func(i, j int) bool {
		return devices[i].DeviceName < devices[j].DeviceName
	})

	// Ensure non-nil slice for clean JSON serialization.
	if devices == nil {
		devices = []SchedulerInfo{}
	}

	return &SchedulerPayload{
		SystemSummary: SchedulerSummary{
			DevicesAudited:         len(devices),
			TuningWarningsDetected: warningCount,
		},
		Devices: devices,
	}, nil
}

// parseSchedulerString parses the kernel's scheduler format string.
// Input: "[mq-deadline] kyber bfq none" → active="mq-deadline", available=["mq-deadline","kyber","bfq","none"]
// Handles edge cases: empty string, "none" without brackets, whitespace-only.
func parseSchedulerString(raw string) (active string, available []string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "none", []string{"none"}
	}

	fields := strings.Fields(raw)
	active = "none"

	for _, f := range fields {
		if strings.HasPrefix(f, "[") && strings.HasSuffix(f, "]") {
			// Extract the bracketed active scheduler.
			active = f[1 : len(f)-1]
			available = append(available, active)
		} else {
			available = append(available, f)
		}
	}

	if len(available) == 0 {
		return "none", []string{"none"}
	}

	return active, available
}

// readScheduler reads /sys/block/<dev>/queue/scheduler and returns the raw
// trimmed content. Returns an empty string if the file is missing or unreadable.
func readScheduler(sysfsBase, devName string) string {
	path := filepath.Join(sysfsBase, "block", devName, "queue", "scheduler")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// readReadAheadKB reads /sys/block/<dev>/queue/read_ahead_kb and returns the
// integer value. Returns 0 if the file is missing or contains invalid data.
func readReadAheadKB(sysfsBase, devName string) int {
	path := filepath.Join(sysfsBase, "block", devName, "queue", "read_ahead_kb")
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	val, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return val
}

// isSchedulerDevice returns true if the block device should be included in the
// scheduler audit. Excludes virtual pseudo-devices (loop, ram, zram, nbd) and
// partitions. Includes physical disks, dm-*, and md* logical volumes.
func isSchedulerDevice(sysfsBase, name string) bool {
	// Exclude known virtual pseudo-device prefixes.
	for _, prefix := range []string{"loop", "ram", "zram", "nbd"} {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}

	// Exclude partitions by checking for the sysfs partition marker file.
	partFile := filepath.Join(sysfsBase, "block", name, "partition")
	if _, err := os.Stat(partFile); err == nil {
		return false // partition file exists → this is a partition
	}

	return true
}

// evaluateWarnings applies heuristic rules to detect sub-optimal scheduler and
// read-ahead configurations and mutates the SchedulerInfo in place.
func evaluateWarnings(info *SchedulerInfo) {
	// Rule 1: NVMe devices should use the "none" scheduler.
	// PCIe NVMe drives have massive internal parallel queues; OS-level scheduling
	// (mq-deadline, bfq, kyber) wastes CPU time re-ordering I/O that the drive's
	// firmware handles natively.
	if strings.HasPrefix(info.DeviceName, "nvme") && info.ActiveScheduler != "none" {
		info.WarningReasons = append(info.WarningReasons,
			fmt.Sprintf("NVMe device should use the 'none' scheduler; '%s' adds unnecessary CPU overhead", info.ActiveScheduler))
	}

	// Rule 2: High read-ahead causes cache thrashing.
	// Lustre clients implement their own read-ahead algorithms. If the underlying
	// OSS block devices also have large read_ahead_kb values, the kernel wastes
	// RAM and PCIe bandwidth reading data that was never requested.
	if info.ReadAheadKB >= readAheadWarningThreshold {
		info.WarningReasons = append(info.WarningReasons,
			fmt.Sprintf("High read-ahead (%d KB) may cause cache thrashing", info.ReadAheadKB))
	}

	info.TuningWarning = len(info.WarningReasons) > 0
}
