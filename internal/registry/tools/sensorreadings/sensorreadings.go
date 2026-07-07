// Package sensorreadings implements the get_sensor_readings diagnostic tool.
//
// It reads the kernel hwmon class (lm-sensors data source) directly from
// sysfs — temperatures, fan speeds, power draw, and voltage rails — and
// normalizes every reading into human units (°C, RPM, W, V). Thermal
// threshold breaches are classified per sensor so an LLM can spot an
// overheating CPU package or a dead fan without decoding millidegrees.
package sensorreadings

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func init() {
	registry.Register(New())
}

const (
	// maxSensorsPerList caps each per-chip sensor list to keep LLM
	// payloads bounded on exotic hardware.
	maxSensorsPerList = 64

	// maxWarningReasons caps the top-level warning list.
	maxWarningReasons = 20
)

// Tool implements the registry.Tool interface for get_sensor_readings.
type Tool struct {
	// sysfsRoot is the sysfs mount point, injectable for tests.
	sysfsRoot string
}

// New returns a new instance of the sensor readings tool.
func New() *Tool {
	return &Tool{sysfsRoot: "/sys"}
}

// Name returns the unique identifier for this tool.
func (t *Tool) Name() string { return "get_sensor_readings" }

// Description provides a brief summary for the LLM.
func (t *Tool) Description() string {
	return "Read hardware sensors (temperatures, fans, power, voltages) from hwmon with thermal threshold classification."
}

// Help returns detailed documentation for the tool.
func (t *Tool) Help() string {
	return `get_sensor_readings — Hardware Sensor (hwmon) Audit

Collects every hardware monitoring sensor the kernel exposes and
normalizes raw sysfs units into human units. Classifies each temperature
against its hardware-defined thresholds to surface overheating parts and
cooling failures (dead fans, dried thermal paste, blocked airflow).

Data Sources (pure sysfs, no binaries — same source lm-sensors uses):
  - Chip Discovery: /sys/class/hwmon/hwmon*/ (attributes are read from
    the hwmon dir itself and, on older kernels, the nested device/ dir)
  - Chip Name: hwmon*/name
  - Temperatures: temp<N>_input (millidegrees C), temp<N>_label,
    temp<N>_max, temp<N>_crit
  - Fans: fan<N>_input (RPM), fan<N>_label
  - Power: power<N>_average preferred over power<N>_input (microwatts),
    power<N>_label
  - Voltages: in<N>_input (millivolts), in<N>_label

Unit Normalization (deterministic):
  - Temperatures: m°C -> °C rounded to 1 decimal
  - Power: µW -> W rounded to 1 decimal
  - Voltages: mV -> V rounded to 3 decimals

Threshold Classification (deterministic, per temperature sensor):
  - reading >= temp<N>_crit  -> "critical"
  - reading >= temp<N>_max   -> "warning"
  - otherwise (or thresholds not exposed) -> "ok"

Output: chips[] each with temps[], fans[], power[], voltages[];
summary {chips, sensors_total, sensors_warning, sensors_critical};
warning_reasons[] naming every over-threshold sensor.

Caveats: sensors with unreadable/garbage values are skipped silently;
chips exposing no readable sensors are omitted. Missing max/crit files
simply omit the thresholds and leave the sensor "ok". VMs typically
expose no hwmon chips at all (tool reports unsupported there).`
}

// Category returns the functional group for this tool.
func (t *Tool) Category() registry.Category {
	return registry.CategoryHardware
}

// Parameters returns the parameter schema (none for this tool).
func (t *Tool) Parameters() []registry.ToolParam {
	return nil
}

// Hidden indicates whether this tool is excluded from LLM discovery.
func (t *Tool) Hidden() bool { return false }

// IsSupported reports whether at least one hwmon chip is exposed.
func (t *Tool) IsSupported() (bool, string) {
	entries, err := os.ReadDir(filepath.Join(t.sysfsRoot, "class", "hwmon"))
	if err != nil {
		return false, "no hwmon sensors exposed (common in VMs)"
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "hwmon") {
			return true, ""
		}
	}
	return false, "no hwmon sensors exposed (common in VMs)"
}

// TempSensor is one normalized temperature reading.
type TempSensor struct {
	Label   string   `json:"label"`
	Celsius float64  `json:"celsius"`
	MaxC    *float64 `json:"max_c,omitempty"`
	CritC   *float64 `json:"crit_c,omitempty"`
	Status  string   `json:"status"`
}

// FanSensor is one fan tachometer reading.
type FanSensor struct {
	Label string `json:"label"`
	RPM   int64  `json:"rpm"`
}

// PowerSensor is one normalized power reading.
type PowerSensor struct {
	Label string  `json:"label"`
	Watts float64 `json:"watts"`
}

// VoltageSensor is one normalized voltage rail reading.
type VoltageSensor struct {
	Label string  `json:"label"`
	Volts float64 `json:"volts"`
}

// Chip groups the sensors belonging to one hwmon chip.
type Chip struct {
	Name     string          `json:"name"`
	Temps    []TempSensor    `json:"temps,omitempty"`
	Fans     []FanSensor     `json:"fans,omitempty"`
	Power    []PowerSensor   `json:"power,omitempty"`
	Voltages []VoltageSensor `json:"voltages,omitempty"`
}

// Summary aggregates sensor counts across all chips.
type Summary struct {
	Chips           int `json:"chips"`
	SensorsTotal    int `json:"sensors_total"`
	SensorsWarning  int `json:"sensors_warning"`
	SensorsCritical int `json:"sensors_critical"`
}

// Output is the data payload of the tool result.
type Output struct {
	Chips          []Chip   `json:"chips"`
	Summary        Summary  `json:"summary"`
	WarningReasons []string `json:"warning_reasons,omitempty"`
}

// readTrimmed reads a sysfs attribute file and returns its
// whitespace-trimmed content. ok is false when the file is unreadable.
func readTrimmed(path string) (string, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(raw)), true
}

// readScaled reads a numeric sysfs attribute and divides it by divisor
// (e.g. 1000 for m°C -> °C). ok is false when the file is missing or
// does not contain a number.
func readScaled(path string, divisor float64) (float64, bool) {
	s, ok := readTrimmed(path)
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v / divisor, true
}

// round1 rounds to 1 decimal place.
func round1(v float64) float64 {
	return math.Round(v*10) / 10
}

// round3 rounds to 3 decimal places.
func round3(v float64) float64 {
	return math.Round(v*1000) / 1000
}

// sensorStatus classifies a temperature against its hardware thresholds:
// >= crit -> "critical", >= max -> "warning", otherwise "ok". Missing
// thresholds never escalate.
func sensorStatus(celsius float64, maxC, critC *float64) string {
	if critC != nil && celsius >= *critC {
		return "critical"
	}
	if maxC != nil && celsius >= *maxC {
		return "warning"
	}
	return "ok"
}

// sensorFiles maps attribute filenames to full paths for one hwmon chip,
// merging the chip dir with its nested device/ dir (older kernel layout).
// Entries in the chip dir take priority. Only direct children are
// considered — no deep recursion.
func sensorFiles(chipDir string) map[string]string {
	files := make(map[string]string)
	for _, dir := range []string{chipDir, filepath.Join(chipDir, "device")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if _, exists := files[name]; !exists {
				files[name] = filepath.Join(dir, name)
			}
		}
	}
	return files
}

// sensorInputPattern matches temp/fan/in "_input" attributes and both
// power reading variants.
var sensorInputPattern = regexp.MustCompile(`^(temp|fan|in|power)(\d+)_(input|average)$`)

// collectChip reads one hwmon chip directory and returns its normalized
// sensor set. Unreadable or unparseable sensors are skipped silently.
func collectChip(chipDir string) Chip {
	files := sensorFiles(chipDir)

	chip := Chip{Name: filepath.Base(chipDir)}
	if name, ok := readTrimmed(files["name"]); ok && name != "" {
		chip.Name = name
	}

	// Group indices per sensor kind so output order is deterministic.
	indices := map[string][]int{}
	seen := map[string]bool{}
	for filename := range files {
		m := sensorInputPattern.FindStringSubmatch(filename)
		if m == nil {
			continue
		}
		kind := m[1]
		if kind == "power" && m[3] != "average" && m[3] != "input" {
			continue
		}
		if (kind == "temp" || kind == "fan" || kind == "in") && m[3] != "input" {
			continue
		}
		idx, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		key := fmt.Sprintf("%s%d", kind, idx)
		if seen[key] {
			continue
		}
		seen[key] = true
		indices[kind] = append(indices[kind], idx)
	}
	for kind := range indices {
		sort.Ints(indices[kind])
	}

	label := func(kind string, idx int) string {
		if l, ok := readTrimmed(files[fmt.Sprintf("%s%d_label", kind, idx)]); ok && l != "" {
			return l
		}
		return fmt.Sprintf("%s%d", kind, idx)
	}

	for _, idx := range indices["temp"] {
		if len(chip.Temps) >= maxSensorsPerList {
			break
		}
		celsius, ok := readScaled(files[fmt.Sprintf("temp%d_input", idx)], 1000)
		if !ok {
			continue
		}
		sensor := TempSensor{Label: label("temp", idx), Celsius: round1(celsius)}
		if maxC, ok := readScaled(files[fmt.Sprintf("temp%d_max", idx)], 1000); ok {
			r := round1(maxC)
			sensor.MaxC = &r
		}
		if critC, ok := readScaled(files[fmt.Sprintf("temp%d_crit", idx)], 1000); ok {
			r := round1(critC)
			sensor.CritC = &r
		}
		sensor.Status = sensorStatus(sensor.Celsius, sensor.MaxC, sensor.CritC)
		chip.Temps = append(chip.Temps, sensor)
	}

	for _, idx := range indices["fan"] {
		if len(chip.Fans) >= maxSensorsPerList {
			break
		}
		rpm, ok := readScaled(files[fmt.Sprintf("fan%d_input", idx)], 1)
		if !ok {
			continue
		}
		chip.Fans = append(chip.Fans, FanSensor{Label: label("fan", idx), RPM: int64(rpm)})
	}

	for _, idx := range indices["power"] {
		if len(chip.Power) >= maxSensorsPerList {
			break
		}
		// Prefer the time-averaged reading over the instantaneous one.
		watts, ok := readScaled(files[fmt.Sprintf("power%d_average", idx)], 1e6)
		if !ok {
			watts, ok = readScaled(files[fmt.Sprintf("power%d_input", idx)], 1e6)
		}
		if !ok {
			continue
		}
		chip.Power = append(chip.Power, PowerSensor{Label: label("power", idx), Watts: round1(watts)})
	}

	for _, idx := range indices["in"] {
		if len(chip.Voltages) >= maxSensorsPerList {
			break
		}
		volts, ok := readScaled(files[fmt.Sprintf("in%d_input", idx)], 1000)
		if !ok {
			continue
		}
		chip.Voltages = append(chip.Voltages, VoltageSensor{Label: label("in", idx), Volts: round3(volts)})
	}

	return chip
}

// Execute scans every hwmon chip and returns normalized sensor readings.
func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	hwmonDir := filepath.Join(t.sysfsRoot, "class", "hwmon")
	entries, err := os.ReadDir(hwmonDir)
	if err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("failed to read %s: %v", hwmonDir, err)), nil
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "hwmon") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	out := Output{Chips: []Chip{}}
	droppedWarnings := 0
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("execution cancelled: %v", err)), nil
		}

		chip := collectChip(filepath.Join(hwmonDir, name))
		sensorCount := len(chip.Temps) + len(chip.Fans) + len(chip.Power) + len(chip.Voltages)
		if sensorCount == 0 {
			// Chip exposes nothing readable: skip per degradation profile.
			continue
		}

		out.Summary.Chips++
		out.Summary.SensorsTotal += sensorCount
		for _, temp := range chip.Temps {
			reason := ""
			switch temp.Status {
			case "warning":
				out.Summary.SensorsWarning++
				reason = fmt.Sprintf("%s/%s at %.1f°C >= max %.1f°C", chip.Name, temp.Label, temp.Celsius, *temp.MaxC)
			case "critical":
				out.Summary.SensorsCritical++
				reason = fmt.Sprintf("%s/%s at %.1f°C >= critical %.1f°C", chip.Name, temp.Label, temp.Celsius, *temp.CritC)
			}
			if reason == "" {
				continue
			}
			if len(out.WarningReasons) < maxWarningReasons {
				out.WarningReasons = append(out.WarningReasons, reason)
			} else {
				droppedWarnings++
			}
		}
		out.Chips = append(out.Chips, chip)
	}
	if droppedWarnings > 0 {
		out.WarningReasons = append(out.WarningReasons, fmt.Sprintf("(+%d more over-threshold sensors)", droppedWarnings))
	}

	status := registry.StatusOK
	summary := fmt.Sprintf("Read %d sensors across %d chips: all within thresholds.",
		out.Summary.SensorsTotal, out.Summary.Chips)
	switch {
	case out.Summary.SensorsCritical > 0:
		status = registry.StatusError
		summary = fmt.Sprintf("Read %d sensors across %d chips: %d CRITICAL, %d warning threshold breaches.",
			out.Summary.SensorsTotal, out.Summary.Chips, out.Summary.SensorsCritical, out.Summary.SensorsWarning)
	case out.Summary.SensorsWarning > 0:
		status = registry.StatusWarning
		summary = fmt.Sprintf("Read %d sensors across %d chips: %d warning threshold breaches.",
			out.Summary.SensorsTotal, out.Summary.Chips, out.Summary.SensorsWarning)
	}

	return registry.NewResult(t.Name(), status, summary, out), nil
}
