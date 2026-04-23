package memory

import (
	"bufio"
	"context"
	"io"
	"os"
	"strconv"
	"strings"
)

type SystemSummary struct {
	FragmentationScore int `json:"fragmentation_score"`
	TotalFreeMB        int `json:"total_free_mb"`
}

type ZoneInfo struct {
	Node               int    `json:"node"`
	Zone               string `json:"zone"`
	FragmentationScore int    `json:"fragmentation_score"`
	TotalFreeMB        int    `json:"total_free_mb"`
	Orders             []int  `json:"orders"`
}

type BuddyInfoResult struct {
	SystemSummary SystemSummary `json:"system_summary"`
	Zones         []ZoneInfo    `json:"zones"`
}

func IsBuddyInfoSupported() bool {
	if _, err := os.Stat("/proc/buddyinfo"); err != nil {
		return false
	}
	return true
}

func GetBuddyInfo(ctx context.Context) (*BuddyInfoResult, error) {
	file, err := os.Open("/proc/buddyinfo")
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	return ParseBuddyInfo(ctx, file)
}

func ParseBuddyInfo(ctx context.Context, r io.Reader) (*BuddyInfoResult, error) {
	var zones []ZoneInfo
	var globalBytes0to3 int64
	var globalBytesTotal int64

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		parts := strings.Fields(line)
		if len(parts) < 4 {
			continue
		}

		nodeStr := strings.TrimSuffix(parts[1], ",")
		nodeID, err := strconv.Atoi(nodeStr)
		if err != nil {
			continue
		}

		zoneName := parts[3]

		orderCounts := make([]int, 0, len(parts)-4)
		for i := 4; i < len(parts); i++ {
			count, _ := strconv.Atoi(parts[i])
			orderCounts = append(orderCounts, count)
		}

		var zoneBytes0to3 int64
		var zoneBytesTotal int64

		for order, count := range orderCounts {
			bytesInOrder := int64(count) * (1 << order) * 4096
			zoneBytesTotal += bytesInOrder
			if order <= 3 {
				zoneBytes0to3 += bytesInOrder
			}
		}

		zoneScore := 0
		if zoneBytesTotal > 0 {
			zoneScore = int((zoneBytes0to3 * 100) / zoneBytesTotal)
		}

		zones = append(zones, ZoneInfo{
			Node:               nodeID,
			Zone:               zoneName,
			FragmentationScore: zoneScore,
			TotalFreeMB:        int(zoneBytesTotal / (1024 * 1024)),
			Orders:             orderCounts,
		})

		globalBytes0to3 += zoneBytes0to3
		globalBytesTotal += zoneBytesTotal
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	globalScore := 0
	if globalBytesTotal > 0 {
		globalScore = int((globalBytes0to3 * 100) / globalBytesTotal)
	}

	result := &BuddyInfoResult{
		SystemSummary: SystemSummary{
			FragmentationScore: globalScore,
			TotalFreeMB:        int(globalBytesTotal / (1024 * 1024)),
		},
		Zones: zones,
	}

	return result, nil
}
