package hardware

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// EDACSystemSummary provides a high-level overview of system RAM health.
type EDACSystemSummary struct {
	EDACSubsystemActive      bool    `json:"edac_subsystem_active"`
	TotalCorrectableErrors   uint64  `json:"total_correctable_errors"`
	TotalUncorrectableErrors uint64  `json:"total_uncorrectable_errors"`
	HealthStatus             string  `json:"health_status"`
	SystemUptimeSeconds      float64 `json:"system_uptime_seconds,omitempty"`
}

// FailingDIMM represents a single memory module reporting errors.
type FailingDIMM struct {
	Label    string `json:"label,omitempty"`
	DevType  string `json:"dimm_dev_type,omitempty"`
	CECount  uint64 `json:"ce_count"`
	UECount  uint64 `json:"ue_count,omitempty"`
	Location string `json:"location"`
}

// EDACController represents a memory controller and its associated DIMMs.
type EDACController struct {
	ControllerID int           `json:"controller_id"`
	CECount      uint64        `json:"ce_count"`
	UECount      uint64        `json:"ue_count"`
	FailingDIMMs []FailingDIMM `json:"failing_dimms"`
}

// EDACErrorsInfo encapsulates the complete EDAC error report.
type EDACErrorsInfo struct {
	SystemSummary EDACSystemSummary `json:"system_summary"`
	Controllers   []EDACController  `json:"controllers,omitempty"`
}

// GetEDACErrorsInfo returns the memory error diagnostics.
func GetEDACErrorsInfo() (*EDACErrorsInfo, error) {
	// Parse uptime
	uptimeData, err := os.ReadFile("/proc/uptime")
	var uptime float64
	if err == nil {
		fields := strings.Fields(string(uptimeData))
		if len(fields) > 0 {
			uptime, _ = strconv.ParseFloat(fields[0], 64)
		}
	}

	return getEDACErrorsInfoFromPath("/sys/devices/system/edac/mc", uptime)
}

func getEDACErrorsInfoFromPath(edacRoot string, uptime float64) (*EDACErrorsInfo, error) {
	info := &EDACErrorsInfo{
		SystemSummary: EDACSystemSummary{
			SystemUptimeSeconds: uptime,
		},
	}

	// Check if EDAC is loaded
	if _, err := os.Stat(edacRoot); os.IsNotExist(err) {
		info.SystemSummary.EDACSubsystemActive = false
		info.SystemSummary.HealthStatus = "degraded"
		return info, nil
	}

	// Read mc directories
	entries, err := os.ReadDir(edacRoot)
	if err != nil {
		info.SystemSummary.EDACSubsystemActive = false
		info.SystemSummary.HealthStatus = "degraded"
		return info, nil
	}

	info.SystemSummary.EDACSubsystemActive = true
	var controllers []EDACController

	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "mc") {
			continue
		}

		mcIDStr := strings.TrimPrefix(entry.Name(), "mc")
		mcID, err := strconv.Atoi(mcIDStr)
		if err != nil {
			continue
		}

		mcPath := filepath.Join(edacRoot, entry.Name())
		ceCount := readUint64Safe(filepath.Join(mcPath, "ce_count"))
		ueCount := readUint64Safe(filepath.Join(mcPath, "ue_count"))

		info.SystemSummary.TotalCorrectableErrors += ceCount
		info.SystemSummary.TotalUncorrectableErrors += ueCount

		ctrl := EDACController{
			ControllerID: mcID,
			CECount:      ceCount,
			UECount:      ueCount,
		}

		// Only parse DIMMs if there are errors on this controller to keep output clean,
		// but since we want to know *which* ones are failing, we do it.
		if ceCount > 0 || ueCount > 0 {
			ctrl.FailingDIMMs = parseFailingDIMMs(mcPath)
		}

		controllers = append(controllers, ctrl)
	}

	info.Controllers = controllers

	// Evaluate Health Status
	if info.SystemSummary.TotalUncorrectableErrors > 0 {
		info.SystemSummary.HealthStatus = "critical"
	} else if info.SystemSummary.TotalCorrectableErrors > 0 {
		info.SystemSummary.HealthStatus = "warning"
	} else {
		info.SystemSummary.HealthStatus = "healthy"
	}

	return info, nil
}

func parseFailingDIMMs(mcPath string) []FailingDIMM {
	var dimms []FailingDIMM

	entries, err := os.ReadDir(mcPath)
	if err != nil {
		return dimms
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !strings.HasPrefix(name, "dimm") && !strings.HasPrefix(name, "rank") && !strings.HasPrefix(name, "csrow") {
			continue
		}

		dimmPath := filepath.Join(mcPath, name)

		// Fallback for csrow ce_count naming
		ce := readUint64Safe(filepath.Join(dimmPath, "dimm_ce_count"))
		if ce == 0 {
			ce = readUint64Safe(filepath.Join(dimmPath, "ce_count"))
		}

		ue := readUint64Safe(filepath.Join(dimmPath, "dimm_ue_count"))
		if ue == 0 {
			ue = readUint64Safe(filepath.Join(dimmPath, "ue_count"))
		}

		if ce > 0 || ue > 0 {
			label := readStringSafe(filepath.Join(dimmPath, "dimm_label"))
			devType := readStringSafe(filepath.Join(dimmPath, "dimm_dev_type"))

			// Some location resolution could be better if we parse more tree, but
			// usually it's just mcX/dimmY
			location := filepath.Base(mcPath) + "/" + name

			dimms = append(dimms, FailingDIMM{
				Label:    label,
				DevType:  devType,
				CECount:  ce,
				UECount:  ue,
				Location: location,
			})
		}
	}

	return dimms
}

func readUint64Safe(path string) uint64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	val, _ := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	return val
}

func readStringSafe(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(bytes.Trim(data, "\x00")))
}
