package filesystemerrors

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// mountinfoFixture covers every detection rule:
//   - nvme0n1p2: rw ext4 with "errors=remount-ro" super option (substring trap)
//   - sda1:      rw ext4 (healthy)
//   - sdb1:      ext4 mounted ro via per-mount options
//   - loop0:     squashfs ro (exempt fstype)
//   - tmpfs:     ro but source is not /dev/ (skipped)
//   - sr0:       iso9660 ro (exempt fstype)
//   - sdd1:      btrfs mounted twice (subvolumes) — must dedup to one stats call
//   - sde1:      ext4 rw per-mount but ro super options (error-triggered remount)
const mountinfoFixture = `22 1 259:2 / / rw,relatime shared:1 - ext4 /dev/nvme0n1p2 rw,errors=remount-ro
30 22 0:26 / /sys rw,nosuid,nodev,noexec,relatime shared:7 - sysfs sysfs rw
31 22 8:1 / /data rw,relatime shared:15 - ext4 /dev/sda1 rw
32 22 8:17 / /backup ro,relatime shared:16 - ext4 /dev/sdb1 ro
33 22 7:0 / /snap/core/1 ro,nodev,relatime shared:17 - squashfs /dev/loop0 ro
34 22 0:44 / /var/lib/foo ro shared:18 - tmpfs tmpfs ro
35 22 8:33 / /srv/media ro,relatime shared:19 - iso9660 /dev/sr0 ro
36 22 8:49 / /mnt/btr rw,relatime shared:20 - btrfs /dev/sdd1 rw,space_cache=v2,subvol=/
37 22 8:49 /home /mnt/btrhome rw,relatime shared:21 - btrfs /dev/sdd1 rw,space_cache=v2,subvol=/home
38 22 8:65 / /mnt/roerr rw,relatime shared:22 - ext4 /dev/sde1 ro
`

const btrfsStatsOutput = `[/dev/sdd1].write_io_errs   3
[/dev/sdd1].read_io_errs    0
[/dev/sdd1].flush_io_errs   0
[/dev/sdd1].corruption_errs 1
[/dev/sdd1].generation_errs 0
`

func mustWrite(tb testing.TB, path, content string) {
	tb.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		tb.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		tb.Fatalf("write %s: %v", path, err)
	}
}

// writeFixtureTree builds a fake procfs (mountinfo) and sysfs (ext4 error
// attributes) tree exercising healthy devices, error counters, absent
// files, the non-device "features" directory, and a garbage counter.
func writeFixtureTree(tb testing.TB) (sysfsRoot, procfsRoot string) {
	tb.Helper()
	dir := tb.TempDir()
	sysfsRoot = filepath.Join(dir, "sys")
	procfsRoot = filepath.Join(dir, "proc")

	mustWrite(tb, filepath.Join(procfsRoot, "self", "mountinfo"), mountinfoFixture)

	ext4 := filepath.Join(sysfsRoot, "fs", "ext4")
	mustWrite(tb, filepath.Join(ext4, "nvme0n1p2", "errors_count"), "0\n")
	mustWrite(tb, filepath.Join(ext4, "sda1", "errors_count"), "3\n")
	mustWrite(tb, filepath.Join(ext4, "sda1", "first_error_time"), "1751000000\n")
	mustWrite(tb, filepath.Join(ext4, "sda1", "last_error_time"), "1751500000\n")
	mustWrite(tb, filepath.Join(ext4, "sda1", "first_error_func"), "ext4_journal_check_start\n")
	if err := os.MkdirAll(filepath.Join(ext4, "sdb1"), 0o755); err != nil {
		tb.Fatalf("mkdir: %v", err)
	}
	// Non-device feature directory must be excluded from the device scan.
	mustWrite(tb, filepath.Join(ext4, "features", "fast_commit"), "supported\n")
	// Unparseable counter: the device is skipped, not zeroed.
	mustWrite(tb, filepath.Join(ext4, "sde1", "errors_count"), "garbage\n")

	return sysfsRoot, procfsRoot
}

func newFixtureTool(tb testing.TB) *Tool {
	tb.Helper()
	sysfsRoot, procfsRoot := writeFixtureTree(tb)
	tool := New()
	tool.sysfsRoot = sysfsRoot
	tool.procfsRoot = procfsRoot
	tool.lookPath = func(file string) (string, error) {
		if file == "btrfs" {
			return "/mock/sbin/btrfs", nil
		}
		return "", errors.New("not found")
	}
	tool.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "btrfs" || len(args) != 3 || args[0] != "device" || args[1] != "stats" {
			return nil, errors.New("unexpected command")
		}
		if args[2] != "/mnt/btr" {
			return nil, errors.New("unexpected mount: " + args[2])
		}
		return []byte(btrfsStatsOutput), nil
	}
	return tool
}

func executeJSON(tb testing.TB, tool *Tool, args string) (*registry.ToolResult, Data) {
	tb.Helper()
	res, err := tool.Execute(context.Background(), json.RawMessage(args))
	if err != nil {
		tb.Fatalf("Execute returned hard error: %v", err)
	}
	var data Data
	if res.Status != registry.StatusError {
		if err := json.Unmarshal(res.Data, &data); err != nil {
			tb.Fatalf("unmarshal result data: %v", err)
		}
	}
	return res, data
}

func TestContractCompliance(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "get_filesystem_errors" {
		t.Errorf("Name() = %q, want get_filesystem_errors", tool.Name())
	}
	if tool.Category() != registry.CategoryStorage {
		t.Errorf("Category() = %q, want storage", tool.Category())
	}
	if tool.Description() == "" {
		t.Error("Description() must be non-empty")
	}
	if tool.Hidden() {
		t.Error("Hidden() must be false")
	}
	if len(tool.Parameters()) != 0 {
		t.Errorf("Parameters() len = %d, want 0", len(tool.Parameters()))
	}

	help := tool.Help()
	for _, src := range []string{"/sys/fs/ext4", "/proc/self/mountinfo", "btrfs device stats", "XFS", "query_dmesg"} {
		if !strings.Contains(help, src) {
			t.Errorf("Help() missing reference %q", src)
		}
	}
}

func TestIsSupported(t *testing.T) {
	t.Parallel()

	tool := newFixtureTool(t)
	if supported, reason := tool.IsSupported(); !supported {
		t.Errorf("IsSupported() = false (%q) with readable mountinfo fixture", reason)
	}

	missing := New()
	missing.procfsRoot = filepath.Join(t.TempDir(), "nowhere")
	supported, reason := missing.IsSupported()
	if supported {
		t.Error("IsSupported() = true without mountinfo")
	}
	if !strings.Contains(reason, "mountinfo") {
		t.Errorf("reason %q should mention mountinfo", reason)
	}
}

func TestExecuteHappyPath(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)

	res, data := executeJSON(t, tool, `{}`)
	if res.Status != registry.StatusWarning {
		t.Fatalf("Status = %s (summary %q), want warning", res.Status, res.Summary)
	}

	// --- ext4: features excluded, sde1 (garbage counter) skipped. ---
	if len(data.Ext4) != 3 {
		t.Fatalf("ext4 len = %d, want 3 (%+v)", len(data.Ext4), data.Ext4)
	}
	byDev := map[string]Ext4Device{}
	for _, d := range data.Ext4 {
		byDev[d.Device] = d
	}

	root, ok := byDev["nvme0n1p2"]
	if !ok {
		t.Fatal("missing nvme0n1p2 entry")
	}
	if root.ErrorsCount != 0 || root.Mount != "/" {
		t.Errorf("nvme0n1p2 = %+v, want healthy entry mounted at /", root)
	}
	if root.FirstError != "" || root.LastError != "" || root.FirstErrorFunc != "" {
		t.Errorf("healthy device must omit error details, got %+v", root)
	}

	sda1, ok := byDev["sda1"]
	if !ok {
		t.Fatal("missing sda1 entry")
	}
	if sda1.ErrorsCount != 3 || sda1.Mount != "/data" {
		t.Errorf("sda1 = %+v, want 3 errors mounted at /data", sda1)
	}
	wantFirst := time.Unix(1751000000, 0).UTC().Format(time.RFC3339)
	if sda1.FirstError != wantFirst {
		t.Errorf("sda1 first_error = %q, want %q", sda1.FirstError, wantFirst)
	}
	wantLast := time.Unix(1751500000, 0).UTC().Format(time.RFC3339)
	if sda1.LastError != wantLast {
		t.Errorf("sda1 last_error = %q, want %q", sda1.LastError, wantLast)
	}
	if sda1.FirstErrorFunc != "ext4_journal_check_start" {
		t.Errorf("sda1 first_error_func = %q", sda1.FirstErrorFunc)
	}

	sdb1, ok := byDev["sdb1"]
	if !ok {
		t.Fatal("missing sdb1 entry (absent errors_count = healthy)")
	}
	if sdb1.ErrorsCount != 0 || sdb1.Mount != "/backup" {
		t.Errorf("sdb1 = %+v, want healthy entry mounted at /backup", sdb1)
	}

	if _, ok := byDev["sde1"]; ok {
		t.Error("sde1 with unparseable errors_count must be skipped")
	}
	if _, ok := byDev["features"]; ok {
		t.Error("the ext4 'features' directory must not be reported as a device")
	}

	// --- read-only mounts: token match only, exempt fstypes skipped. ---
	if len(data.ReadonlyMounts) != 2 {
		t.Fatalf("readonly_mounts = %+v, want 2 entries", data.ReadonlyMounts)
	}
	if data.ReadonlyMounts[0].Mount != "/backup" || data.ReadonlyMounts[0].Device != "/dev/sdb1" || data.ReadonlyMounts[0].Fstype != "ext4" {
		t.Errorf("readonly[0] = %+v, want /backup via /dev/sdb1", data.ReadonlyMounts[0])
	}
	if data.ReadonlyMounts[1].Mount != "/mnt/roerr" || data.ReadonlyMounts[1].Device != "/dev/sde1" {
		t.Errorf("readonly[1] = %+v, want /mnt/roerr (ro in super options)", data.ReadonlyMounts[1])
	}

	// --- btrfs: two subvolume mounts of /dev/sdd1 dedup to one entry. ---
	if len(data.Btrfs) != 1 {
		t.Fatalf("btrfs = %+v, want single deduplicated entry", data.Btrfs)
	}
	b := data.Btrfs[0]
	if b.Mount != "/mnt/btr" || b.Device != "/dev/sdd1" {
		t.Errorf("btrfs entry = %+v, want /mnt/btr via /dev/sdd1", b)
	}
	if b.WriteIOErrs != 3 || b.ReadIOErrs != 0 || b.FlushIOErrs != 0 || b.CorruptionErrs != 1 || b.GenerationErrs != 0 {
		t.Errorf("btrfs counters = %+v, want write=3 corruption=1", b)
	}

	// --- summary & warnings ---
	if data.Summary.Ext4DevicesChecked != 3 {
		t.Errorf("ext4_devices_checked = %d, want 3", data.Summary.Ext4DevicesChecked)
	}
	if data.Summary.DevicesWithErrors != 2 {
		t.Errorf("devices_with_errors = %d, want 2 (sda1 + btrfs sdd1)", data.Summary.DevicesWithErrors)
	}
	if data.Summary.ReadonlyCount != 2 {
		t.Errorf("readonly_count = %d, want 2", data.Summary.ReadonlyCount)
	}

	if len(data.WarningReasons) != 4 {
		t.Fatalf("warning_reasons = %v, want 4 entries", data.WarningReasons)
	}
	joined := strings.Join(data.WarningReasons, "\n")
	for _, want := range []string{
		"ext4 sda1 has recorded 3 filesystem errors since last fsck — check dmesg",
		"/backup is mounted read-only — possible error-triggered remount",
		"/mnt/roerr is mounted read-only — possible error-triggered remount",
		"write_io_errs=3",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("warning_reasons missing %q in:\n%s", want, joined)
		}
	}
}

func TestExecuteHealthySystem(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sysfsRoot := filepath.Join(dir, "sys")
	procfsRoot := filepath.Join(dir, "proc")
	mustWrite(t, filepath.Join(procfsRoot, "self", "mountinfo"),
		"22 1 259:2 / / rw,relatime shared:1 - ext4 /dev/nvme0n1p2 rw\n")
	mustWrite(t, filepath.Join(sysfsRoot, "fs", "ext4", "nvme0n1p2", "errors_count"), "0\n")

	tool := New()
	tool.sysfsRoot = sysfsRoot
	tool.procfsRoot = procfsRoot
	tool.lookPath = func(string) (string, error) { return "", errors.New("not found") }

	res, data := executeJSON(t, tool, `{}`)
	if res.Status != registry.StatusOK {
		t.Fatalf("Status = %s (summary %q), want ok", res.Status, res.Summary)
	}
	if len(data.Ext4) != 1 || data.Ext4[0].ErrorsCount != 0 {
		t.Errorf("ext4 = %+v, want single healthy device", data.Ext4)
	}
	if len(data.ReadonlyMounts) != 0 {
		t.Errorf("readonly_mounts = %+v, want empty", data.ReadonlyMounts)
	}
	if data.Btrfs != nil {
		t.Errorf("btrfs = %+v, want omitted with no btrfs mounts", data.Btrfs)
	}
	if len(data.WarningReasons) != 0 {
		t.Errorf("warning_reasons = %v, want empty", data.WarningReasons)
	}
	if data.Note != "" {
		t.Errorf("note = %q, want empty (no btrfs mounts to enrich)", data.Note)
	}
}

func TestExecuteNoExt4OrBtrfs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	procfsRoot := filepath.Join(dir, "proc")
	mustWrite(t, filepath.Join(procfsRoot, "self", "mountinfo"),
		"30 22 0:26 / /sys rw,nosuid shared:7 - sysfs sysfs rw\n"+
			"31 22 0:27 / /run rw shared:8 - tmpfs tmpfs rw\n")

	tool := New()
	tool.sysfsRoot = filepath.Join(dir, "sys")
	tool.procfsRoot = procfsRoot
	tool.lookPath = func(string) (string, error) { return "", errors.New("not found") }

	res, data := executeJSON(t, tool, `{}`)
	if res.Status != registry.StatusOK {
		t.Fatalf("Status = %s, want ok on a node without ext4/btrfs", res.Status)
	}
	if len(data.Ext4) != 0 || len(data.ReadonlyMounts) != 0 || data.Btrfs != nil {
		t.Errorf("expected empty report, got %+v", data)
	}
	if data.Summary.Ext4DevicesChecked != 0 {
		t.Errorf("ext4_devices_checked = %d, want 0", data.Summary.Ext4DevicesChecked)
	}
}

func TestExecuteBtrfsBinaryAbsent(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)
	tool.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	tool.execCommand = func(context.Context, string, ...string) ([]byte, error) {
		t.Error("execCommand must not be called when the btrfs binary is absent")
		return nil, errors.New("unreachable")
	}

	res, data := executeJSON(t, tool, `{}`)
	if res.Status != registry.StatusWarning {
		t.Fatalf("Status = %s, want warning (ext4/ro findings remain)", res.Status)
	}
	if data.Btrfs != nil {
		t.Errorf("btrfs = %+v, want omitted without the binary", data.Btrfs)
	}
	if !strings.Contains(data.Note, "btrfs") {
		t.Errorf("note = %q, want btrfs-binary explanation", data.Note)
	}
}

func TestExecuteBtrfsCommandFailure(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)
	tool.execCommand = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("btrfs exploded")
	}

	res, data := executeJSON(t, tool, `{}`)
	if res.Status != registry.StatusWarning {
		t.Fatalf("Status = %s, want warning (other findings intact)", res.Status)
	}
	if len(data.Btrfs) != 0 {
		t.Errorf("btrfs = %+v, want no entries when the command fails", data.Btrfs)
	}
}

func TestExecuteArgsIgnored(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)

	// The tool declares no parameters; arbitrary or malformed args must
	// not break execution.
	for _, args := range []string{`{"junk": true}`, `{not json`, ``} {
		res, err := tool.Execute(context.Background(), json.RawMessage(args))
		if err != nil {
			t.Fatalf("Execute(%q) returned hard error: %v", args, err)
		}
		if res.Status == registry.StatusError {
			t.Errorf("Execute(%q) status = error, want args ignored", args)
		}
	}
}

func TestExecuteMountinfoUnreadable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tool := New()
	tool.sysfsRoot = filepath.Join(dir, "sys")
	tool.procfsRoot = filepath.Join(dir, "proc")

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute must not return hard error, got %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %s, want error when mountinfo is unreadable", res.Status)
	}
}

func TestExecuteContextCancelled(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := tool.Execute(ctx, nil)
	if err != nil {
		t.Fatalf("Execute must encapsulate cancellation, got hard error %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %s, want error on cancelled context", res.Status)
	}
}

func TestParseMountInfo(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		input string
		want  int
		check func(t *testing.T, entries []mountEntry)
	}{
		{
			name:  "standard line",
			input: "22 1 259:2 / / rw,relatime shared:1 - ext4 /dev/nvme0n1p2 rw,errors=remount-ro\n",
			want:  1,
			check: func(t *testing.T, entries []mountEntry) {
				e := entries[0]
				if e.mountPoint != "/" || e.mountOpts != "rw,relatime" || e.fstype != "ext4" ||
					e.source != "/dev/nvme0n1p2" || e.superOpts != "rw,errors=remount-ro" {
					t.Errorf("entry = %+v", e)
				}
			},
		},
		{
			name:  "multiple optional fields",
			input: "36 35 98:0 /mnt1 /mnt2 rw,noatime master:1 shared:42 - ext3 /dev/root rw,errors=continue\n",
			want:  1,
			check: func(t *testing.T, entries []mountEntry) {
				if entries[0].fstype != "ext3" || entries[0].mountPoint != "/mnt2" {
					t.Errorf("entry = %+v", entries[0])
				}
			},
		},
		{
			name:  "no optional fields",
			input: "22 1 8:1 / /plain rw - ext4 /dev/sda1 rw\n",
			want:  1,
			check: func(t *testing.T, entries []mountEntry) {
				if entries[0].mountPoint != "/plain" || entries[0].superOpts != "rw" {
					t.Errorf("entry = %+v", entries[0])
				}
			},
		},
		{
			name:  "missing separator skipped",
			input: "22 1 259:2 / / rw,relatime shared:1 ext4 /dev/nvme0n1p2 rw\n",
			want:  0,
		},
		{
			name:  "short line skipped",
			input: "22 1 259:2\n\n",
			want:  0,
		},
		{
			name:  "missing super options tolerated",
			input: "22 1 259:2 / / rw shared:1 - ext4 /dev/sda1\n",
			want:  1,
			check: func(t *testing.T, entries []mountEntry) {
				if entries[0].superOpts != "" {
					t.Errorf("superOpts = %q, want empty", entries[0].superOpts)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			entries := parseMountInfo([]byte(tc.input))
			if len(entries) != tc.want {
				t.Fatalf("entries = %d, want %d (%+v)", len(entries), tc.want, entries)
			}
			if tc.check != nil {
				tc.check(t, entries)
			}
		})
	}
}

func TestHasMountOpt(t *testing.T) {
	t.Parallel()
	cases := []struct {
		opts string
		opt  string
		want bool
	}{
		{"ro,relatime", "ro", true},
		{"rw,relatime", "ro", false},
		{"rw,errors=remount-ro", "ro", false}, // substring trap
		{"rw,ro", "ro", true},
		{"ro", "ro", true},
		{"", "ro", false},
	}
	for _, tc := range cases {
		if got := hasMountOpt(tc.opts, tc.opt); got != tc.want {
			t.Errorf("hasMountOpt(%q, %q) = %t, want %t", tc.opts, tc.opt, got, tc.want)
		}
	}
}

func TestFindReadonlyMounts(t *testing.T) {
	t.Parallel()
	entries := parseMountInfo([]byte(mountinfoFixture))
	got := findReadonlyMounts(entries)
	if len(got) != 2 {
		t.Fatalf("readonly mounts = %+v, want 2", got)
	}
	if got[0].Mount != "/backup" || got[1].Mount != "/mnt/roerr" {
		t.Errorf("mounts = %s,%s want /backup,/mnt/roerr", got[0].Mount, got[1].Mount)
	}
}

func TestExt4MountMap(t *testing.T) {
	t.Parallel()
	entries := parseMountInfo([]byte(mountinfoFixture))
	m := ext4MountMap(entries)
	if m["nvme0n1p2"] != "/" || m["sda1"] != "/data" || m["sdb1"] != "/backup" {
		t.Errorf("ext4 mount map = %v", m)
	}
	if _, ok := m["sdd1"]; ok {
		t.Error("btrfs source must not appear in the ext4 mount map")
	}
}

func TestCollectExt4NoSysfs(t *testing.T) {
	t.Parallel()
	tool := New()
	tool.sysfsRoot = filepath.Join(t.TempDir(), "sys")
	if got := tool.collectExt4(map[string]string{}); len(got) != 0 {
		t.Errorf("collectExt4 without /sys/fs/ext4 = %+v, want empty", got)
	}
}

func TestParseBtrfsStats(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		input string
		want  int
		check func(t *testing.T, stats []BtrfsDeviceStats)
	}{
		{
			name:  "single device",
			input: btrfsStatsOutput,
			want:  1,
			check: func(t *testing.T, stats []BtrfsDeviceStats) {
				s := stats[0]
				if s.Device != "/dev/sdd1" || s.WriteIOErrs != 3 || s.CorruptionErrs != 1 {
					t.Errorf("stats = %+v", s)
				}
				if s.Mount != "/mnt/btr" {
					t.Errorf("mount = %q, want /mnt/btr", s.Mount)
				}
			},
		},
		{
			name: "multiple devices",
			input: "[/dev/sda1].write_io_errs   0\n[/dev/sda1].read_io_errs    2\n" +
				"[/dev/sdb1].write_io_errs   5\n[/dev/sdb1].generation_errs 7\n",
			want: 2,
			check: func(t *testing.T, stats []BtrfsDeviceStats) {
				if stats[0].Device != "/dev/sda1" || stats[0].ReadIOErrs != 2 {
					t.Errorf("stats[0] = %+v", stats[0])
				}
				if stats[1].Device != "/dev/sdb1" || stats[1].WriteIOErrs != 5 || stats[1].GenerationErrs != 7 {
					t.Errorf("stats[1] = %+v", stats[1])
				}
			},
		},
		{
			name:  "garbage and unknown counters ignored",
			input: "not a stats line\n[/dev/sda1].future_counter 9\n[/dev/sda1].flush_io_errs 4\n",
			want:  1,
			check: func(t *testing.T, stats []BtrfsDeviceStats) {
				if stats[0].FlushIOErrs != 4 {
					t.Errorf("stats = %+v, want flush=4", stats[0])
				}
			},
		},
		{
			name:  "empty output",
			input: "",
			want:  0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stats := parseBtrfsStats("/mnt/btr", []byte(tc.input))
			if len(stats) != tc.want {
				t.Fatalf("stats len = %d, want %d (%+v)", len(stats), tc.want, stats)
			}
			if tc.check != nil {
				tc.check(t, stats)
			}
		})
	}
}

func TestReadEpochRFC3339(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	valid := filepath.Join(dir, "valid")
	mustWrite(t, valid, "1751000000\n")
	want := time.Unix(1751000000, 0).UTC().Format(time.RFC3339)
	if got := readEpochRFC3339(valid); got != want {
		t.Errorf("readEpochRFC3339(valid) = %q, want %q", got, want)
	}

	zero := filepath.Join(dir, "zero")
	mustWrite(t, zero, "0\n")
	if got := readEpochRFC3339(zero); got != "" {
		t.Errorf("readEpochRFC3339(zero) = %q, want empty", got)
	}

	if got := readEpochRFC3339(filepath.Join(dir, "missing")); got != "" {
		t.Errorf("readEpochRFC3339(missing) = %q, want empty", got)
	}

	garbage := filepath.Join(dir, "garbage")
	mustWrite(t, garbage, "not a number\n")
	if got := readEpochRFC3339(garbage); got != "" {
		t.Errorf("readEpochRFC3339(garbage) = %q, want empty", got)
	}
}

func TestReadTrimmed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	f := filepath.Join(dir, "func")
	mustWrite(t, f, "  ext4_journal_check_start \n")
	if got := readTrimmed(f); got != "ext4_journal_check_start" {
		t.Errorf("readTrimmed = %q", got)
	}
	if got := readTrimmed(filepath.Join(dir, "missing")); got != "" {
		t.Errorf("readTrimmed(missing) = %q, want empty", got)
	}
}

func TestBuildWarnings(t *testing.T) {
	t.Parallel()

	ext4 := []Ext4Device{
		{Device: "sda1", ErrorsCount: 3},
		{Device: "sdb1", ErrorsCount: 0},
	}
	ro := []ReadonlyMount{{Mount: "/backup", Device: "/dev/sdb1", Fstype: "ext4"}}
	btrfs := []BtrfsDeviceStats{
		{Mount: "/mnt/btr", Device: "/dev/sdd1", CorruptionErrs: 1},
		{Mount: "/mnt/ok", Device: "/dev/sdf1"},
	}

	got := buildWarnings(ext4, ro, btrfs)
	if len(got) != 3 {
		t.Fatalf("warnings = %v, want 3", got)
	}
	if !strings.Contains(got[0], "sda1") || !strings.Contains(got[0], "3 filesystem errors") {
		t.Errorf("ext4 warning = %q", got[0])
	}
	if !strings.Contains(got[1], "/backup") || !strings.Contains(got[1], "read-only") {
		t.Errorf("ro warning = %q", got[1])
	}
	if !strings.Contains(got[2], "/dev/sdd1") || !strings.Contains(got[2], "corruption_errs=1") {
		t.Errorf("btrfs warning = %q", got[2])
	}

	if got := buildWarnings(nil, nil, nil); len(got) != 0 {
		t.Errorf("no findings must yield no warnings, got %v", got)
	}
}
