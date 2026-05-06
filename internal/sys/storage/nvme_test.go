package storage

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDiscoverNVMeDevices(t *testing.T) {
	tmpDir := t.TempDir()

	// Create mock sysfs
	classNvme := filepath.Join(tmpDir, "class", "nvme")
	if err := os.MkdirAll(classNvme, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(classNvme, "nvme0"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(classNvme, "nvme1"), 0755); err != nil {
		t.Fatal(err)
	}
	// Should ignore non-nvme dirs or anything that doesn't match nvme[0-9]*
	if err := os.Mkdir(filepath.Join(classNvme, "nvme-subsys0"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(classNvme, "other"), 0755); err != nil {
		t.Fatal(err)
	}

	devices, err := DiscoverNVMeDevices(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := []string{"nvme0", "nvme1"}
	if !reflect.DeepEqual(devices, expected) {
		t.Errorf("expected %v, got %v", expected, devices)
	}
}

func TestParseNVMeOutput(t *testing.T) {
	// Example nvme-cli json output
	jsonData := []byte(`{
		"temperature_c": 48,
		"temperature": 321, 
		"percent_used": 12,
		"avail_spare": 100,
		"media_errors": 0,
		"critical_warning": 0
	}`)
	// Some nvme outputs report temperature_c maybe? Wait, actual nvme smart-log gives temperature in Kelvin (e.g. 321 K = 48 C).

	drive, err := ParseNVMeOutput(jsonData, "nvme0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if drive.DeviceName != "nvme0" || drive.TemperatureC != 48 || drive.PercentUsed != 12 || drive.AvailableSparePct != 100 || drive.MediaErrors != 0 || drive.CriticalWarning != 0 {
		t.Errorf("unexpected drive parsing result: %+v", drive)
	}
	if drive.Status != "healthy" {
		t.Errorf("expected status healthy, got %s", drive.Status)
	}
}

func TestParseNVMeOutputCritical(t *testing.T) {
	jsonData := []byte(`{
		"temperature": 355, 
		"percent_used": 96,
		"avail_spare": 8,
		"media_errors": 4,
		"critical_warning": 1
	}`)

	drive, err := ParseNVMeOutput(jsonData, "nvme1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 355 K = 82 C
	if drive.TemperatureC != 82 || drive.PercentUsed != 96 || drive.Status != "critical" {
		t.Errorf("unexpected parsing logic: %+v", drive)
	}
	if len(drive.WarningReasons) != 4 {
		t.Errorf("expected 4 warnings, got %d: %v", len(drive.WarningReasons), drive.WarningReasons)
	}
}
