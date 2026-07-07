package multipathstatus

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

// writeFile is a test helper that creates a file (and parents) with content.
func writeFile(t testing.TB, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// mkdir is a test helper that creates a directory tree.
func mkdir(t testing.TB, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

// newFixtureTool builds a tool wired to a fake sysfs tree with one healthy
// multipath map (mpatha: sda running, sdb offline) and an LVM dm device
// that must be ignored. multipathd is absent by default.
func newFixtureTool(t testing.TB) *Tool {
	t.Helper()
	sysRoot := filepath.Join(t.TempDir(), "sys")

	dm0 := filepath.Join(sysRoot, "block", "dm-0")
	writeFile(t, filepath.Join(dm0, "dm", "uuid"), "mpath-3600508b4000156d700012000000b0000\n")
	writeFile(t, filepath.Join(dm0, "dm", "name"), "mpatha\n")
	mkdir(t, filepath.Join(dm0, "slaves", "sda"))
	mkdir(t, filepath.Join(dm0, "slaves", "sdb"))
	writeFile(t, filepath.Join(sysRoot, "block", "sda", "device", "state"), "running\n")
	writeFile(t, filepath.Join(sysRoot, "block", "sdb", "device", "state"), "offline\n")

	// Noise: an LVM device-mapper volume must not be reported.
	dm1 := filepath.Join(sysRoot, "block", "dm-1")
	writeFile(t, filepath.Join(dm1, "dm", "uuid"), "LVM-abc123\n")
	writeFile(t, filepath.Join(dm1, "dm", "name"), "vg-root\n")

	tool := New()
	tool.sysfsRoot = sysRoot
	tool.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	tool.execCommand = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("must not be called when multipathd is absent")
	}
	return tool
}

// addAllPathsDownMap adds mpathb whose every path is offline.
func addAllPathsDownMap(t testing.TB, tool *Tool) {
	t.Helper()
	dm2 := filepath.Join(tool.sysfsRoot, "block", "dm-2")
	writeFile(t, filepath.Join(dm2, "dm", "uuid"), "mpath-360000000000000000000000000000002\n")
	writeFile(t, filepath.Join(dm2, "dm", "name"), "mpathb\n")
	mkdir(t, filepath.Join(dm2, "slaves", "sdc"))
	mkdir(t, filepath.Join(dm2, "slaves", "sdd"))
	writeFile(t, filepath.Join(tool.sysfsRoot, "block", "sdc", "device", "state"), "offline\n")
	writeFile(t, filepath.Join(tool.sysfsRoot, "block", "sdd", "device", "state"), "offline\n")
}

func decodeOutput(t *testing.T, res *registry.ToolResult) Output {
	t.Helper()
	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("failed to unmarshal output: %v (raw: %s)", err, string(res.Data))
	}
	return out
}

func TestMultipathStatus_Contract(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "get_multipath_status" {
		t.Errorf("expected get_multipath_status, got %s", tool.Name())
	}
	if tool.Category() != registry.CategoryStorage {
		t.Errorf("expected storage, got %s", tool.Category())
	}
	if tool.Description() == "" {
		t.Error("expected non-empty description")
	}
	if tool.Hidden() {
		t.Error("expected Hidden() == false")
	}
	if len(tool.Parameters()) != 0 {
		t.Errorf("expected 0 parameters, got %d", len(tool.Parameters()))
	}

	help := tool.Help()
	for _, src := range []string{"/sys/block", "dm/uuid", "multipathd"} {
		if !strings.Contains(help, src) {
			t.Errorf("Help() must reference data source %q", src)
		}
	}
}

func TestMultipathStatus_IsSupported(t *testing.T) {
	t.Parallel()

	t.Run("mpath device in sysfs, no binaries", func(t *testing.T) {
		t.Parallel()
		tool := newFixtureTool(t)
		supported, reason := tool.IsSupported()
		if !supported {
			t.Errorf("expected supported via sysfs mpath device, got false (%s)", reason)
		}
	})

	t.Run("binary present, no devices", func(t *testing.T) {
		t.Parallel()
		tool := New()
		tool.sysfsRoot = t.TempDir()
		tool.lookPath = func(file string) (string, error) {
			if file == "multipathd" {
				return "/mock/sbin/multipathd", nil
			}
			return "", errors.New("not found")
		}
		supported, _ := tool.IsSupported()
		if !supported {
			t.Error("expected supported via multipathd binary")
		}
	})

	t.Run("nothing present", func(t *testing.T) {
		t.Parallel()
		tool := New()
		tool.sysfsRoot = t.TempDir()
		tool.lookPath = func(string) (string, error) { return "", errors.New("not found") }
		supported, reason := tool.IsSupported()
		if supported {
			t.Error("expected unsupported with no devices and no binaries")
		}
		if reason != "no multipath devices and multipathd not installed" {
			t.Errorf("unexpected reason: %q", reason)
		}
	})
}

func TestMultipathStatus_Execute_NativeScan(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected encapsulated errors, got hard error: %v", err)
	}
	if res.Status != registry.StatusWarning {
		t.Fatalf("expected warning (one offline path), got %s (%s)", res.Status, res.Summary)
	}

	out := decodeOutput(t, res)
	if len(out.Maps) != 1 {
		t.Fatalf("expected 1 map (LVM dm ignored), got %d", len(out.Maps))
	}

	m := out.Maps[0]
	if m.Name != "mpatha" {
		t.Errorf("expected name mpatha, got %s", m.Name)
	}
	if m.UUID != "mpath-3600508b4000156d700012000000b0000" {
		t.Errorf("unexpected uuid: %s", m.UUID)
	}
	if m.TotalPaths != 2 || m.ActivePaths != 1 || m.FailedPaths != 1 {
		t.Errorf("expected paths total=2 active=1 failed=1, got %+v", m)
	}
	if len(m.Paths) != 2 {
		t.Fatalf("expected 2 paths, got %d", len(m.Paths))
	}
	if m.Paths[0].Dev != "sda" || m.Paths[0].State != "running" {
		t.Errorf("unexpected path 0: %+v", m.Paths[0])
	}
	if m.Paths[1].Dev != "sdb" || m.Paths[1].State != "offline" {
		t.Errorf("unexpected path 1: %+v", m.Paths[1])
	}
	if m.DMState != "" {
		t.Errorf("expected no dm_state without multipathd, got %q", m.DMState)
	}

	joined := strings.ToLower(strings.Join(m.WarningReasons, "; "))
	if !strings.Contains(joined, "sdb") {
		t.Errorf("warning_reasons should mention offline path sdb, got %v", m.WarningReasons)
	}

	if out.Summary.TotalMaps != 1 || out.Summary.MapsWithFailedPaths != 1 || out.Summary.MapsWithNoActivePaths != 0 {
		t.Errorf("unexpected summary: %+v", out.Summary)
	}
}

func TestMultipathStatus_Execute_AllPathsDownIsCritical(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)
	addAllPathsDownMap(t, tool)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Fatalf("expected status error when a map has zero active paths, got %s", res.Status)
	}

	out := decodeOutput(t, res)
	if len(out.Maps) != 2 {
		t.Fatalf("expected 2 maps sorted by name, got %d", len(out.Maps))
	}
	if out.Maps[0].Name != "mpatha" || out.Maps[1].Name != "mpathb" {
		t.Fatalf("expected sorted [mpatha mpathb], got [%s %s]", out.Maps[0].Name, out.Maps[1].Name)
	}

	b := out.Maps[1]
	if b.ActivePaths != 0 || b.FailedPaths != 2 {
		t.Errorf("expected active=0 failed=2, got %+v", b)
	}
	joined := strings.ToLower(strings.Join(b.WarningReasons, "; "))
	if !strings.Contains(joined, "no active paths") {
		t.Errorf("expected critical no-active-paths warning, got %v", b.WarningReasons)
	}

	if out.Summary.MapsWithNoActivePaths != 1 {
		t.Errorf("expected maps_with_no_active_paths 1, got %d", out.Summary.MapsWithNoActivePaths)
	}
}

func TestMultipathStatus_Execute_NoMaps(t *testing.T) {
	t.Parallel()
	tool := New()
	tool.sysfsRoot = t.TempDir()
	tool.lookPath = func(string) (string, error) { return "", errors.New("not found") }

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("expected status ok, got %s", res.Status)
	}
	if !strings.Contains(string(res.Data), `"maps":[]`) {
		t.Errorf("expected empty maps to serialize as [], got: %s", string(res.Data))
	}
}

func TestMultipathStatus_Execute_MultipathdEnrichment(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)
	tool.lookPath = func(file string) (string, error) {
		if file == "multipathd" {
			return "/mock/sbin/multipathd", nil
		}
		return "", errors.New("not found")
	}
	tool.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "multipathd" {
			return nil, errors.New("unexpected command: " + name)
		}
		return []byte("mpatha 3600508b4000156d700012000000b0000 2 active\n"), nil
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := decodeOutput(t, res)
	if len(out.Maps) != 1 {
		t.Fatalf("expected 1 map, got %d", len(out.Maps))
	}
	if out.Maps[0].DMState != "active" {
		t.Errorf("expected enriched dm_state=active, got %q", out.Maps[0].DMState)
	}
}

func TestMultipathStatus_Execute_MultipathdErrorsIgnored(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)
	tool.lookPath = func(file string) (string, error) {
		if file == "multipathd" {
			return "/mock/sbin/multipathd", nil
		}
		return "", errors.New("not found")
	}
	tool.execCommand = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("multipathd socket unavailable")
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Native sysfs remains the source of truth: still one map, no dm_state.
	out := decodeOutput(t, res)
	if len(out.Maps) != 1 {
		t.Fatalf("expected 1 map despite multipathd failure, got %d", len(out.Maps))
	}
	if out.Maps[0].DMState != "" {
		t.Errorf("expected empty dm_state, got %q", out.Maps[0].DMState)
	}
}

func TestMultipathStatus_Execute_ContextCancelled(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := tool.Execute(ctx, nil)
	if err != nil {
		t.Fatalf("cancellation must be encapsulated, got hard error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("expected status error on cancelled context, got %s", res.Status)
	}
}

func TestParseMultipathdMaps(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want map[string]string
	}{
		{
			name: "two maps",
			in:   "mpatha 36005 4 active\nmpathb 36006 2 suspend\n",
			want: map[string]string{"mpatha": "active", "mpathb": "suspend"},
		},
		{
			name: "short lines skipped",
			in:   "garbage\n\nmpathc 36007 1 active\n",
			want: map[string]string{"mpathc": "active"},
		},
		{
			name: "empty output",
			in:   "",
			want: map[string]string{},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := parseMultipathdMaps([]byte(tc.in))
			if len(got) != len(tc.want) {
				t.Fatalf("expected %d entries, got %d: %v", len(tc.want), len(got), got)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("expected %s=%s, got %s", k, v, got[k])
				}
			}
		})
	}
}

func TestHasMpathDevices(t *testing.T) {
	t.Parallel()

	tool := newFixtureTool(t)
	if !hasMpathDevices(tool.sysfsRoot) {
		t.Error("expected mpath devices detected in fixture")
	}
	if hasMpathDevices(t.TempDir()) {
		t.Error("expected no mpath devices in empty tree")
	}
}

func TestCollectMap_MissingSlaveState(t *testing.T) {
	t.Parallel()
	sysRoot := filepath.Join(t.TempDir(), "sys")
	dm0 := filepath.Join(sysRoot, "block", "dm-0")
	writeFile(t, filepath.Join(dm0, "dm", "uuid"), "mpath-360x\n")
	writeFile(t, filepath.Join(dm0, "dm", "name"), "mpathx\n")
	mkdir(t, filepath.Join(dm0, "slaves", "sdq"))
	// sdq has no device/state file → state "unknown", counted as not active.

	m := collectMap(sysRoot, "dm-0")
	if m.TotalPaths != 1 || m.ActivePaths != 0 || m.FailedPaths != 1 {
		t.Errorf("expected total=1 active=0 failed=1, got %+v", m)
	}
	if m.Paths[0].State != "unknown" {
		t.Errorf("expected state unknown for missing state file, got %q", m.Paths[0].State)
	}
}
