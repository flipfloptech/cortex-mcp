package lustreserverstats

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// ossStats has 12 counters so the top-10 cap is exercised.
const ossStats = `snapshot_time             1589909588.361379170 secs.nsecs
read_bytes                1200 samples [bytes] 4096 1048576 20971520
write_bytes               2000 samples [bytes] 344 1048576 23437862
setattr                   900 samples [reqs]
punch                     800 samples [reqs]
sync                      700 samples [reqs]
destroy                   600 samples [reqs]
create                    500 samples [reqs]
statfs                    400 samples [reqs]
get_info                  300 samples [reqs]
connect                   200 samples [reqs]
disconnect                100 samples [reqs]
ping                      50 samples [reqs]
`

const ossBrwStats = `snapshot_time:         1589909588.361379170 (secs.nsecs)

                           read      |     write
pages per bulk r/w     rpcs  % cum % |  rpcs   % cum %
1:                     108 100 100   |    39  19  19
2:                       0   0 100   |     5   2  22

                           read      |     write
disk I/O size          ios   % cum % |   ios   % cum %
4K:                    100  50  50   |    13   6   6
8K:                     60  30  80   |    15   7  13
16K:                    20  10  90   |    22  10  24
32K:                    10   5  95   |     0   0  24
64K:                     6   3  98   |    30  14  38
128K:                    3   1  99   |    40  19  57
256K:                    1   0 100   |     0   0  57
1M:                      0   0 100   |    92  43 100

                           read      |     write
I/O time (1/1000s)     ios   % cum % |   ios   % cum %
4:                     108 100 100   |     0   0   0
`

const mdtStats = `snapshot_time             1589909588.361379170 secs.nsecs
req_waittime              5000 samples [usec] 10 1000 50000
req_active                4000 samples [reqs]
`

const mdtMdStats = `snapshot_time             1589909588.361379170 secs.nsecs
open                      1024 samples [reqs]
close                     873 samples [reqs]
getattr                   1425 samples [reqs]
setattr                   77 samples [reqs]
`

const mgsStats = `snapshot_time             1589909588.361379170 secs.nsecs
req_waittime              12 samples [usec] 10 100 500
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

// writeServerFixture builds a fake Lustre server tree split across a sysfs
// root (tunables) and a procfs root (stats files) to exercise the
// sysfs-first / procfs-fallback resolution, mirroring real Lustre layouts.
func writeServerFixture(tb testing.TB) (sysfsRoot, procfsRoot string) {
	tb.Helper()
	dir := tb.TempDir()
	sysfsRoot = filepath.Join(dir, "sys_fs_lustre")
	procfsRoot = filepath.Join(dir, "proc_fs_lustre")

	// OSS target: tunables in sysfs, stats + brw_stats only in procfs.
	ost := filepath.Join(sysfsRoot, "obdfilter", "testfs-OST0000")
	mustWrite(tb, filepath.Join(ost, "num_exports"), "5\n")
	mustWrite(tb, filepath.Join(ost, "kbytestotal"), "1000000\n")
	mustWrite(tb, filepath.Join(ost, "kbytesfree"), "250000\n")
	mustWrite(tb, filepath.Join(ost, "filestotal"), "500000\n")
	mustWrite(tb, filepath.Join(ost, "filesfree"), "400000\n")
	ostProc := filepath.Join(procfsRoot, "obdfilter", "testfs-OST0000")
	mustWrite(tb, filepath.Join(ostProc, "stats"), ossStats)
	mustWrite(tb, filepath.Join(ostProc, "brw_stats"), ossBrwStats)

	// MDS target: tunables in sysfs, stats + md_stats in procfs.
	mdt := filepath.Join(sysfsRoot, "mdt", "testfs-MDT0000")
	mustWrite(tb, filepath.Join(mdt, "num_exports"), "12\n")
	mustWrite(tb, filepath.Join(mdt, "kbytestotal"), "200000\n")
	mustWrite(tb, filepath.Join(mdt, "kbytesfree"), "150000\n")
	mustWrite(tb, filepath.Join(mdt, "filestotal"), "1000000\n")
	mustWrite(tb, filepath.Join(mdt, "filesfree"), "900000\n")
	mdtProc := filepath.Join(procfsRoot, "mdt", "testfs-MDT0000")
	mustWrite(tb, filepath.Join(mdtProc, "stats"), mdtStats)
	mustWrite(tb, filepath.Join(mdtProc, "md_stats"), mdtMdStats)

	// MGS target: everything in sysfs.
	mgs := filepath.Join(sysfsRoot, "mgs", "MGS")
	mustWrite(tb, filepath.Join(mgs, "num_exports"), "20\n")
	mustWrite(tb, filepath.Join(mgs, "stats"), mgsStats)

	return sysfsRoot, procfsRoot
}

func newFixtureTool(tb testing.TB) *Tool {
	tb.Helper()
	sysfsRoot, procfsRoot := writeServerFixture(tb)
	return &Tool{sysfsPath: sysfsRoot, procfsPath: procfsRoot}
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

	if tool.Name() != "get_lustre_server_stats" {
		t.Errorf("Name() = %q, want get_lustre_server_stats", tool.Name())
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
	help := tool.Help()
	for _, src := range []string{"/sys/fs/lustre", "/proc/fs/lustre", "brw_stats", "obdfilter", "mdt", "mgs"} {
		if !strings.Contains(help, src) {
			t.Errorf("Help() missing data source reference %q", src)
		}
	}

	params := tool.Parameters()
	if len(params) != 1 {
		t.Fatalf("Parameters() len = %d, want 1", len(params))
	}
	if params[0].Name != "target" || params[0].Type != "string" || params[0].Required {
		t.Errorf("expected optional string param 'target', got %+v", params[0])
	}
}

func TestIsSupportedCallable(t *testing.T) {
	t.Parallel()
	// DetectNodeRoles inspects the real host sysfs, so only the contract
	// shape is asserted here: an unsupported verdict must carry a reason.
	supported, reason := New().IsSupported()
	if !supported && reason == "" {
		t.Error("IsSupported() = false with empty reason")
	}
}

func TestExecuteAllRoles(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)

	res, data := executeJSON(t, tool, `{}`)
	if res.Status != registry.StatusOK {
		t.Fatalf("Status = %s (summary %q), want ok", res.Status, res.Summary)
	}

	if !data.RoleSummary.IsOSS || !data.RoleSummary.IsMDS || !data.RoleSummary.IsMGS {
		t.Errorf("role_summary = %+v, want all roles true", data.RoleSummary)
	}
	if len(data.Targets) != 3 {
		t.Fatalf("targets len = %d, want 3", len(data.Targets))
	}

	byName := map[string]TargetStats{}
	for _, tgt := range data.Targets {
		byName[tgt.Name] = tgt
	}

	ost, ok := byName["testfs-OST0000"]
	if !ok {
		t.Fatal("missing testfs-OST0000 target")
	}
	if ost.Type != "obdfilter" {
		t.Errorf("OST type = %q, want obdfilter", ost.Type)
	}
	if ost.NumExports != 5 {
		t.Errorf("OST num_exports = %d, want 5", ost.NumExports)
	}
	if ost.Capacity.KBytesTotal != 1000000 || ost.Capacity.KBytesFree != 250000 {
		t.Errorf("OST capacity = %+v", ost.Capacity)
	}
	if math.Abs(ost.Capacity.UsedPct-75.0) > 0.01 {
		t.Errorf("OST capacity used_pct = %f, want 75.0", ost.Capacity.UsedPct)
	}
	if ost.Inodes.Total != 500000 || ost.Inodes.Free != 400000 {
		t.Errorf("OST inodes = %+v", ost.Inodes)
	}
	if math.Abs(ost.Inodes.UsedPct-20.0) > 0.01 {
		t.Errorf("OST inodes used_pct = %f, want 20.0", ost.Inodes.UsedPct)
	}

	// top_stats: capped at 10 of the 12 counters, sorted by count desc.
	if len(ost.TopStats) != 10 {
		t.Fatalf("OST top_stats len = %d, want 10", len(ost.TopStats))
	}
	if ost.TopStats[0].Name != "write_bytes" || ost.TopStats[0].Count != 2000 {
		t.Errorf("OST top stat = %+v, want write_bytes/2000", ost.TopStats[0])
	}
	if ost.TopStats[1].Name != "read_bytes" || ost.TopStats[1].Count != 1200 {
		t.Errorf("OST 2nd stat = %+v, want read_bytes/1200", ost.TopStats[1])
	}
	for _, s := range ost.TopStats {
		if s.Name == "disconnect" || s.Name == "ping" || s.Name == "snapshot_time" {
			t.Errorf("top_stats must exclude truncated/non-counter entry %q", s.Name)
		}
	}

	// brw disk I/O size histogram: top 5 buckets per direction.
	if ost.BrwIOSizes == nil {
		t.Fatal("OST brw_io_sizes missing")
	}
	if len(ost.BrwIOSizes.Read) != 5 {
		t.Fatalf("brw read buckets = %d, want 5", len(ost.BrwIOSizes.Read))
	}
	if ost.BrwIOSizes.Read[0].Size != "4K" || math.Abs(ost.BrwIOSizes.Read[0].Pct-50) > 0.01 {
		t.Errorf("brw read[0] = %+v, want 4K/50%%", ost.BrwIOSizes.Read[0])
	}
	if len(ost.BrwIOSizes.Write) != 5 {
		t.Fatalf("brw write buckets = %d, want 5", len(ost.BrwIOSizes.Write))
	}
	if ost.BrwIOSizes.Write[0].Size != "1M" || math.Abs(ost.BrwIOSizes.Write[0].Pct-43) > 0.01 {
		t.Errorf("brw write[0] = %+v, want 1M/43%%", ost.BrwIOSizes.Write[0])
	}
	if ost.BrwIOSizes.Write[1].Size != "128K" {
		t.Errorf("brw write[1].Size = %q, want 128K", ost.BrwIOSizes.Write[1].Size)
	}

	mdt, ok := byName["testfs-MDT0000"]
	if !ok {
		t.Fatal("missing testfs-MDT0000 target")
	}
	if mdt.Type != "mdt" {
		t.Errorf("MDT type = %q, want mdt", mdt.Type)
	}
	if mdt.NumExports != 12 {
		t.Errorf("MDT num_exports = %d, want 12", mdt.NumExports)
	}
	if mdt.BrwIOSizes != nil {
		t.Error("MDT must not carry brw_io_sizes")
	}
	// md_stats counters merged with stats counters.
	mdCounts := map[string]uint64{}
	for _, s := range mdt.TopStats {
		mdCounts[s.Name] = s.Count
	}
	for name, want := range map[string]uint64{"open": 1024, "close": 873, "getattr": 1425, "setattr": 77, "req_waittime": 5000} {
		if mdCounts[name] != want {
			t.Errorf("MDT top_stats[%s] = %d, want %d", name, mdCounts[name], want)
		}
	}

	mgs, ok := byName["MGS"]
	if !ok {
		t.Fatal("missing MGS target")
	}
	if mgs.Type != "mgs" {
		t.Errorf("MGS type = %q, want mgs", mgs.Type)
	}
	if mgs.NumExports != 20 {
		t.Errorf("MGS num_exports = %d, want 20", mgs.NumExports)
	}
	if mgs.BrwIOSizes != nil {
		t.Error("MGS must not carry brw_io_sizes")
	}
	if mgs.Capacity.KBytesTotal != 0 || mgs.Capacity.UsedPct != 0 {
		t.Errorf("MGS capacity should be zeros, got %+v", mgs.Capacity)
	}
}

func TestExecuteTargetFilter(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)

	res, data := executeJSON(t, tool, `{"target":"testfs-MDT0000"}`)
	if res.Status != registry.StatusOK {
		t.Fatalf("Status = %s, want ok", res.Status)
	}
	if len(data.Targets) != 1 {
		t.Fatalf("targets len = %d, want 1", len(data.Targets))
	}
	if data.Targets[0].Name != "testfs-MDT0000" {
		t.Errorf("target name = %q, want testfs-MDT0000", data.Targets[0].Name)
	}
	// Role summary still reflects the whole node, not the filter.
	if !data.RoleSummary.IsOSS || !data.RoleSummary.IsMGS {
		t.Errorf("role_summary must be unaffected by filter, got %+v", data.RoleSummary)
	}
}

func TestExecuteTargetNotFound(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)

	res, _ := executeJSON(t, tool, `{"target":"nosuch-OST9999"}`)
	if res.Status != registry.StatusError {
		t.Fatalf("Status = %s, want error", res.Status)
	}
	if !strings.Contains(res.Summary, "nosuch-OST9999") {
		t.Errorf("Summary %q should name the missing target", res.Summary)
	}
}

func TestExecuteNoTargets(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tool := &Tool{
		sysfsPath:  filepath.Join(dir, "sys"),
		procfsPath: filepath.Join(dir, "proc"),
	}

	res, data := executeJSON(t, tool, `{}`)
	if res.Status != registry.StatusWarning {
		t.Fatalf("Status = %s, want warning", res.Status)
	}
	if len(data.Targets) != 0 {
		t.Errorf("targets len = %d, want 0", len(data.Targets))
	}
	if data.RoleSummary.IsOSS || data.RoleSummary.IsMDS || data.RoleSummary.IsMGS {
		t.Errorf("role_summary = %+v, want all false", data.RoleSummary)
	}
}

func TestExecutePartialTarget(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sysfsRoot := filepath.Join(dir, "sys")
	// Bare target dir: no num_exports, capacity, stats, or brw_stats.
	if err := os.MkdirAll(filepath.Join(sysfsRoot, "obdfilter", "bare-OST0001"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	tool := &Tool{sysfsPath: sysfsRoot, procfsPath: filepath.Join(dir, "proc")}

	res, data := executeJSON(t, tool, `{}`)
	if res.Status != registry.StatusOK {
		t.Fatalf("Status = %s, want ok", res.Status)
	}
	if len(data.Targets) != 1 {
		t.Fatalf("targets len = %d, want 1", len(data.Targets))
	}
	tgt := data.Targets[0]
	if tgt.NumExports != 0 || tgt.Capacity.KBytesTotal != 0 || tgt.Inodes.Total != 0 {
		t.Errorf("partial target must report zeros, got %+v", tgt)
	}
	if tgt.BrwIOSizes != nil {
		t.Error("missing brw_stats must omit brw_io_sizes")
	}
	if len(tgt.TopStats) != 0 {
		t.Errorf("missing stats must yield empty top_stats, got %+v", tgt.TopStats)
	}
}

func TestExecuteBadArgs(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{not json`))
	if err != nil {
		t.Fatalf("Execute must not return hard error, got %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %s, want error", res.Status)
	}
}

func TestExecuteContextCancelled(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := tool.Execute(ctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute must encapsulate cancellation, got hard error %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %s, want error on cancelled context", res.Status)
	}
}

func TestParseStatsCounters(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		input string
		want  map[string]uint64
	}{
		{
			name:  "counters with min max sum",
			input: "read_bytes 22 samples [bytes] 0 4194304 20971520\nwrite_bytes 13 samples [bytes] 344 1048576 23437862\n",
			want:  map[string]uint64{"read_bytes": 22, "write_bytes": 13},
		},
		{
			name:  "skips snapshot_time",
			input: "snapshot_time 1589909588.361379170 secs.nsecs\nopen 5 samples [reqs]\n",
			want:  map[string]uint64{"open": 5},
		},
		{
			name:  "skips malformed lines",
			input: "garbage\nopen notanumber samples [reqs]\nclose 7 samples [reqs]\n\n",
			want:  map[string]uint64{"close": 7},
		},
		{
			name:  "empty input",
			input: "",
			want:  map[string]uint64{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := parseStatsCounters([]byte(tc.input))
			if len(got) != len(tc.want) {
				t.Fatalf("len = %d, want %d (%v)", len(got), len(tc.want), got)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("counter[%s] = %d, want %d", k, got[k], v)
				}
			}
		})
	}
}

func TestTopCounters(t *testing.T) {
	t.Parallel()
	counters := map[string]uint64{"a": 5, "b": 50, "c": 20, "d": 20}

	top := topCounters(counters, 3)
	if len(top) != 3 {
		t.Fatalf("len = %d, want 3", len(top))
	}
	if top[0].Name != "b" || top[0].Count != 50 {
		t.Errorf("top[0] = %+v, want b/50", top[0])
	}
	// Ties broken by name for deterministic output.
	if top[1].Name != "c" || top[2].Name != "d" {
		t.Errorf("tie order = %s,%s, want c,d", top[1].Name, top[2].Name)
	}

	if got := topCounters(map[string]uint64{}, 5); len(got) != 0 {
		t.Errorf("empty input should yield no counters, got %+v", got)
	}
	if got := topCounters(counters, 10); len(got) != 4 {
		t.Errorf("n larger than input should return all, got %d", len(got))
	}
}

func TestParseBrwIOSizes(t *testing.T) {
	t.Parallel()

	t.Run("full histogram", func(t *testing.T) {
		t.Parallel()
		got := parseBrwIOSizes([]byte(ossBrwStats), 5)
		if got == nil {
			t.Fatal("expected parsed brw_io_sizes")
		}
		if len(got.Read) != 5 || len(got.Write) != 5 {
			t.Fatalf("read/write lens = %d/%d, want 5/5", len(got.Read), len(got.Write))
		}
		if got.Read[0].Size != "4K" || math.Abs(got.Read[0].Pct-50) > 0.01 {
			t.Errorf("read[0] = %+v, want 4K/50", got.Read[0])
		}
		if got.Read[4].Size != "64K" {
			t.Errorf("read[4].Size = %q, want 64K", got.Read[4].Size)
		}
		if got.Write[0].Size != "1M" || math.Abs(got.Write[0].Pct-43) > 0.01 {
			t.Errorf("write[0] = %+v, want 1M/43", got.Write[0])
		}
		// Zero-sample buckets must never surface.
		for _, b := range append(append([]BrwBucket{}, got.Read...), got.Write...) {
			if b.Size == "32K" && b.Pct == 0 {
				t.Errorf("zero-count bucket leaked into output: %+v", b)
			}
		}
	})

	t.Run("missing disk IO size section", func(t *testing.T) {
		t.Parallel()
		input := "snapshot_time: 123 (secs.nsecs)\n\n read | write\npages per bulk r/w rpcs %% cum %% | rpcs %% cum %%\n1: 10 100 100 | 5 100 100\n"
		if got := parseBrwIOSizes([]byte(input), 5); got != nil {
			t.Errorf("expected nil for histogram without disk I/O size section, got %+v", got)
		}
	})

	t.Run("garbage input", func(t *testing.T) {
		t.Parallel()
		if got := parseBrwIOSizes([]byte("complete nonsense\n\n"), 5); got != nil {
			t.Errorf("expected nil for garbage input, got %+v", got)
		}
	})

	t.Run("empty input", func(t *testing.T) {
		t.Parallel()
		if got := parseBrwIOSizes(nil, 5); got != nil {
			t.Errorf("expected nil for empty input, got %+v", got)
		}
	})
}

func TestUsedPct(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		total, free uint64
		want        float64
	}{
		{"three quarters used", 1000000, 250000, 75.0},
		{"all free", 100, 100, 0.0},
		{"all used", 100, 0, 100.0},
		{"zero total", 0, 0, 0.0},
		{"free exceeds total", 100, 200, 0.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := usedPct(tc.total, tc.free); math.Abs(got-tc.want) > 0.01 {
				t.Errorf("usedPct(%d, %d) = %f, want %f", tc.total, tc.free, got, tc.want)
			}
		})
	}
}

func TestReadUintFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	good := filepath.Join(dir, "good")
	mustWrite(t, good, "42\n")
	if v, ok := readUintFile(good); !ok || v != 42 {
		t.Errorf("readUintFile(good) = %d,%t, want 42,true", v, ok)
	}

	bad := filepath.Join(dir, "bad")
	mustWrite(t, bad, "not a number\n")
	if _, ok := readUintFile(bad); ok {
		t.Error("readUintFile(bad) should not parse")
	}

	if _, ok := readUintFile(filepath.Join(dir, "missing")); ok {
		t.Error("readUintFile(missing) should fail")
	}
}

func TestResolvePathFallback(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sysfsRoot := filepath.Join(dir, "sys")
	procfsRoot := filepath.Join(dir, "proc")
	mustWrite(t, filepath.Join(sysfsRoot, "both"), "sys\n")
	mustWrite(t, filepath.Join(procfsRoot, "both"), "proc\n")
	mustWrite(t, filepath.Join(procfsRoot, "proconly"), "proc\n")

	tool := &Tool{sysfsPath: sysfsRoot, procfsPath: procfsRoot}

	if got := tool.resolvePath("both"); got != filepath.Join(sysfsRoot, "both") {
		t.Errorf("resolvePath(both) = %q, want sysfs to win", got)
	}
	if got := tool.resolvePath("proconly"); got != filepath.Join(procfsRoot, "proconly") {
		t.Errorf("resolvePath(proconly) = %q, want procfs fallback", got)
	}
	if got := tool.resolvePath("nowhere"); got != "" {
		t.Errorf("resolvePath(nowhere) = %q, want empty", got)
	}
}

func TestGetSubdirsFallback(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sysfsRoot := filepath.Join(dir, "sys")
	procfsRoot := filepath.Join(dir, "proc")
	if err := os.MkdirAll(filepath.Join(procfsRoot, "obdfilter", "fs-OST0000"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	tool := &Tool{sysfsPath: sysfsRoot, procfsPath: procfsRoot}
	got := tool.getSubdirs("obdfilter")
	if len(got) != 1 || got[0] != "fs-OST0000" {
		t.Errorf("getSubdirs = %v, want [fs-OST0000] via procfs fallback", got)
	}
	if got := tool.getSubdirs("mdt"); got != nil {
		t.Errorf("getSubdirs(mdt) = %v, want nil", got)
	}
}
