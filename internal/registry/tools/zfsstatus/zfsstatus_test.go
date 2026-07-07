package zfsstatus

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

const arcstatsFixture = `13 1 0x01 123 33456 8574782756 622981939884293
name                            type data
hits                            4    9607
misses                          4    393
deleted                         4    120480
size                            4    8589934592
c_max                           4    17179869184
`

// zpool list -Hp -o name,size,alloc,free,frag,cap,health (tab-separated).
const zpoolListFixture = "tank\t1099511627776\t549755813888\t549755813888\t23\t50\tONLINE\n" +
	"backup\t107374182400\t98784247808\t8589934592\t45\t92\tDEGRADED\n"

const zpoolStatusTank = `  pool: tank
 state: ONLINE
  scan: scrub repaired 0B in 00:10:00 with 0 errors on Sun Jul  6 00:34:01 2025
config:

	NAME        STATE     READ WRITE CKSUM
	tank        ONLINE       0     0     0
	  mirror-0  ONLINE       0     0     0
	    sda     ONLINE       0     0     0
	    sdb     ONLINE       0     0     0

errors: No known data errors
`

const zpoolStatusBackup = `  pool: backup
 state: DEGRADED
status: One or more devices is currently being resilvered.
  scan: resilver in progress since Mon Jul  7 10:00:00 2025
	1.62T scanned at 1.21G/s, 1.35T issued at 1.01G/s, 1.62T total
	0B repaired, 83.12% done, 00:04:33 to go
config:

	NAME        STATE     READ WRITE CKSUM
	backup      DEGRADED     0     0     0
	  mirror-0  DEGRADED     0     0     0
	    sdc     ONLINE       1     0     0
	    sdd     OFFLINE      2     1     3

errors: No known data errors
`

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

// newFixtureTool builds a tool wired to fake procfs/sysfs trees (module
// loaded, arcstats present) and a mocked zpool binary reporting two pools.
func newFixtureTool(t testing.TB) *Tool {
	t.Helper()
	root := t.TempDir()
	procRoot := filepath.Join(root, "proc")
	sysRoot := filepath.Join(root, "sys")

	writeFile(t, filepath.Join(procRoot, "spl", "kstat", "zfs", "arcstats"), arcstatsFixture)
	writeFile(t, filepath.Join(sysRoot, "module", "zfs", "version"), "2.2.4-1\n")

	tool := New()
	tool.procfsRoot = procRoot
	tool.sysfsRoot = sysRoot
	tool.lookPath = func(file string) (string, error) {
		if file == "zpool" {
			return "/mock/sbin/zpool", nil
		}
		return "", errors.New("not found")
	}
	tool.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "zpool" {
			return nil, errors.New("unexpected command: " + name)
		}
		if len(args) == 0 {
			return nil, errors.New("missing subcommand")
		}
		switch args[0] {
		case "list":
			return []byte(zpoolListFixture), nil
		case "status":
			switch args[len(args)-1] {
			case "tank":
				return []byte(zpoolStatusTank), nil
			case "backup":
				return []byte(zpoolStatusBackup), nil
			}
		}
		return nil, errors.New("unexpected zpool args")
	}
	return tool
}

func decodeOutput(t *testing.T, res *registry.ToolResult) Output {
	t.Helper()
	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("failed to unmarshal output: %v (raw: %s)", err, string(res.Data))
	}
	return out
}

func TestZFSStatus_Contract(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "get_zfs_status" {
		t.Errorf("expected get_zfs_status, got %s", tool.Name())
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
	for _, src := range []string{"/proc/spl/kstat/zfs/arcstats", "/sys/module/zfs", "zpool"} {
		if !strings.Contains(help, src) {
			t.Errorf("Help() must reference data source %q", src)
		}
	}
}

func TestZFSStatus_IsSupported(t *testing.T) {
	t.Parallel()

	t.Run("module loaded", func(t *testing.T) {
		t.Parallel()
		tool := newFixtureTool(t)
		tool.lookPath = func(string) (string, error) { return "", errors.New("not found") }
		supported, reason := tool.IsSupported()
		if !supported {
			t.Errorf("expected supported via /sys/module/zfs, got false (%s)", reason)
		}
	})

	t.Run("binary only", func(t *testing.T) {
		t.Parallel()
		tool := newFixtureTool(t)
		tool.sysfsRoot = t.TempDir()
		supported, _ := tool.IsSupported()
		if !supported {
			t.Error("expected supported via zpool binary")
		}
	})

	t.Run("neither", func(t *testing.T) {
		t.Parallel()
		tool := newFixtureTool(t)
		tool.sysfsRoot = t.TempDir()
		tool.lookPath = func(string) (string, error) { return "", errors.New("not found") }
		supported, reason := tool.IsSupported()
		if supported {
			t.Error("expected unsupported with no module and no binary")
		}
		if !strings.Contains(reason, "ZFS") {
			t.Errorf("reason should mention ZFS, got %q", reason)
		}
	})
}

func TestZFSStatus_Execute_FullStack(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected encapsulated errors, got hard error: %v", err)
	}
	if res.Status != registry.StatusWarning {
		t.Fatalf("expected warning (backup pool degraded), got %s (%s)", res.Status, res.Summary)
	}

	out := decodeOutput(t, res)

	if out.ZFSVersion != "2.2.4-1" {
		t.Errorf("expected zfs_version 2.2.4-1, got %q", out.ZFSVersion)
	}

	if out.ARC == nil {
		t.Fatal("expected arc block present")
	}
	if out.ARC.SizeMB != 8192 {
		t.Errorf("expected arc size_mb 8192, got %d", out.ARC.SizeMB)
	}
	if out.ARC.MaxMB != 16384 {
		t.Errorf("expected arc max_mb 16384, got %d", out.ARC.MaxMB)
	}
	if out.ARC.HitRatioPct != 96.07 {
		t.Errorf("expected arc hit_ratio_pct 96.07, got %v", out.ARC.HitRatioPct)
	}

	if len(out.Pools) != 2 {
		t.Fatalf("expected 2 pools, got %d", len(out.Pools))
	}

	tank := out.Pools[0]
	if tank.Name != "tank" || tank.Health != "ONLINE" {
		t.Errorf("unexpected tank pool: %+v", tank)
	}
	if tank.SizeGB != 1024.0 {
		t.Errorf("expected tank size_gb 1024.0, got %v", tank.SizeGB)
	}
	if tank.AllocGB != 512.0 {
		t.Errorf("expected tank alloc_gb 512.0, got %v", tank.AllocGB)
	}
	if tank.FragPct != 23 {
		t.Errorf("expected tank frag_pct 23, got %v", tank.FragPct)
	}
	if tank.CapacityPct != 50 {
		t.Errorf("expected tank capacity_pct 50, got %v", tank.CapacityPct)
	}
	if tank.ScanState != "scrub repaired" {
		t.Errorf("expected tank scan_state 'scrub repaired', got %q", tank.ScanState)
	}
	if tank.ScanPct != 0 {
		t.Errorf("expected tank scan_pct 0, got %v", tank.ScanPct)
	}
	if tank.Errors.Read != 0 || tank.Errors.Write != 0 || tank.Errors.Cksum != 0 {
		t.Errorf("expected zero tank errors, got %+v", tank.Errors)
	}
	if len(tank.WarningReasons) != 0 {
		t.Errorf("expected no tank warnings, got %v", tank.WarningReasons)
	}

	backup := out.Pools[1]
	if backup.Health != "DEGRADED" {
		t.Errorf("expected backup health DEGRADED, got %s", backup.Health)
	}
	if backup.CapacityPct != 92 {
		t.Errorf("expected backup capacity_pct 92, got %v", backup.CapacityPct)
	}
	if backup.ScanState != "resilver in progress" {
		t.Errorf("expected backup scan_state 'resilver in progress', got %q", backup.ScanState)
	}
	if backup.ScanPct != 83.12 {
		t.Errorf("expected backup scan_pct 83.12, got %v", backup.ScanPct)
	}
	// Summed across all config rows: sdc(1,0,0) + sdd(2,1,3).
	if backup.Errors.Read != 3 || backup.Errors.Write != 1 || backup.Errors.Cksum != 3 {
		t.Errorf("expected backup errors read=3 write=1 cksum=3, got %+v", backup.Errors)
	}

	joined := strings.ToLower(strings.Join(backup.WarningReasons, "; "))
	for _, want := range []string{"degraded", "errors", "capacity"} {
		if !strings.Contains(joined, want) {
			t.Errorf("backup warning_reasons should mention %q, got %v", want, backup.WarningReasons)
		}
	}
}

func TestZFSStatus_Execute_NoPools(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)
	tool.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte(""), nil
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("expected status ok, got %s (%s)", res.Status, res.Summary)
	}
	if !strings.Contains(string(res.Data), `"pools":[]`) {
		t.Errorf("expected empty pools to serialize as [], got: %s", string(res.Data))
	}
}

func TestZFSStatus_Execute_ArcstatsMissing(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)
	if err := os.Remove(filepath.Join(tool.procfsRoot, "spl", "kstat", "zfs", "arcstats")); err != nil {
		t.Fatal(err)
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := decodeOutput(t, res)
	if out.ARC != nil {
		t.Errorf("expected arc block omitted, got %+v", out.ARC)
	}
	if strings.Contains(string(res.Data), `"arc"`) {
		t.Errorf("expected no arc key in JSON, got: %s", string(res.Data))
	}
	if len(out.Pools) != 2 {
		t.Errorf("pools must still be reported, got %d", len(out.Pools))
	}
}

func TestZFSStatus_Execute_ZpoolBinaryMissing(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)
	tool.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	tool.execCommand = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("must not be called without zpool binary")
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != registry.StatusDegraded {
		t.Fatalf("expected status degraded (arc-only), got %s", res.Status)
	}

	out := decodeOutput(t, res)
	if out.ARC == nil {
		t.Error("expected arc block present in arc-only mode")
	}
	if len(out.Pools) != 0 {
		t.Errorf("expected no pools, got %d", len(out.Pools))
	}
	if !strings.Contains(strings.ToLower(out.Note), "zpool") {
		t.Errorf("expected note explaining missing zpool binary, got %q", out.Note)
	}
}

func TestZFSStatus_Execute_ZpoolListFails(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)
	tool.execCommand = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("zpool: cannot open state file")
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != registry.StatusDegraded {
		t.Fatalf("expected status degraded when zpool list fails, got %s", res.Status)
	}

	out := decodeOutput(t, res)
	if out.Note == "" {
		t.Error("expected note explaining zpool list failure")
	}
}

func TestZFSStatus_Execute_StatusCommandFailureSkipsEnrichment(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)
	tool.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "list" {
			return []byte(zpoolListFixture), nil
		}
		return nil, errors.New("status unavailable")
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := decodeOutput(t, res)
	if len(out.Pools) != 2 {
		t.Fatalf("expected 2 pools despite status failures, got %d", len(out.Pools))
	}
	if out.Pools[0].ScanState != "none" {
		t.Errorf("expected scan_state none without status output, got %q", out.Pools[0].ScanState)
	}
	// backup is still DEGRADED + over capacity from the list output alone.
	if res.Status != registry.StatusWarning {
		t.Errorf("expected warning from list-derived health, got %s", res.Status)
	}
}

func TestZFSStatus_Execute_ContextCancelled(t *testing.T) {
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

func TestParseARCStats(t *testing.T) {
	t.Parallel()

	t.Run("full fixture", func(t *testing.T) {
		t.Parallel()
		arc := parseARCStats([]byte(arcstatsFixture))
		if arc == nil {
			t.Fatal("expected arc, got nil")
		}
		if arc.SizeMB != 8192 || arc.MaxMB != 16384 || arc.HitRatioPct != 96.07 {
			t.Errorf("unexpected arc: %+v", arc)
		}
	})

	t.Run("no recognized keys yields nil", func(t *testing.T) {
		t.Parallel()
		if arc := parseARCStats([]byte("name type data\nfoo 4 1\n")); arc != nil {
			t.Errorf("expected nil, got %+v", arc)
		}
	})

	t.Run("empty input yields nil", func(t *testing.T) {
		t.Parallel()
		if arc := parseARCStats(nil); arc != nil {
			t.Errorf("expected nil, got %+v", arc)
		}
	})

	t.Run("zero lookups yields zero ratio", func(t *testing.T) {
		t.Parallel()
		arc := parseARCStats([]byte("size 4 1048576\nc_max 4 2097152\nhits 4 0\nmisses 4 0\n"))
		if arc == nil {
			t.Fatal("expected arc, got nil")
		}
		if arc.HitRatioPct != 0 {
			t.Errorf("expected 0 ratio for zero lookups, got %v", arc.HitRatioPct)
		}
	})
}

func TestParseZpoolList(t *testing.T) {
	t.Parallel()

	t.Run("two pools", func(t *testing.T) {
		t.Parallel()
		pools := parseZpoolList([]byte(zpoolListFixture))
		if len(pools) != 2 {
			t.Fatalf("expected 2 pools, got %d", len(pools))
		}
		if pools[0].Name != "tank" || pools[0].SizeGB != 1024.0 || pools[0].Health != "ONLINE" {
			t.Errorf("unexpected pool 0: %+v", pools[0])
		}
		if pools[1].Name != "backup" || pools[1].CapacityPct != 92 {
			t.Errorf("unexpected pool 1: %+v", pools[1])
		}
	})

	t.Run("dash fragmentation degrades to zero", func(t *testing.T) {
		t.Parallel()
		pools := parseZpoolList([]byte("old\t1073741824\t0\t1073741824\t-\t0\tONLINE\n"))
		if len(pools) != 1 {
			t.Fatalf("expected 1 pool, got %d", len(pools))
		}
		if pools[0].FragPct != 0 {
			t.Errorf("expected frag_pct 0 for '-', got %v", pools[0].FragPct)
		}
	})

	t.Run("short and empty lines skipped", func(t *testing.T) {
		t.Parallel()
		pools := parseZpoolList([]byte("no pools available\n\n"))
		if len(pools) != 0 {
			t.Errorf("expected 0 pools, got %+v", pools)
		}
	})
}

func TestParseZpoolStatus(t *testing.T) {
	t.Parallel()

	t.Run("resilver in progress", func(t *testing.T) {
		t.Parallel()
		state, pct, errs := parseZpoolStatus([]byte(zpoolStatusBackup))
		if state != "resilver in progress" {
			t.Errorf("expected 'resilver in progress', got %q", state)
		}
		if pct != 83.12 {
			t.Errorf("expected 83.12, got %v", pct)
		}
		if errs.Read != 3 || errs.Write != 1 || errs.Cksum != 3 {
			t.Errorf("expected errors 3/1/3, got %+v", errs)
		}
	})

	t.Run("scrub repaired", func(t *testing.T) {
		t.Parallel()
		state, pct, errs := parseZpoolStatus([]byte(zpoolStatusTank))
		if state != "scrub repaired" {
			t.Errorf("expected 'scrub repaired', got %q", state)
		}
		if pct != 0 {
			t.Errorf("expected 0 pct, got %v", pct)
		}
		if errs.Read != 0 || errs.Write != 0 || errs.Cksum != 0 {
			t.Errorf("expected zero errors, got %+v", errs)
		}
	})

	t.Run("no scan line", func(t *testing.T) {
		t.Parallel()
		state, pct, _ := parseZpoolStatus([]byte("  pool: x\n state: ONLINE\nconfig:\n\n\tNAME STATE READ WRITE CKSUM\n\tx ONLINE 0 0 0\n"))
		if state != "none" {
			t.Errorf("expected 'none', got %q", state)
		}
		if pct != 0 {
			t.Errorf("expected 0 pct, got %v", pct)
		}
	})

	t.Run("human-suffixed error counts", func(t *testing.T) {
		t.Parallel()
		out := "  pool: y\n state: ONLINE\nconfig:\n\n\tNAME STATE READ WRITE CKSUM\n\ty ONLINE 1.05K 0 3\n"
		_, _, errs := parseZpoolStatus([]byte(out))
		if errs.Read != 1050 || errs.Cksum != 3 {
			t.Errorf("expected read=1050 cksum=3, got %+v", errs)
		}
	})
}

func TestParseZfsCount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want int64
	}{
		{"0", 0},
		{"42", 42},
		{"1.05K", 1050},
		{"3M", 3000000},
		{"2G", 2000000000},
		{"1T", 1000000000000},
		{"-", 0},
		{"", 0},
		{"junk", 0},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			if got := parseZfsCount(tc.in); got != tc.want {
				t.Errorf("parseZfsCount(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestApplyPoolWarnings(t *testing.T) {
	t.Parallel()

	t.Run("healthy pool has no warnings", func(t *testing.T) {
		t.Parallel()
		p := Pool{Name: "tank", Health: "ONLINE", CapacityPct: 50, WarningReasons: []string{}}
		applyPoolWarnings(&p)
		if len(p.WarningReasons) != 0 {
			t.Errorf("expected no warnings, got %v", p.WarningReasons)
		}
	})

	t.Run("all rules fire", func(t *testing.T) {
		t.Parallel()
		p := Pool{
			Name:           "bad",
			Health:         "FAULTED",
			CapacityPct:    95,
			Errors:         PoolErrors{Read: 1, Write: 2, Cksum: 3},
			WarningReasons: []string{},
		}
		applyPoolWarnings(&p)
		if len(p.WarningReasons) != 3 {
			t.Fatalf("expected 3 warnings, got %v", p.WarningReasons)
		}
	})
}
