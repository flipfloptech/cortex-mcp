package raidhealth

import (
	"context"
	"encoding/json"
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

// newFixtureTool builds a tool wired to a fake procfs/sysfs tree containing
// a healthy raid1 array (md0) plus non-md noise devices.
func newFixtureTool(t testing.TB) *Tool {
	t.Helper()
	root := t.TempDir()
	procRoot := filepath.Join(root, "proc")
	sysRoot := filepath.Join(root, "sys")

	writeFile(t, filepath.Join(procRoot, "mdstat"), "Personalities : [raid1]\nmd0 : active raid1 sdb1[1] sda1[0]\n")

	md0 := filepath.Join(sysRoot, "block", "md0", "md")
	writeFile(t, filepath.Join(md0, "array_state"), "clean\n")
	writeFile(t, filepath.Join(md0, "degraded"), "0\n")
	writeFile(t, filepath.Join(md0, "sync_action"), "idle\n")
	writeFile(t, filepath.Join(md0, "sync_completed"), "none\n")
	writeFile(t, filepath.Join(md0, "mismatch_cnt"), "0\n")
	writeFile(t, filepath.Join(md0, "raid_disks"), "2\n")
	writeFile(t, filepath.Join(md0, "level"), "raid1\n")
	writeFile(t, filepath.Join(md0, "dev-sda1", "state"), "in_sync\n")
	writeFile(t, filepath.Join(md0, "dev-sdb1", "state"), "in_sync\n")

	// Noise: a plain disk and a dm device must be ignored (no md/ subdir).
	writeFile(t, filepath.Join(sysRoot, "block", "sda", "size"), "1000\n")
	writeFile(t, filepath.Join(sysRoot, "block", "dm-0", "dm", "name"), "vg-root\n")

	tool := New()
	tool.procfsRoot = procRoot
	tool.sysfsRoot = sysRoot
	return tool
}

// addDegradedArray adds md1: degraded raid5 mid-recovery with a faulty member
// and a non-zero mismatch count.
func addDegradedArray(t testing.TB, tool *Tool) {
	t.Helper()
	md1 := filepath.Join(tool.sysfsRoot, "block", "md1", "md")
	writeFile(t, filepath.Join(md1, "array_state"), "active\n")
	writeFile(t, filepath.Join(md1, "degraded"), "1\n")
	writeFile(t, filepath.Join(md1, "sync_action"), "recover\n")
	writeFile(t, filepath.Join(md1, "sync_completed"), "12345 / 24690\n")
	writeFile(t, filepath.Join(md1, "mismatch_cnt"), "128\n")
	writeFile(t, filepath.Join(md1, "raid_disks"), "3\n")
	writeFile(t, filepath.Join(md1, "level"), "raid5\n")
	writeFile(t, filepath.Join(md1, "dev-sdc1", "state"), "faulty\n")
	writeFile(t, filepath.Join(md1, "dev-sdd1", "state"), "in_sync\n")
	writeFile(t, filepath.Join(md1, "dev-sde1", "state"), "spare\n")
}

func decodeOutput(t *testing.T, res *registry.ToolResult) Output {
	t.Helper()
	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("failed to unmarshal output: %v (raw: %s)", err, string(res.Data))
	}
	return out
}

func TestRaidHealth_Contract(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "get_raid_health" {
		t.Errorf("expected get_raid_health, got %s", tool.Name())
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
	for _, src := range []string{"/proc/mdstat", "/sys/block"} {
		if !strings.Contains(help, src) {
			t.Errorf("Help() must reference data source %q", src)
		}
	}
}

func TestRaidHealth_IsSupported(t *testing.T) {
	t.Parallel()

	t.Run("mdstat present", func(t *testing.T) {
		t.Parallel()
		tool := newFixtureTool(t)
		supported, reason := tool.IsSupported()
		if !supported {
			t.Errorf("expected supported=true, got false (reason: %s)", reason)
		}
	})

	t.Run("mdstat absent", func(t *testing.T) {
		t.Parallel()
		tool := New()
		tool.procfsRoot = t.TempDir()
		tool.sysfsRoot = t.TempDir()
		supported, reason := tool.IsSupported()
		if supported {
			t.Error("expected supported=false when /proc/mdstat is missing")
		}
		if !strings.Contains(reason, "mdstat") {
			t.Errorf("reason should mention mdstat, got: %s", reason)
		}
	})
}

func TestRaidHealth_Execute_Healthy(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected encapsulated errors, got hard error: %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("expected status ok, got %s (%s)", res.Status, res.Summary)
	}

	out := decodeOutput(t, res)
	if len(out.Arrays) != 1 {
		t.Fatalf("expected 1 array, got %d", len(out.Arrays))
	}

	a := out.Arrays[0]
	if a.Name != "md0" {
		t.Errorf("expected name md0, got %s", a.Name)
	}
	if a.Level != "raid1" {
		t.Errorf("expected level raid1, got %s", a.Level)
	}
	if a.ArrayState != "clean" {
		t.Errorf("expected array_state clean, got %s", a.ArrayState)
	}
	if a.Degraded {
		t.Error("expected degraded=false")
	}
	if a.SyncAction != "idle" {
		t.Errorf("expected sync_action idle, got %s", a.SyncAction)
	}
	if a.RebuildPct != 0 {
		t.Errorf("expected rebuild_pct 0, got %v", a.RebuildPct)
	}
	if a.MismatchCnt != 0 {
		t.Errorf("expected mismatch_cnt 0, got %d", a.MismatchCnt)
	}
	if a.RaidDisks != 2 {
		t.Errorf("expected raid_disks 2, got %d", a.RaidDisks)
	}
	if len(a.Members) != 2 {
		t.Fatalf("expected 2 members, got %d", len(a.Members))
	}
	if a.Members[0].Dev != "sda1" || a.Members[0].State != "in_sync" {
		t.Errorf("unexpected member 0: %+v", a.Members[0])
	}
	if len(a.FailedMembers) != 0 {
		t.Errorf("expected no failed members, got %v", a.FailedMembers)
	}
	if len(a.WarningReasons) != 0 {
		t.Errorf("expected no warnings, got %v", a.WarningReasons)
	}

	if out.Summary.Total != 1 || out.Summary.DegradedCount != 0 || out.Summary.RebuildingCount != 0 {
		t.Errorf("unexpected summary: %+v", out.Summary)
	}
}

func TestRaidHealth_Execute_DegradedRebuilding(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)
	addDegradedArray(t, tool)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != registry.StatusWarning {
		t.Fatalf("expected status warning, got %s", res.Status)
	}

	out := decodeOutput(t, res)
	if len(out.Arrays) != 2 {
		t.Fatalf("expected 2 arrays, got %d", len(out.Arrays))
	}

	// Arrays must be sorted by name: md0, md1.
	if out.Arrays[0].Name != "md0" || out.Arrays[1].Name != "md1" {
		t.Fatalf("expected sorted arrays [md0 md1], got [%s %s]", out.Arrays[0].Name, out.Arrays[1].Name)
	}

	a := out.Arrays[1]
	if !a.Degraded {
		t.Error("expected degraded=true")
	}
	if a.SyncAction != "recover" {
		t.Errorf("expected sync_action recover, got %s", a.SyncAction)
	}
	if a.RebuildPct != 50.0 {
		t.Errorf("expected rebuild_pct 50.0, got %v", a.RebuildPct)
	}
	if a.MismatchCnt != 128 {
		t.Errorf("expected mismatch_cnt 128, got %d", a.MismatchCnt)
	}
	if len(a.FailedMembers) != 1 || a.FailedMembers[0] != "sdc1" {
		t.Errorf("expected failed_members [sdc1], got %v", a.FailedMembers)
	}

	joined := strings.ToLower(strings.Join(a.WarningReasons, "; "))
	for _, want := range []string{"degraded", "faulty", "recover", "mismatch"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warning_reasons should mention %q, got %v", want, a.WarningReasons)
		}
	}

	if out.Summary.Total != 2 {
		t.Errorf("expected total 2, got %d", out.Summary.Total)
	}
	if out.Summary.DegradedCount != 1 {
		t.Errorf("expected degraded_count 1, got %d", out.Summary.DegradedCount)
	}
	if out.Summary.RebuildingCount != 1 {
		t.Errorf("expected rebuilding_count 1, got %d", out.Summary.RebuildingCount)
	}
}

func TestRaidHealth_Execute_NoArrays(t *testing.T) {
	t.Parallel()
	tool := New()
	root := t.TempDir()
	tool.procfsRoot = filepath.Join(root, "proc")
	tool.sysfsRoot = filepath.Join(root, "sys")
	writeFile(t, filepath.Join(tool.procfsRoot, "mdstat"), "Personalities :\nunused devices: <none>\n")

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("expected status ok, got %s", res.Status)
	}

	// Zero arrays is a valid state and must serialize as [], not null.
	if !strings.Contains(string(res.Data), `"arrays":[]`) {
		t.Errorf("expected empty arrays to serialize as [], got: %s", string(res.Data))
	}

	out := decodeOutput(t, res)
	if out.Summary.Total != 0 {
		t.Errorf("expected total 0, got %d", out.Summary.Total)
	}
}

func TestRaidHealth_Execute_MissingAttrs(t *testing.T) {
	t.Parallel()
	tool := New()
	root := t.TempDir()
	tool.procfsRoot = filepath.Join(root, "proc")
	tool.sysfsRoot = filepath.Join(root, "sys")
	writeFile(t, filepath.Join(tool.procfsRoot, "mdstat"), "Personalities : [raid0]\n")
	// md2 has an md/ dir but no attribute files at all.
	if err := os.MkdirAll(filepath.Join(tool.sysfsRoot, "block", "md2", "md"), 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("expected status ok, got %s (%s)", res.Status, res.Summary)
	}

	out := decodeOutput(t, res)
	if len(out.Arrays) != 1 {
		t.Fatalf("expected 1 array, got %d", len(out.Arrays))
	}
	a := out.Arrays[0]
	if a.Name != "md2" || a.Level != "" || a.ArrayState != "" || a.Degraded || a.RebuildPct != 0 || a.MismatchCnt != 0 || a.RaidDisks != 0 {
		t.Errorf("expected zero values for missing attrs, got %+v", a)
	}
	if len(a.Members) != 0 {
		t.Errorf("expected no members, got %v", a.Members)
	}
}

func TestRaidHealth_Execute_ContextCancelled(t *testing.T) {
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

func TestParseSyncCompleted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want float64
	}{
		{"none keyword", "none", 0},
		{"empty", "", 0},
		{"half", "12345 / 24690", 50.0},
		{"one third rounded", "1 / 3", 33.3},
		{"complete", "24690 / 24690", 100.0},
		{"garbage", "delayed", 0},
		{"zero denominator", "5 / 0", 0},
		{"missing denominator", "5 /", 0},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := parseSyncCompleted(tc.in); got != tc.want {
				t.Errorf("parseSyncCompleted(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestReadAttr(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "array_state"), "  clean\n")

	if got := readAttr(dir, "array_state"); got != "clean" {
		t.Errorf("expected trimmed 'clean', got %q", got)
	}
	if got := readAttr(dir, "missing_attr"); got != "" {
		t.Errorf("expected empty string for missing attr, got %q", got)
	}
}

func TestReadMembers(t *testing.T) {
	t.Parallel()
	mdDir := filepath.Join(t.TempDir(), "md")
	writeFile(t, filepath.Join(mdDir, "dev-sdb1", "state"), "in_sync\n")
	writeFile(t, filepath.Join(mdDir, "dev-sda1", "state"), "faulty,write_error\n")
	// dev-sdz1 has no state file → zero value state.
	if err := os.MkdirAll(filepath.Join(mdDir, "dev-sdz1"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Non dev-* entries are ignored.
	if err := os.MkdirAll(filepath.Join(mdDir, "bitmap"), 0o755); err != nil {
		t.Fatal(err)
	}

	members := readMembers(mdDir)
	if len(members) != 3 {
		t.Fatalf("expected 3 members, got %d: %v", len(members), members)
	}
	// Sorted by device name.
	if members[0].Dev != "sda1" || members[1].Dev != "sdb1" || members[2].Dev != "sdz1" {
		t.Errorf("expected sorted [sda1 sdb1 sdz1], got %v", members)
	}
	if members[0].State != "faulty,write_error" {
		t.Errorf("expected state 'faulty,write_error', got %q", members[0].State)
	}
	if members[2].State != "" {
		t.Errorf("expected empty state for missing file, got %q", members[2].State)
	}

	if got := readMembers(filepath.Join(mdDir, "nonexistent")); got != nil {
		t.Errorf("expected nil for missing dir, got %v", got)
	}
}

func TestCollectArray(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)
	addDegradedArray(t, tool)

	a := collectArray(tool.sysfsRoot, "md1")
	if a.Name != "md1" || a.Level != "raid5" || !a.Degraded {
		t.Errorf("unexpected array: %+v", a)
	}
	if a.RebuildPct != 50.0 {
		t.Errorf("expected rebuild_pct 50.0, got %v", a.RebuildPct)
	}
	if len(a.Members) != 3 {
		t.Errorf("expected 3 members, got %d", len(a.Members))
	}
	if len(a.WarningReasons) == 0 {
		t.Error("expected warnings on degraded array")
	}
}
