package gpustatus

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// nvidiaCSVFixture mirrors real `nvidia-smi --query-gpu=... --format=csv,noheader,nounits`
// output. GPU 0 is hot (91C), has 5 uncorrected ECC errors and >95% VRAM used.
// GPU 1 is healthy and reports "[N/A]" for ECC (GeForce-style board).
const nvidiaCSVFixture = `0, NVIDIA A100-SXM4-80GB, 96, 79544, 81920, 91, 312.45, 400.00, 5, P0
1, NVIDIA A100-SXM4-80GB, 12, 4096, 81920, 44, 88.10, 400.00, [N/A], P8
`

// rocmJSONFixture mirrors real `rocm-smi --showuse --showmemuse --showtemp --showpower --json`
// output. Keys intentionally vary per card and a non-card "system" key is present.
const rocmJSONFixture = `{
  "card0": {
    "GPU use (%)": "12",
    "GPU memory use (%)": "4",
    "Temperature (Sensor edge) (C)": "45.0",
    "Temperature (Sensor junction) (C)": "48.5",
    "Average Graphics Package Power (W)": "35.0"
  },
  "card1": {
    "GPU use (%)": "97",
    "GPU memory use (%)": "96.2",
    "Temperature (Sensor edge) (C)": "92.0",
    "Average Graphics Package Power (W)": "289.0"
  },
  "system": {
    "Driver version": "6.3.2"
  }
}`

// stubEnv swaps the package-level test seams and restores them on cleanup.
// Tests that call it mutate shared package state and must NOT call t.Parallel().
func stubEnv(t *testing.T, lookPaths map[string]bool, cmd func(ctx context.Context, name string, args ...string) ([]byte, error), sysfs string) {
	t.Helper()
	oldLook, oldCmd, oldSysfs := execLookPath, execCommand, sysfsRoot
	t.Cleanup(func() {
		execLookPath, execCommand, sysfsRoot = oldLook, oldCmd, oldSysfs
	})
	execLookPath = func(file string) (string, error) {
		if lookPaths[file] {
			return "/mock/bin/" + file, nil
		}
		return "", errors.New("executable file not found in $PATH")
	}
	if cmd != nil {
		execCommand = cmd
	} else {
		execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			t.Errorf("unexpected execCommand invocation: %s %v", name, args)
			return nil, errors.New("unexpected invocation")
		}
	}
	if sysfs == "" {
		sysfs = t.TempDir() // empty sysfs tree: no GPUs
	}
	sysfsRoot = sysfs
}

// writeSysfsFile creates a (possibly nested) file under a fake sysfs root.
func writeSysfsFile(t *testing.T, root string, rel string, content string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestToolContract(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "get_gpu_status" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "get_gpu_status")
	}
	if tool.Category() != registry.CategoryHardware {
		t.Errorf("Category() = %q, want %q", tool.Category(), registry.CategoryHardware)
	}
	if tool.Hidden() {
		t.Error("Hidden() = true, want false")
	}
	if tool.Description() == "" {
		t.Error("Description() must not be empty")
	}
	if len(tool.Parameters()) != 0 {
		t.Errorf("Parameters() = %v, want none", tool.Parameters())
	}
	help := tool.Help()
	for _, src := range []string{"nvidia-smi", "rocm-smi", "gpu_busy_percent"} {
		if !strings.Contains(help, src) {
			t.Errorf("Help() must reference data source %q", src)
		}
	}
}

func TestIsSupported(t *testing.T) {
	tests := []struct {
		name       string
		lookPaths  map[string]bool
		sysfsCard  bool
		want       bool
		wantReason string
	}{
		{name: "nvidia-smi in PATH", lookPaths: map[string]bool{"nvidia-smi": true}, want: true},
		{name: "rocm-smi in PATH", lookPaths: map[string]bool{"rocm-smi": true}, want: true},
		{name: "sysfs amdgpu telemetry only", lookPaths: map[string]bool{}, sysfsCard: true, want: true},
		{
			name:       "nothing available",
			lookPaths:  map[string]bool{},
			want:       false,
			wantReason: "no NVIDIA/AMD GPU tooling or sysfs telemetry found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sysfs := t.TempDir()
			if tc.sysfsCard {
				writeSysfsFile(t, sysfs, "class/drm/card0/device/gpu_busy_percent", "3\n")
			}
			stubEnv(t, tc.lookPaths, func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return nil, errors.New("IsSupported must not execute commands")
			}, sysfs)

			got, reason := New().IsSupported()
			if got != tc.want {
				t.Errorf("IsSupported() = %v, want %v (reason %q)", got, tc.want, reason)
			}
			if !tc.want && reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", reason, tc.wantReason)
			}
		})
	}
}

func TestExecute_NvidiaBackend(t *testing.T) {
	var gotName string
	var gotArgs []string
	stubEnv(t, map[string]bool{"nvidia-smi": true, "rocm-smi": true}, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		gotName = name
		gotArgs = args
		return []byte(nvidiaCSVFixture), nil
	}, "")

	res, err := New().Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil (errors must be encapsulated)", err)
	}
	if res.Status != registry.StatusWarning {
		t.Fatalf("Status = %q, want %q (GPU 0 must trip thresholds); summary: %s", res.Status, registry.StatusWarning, res.Summary)
	}

	if gotName != "nvidia-smi" {
		t.Errorf("invoked %q, want nvidia-smi", gotName)
	}
	wantQuery := "--query-gpu=index,name,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw,power.limit,ecc.errors.uncorrected.volatile.total,pstate"
	if len(gotArgs) != 2 || gotArgs[0] != wantQuery || gotArgs[1] != "--format=csv,noheader,nounits" {
		t.Errorf("nvidia-smi args = %v", gotArgs)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("Data unmarshal: %v", err)
	}
	if out.Backend != "nvidia-smi" {
		t.Errorf("backend = %q, want nvidia-smi", out.Backend)
	}
	if len(out.GPUs) != 2 {
		t.Fatalf("got %d GPUs, want 2", len(out.GPUs))
	}

	g0 := out.GPUs[0]
	if g0.Index != 0 || g0.Name != "NVIDIA A100-SXM4-80GB" {
		t.Errorf("gpu0 identity = %+v", g0)
	}
	if g0.UtilizationPct == nil || *g0.UtilizationPct != 96 {
		t.Errorf("gpu0 utilization = %v, want 96", g0.UtilizationPct)
	}
	if g0.MemoryUsedMB == nil || *g0.MemoryUsedMB != 79544 || g0.MemoryTotalMB == nil || *g0.MemoryTotalMB != 81920 {
		t.Errorf("gpu0 memory = %v/%v", g0.MemoryUsedMB, g0.MemoryTotalMB)
	}
	if g0.MemoryUsedPct == nil || *g0.MemoryUsedPct != 97.1 {
		t.Errorf("gpu0 memory_used_pct = %v, want 97.1 (1dp)", g0.MemoryUsedPct)
	}
	if g0.TemperatureC == nil || *g0.TemperatureC != 91 {
		t.Errorf("gpu0 temperature = %v, want 91", g0.TemperatureC)
	}
	if g0.PowerDrawW == nil || *g0.PowerDrawW != 312.45 || g0.PowerLimitW == nil || *g0.PowerLimitW != 400 {
		t.Errorf("gpu0 power = %v/%v", g0.PowerDrawW, g0.PowerLimitW)
	}
	if g0.ECCUncorrected == nil || *g0.ECCUncorrected != 5 {
		t.Errorf("gpu0 ecc_uncorrected = %v, want 5", g0.ECCUncorrected)
	}
	if g0.PState != "P0" {
		t.Errorf("gpu0 pstate = %q, want P0", g0.PState)
	}
	if len(g0.WarningReasons) != 3 {
		t.Errorf("gpu0 warning_reasons = %v, want 3 (temp, ecc, memory)", g0.WarningReasons)
	}

	g1 := out.GPUs[1]
	if g1.ECCUncorrected != nil {
		t.Errorf("gpu1 ecc_uncorrected = %v, want omitted for [N/A]", g1.ECCUncorrected)
	}
	if g1.MemoryUsedPct == nil || *g1.MemoryUsedPct != 5.0 {
		t.Errorf("gpu1 memory_used_pct = %v, want 5.0", g1.MemoryUsedPct)
	}
	if len(g1.WarningReasons) != 0 {
		t.Errorf("gpu1 warning_reasons = %v, want none", g1.WarningReasons)
	}

	// "[N/A]" fields must be entirely absent from the marshalled JSON.
	var generic struct {
		GPUs []map[string]any `json:"gpus"`
	}
	if err := json.Unmarshal(res.Data, &generic); err != nil {
		t.Fatal(err)
	}
	if _, present := generic.GPUs[1]["ecc_uncorrected"]; present {
		t.Error("gpu1 JSON must omit ecc_uncorrected for [N/A]")
	}
	if _, present := generic.GPUs[0]["ecc_uncorrected"]; !present {
		t.Error("gpu0 JSON must include ecc_uncorrected")
	}
}

func TestExecute_RocmBackend(t *testing.T) {
	stubEnv(t, map[string]bool{"rocm-smi": true}, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "rocm-smi" {
			t.Errorf("invoked %q, want rocm-smi", name)
		}
		want := []string{"--showuse", "--showmemuse", "--showtemp", "--showpower", "--json"}
		if len(args) != len(want) {
			t.Errorf("rocm-smi args = %v, want %v", args, want)
		} else {
			for i := range want {
				if args[i] != want[i] {
					t.Errorf("rocm-smi args = %v, want %v", args, want)
					break
				}
			}
		}
		return []byte(rocmJSONFixture), nil
	}, "")

	res, err := New().Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if res.Status != registry.StatusWarning {
		t.Fatalf("Status = %q, want warning (card1 is hot and VRAM-saturated)", res.Status)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Backend != "rocm-smi" {
		t.Errorf("backend = %q, want rocm-smi", out.Backend)
	}
	if len(out.GPUs) != 2 {
		t.Fatalf("got %d GPUs, want 2 (the \"system\" key must be skipped)", len(out.GPUs))
	}

	g0 := out.GPUs[0]
	if g0.Index != 0 {
		t.Errorf("gpu0 index = %d, want 0", g0.Index)
	}
	if g0.UtilizationPct == nil || *g0.UtilizationPct != 12 {
		t.Errorf("gpu0 utilization = %v, want 12", g0.UtilizationPct)
	}
	if g0.TemperatureC == nil || *g0.TemperatureC != 45.0 {
		t.Errorf("gpu0 temperature = %v, want 45.0 (edge sensor preferred)", g0.TemperatureC)
	}
	if g0.PowerDrawW == nil || *g0.PowerDrawW != 35.0 {
		t.Errorf("gpu0 power = %v, want 35.0", g0.PowerDrawW)
	}
	if g0.MemoryUsedPct == nil || *g0.MemoryUsedPct != 4.0 {
		t.Errorf("gpu0 memory_used_pct = %v, want 4.0", g0.MemoryUsedPct)
	}
	if len(g0.WarningReasons) != 0 {
		t.Errorf("gpu0 warnings = %v, want none", g0.WarningReasons)
	}

	g1 := out.GPUs[1]
	if g1.Index != 1 {
		t.Errorf("gpu1 index = %d, want 1", g1.Index)
	}
	if len(g1.WarningReasons) != 2 {
		t.Errorf("gpu1 warnings = %v, want 2 (temp 92 > 85, memory 96.2 > 95)", g1.WarningReasons)
	}

	// pstate and ecc are NVIDIA-only and must be absent on the rocm backend.
	var generic struct {
		GPUs []map[string]any `json:"gpus"`
	}
	if err := json.Unmarshal(res.Data, &generic); err != nil {
		t.Fatal(err)
	}
	for i, g := range generic.GPUs {
		if _, present := g["pstate"]; present {
			t.Errorf("gpu%d JSON must omit pstate on rocm backend", i)
		}
		if _, present := g["ecc_uncorrected"]; present {
			t.Errorf("gpu%d JSON must omit ecc_uncorrected on rocm backend", i)
		}
	}
}

func TestExecute_SysfsFallback(t *testing.T) {
	sysfs := t.TempDir()
	// card0: full amdgpu telemetry.
	writeSysfsFile(t, sysfs, "class/drm/card0/device/gpu_busy_percent", "42\n")
	writeSysfsFile(t, sysfs, "class/drm/card0/device/mem_info_vram_used", "4294967296\n")   // 4096 MB
	writeSysfsFile(t, sysfs, "class/drm/card0/device/mem_info_vram_total", "17179869184\n") // 16384 MB
	writeSysfsFile(t, sysfs, "class/drm/card0/device/hwmon/hwmon3/temp1_input", "45000\n")  // 45 C
	writeSysfsFile(t, sysfs, "class/drm/card0/device/hwmon/hwmon3/power1_average", "35000000\n")
	// card1: partial telemetry (utilization only).
	writeSysfsFile(t, sysfs, "class/drm/card1/device/gpu_busy_percent", "7\n")
	// Connector directory and render node must be ignored.
	writeSysfsFile(t, sysfs, "class/drm/card0-eDP-1/device/gpu_busy_percent", "99\n")
	writeSysfsFile(t, sysfs, "class/drm/renderD128/device/gpu_busy_percent", "99\n")

	stubEnv(t, map[string]bool{}, nil, sysfs)

	res, err := New().Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("Status = %q, want ok; summary: %s", res.Status, res.Summary)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Backend != "sysfs" {
		t.Errorf("backend = %q, want sysfs", out.Backend)
	}
	if len(out.GPUs) != 2 {
		t.Fatalf("got %d GPUs, want 2 (connector/render nodes excluded)", len(out.GPUs))
	}

	g0 := out.GPUs[0]
	if g0.Index != 0 {
		t.Errorf("gpu0 index = %d", g0.Index)
	}
	if g0.UtilizationPct == nil || *g0.UtilizationPct != 42 {
		t.Errorf("gpu0 utilization = %v, want 42", g0.UtilizationPct)
	}
	if g0.MemoryUsedMB == nil || *g0.MemoryUsedMB != 4096 {
		t.Errorf("gpu0 memory_used_mb = %v, want 4096", g0.MemoryUsedMB)
	}
	if g0.MemoryTotalMB == nil || *g0.MemoryTotalMB != 16384 {
		t.Errorf("gpu0 memory_total_mb = %v, want 16384", g0.MemoryTotalMB)
	}
	if g0.MemoryUsedPct == nil || *g0.MemoryUsedPct != 25.0 {
		t.Errorf("gpu0 memory_used_pct = %v, want 25.0", g0.MemoryUsedPct)
	}
	if g0.TemperatureC == nil || *g0.TemperatureC != 45 {
		t.Errorf("gpu0 temperature = %v, want 45", g0.TemperatureC)
	}
	if g0.PowerDrawW == nil || *g0.PowerDrawW != 35 {
		t.Errorf("gpu0 power = %v, want 35", g0.PowerDrawW)
	}

	g1 := out.GPUs[1]
	if g1.UtilizationPct == nil || *g1.UtilizationPct != 7 {
		t.Errorf("gpu1 utilization = %v, want 7", g1.UtilizationPct)
	}

	// Missing sysfs telemetry must be absent from the JSON, not zeroed.
	var generic struct {
		GPUs []map[string]any `json:"gpus"`
	}
	if err := json.Unmarshal(res.Data, &generic); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"temperature_c", "power_draw_w", "memory_used_mb", "memory_total_mb", "memory_used_pct"} {
		if _, present := generic.GPUs[1][key]; present {
			t.Errorf("gpu1 JSON must omit %s when sysfs file is missing", key)
		}
	}
}

func TestExecute_NvidiaFailsFallsBackToRocm(t *testing.T) {
	stubEnv(t, map[string]bool{"nvidia-smi": true, "rocm-smi": true}, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "nvidia-smi" {
			return []byte("NVIDIA-SMI has failed because it couldn't communicate with the NVIDIA driver."), errors.New("exit status 9")
		}
		return []byte(rocmJSONFixture), nil
	}, "")

	res, err := New().Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Backend != "rocm-smi" {
		t.Errorf("backend = %q, want rocm-smi fallback", out.Backend)
	}
	if len(out.GPUs) != 2 {
		t.Errorf("got %d GPUs, want 2", len(out.GPUs))
	}
}

func TestExecute_AllBackendsFail(t *testing.T) {
	stubEnv(t, map[string]bool{"nvidia-smi": true, "rocm-smi": true}, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, errors.New("boom")
	}, "")

	res, err := New().Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil (encapsulated)", err)
	}
	if res.Status != registry.StatusError {
		t.Fatalf("Status = %q, want error", res.Status)
	}
	if !strings.Contains(res.Summary, "nvidia-smi") || !strings.Contains(res.Summary, "rocm-smi") {
		t.Errorf("Summary %q should mention the failed backends", res.Summary)
	}
}

func TestExecute_NoBackendsAtAll(t *testing.T) {
	stubEnv(t, map[string]bool{}, nil, "")

	res, err := New().Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if res.Status != registry.StatusError {
		t.Fatalf("Status = %q, want error", res.Status)
	}
}

func TestExecute_ContextCancelled(t *testing.T) {
	invoked := false
	stubEnv(t, map[string]bool{"nvidia-smi": true}, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		invoked = true
		return []byte(nvidiaCSVFixture), nil
	}, "")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := New().Execute(ctx, nil)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil (encapsulated)", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %q, want error for cancelled context", res.Status)
	}
	if invoked {
		t.Error("execCommand must not be invoked after context cancellation")
	}
}

func TestParseNvidiaSMI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want int
		spot func(t *testing.T, gpus []GPU)
	}{
		{
			name: "two well formed rows",
			in:   nvidiaCSVFixture,
			want: 2,
			spot: func(t *testing.T, gpus []GPU) {
				if gpus[0].Index != 0 || gpus[1].Index != 1 {
					t.Errorf("indices = %d,%d", gpus[0].Index, gpus[1].Index)
				}
			},
		},
		{
			name: "all values not available",
			in:   "0, [N/A], [N/A], [N/A], [N/A], [N/A], [N/A], [N/A], [N/A], [N/A]\n",
			want: 1,
			spot: func(t *testing.T, gpus []GPU) {
				g := gpus[0]
				if g.Name != "" || g.PState != "" {
					t.Errorf("string fields must be empty: %+v", g)
				}
				if g.UtilizationPct != nil || g.MemoryUsedMB != nil || g.MemoryTotalMB != nil ||
					g.TemperatureC != nil || g.PowerDrawW != nil || g.PowerLimitW != nil || g.ECCUncorrected != nil {
					t.Errorf("numeric fields must be nil: %+v", g)
				}
			},
		},
		{
			name: "comma inside GPU name",
			in:   "0, NVIDIA, GeForce RTX 3090, 5, 100, 24576, 50, 100.00, 350.00, 0, P2\n",
			want: 1,
			spot: func(t *testing.T, gpus []GPU) {
				if gpus[0].Name != "NVIDIA, GeForce RTX 3090" {
					t.Errorf("name = %q", gpus[0].Name)
				}
				if gpus[0].ECCUncorrected == nil || *gpus[0].ECCUncorrected != 0 {
					t.Errorf("ecc = %v, want 0 present", gpus[0].ECCUncorrected)
				}
			},
		},
		{name: "malformed row skipped", in: "garbage output\n", want: 0},
		{name: "non numeric index skipped", in: "x, name, 1, 2, 3, 4, 5, 6, 7, P0\n", want: 0},
		{name: "empty input", in: "", want: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gpus := parseNvidiaSMI([]byte(tc.in))
			if len(gpus) != tc.want {
				t.Fatalf("parsed %d GPUs, want %d: %+v", len(gpus), tc.want, gpus)
			}
			if tc.spot != nil {
				tc.spot(t, gpus)
			}
		})
	}
}

func TestParseNvidiaFloat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want *float64
	}{
		{"312.45", ptr(312.45)},
		{"97", ptr(97.0)},
		{" 44 ", ptr(44.0)},
		{"[N/A]", nil},
		{"N/A", nil},
		{"[Not Supported]", nil},
		{"", nil},
		{"abc", nil},
	}
	for _, tc := range tests {
		got := parseNvidiaFloat(tc.in)
		switch {
		case tc.want == nil && got != nil:
			t.Errorf("parseNvidiaFloat(%q) = %v, want nil", tc.in, *got)
		case tc.want != nil && (got == nil || *got != *tc.want):
			t.Errorf("parseNvidiaFloat(%q) = %v, want %v", tc.in, got, *tc.want)
		}
	}
}

func TestParseRocmSMI(t *testing.T) {
	t.Parallel()

	t.Run("typical payload", func(t *testing.T) {
		t.Parallel()
		gpus, err := parseRocmSMI([]byte(rocmJSONFixture))
		if err != nil {
			t.Fatal(err)
		}
		if len(gpus) != 2 {
			t.Fatalf("parsed %d GPUs, want 2", len(gpus))
		}
		if gpus[0].Index != 0 || gpus[1].Index != 1 {
			t.Errorf("cards must be sorted by index: %+v", gpus)
		}
		if gpus[0].TemperatureC == nil || *gpus[0].TemperatureC != 45.0 {
			t.Errorf("edge temperature must win over junction: %v", gpus[0].TemperatureC)
		}
	})

	t.Run("numeric json values", func(t *testing.T) {
		t.Parallel()
		gpus, err := parseRocmSMI([]byte(`{"card2": {"GPU use (%)": 55, "Temperature (Sensor edge) (C)": 61.5}}`))
		if err != nil {
			t.Fatal(err)
		}
		if len(gpus) != 1 || gpus[0].Index != 2 {
			t.Fatalf("gpus = %+v", gpus)
		}
		if gpus[0].UtilizationPct == nil || *gpus[0].UtilizationPct != 55 {
			t.Errorf("utilization = %v, want 55", gpus[0].UtilizationPct)
		}
		if gpus[0].TemperatureC == nil || *gpus[0].TemperatureC != 61.5 {
			t.Errorf("temperature = %v, want 61.5", gpus[0].TemperatureC)
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		t.Parallel()
		if _, err := parseRocmSMI([]byte("not json")); err == nil {
			t.Error("expected error for invalid JSON")
		}
	})

	t.Run("no card keys", func(t *testing.T) {
		t.Parallel()
		gpus, err := parseRocmSMI([]byte(`{"system": {"Driver version": "6.3.2"}}`))
		if err != nil {
			t.Fatal(err)
		}
		if len(gpus) != 0 {
			t.Errorf("gpus = %+v, want none", gpus)
		}
	})
}

func TestRocmFloat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   any
		want *float64
	}{
		{"string float", "45.0", ptr(45.0)},
		{"string int", "12", ptr(12.0)},
		{"string percent suffix", "96.2%", ptr(96.2)},
		{"native float", 61.5, ptr(61.5)},
		{"not available", "N/A", nil},
		{"empty string", "", nil},
		{"non numeric", "unsupported", nil},
		{"wrong type", []any{1.0}, nil},
	}
	for _, tc := range tests {
		got := rocmFloat(tc.in)
		switch {
		case tc.want == nil && got != nil:
			t.Errorf("%s: rocmFloat(%v) = %v, want nil", tc.name, tc.in, *got)
		case tc.want != nil && (got == nil || *got != *tc.want):
			t.Errorf("%s: rocmFloat(%v) = %v, want %v", tc.name, tc.in, got, *tc.want)
		}
	}
}

func TestDiscoverSysfsCards(t *testing.T) {
	t.Parallel()

	sysfs := t.TempDir()
	writeSysfsFile(t, sysfs, "class/drm/card0/device/gpu_busy_percent", "1\n")
	writeSysfsFile(t, sysfs, "class/drm/card1/device/gpu_busy_percent", "2\n")
	writeSysfsFile(t, sysfs, "class/drm/card2/device/somethingelse", "x\n") // no telemetry: skip
	writeSysfsFile(t, sysfs, "class/drm/card0-eDP-1/device/gpu_busy_percent", "9\n")
	writeSysfsFile(t, sysfs, "class/drm/renderD128/device/gpu_busy_percent", "9\n")

	cards := discoverSysfsCards(sysfs)
	if len(cards) != 2 || cards[0] != 0 || cards[1] != 1 {
		t.Errorf("discoverSysfsCards = %v, want [0 1]", cards)
	}

	if got := discoverSysfsCards(filepath.Join(sysfs, "missing")); got != nil {
		t.Errorf("missing root should yield nil, got %v", got)
	}
}

func TestCollectSysfsGPUs_PartialTelemetry(t *testing.T) {
	t.Parallel()

	sysfs := t.TempDir()
	writeSysfsFile(t, sysfs, "class/drm/card0/device/gpu_busy_percent", "13\n")
	writeSysfsFile(t, sysfs, "class/drm/card0/device/mem_info_vram_used", "not-a-number\n")

	gpus := collectSysfsGPUs(sysfs)
	if len(gpus) != 1 {
		t.Fatalf("got %d GPUs, want 1", len(gpus))
	}
	g := gpus[0]
	if g.UtilizationPct == nil || *g.UtilizationPct != 13 {
		t.Errorf("utilization = %v, want 13", g.UtilizationPct)
	}
	if g.MemoryUsedMB != nil {
		t.Errorf("unparseable vram file must yield nil, got %v", *g.MemoryUsedMB)
	}
	if g.TemperatureC != nil || g.PowerDrawW != nil {
		t.Errorf("missing hwmon must yield nil temp/power: %+v", g)
	}
}

func TestReadSysfsScaledFloat(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	temp := filepath.Join(dir, "temp1_input")
	if err := os.WriteFile(temp, []byte("45000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readSysfsScaledFloat(temp, 1000); got == nil || *got != 45.0 {
		t.Errorf("readSysfsScaledFloat = %v, want 45.0", got)
	}
	if got := readSysfsScaledFloat(filepath.Join(dir, "missing"), 1000); got != nil {
		t.Errorf("missing file = %v, want nil", *got)
	}
	garbage := filepath.Join(dir, "garbage")
	if err := os.WriteFile(garbage, []byte("oops\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readSysfsScaledFloat(garbage, 1000); got != nil {
		t.Errorf("garbage file = %v, want nil", *got)
	}
}

func TestFinalizeGPU(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		gpu          GPU
		wantPct      *float64
		wantWarnings int
	}{
		{
			name:         "healthy boundary values do not warn",
			gpu:          GPU{TemperatureC: ptr(85.0), ECCUncorrected: ptrInt(0), MemoryUsedMB: ptr(9500.0), MemoryTotalMB: ptr(10000.0)},
			wantPct:      ptr(95.0),
			wantWarnings: 0,
		},
		{
			name:         "all thresholds exceeded",
			gpu:          GPU{TemperatureC: ptr(86.0), ECCUncorrected: ptrInt(1), MemoryUsedMB: ptr(9600.0), MemoryTotalMB: ptr(10000.0)},
			wantPct:      ptr(96.0),
			wantWarnings: 3,
		},
		{
			name:    "memory pct rounded to one decimal",
			gpu:     GPU{MemoryUsedMB: ptr(79544.0), MemoryTotalMB: ptr(81920.0)},
			wantPct: ptr(97.1),
			// 97.1 > 95 triggers the memory warning
			wantWarnings: 1,
		},
		{
			name:         "zero total memory yields no pct",
			gpu:          GPU{MemoryUsedMB: ptr(10.0), MemoryTotalMB: ptr(0.0)},
			wantPct:      nil,
			wantWarnings: 0,
		},
		{
			name:         "preset pct from rocm is kept",
			gpu:          GPU{MemoryUsedPct: ptr(96.24)},
			wantPct:      ptr(96.2),
			wantWarnings: 1,
		},
		{
			name:         "no telemetry no warnings",
			gpu:          GPU{},
			wantPct:      nil,
			wantWarnings: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := tc.gpu
			finalizeGPU(&g)
			switch {
			case tc.wantPct == nil && g.MemoryUsedPct != nil:
				t.Errorf("memory_used_pct = %v, want nil", *g.MemoryUsedPct)
			case tc.wantPct != nil && (g.MemoryUsedPct == nil || *g.MemoryUsedPct != *tc.wantPct):
				t.Errorf("memory_used_pct = %v, want %v", g.MemoryUsedPct, *tc.wantPct)
			}
			if len(g.WarningReasons) != tc.wantWarnings {
				t.Errorf("warnings = %v, want %d", g.WarningReasons, tc.wantWarnings)
			}
			if g.WarningReasons == nil {
				t.Error("warning_reasons must be non-nil after finalize")
			}
		})
	}
}

func TestBuildResult(t *testing.T) {
	t.Parallel()

	res := buildResult("get_gpu_status", "sysfs", []GPU{{Index: 0, TemperatureC: ptr(90.0)}, {Index: 1}})
	if res.Status != registry.StatusWarning {
		t.Errorf("status = %q, want warning", res.Status)
	}
	if !strings.Contains(res.Summary, "sysfs") {
		t.Errorf("summary %q must mention the backend", res.Summary)
	}

	res = buildResult("get_gpu_status", "nvidia-smi", []GPU{{Index: 0}})
	if res.Status != registry.StatusOK {
		t.Errorf("status = %q, want ok", res.Status)
	}
}

func TestRound1(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in, want float64
	}{
		{97.099609375, 97.1},
		{5.0, 5.0},
		{2.25, 2.3},
		{0, 0},
	}
	for _, tc := range tests {
		if got := round1(tc.in); got != tc.want {
			t.Errorf("round1(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func ptr(f float64) *float64 { return &f }
func ptrInt(i int64) *int64  { return &i }
