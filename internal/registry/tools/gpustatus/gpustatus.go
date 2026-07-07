// Package gpustatus implements the get_gpu_status diagnostic tool.
//
// It reports per-GPU utilization, memory pressure, thermals, power draw and
// (on NVIDIA) ECC health. Vendor CLIs are the justified primary data source
// here because GPU telemetry protocols (NVML, ROCm SMI lib) are proprietary:
// nvidia-smi is tried first, then rocm-smi, and finally a native sysfs
// amdgpu fallback that reads partial telemetry directly from
// /sys/class/drm/card*/device.
package gpustatus

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// Package-level seams so tests can fake binaries and the sysfs tree without
// ever executing real commands.
var (
	execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).Output()
	}
	execLookPath = exec.LookPath
	sysfsRoot    = "/sys"
)

// nvidiaQueryArgs is the exact nvidia-smi CSV telemetry query.
var nvidiaQueryArgs = []string{
	"--query-gpu=index,name,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw,power.limit,ecc.errors.uncorrected.volatile.total,pstate",
	"--format=csv,noheader,nounits",
}

// rocmQueryArgs is the rocm-smi JSON telemetry query.
var rocmQueryArgs = []string{"--showuse", "--showmemuse", "--showtemp", "--showpower", "--json"}

// cardNameRe matches primary DRM card nodes (card0, card1, ...) while
// excluding connector children (card0-eDP-1) and render nodes (renderD128).
var cardNameRe = regexp.MustCompile(`^card(\d+)$`)

// Warning thresholds.
const (
	tempWarnC      = 85.0
	memoryWarnPct  = 95.0
	unsupportedMsg = "no NVIDIA/AMD GPU tooling or sysfs telemetry found"
)

// GPU is the per-device telemetry snapshot. Numeric fields are pointers so
// values a backend cannot provide (e.g. "[N/A]" from nvidia-smi, missing
// sysfs files) are omitted from the JSON instead of reading as zero.
type GPU struct {
	Index          int      `json:"index"`
	Name           string   `json:"name,omitempty"`
	UtilizationPct *float64 `json:"utilization_pct,omitempty"`
	MemoryUsedMB   *float64 `json:"memory_used_mb,omitempty"`
	MemoryTotalMB  *float64 `json:"memory_total_mb,omitempty"`
	MemoryUsedPct  *float64 `json:"memory_used_pct,omitempty"`
	TemperatureC   *float64 `json:"temperature_c,omitempty"`
	PowerDrawW     *float64 `json:"power_draw_w,omitempty"`
	PowerLimitW    *float64 `json:"power_limit_w,omitempty"`
	ECCUncorrected *int64   `json:"ecc_uncorrected,omitempty"`
	PState         string   `json:"pstate,omitempty"`
	WarningReasons []string `json:"warning_reasons"`
}

// Output is the tool's data payload.
type Output struct {
	Backend string `json:"backend"`
	GPUs    []GPU  `json:"gpus"`
}

// Tool implements registry.Tool for get_gpu_status.
type Tool struct{}

// New constructs the tool.
func New() *Tool {
	return &Tool{}
}

// Name returns the unique tool identifier.
func (t *Tool) Name() string {
	return "get_gpu_status"
}

// Description returns the one-line summary for get_tool_list.
func (t *Tool) Description() string {
	return "Report GPU utilization, VRAM pressure, thermals, power draw and ECC health for NVIDIA/AMD accelerators."
}

// Help returns the full tool help text.
func (t *Tool) Help() string {
	return `Collects a per-GPU health snapshot covering utilization, VRAM usage, temperature, power draw/limit and (NVIDIA only) volatile uncorrected ECC errors and performance state.

Data sources, in priority order:
1. nvidia-smi --query-gpu=... --format=csv,noheader,nounits (NVIDIA proprietary NVML telemetry).
2. rocm-smi --showuse --showmemuse --showtemp --showpower --json (AMD ROCm telemetry; keys are parsed defensively as they vary between versions).
3. Native sysfs amdgpu fallback: /sys/class/drm/card<N>/device/gpu_busy_percent, mem_info_vram_used, mem_info_vram_total and hwmon temp1_input/power1_average.

Analysis is heuristic: per-GPU warning_reasons flag temperature > 85C, uncorrected ECC errors > 0 and memory_used_pct > 95%. memory_used_pct is pre-computed to one decimal place. Fields a backend reports as "[N/A]" (or that are missing from sysfs) are omitted from the output rather than zeroed.`
}

// Category classifies the tool as hardware diagnostics.
func (t *Tool) Category() registry.Category {
	return registry.CategoryHardware
}

// Parameters returns the parameter schema (none for this tool).
func (t *Tool) Parameters() []registry.ToolParam {
	return nil
}

// Hidden returns false; get_gpu_status is a public tool.
func (t *Tool) Hidden() bool {
	return false
}

// IsSupported reports whether any GPU telemetry source exists on this node.
func (t *Tool) IsSupported() (bool, string) {
	if _, err := execLookPath("nvidia-smi"); err == nil {
		return true, ""
	}
	if _, err := execLookPath("rocm-smi"); err == nil {
		return true, ""
	}
	if len(discoverSysfsCards(sysfsRoot)) > 0 {
		return true, ""
	}
	return false, unsupportedMsg
}

// Execute gathers GPU telemetry from the highest-priority available backend.
// The tool takes no parameters; args are ignored.
func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	_ = args // no parameters
	start := time.Now()

	if err := ctx.Err(); err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("execution cancelled: %v", err)), nil
	}

	var attempts []string

	if _, err := execLookPath("nvidia-smi"); err == nil {
		out, runErr := execCommand(ctx, "nvidia-smi", nvidiaQueryArgs...)
		if runErr == nil {
			if gpus := parseNvidiaSMI(out); len(gpus) > 0 {
				res := buildResult(t.Name(), "nvidia-smi", gpus)
				res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
				return res, nil
			}
			attempts = append(attempts, "nvidia-smi: no GPUs parsed from output")
		} else {
			attempts = append(attempts, fmt.Sprintf("nvidia-smi: %v", runErr))
		}
		if err := ctx.Err(); err != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("execution cancelled: %v", err)), nil
		}
	}

	if _, err := execLookPath("rocm-smi"); err == nil {
		out, runErr := execCommand(ctx, "rocm-smi", rocmQueryArgs...)
		if runErr == nil {
			gpus, parseErr := parseRocmSMI(out)
			if parseErr == nil && len(gpus) > 0 {
				res := buildResult(t.Name(), "rocm-smi", gpus)
				res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
				return res, nil
			}
			if parseErr != nil {
				attempts = append(attempts, fmt.Sprintf("rocm-smi: %v", parseErr))
			} else {
				attempts = append(attempts, "rocm-smi: no GPUs parsed from output")
			}
		} else {
			attempts = append(attempts, fmt.Sprintf("rocm-smi: %v", runErr))
		}
		if err := ctx.Err(); err != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("execution cancelled: %v", err)), nil
		}
	}

	if gpus := collectSysfsGPUs(sysfsRoot); len(gpus) > 0 {
		res := buildResult(t.Name(), "sysfs", gpus)
		res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
		return res, nil
	}

	msg := "no GPU telemetry available"
	if len(attempts) > 0 {
		msg += ": " + strings.Join(attempts, "; ")
	} else {
		msg += ": " + unsupportedMsg
	}
	return registry.NewErrorResult(t.Name(), msg), nil
}

// buildResult finalizes derived fields/warnings and wraps the GPUs into a
// standard ToolResult envelope.
func buildResult(toolName, backend string, gpus []GPU) *registry.ToolResult {
	warned := 0
	for i := range gpus {
		finalizeGPU(&gpus[i])
		if len(gpus[i].WarningReasons) > 0 {
			warned++
		}
	}

	status := registry.StatusOK
	summary := fmt.Sprintf("GPU status via %s: %d GPU(s), all healthy", backend, len(gpus))
	if warned > 0 {
		status = registry.StatusWarning
		summary = fmt.Sprintf("GPU status via %s: %d of %d GPU(s) report warnings", backend, warned, len(gpus))
	}

	res := registry.NewResult(toolName, status, summary, Output{Backend: backend, GPUs: gpus})
	res.Metadata.FilteringMethod = "heuristic"
	return res
}

// parseNvidiaSMI parses `nvidia-smi --format=csv,noheader,nounits` rows.
// Malformed rows are skipped; commas inside the GPU name are re-joined by
// anchoring the eight trailing numeric/state columns from the right.
func parseNvidiaSMI(out []byte) []GPU {
	var gpus []GPU
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) < 10 {
			continue
		}
		for i := range fields {
			fields[i] = strings.TrimSpace(fields[i])
		}
		n := len(fields)

		idx, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}

		name := strings.Join(fields[1:n-8], ", ")
		if strings.EqualFold(name, "[n/a]") || strings.EqualFold(name, "n/a") {
			name = ""
		}

		g := GPU{
			Index:          idx,
			Name:           name,
			UtilizationPct: parseNvidiaFloat(fields[n-8]),
			MemoryUsedMB:   parseNvidiaFloat(fields[n-7]),
			MemoryTotalMB:  parseNvidiaFloat(fields[n-6]),
			TemperatureC:   parseNvidiaFloat(fields[n-5]),
			PowerDrawW:     parseNvidiaFloat(fields[n-4]),
			PowerLimitW:    parseNvidiaFloat(fields[n-3]),
		}
		if v := parseNvidiaFloat(fields[n-2]); v != nil {
			ecc := int64(*v)
			g.ECCUncorrected = &ecc
		}
		if p := fields[n-1]; !strings.EqualFold(p, "[n/a]") && !strings.EqualFold(p, "n/a") {
			g.PState = p
		}
		gpus = append(gpus, g)
	}
	return gpus
}

// parseNvidiaFloat converts a nvidia-smi CSV cell to a float, mapping the
// "[N/A]" / "N/A" / "[Not Supported]" sentinels (and garbage) to nil.
func parseNvidiaFloat(s string) *float64 {
	s = strings.TrimSpace(s)
	switch {
	case s == "",
		strings.EqualFold(s, "n/a"),
		strings.EqualFold(s, "[n/a]"),
		strings.EqualFold(s, "not supported"),
		strings.EqualFold(s, "[not supported]"):
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &v
}

// parseRocmSMI parses `rocm-smi --json` output. Key names vary between ROCm
// releases, so matching is defensive: card entries are detected by the
// "card<N>" key shape and metric keys by case-insensitive substrings.
func parseRocmSMI(out []byte) ([]GPU, error) {
	var raw map[string]map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("rocm-smi json parse: %w", err)
	}

	var gpus []GPU
	for cardKey, fields := range raw {
		m := cardNameRe.FindStringSubmatch(cardKey)
		if m == nil {
			continue // e.g. the "system" section
		}
		idx, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}

		keys := make([]string, 0, len(fields))
		for k := range fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		g := GPU{Index: idx}
		edgeTemp, avgPower := false, false
		for _, k := range keys {
			lk := strings.ToLower(k)
			v := rocmFloat(fields[k])
			switch {
			case strings.Contains(lk, "temperature"):
				if v == nil {
					continue
				}
				if strings.Contains(lk, "edge") {
					g.TemperatureC = v
					edgeTemp = true
				} else if !edgeTemp && g.TemperatureC == nil {
					g.TemperatureC = v
				}
			case strings.Contains(lk, "gpu use"):
				if v != nil {
					g.UtilizationPct = v
				}
			case strings.Contains(lk, "memory use") || strings.Contains(lk, "vram%"):
				if v != nil {
					g.MemoryUsedPct = v
				}
			case strings.Contains(lk, "power") && strings.Contains(lk, "(w)"):
				if v == nil {
					continue
				}
				if strings.Contains(lk, "average") {
					g.PowerDrawW = v
					avgPower = true
				} else if !avgPower && g.PowerDrawW == nil {
					g.PowerDrawW = v
				}
			case strings.Contains(lk, "card series"):
				if s, ok := fields[k].(string); ok {
					g.Name = s
				}
			}
		}
		gpus = append(gpus, g)
	}

	sort.Slice(gpus, func(i, j int) bool { return gpus[i].Index < gpus[j].Index })
	return gpus, nil
}

// rocmFloat converts a rocm-smi JSON value (string or number) to a float,
// tolerating "%"-suffixed strings and mapping non-numeric values to nil.
func rocmFloat(v any) *float64 {
	switch x := v.(type) {
	case float64:
		return &x
	case string:
		s := strings.TrimSuffix(strings.TrimSpace(x), "%")
		if s == "" || strings.EqualFold(s, "n/a") {
			return nil
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil
		}
		return &f
	default:
		return nil
	}
}

// discoverSysfsCards returns the sorted indices of DRM cards that expose
// amdgpu busy telemetry (<root>/class/drm/card<N>/device/gpu_busy_percent).
func discoverSysfsCards(root string) []int {
	drmDir := filepath.Join(root, "class", "drm")
	entries, err := os.ReadDir(drmDir)
	if err != nil {
		return nil
	}

	var cards []int
	for _, e := range entries {
		m := cardNameRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		idx, convErr := strconv.Atoi(m[1])
		if convErr != nil {
			continue
		}
		if _, statErr := os.Stat(filepath.Join(drmDir, e.Name(), "device", "gpu_busy_percent")); statErr != nil {
			continue
		}
		cards = append(cards, idx)
	}
	sort.Ints(cards)
	return cards
}

// collectSysfsGPUs reads the native amdgpu sysfs telemetry for every
// discovered card. Missing or unreadable files degrade to absent fields.
func collectSysfsGPUs(root string) []GPU {
	var gpus []GPU
	for _, idx := range discoverSysfsCards(root) {
		dev := filepath.Join(root, "class", "drm", fmt.Sprintf("card%d", idx), "device")
		g := GPU{
			Index:          idx,
			UtilizationPct: readSysfsScaledFloat(filepath.Join(dev, "gpu_busy_percent"), 1),
			MemoryUsedMB:   readSysfsScaledFloat(filepath.Join(dev, "mem_info_vram_used"), 1024*1024),
			MemoryTotalMB:  readSysfsScaledFloat(filepath.Join(dev, "mem_info_vram_total"), 1024*1024),
		}
		if hwmons, err := filepath.Glob(filepath.Join(dev, "hwmon", "hwmon*")); err == nil {
			sort.Strings(hwmons)
			for _, h := range hwmons {
				if g.TemperatureC == nil {
					g.TemperatureC = readSysfsScaledFloat(filepath.Join(h, "temp1_input"), 1000) // millidegrees C
				}
				if g.PowerDrawW == nil {
					g.PowerDrawW = readSysfsScaledFloat(filepath.Join(h, "power1_average"), 1_000_000) // microwatts
				}
			}
		}
		gpus = append(gpus, g)
	}
	return gpus
}

// readSysfsScaledFloat reads a numeric sysfs attribute and divides it by
// divisor, rounding to one decimal place. Missing/garbage files yield nil.
func readSysfsScaledFloat(path string, divisor float64) *float64 {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
	if err != nil {
		return nil
	}
	scaled := round1(v / divisor)
	return &scaled
}

// finalizeGPU derives memory_used_pct (one decimal place) and evaluates the
// heuristic warning thresholds for a single GPU.
func finalizeGPU(g *GPU) {
	if g.MemoryUsedPct == nil {
		if g.MemoryUsedMB != nil && g.MemoryTotalMB != nil && *g.MemoryTotalMB > 0 {
			pct := round1(*g.MemoryUsedMB / *g.MemoryTotalMB * 100)
			g.MemoryUsedPct = &pct
		}
	} else {
		pct := round1(*g.MemoryUsedPct)
		g.MemoryUsedPct = &pct
	}

	g.WarningReasons = []string{}
	if g.TemperatureC != nil && *g.TemperatureC > tempWarnC {
		g.WarningReasons = append(g.WarningReasons, fmt.Sprintf("temperature %.0fC exceeds %.0fC threshold", *g.TemperatureC, tempWarnC))
	}
	if g.ECCUncorrected != nil && *g.ECCUncorrected > 0 {
		g.WarningReasons = append(g.WarningReasons, fmt.Sprintf("%d volatile uncorrected ECC error(s) detected", *g.ECCUncorrected))
	}
	if g.MemoryUsedPct != nil && *g.MemoryUsedPct > memoryWarnPct {
		g.WarningReasons = append(g.WarningReasons, fmt.Sprintf("memory utilization %.1f%% exceeds %.0f%% threshold", *g.MemoryUsedPct, memoryWarnPct))
	}
}

// round1 rounds to one decimal place.
func round1(v float64) float64 {
	return math.Round(v*10) / 10
}

func init() {
	registry.Register(New())
}
