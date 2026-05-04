package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

type NVMeDrive struct {
	DeviceName        string   `json:"device_name"`
	TemperatureC      int      `json:"temperature_c"`
	PercentUsed       int      `json:"percent_used"`
	AvailableSparePct int      `json:"available_spare_pct"`
	MediaErrors       int      `json:"media_errors"`
	CriticalWarning   int      `json:"critical_warning"`
	Status            string   `json:"status"`
	WarningReasons    []string `json:"warning_reasons"`
}

type nvmeSmartLog struct {
	Temperature     int `json:"temperature"`
	PercentUsed     int `json:"percent_used"`
	AvailSpare      int `json:"avail_spare"`
	MediaErrors     int `json:"media_errors"`
	CriticalWarning int `json:"critical_warning"`
}

func DiscoverNVMeDevices(sysfsRoot string) ([]string, error) {
	classNvme := filepath.Join(sysfsRoot, "class", "nvme")
	entries, err := os.ReadDir(classNvme)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}

	var devices []string
	re := regexp.MustCompile(`^nvme\d+$`)
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			if re.MatchString(entry.Name()) {
				devices = append(devices, entry.Name())
			}
		}
	}
	sort.Strings(devices)
	return devices, nil
}

func ParseNVMeOutput(jsonData []byte, devName string) (*NVMeDrive, error) {
	var log nvmeSmartLog
	if err := json.Unmarshal(jsonData, &log); err != nil {
		return nil, err
	}

	drive := &NVMeDrive{
		DeviceName:        devName,
		PercentUsed:       log.PercentUsed,
		AvailableSparePct: log.AvailSpare,
		MediaErrors:       log.MediaErrors,
		CriticalWarning:   log.CriticalWarning,
		WarningReasons:    []string{},
	}

	// Temperature in NVMe smart-log is often reported in Kelvin.
	// 0 Kelvin is absolute zero (-273.15C). Kelvins are generally absolute, no negatives.
	drive.TemperatureC = log.Temperature - 273

	if drive.TemperatureC > 75 {
		drive.WarningReasons = append(drive.WarningReasons, fmt.Sprintf("Thermal Warning: Temperature is %d°C (Throttling likely)", drive.TemperatureC))
	}
	if drive.PercentUsed > 90 {
		drive.WarningReasons = append(drive.WarningReasons, fmt.Sprintf("Degradation: Drive has consumed %d%% of rated life span", drive.PercentUsed))
	}
	if drive.MediaErrors > 0 {
		drive.WarningReasons = append(drive.WarningReasons, fmt.Sprintf("Data Integrity: %d unrecovered media errors detected", drive.MediaErrors))
	}
	if drive.CriticalWarning > 0 {
		drive.WarningReasons = append(drive.WarningReasons, fmt.Sprintf("Hardware: Controller reports critical warning bitmask %d", drive.CriticalWarning))
	}

	if len(drive.WarningReasons) > 0 {
		drive.Status = "critical"
	} else {
		drive.Status = "healthy"
	}

	return drive, nil
}
