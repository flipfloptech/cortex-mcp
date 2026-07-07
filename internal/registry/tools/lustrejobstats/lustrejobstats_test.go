package lustrejobstats

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// ost0JobStats mixes an active fio job, a quoted job id with a space, and a
// fully idle job (zero-sample read/write counters).
const ost0JobStats = `job_stats:
- job_id:          run_fio.1234
  snapshot_time:   1751900000
  read_bytes:      { samples:        120, unit: bytes, min:  4096, max: 4194304, sum:  50331648 }
  write_bytes:     { samples:        300, unit: bytes, min:  4096, max: 4194304, sum: 838860800 }
  getattr:         { samples: 4, unit: reqs }
  punch:           { samples: 2, unit: reqs }
- job_id:          "backup rsync.5678"
  snapshot_time:   1751900100
  read_bytes:      { samples: 50, unit: bytes, min: 4096, max: 1048576, sum: 10485760 }
  write_bytes:     { samples: 10, unit: bytes, min: 4096, max: 65536, sum: 655360 }
- job_id:          idle_job.999
  snapshot_time:   1751900200
  read_bytes:      { samples: 0, unit: bytes, min: 0, max: 0, sum: 0 }
  write_bytes:     { samples: 0, unit: bytes, min: 0, max: 0, sum: 0 }
`

// ost1JobStats repeats run_fio.1234 so cross-target aggregation is exercised.
const ost1JobStats = `job_stats:
- job_id:          run_fio.1234
  snapshot_time:   1751900000
  read_bytes:      { samples: 30, unit: bytes, min: 4096, max: 4194304, sum: 10485760 }
  write_bytes:     { samples: 100, unit: bytes, min: 4096, max: 4194304, sum: 209715200 }
`

// mdtJobStats carries metadata op counters (usecs units, zero-sample mkdir).
const mdtJobStats = `job_stats:
- job_id:          run_fio.1234
  snapshot_time:   1751900000
  open:            { samples: 500, unit: usecs, min: 2, max: 4324, sum: 12345 }
  close:           { samples: 500, unit: usecs, min: 1, max: 100, sum: 3000 }
  getattr:         { samples: 1200, unit: usecs, min: 1, max: 50, sum: 9000 }
  mkdir:           { samples: 0, unit: usecs }
- job_id:          "backup rsync.5678"
  snapshot_time:   1751900100
  unlink:          { samples: 250, unit: usecs, min: 1, max: 10, sum: 500 }
  getattr:         { samples: 100, unit: usecs, min: 1, max: 10, sum: 200 }
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

// writeJobStatsFixture builds a fake dual-root Lustre tree:
//   - testfs-OST0000: target dir in sysfs, job_stats only in procfs (fallback)
//   - testfs-OST0001: job_stats directly in sysfs
//   - testfs-MDT0000: target dir in sysfs, job_stats only in procfs
func writeJobStatsFixture(tb testing.TB) (sysfsRoot, procfsRoot string) {
	tb.Helper()
	dir := tb.TempDir()
	sysfsRoot = filepath.Join(dir, "sys")
	procfsRoot = filepath.Join(dir, "proc")

	if err := os.MkdirAll(filepath.Join(sysfsRoot, "fs", "lustre", "obdfilter", "testfs-OST0000"), 0o755); err != nil {
		tb.Fatalf("mkdir: %v", err)
	}
	mustWrite(tb, filepath.Join(procfsRoot, "fs", "lustre", "obdfilter", "testfs-OST0000", "job_stats"), ost0JobStats)

	mustWrite(tb, filepath.Join(sysfsRoot, "fs", "lustre", "obdfilter", "testfs-OST0001", "job_stats"), ost1JobStats)

	if err := os.MkdirAll(filepath.Join(sysfsRoot, "fs", "lustre", "mdt", "testfs-MDT0000"), 0o755); err != nil {
		tb.Fatalf("mkdir: %v", err)
	}
	mustWrite(tb, filepath.Join(procfsRoot, "fs", "lustre", "mdt", "testfs-MDT0000", "job_stats"), mdtJobStats)

	return sysfsRoot, procfsRoot
}

func newFixtureTool(tb testing.TB) *Tool {
	tb.Helper()
	sysfsRoot, procfsRoot := writeJobStatsFixture(tb)
	return &Tool{sysfsRoot: sysfsRoot, procfsRoot: procfsRoot}
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

	if tool.Name() != "get_lustre_job_stats" {
		t.Errorf("Name() = %q, want get_lustre_job_stats", tool.Name())
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
	for _, src := range []string{"/sys/fs/lustre", "/proc/fs/lustre", "job_stats", "obdfilter", "mdt", "jobid_var"} {
		if !strings.Contains(help, src) {
			t.Errorf("Help() missing data source reference %q", src)
		}
	}

	params := tool.Parameters()
	if len(params) != 2 {
		t.Fatalf("Parameters() len = %d, want 2", len(params))
	}
	byName := map[string]registry.ToolParam{}
	for _, p := range params {
		byName[p.Name] = p
	}
	if p, ok := byName["target"]; !ok || p.Type != "string" || p.Required {
		t.Errorf("expected optional string param 'target', got %+v", p)
	}
	if p, ok := byName["limit"]; !ok || p.Type != "integer" || p.Required {
		t.Errorf("expected optional integer param 'limit', got %+v", p)
	}
}

func TestIsSupportedCallable(t *testing.T) {
	t.Parallel()
	// DetectNodeRoles inspects the real host sysfs, so only the contract
	// shape is asserted: an unsupported verdict must carry the documented
	// reason, and a supported one an empty reason.
	supported, reason := New().IsSupported()
	if !supported && !strings.Contains(reason, "not OSS/MDS") {
		t.Errorf("IsSupported() = false with reason %q, want mention of 'not OSS/MDS'", reason)
	}
	if supported && reason != "" {
		t.Errorf("IsSupported() = true must carry empty reason, got %q", reason)
	}
}

func TestExecuteHappyPath(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)

	res, data := executeJSON(t, tool, `{}`)
	if res.Status != registry.StatusOK {
		t.Fatalf("Status = %s (summary %q), want ok", res.Status, res.Summary)
	}
	if data.TargetsScanned != 3 {
		t.Errorf("targets_scanned = %d, want 3", data.TargetsScanned)
	}
	if data.TotalJobsTracked != 3 {
		t.Errorf("total_jobs_tracked = %d, want 3", data.TotalJobsTracked)
	}
	if data.ParseErrors != 0 {
		t.Errorf("parse_errors = %d, want 0", data.ParseErrors)
	}
	if data.WarningReasons == nil || len(data.WarningReasons) != 0 {
		t.Errorf("warning_reasons = %v, want present-but-empty (attribution tool)", data.WarningReasons)
	}

	// --- top_jobs_by_write: idle_job.999 filtered out, fio first. ---
	if len(data.TopJobsByWrite) != 2 {
		t.Fatalf("top_jobs_by_write len = %d, want 2 (%+v)", len(data.TopJobsByWrite), data.TopJobsByWrite)
	}
	fio := data.TopJobsByWrite[0]
	if fio.JobID != "run_fio.1234" {
		t.Fatalf("top write job = %q, want run_fio.1234", fio.JobID)
	}
	// 838860800 + 209715200 bytes = 1000.0 MB across both OSTs.
	if math.Abs(fio.WriteMB-1000.0) > 0.01 {
		t.Errorf("fio write_mb = %f, want 1000.0", fio.WriteMB)
	}
	if fio.WriteOps != 400 {
		t.Errorf("fio write_ops = %d, want 400", fio.WriteOps)
	}
	// 50331648 + 10485760 bytes = 58.0 MB.
	if math.Abs(fio.ReadMB-58.0) > 0.01 {
		t.Errorf("fio read_mb = %f, want 58.0", fio.ReadMB)
	}
	if fio.ReadOps != 150 {
		t.Errorf("fio read_ops = %d, want 150", fio.ReadOps)
	}
	wantTargets := []string{"testfs-OST0000", "testfs-OST0001"}
	if len(fio.Targets) != 2 || fio.Targets[0] != wantTargets[0] || fio.Targets[1] != wantTargets[1] {
		t.Errorf("fio targets = %v, want %v", fio.Targets, wantTargets)
	}

	rsync := data.TopJobsByWrite[1]
	if rsync.JobID != "backup rsync.5678" {
		t.Errorf("2nd write job = %q, want quoted id 'backup rsync.5678'", rsync.JobID)
	}
	// 655360 bytes = 0.625 MB -> 0.6 at 1dp.
	if math.Abs(rsync.WriteMB-0.6) > 0.01 {
		t.Errorf("rsync write_mb = %f, want 0.6", rsync.WriteMB)
	}
	if len(rsync.Targets) != 1 || rsync.Targets[0] != "testfs-OST0000" {
		t.Errorf("rsync targets = %v, want [testfs-OST0000]", rsync.Targets)
	}

	// --- top_jobs_by_read ---
	if len(data.TopJobsByRead) != 2 {
		t.Fatalf("top_jobs_by_read len = %d, want 2", len(data.TopJobsByRead))
	}
	if data.TopJobsByRead[0].JobID != "run_fio.1234" || data.TopJobsByRead[1].JobID != "backup rsync.5678" {
		t.Errorf("read order = %s,%s want run_fio.1234,backup rsync.5678",
			data.TopJobsByRead[0].JobID, data.TopJobsByRead[1].JobID)
	}
	if math.Abs(data.TopJobsByRead[1].ReadMB-10.0) > 0.01 {
		t.Errorf("rsync read_mb = %f, want 10.0", data.TopJobsByRead[1].ReadMB)
	}

	// --- top_jobs_by_metadata_ops (MDT only) ---
	if len(data.TopJobsByMetadataOps) != 2 {
		t.Fatalf("top_jobs_by_metadata_ops len = %d, want 2 (%+v)", len(data.TopJobsByMetadataOps), data.TopJobsByMetadataOps)
	}
	meta := data.TopJobsByMetadataOps[0]
	if meta.JobID != "run_fio.1234" {
		t.Errorf("top metadata job = %q, want run_fio.1234", meta.JobID)
	}
	if meta.TotalOps != 2200 {
		t.Errorf("fio total_ops = %d, want 2200 (zero-sample mkdir excluded)", meta.TotalOps)
	}
	if meta.TopOp != "getattr" {
		t.Errorf("fio top_op = %q, want getattr", meta.TopOp)
	}
	if len(meta.Targets) != 1 || meta.Targets[0] != "testfs-MDT0000" {
		t.Errorf("fio metadata targets = %v, want [testfs-MDT0000]", meta.Targets)
	}
	meta2 := data.TopJobsByMetadataOps[1]
	if meta2.JobID != "backup rsync.5678" || meta2.TotalOps != 350 || meta2.TopOp != "unlink" {
		t.Errorf("2nd metadata job = %+v, want backup rsync.5678/350/unlink", meta2)
	}
}

func TestExecuteTargetFilter(t *testing.T) {
	t.Parallel()
	tool := newFixtureTool(t)

	res, data := executeJSON(t, tool, `{"target":"testfs-OST0000"}`)
	if res.Status != registry.StatusOK {
		t.Fatalf("Status = %s, want ok", res.Status)
	}
	if data.TargetsScanned != 1 {
		t.Errorf("targets_scanned = %d, want 1", data.TargetsScanned)
	}
	if data.TotalJobsTracked != 3 {
		t.Errorf("total_jobs_tracked = %d, want 3 (all jobs on OST0000)", data.TotalJobsTracked)
	}
	// Only OST0000's contribution: fio write = 838860800 bytes = 800.0 MB.
	if len(data.TopJobsByWrite) == 0 || data.TopJobsByWrite[0].JobID != "run_fio.1234" {
		t.Fatalf("top_jobs_by_write = %+v, want run_fio.1234 first", data.TopJobsByWrite)
	}
	if math.Abs(data.TopJobsByWrite[0].WriteMB-800.0) > 0.01 {
		t.Errorf("filtered fio write_mb = %f, want 800.0", data.TopJobsByWrite[0].WriteMB)
	}
	// No MDT scanned: metadata leaderboard must be omitted.
	if len(data.TopJobsByMetadataOps) != 0 {
		t.Errorf("top_jobs_by_metadata_ops = %+v, want omitted without MDTs", data.TopJobsByMetadataOps)
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

func TestExecuteLimit(t *testing.T) {
	t.Parallel()

	// Generate 60 jobs on one OST so default (15), explicit, and cap (50)
	// behaviors are all observable.
	var sb strings.Builder
	sb.WriteString("job_stats:\n")
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&sb, "- job_id:          job.%02d\n", i)
		fmt.Fprintf(&sb, "  snapshot_time:   1751900000\n")
		fmt.Fprintf(&sb, "  read_bytes:      { samples: %d, unit: bytes, min: 4096, max: 4194304, sum: %d }\n", i+1, (i+1)*1048576)
		fmt.Fprintf(&sb, "  write_bytes:     { samples: %d, unit: bytes, min: 4096, max: 4194304, sum: %d }\n", i+1, (i+1)*1048576)
	}
	dir := t.TempDir()
	sysfsRoot := filepath.Join(dir, "sys")
	mustWrite(t, filepath.Join(sysfsRoot, "fs", "lustre", "obdfilter", "big-OST0000", "job_stats"), sb.String())
	tool := &Tool{sysfsRoot: sysfsRoot, procfsRoot: filepath.Join(dir, "proc")}

	cases := []struct {
		name string
		args string
		want int
	}{
		{"default 15", `{}`, 15},
		{"explicit 5", `{"limit":5}`, 5},
		{"cap at 50", `{"limit":100}`, 50},
		{"zero uses default", `{"limit":0}`, 15},
		{"negative uses default", `{"limit":-3}`, 15},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, data := executeJSON(t, tool, tc.args)
			if res.Status != registry.StatusOK {
				t.Fatalf("Status = %s, want ok", res.Status)
			}
			if len(data.TopJobsByWrite) != tc.want {
				t.Errorf("top_jobs_by_write len = %d, want %d", len(data.TopJobsByWrite), tc.want)
			}
			if len(data.TopJobsByRead) != tc.want {
				t.Errorf("top_jobs_by_read len = %d, want %d", len(data.TopJobsByRead), tc.want)
			}
			if data.TotalJobsTracked != 60 {
				t.Errorf("total_jobs_tracked = %d, want 60", data.TotalJobsTracked)
			}
			// Biggest writer (job.59) must always lead the board.
			if data.TopJobsByWrite[0].JobID != "job.59" {
				t.Errorf("top writer = %q, want job.59", data.TopJobsByWrite[0].JobID)
			}
		})
	}
}

func TestExecuteJobStatsNotEnabled(t *testing.T) {
	t.Parallel()

	// Targets exist but job_stats files are absent (OST) or header-only
	// empty (MDT): degrade with the jobid_var hint.
	dir := t.TempDir()
	sysfsRoot := filepath.Join(dir, "sys")
	if err := os.MkdirAll(filepath.Join(sysfsRoot, "fs", "lustre", "obdfilter", "testfs-OST0000"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mustWrite(t, filepath.Join(sysfsRoot, "fs", "lustre", "mdt", "testfs-MDT0000", "job_stats"), "job_stats:\n")
	tool := &Tool{sysfsRoot: sysfsRoot, procfsRoot: filepath.Join(dir, "proc")}

	res, data := executeJSON(t, tool, `{}`)
	if res.Status != registry.StatusDegraded {
		t.Fatalf("Status = %s, want degraded", res.Status)
	}
	if !strings.Contains(data.Note, "jobid_var") {
		t.Errorf("note = %q, want jobid_var enablement hint", data.Note)
	}
	if data.TargetsScanned != 2 {
		t.Errorf("targets_scanned = %d, want 2", data.TargetsScanned)
	}
	if data.TotalJobsTracked != 0 {
		t.Errorf("total_jobs_tracked = %d, want 0", data.TotalJobsTracked)
	}
}

func TestExecuteNoTargets(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tool := &Tool{
		sysfsRoot:  filepath.Join(dir, "sys"),
		procfsRoot: filepath.Join(dir, "proc"),
	}

	res, data := executeJSON(t, tool, `{}`)
	if res.Status != registry.StatusWarning {
		t.Fatalf("Status = %s, want warning", res.Status)
	}
	if data.TargetsScanned != 0 {
		t.Errorf("targets_scanned = %d, want 0", data.TargetsScanned)
	}
}

func TestExecuteMalformedBlocks(t *testing.T) {
	t.Parallel()

	const malformed = `job_stats:
- job_id:
  snapshot_time: 123
  read_bytes: { samples: 5, unit: bytes, min: 1, max: 2, sum: 100 }
- job_id: good.1
  snapshot_time: 124
  write_bytes: { samples: 7, unit: bytes, min: 1, max: 2, sum: 7340032 }
  bad_counter: { garbage }
`
	dir := t.TempDir()
	sysfsRoot := filepath.Join(dir, "sys")
	mustWrite(t, filepath.Join(sysfsRoot, "fs", "lustre", "obdfilter", "testfs-OST0000", "job_stats"), malformed)
	tool := &Tool{sysfsRoot: sysfsRoot, procfsRoot: filepath.Join(dir, "proc")}

	res, data := executeJSON(t, tool, `{}`)
	if res.Status != registry.StatusOK {
		t.Fatalf("Status = %s, want ok (good job still parsed)", res.Status)
	}
	if data.TotalJobsTracked != 1 {
		t.Errorf("total_jobs_tracked = %d, want 1", data.TotalJobsTracked)
	}
	if data.ParseErrors != 2 {
		t.Errorf("parse_errors = %d, want 2 (empty job_id block + bad counter)", data.ParseErrors)
	}
	if len(data.TopJobsByWrite) != 1 || data.TopJobsByWrite[0].JobID != "good.1" {
		t.Fatalf("top_jobs_by_write = %+v, want single good.1", data.TopJobsByWrite)
	}
	if math.Abs(data.TopJobsByWrite[0].WriteMB-7.0) > 0.01 {
		t.Errorf("good.1 write_mb = %f, want 7.0", data.TopJobsByWrite[0].WriteMB)
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

func TestParseJobStats(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		input      string
		wantJobs   int
		wantErrors int
		check      func(t *testing.T, jobs []jobRecord)
	}{
		{
			name: "OST block with min max sum",
			input: "job_stats:\n" +
				"- job_id:          run_fio.1234\n" +
				"  snapshot_time:   1751900000\n" +
				"  read_bytes:      { samples:        120, unit: bytes, min:  4096, max: 4194304, sum:  50331648 }\n" +
				"  write_bytes:     { samples:        300, unit: bytes, min:  4096, max: 4194304, sum: 838860800 }\n",
			wantJobs: 1,
			check: func(t *testing.T, jobs []jobRecord) {
				if jobs[0].JobID != "run_fio.1234" {
					t.Errorf("job id = %q", jobs[0].JobID)
				}
				rb := jobs[0].Counters["read_bytes"]
				if rb.Samples != 120 || rb.Sum != 50331648 {
					t.Errorf("read_bytes = %+v, want 120/50331648", rb)
				}
				wb := jobs[0].Counters["write_bytes"]
				if wb.Samples != 300 || wb.Sum != 838860800 {
					t.Errorf("write_bytes = %+v, want 300/838860800", wb)
				}
			},
		},
		{
			name: "MDT op counters without sum",
			input: "job_stats:\n" +
				"- job_id:          cp.0\n" +
				"  snapshot_time:   1751900050\n" +
				"  open:            { samples: 500, unit: usecs, min: 2, max: 4324, sum: 12345 }\n" +
				"  statfs:          { samples: 12, unit: reqs }\n",
			wantJobs: 1,
			check: func(t *testing.T, jobs []jobRecord) {
				if jobs[0].Counters["open"].Samples != 500 {
					t.Errorf("open samples = %d, want 500", jobs[0].Counters["open"].Samples)
				}
				st := jobs[0].Counters["statfs"]
				if st.Samples != 12 || st.Sum != 0 {
					t.Errorf("statfs = %+v, want 12 samples, 0 sum", st)
				}
			},
		},
		{
			name:     "quoted job id with spaces and dots",
			input:    "job_stats:\n- job_id:          \"sqlplus @update.sql user7\"\n  snapshot_time: 1\n  open: { samples: 3, unit: usecs }\n",
			wantJobs: 1,
			check: func(t *testing.T, jobs []jobRecord) {
				if jobs[0].JobID != "sqlplus @update.sql user7" {
					t.Errorf("job id = %q, want unquoted content", jobs[0].JobID)
				}
			},
		},
		{
			name:     "empty file",
			input:    "",
			wantJobs: 0,
		},
		{
			name:     "header only",
			input:    "job_stats:\n",
			wantJobs: 0,
		},
		{
			name:       "empty job_id skips block",
			input:      "job_stats:\n- job_id:\n  open: { samples: 3, unit: usecs }\n- job_id: ok.1\n  open: { samples: 4, unit: usecs }\n",
			wantJobs:   1,
			wantErrors: 1,
			check: func(t *testing.T, jobs []jobRecord) {
				if jobs[0].JobID != "ok.1" {
					t.Errorf("surviving job = %q, want ok.1", jobs[0].JobID)
				}
			},
		},
		{
			name:       "unparseable counter braces counted",
			input:      "job_stats:\n- job_id: ok.2\n  open: { samples: notanumber }\n  close: { samples: 9, unit: usecs }\n",
			wantJobs:   1,
			wantErrors: 1,
			check: func(t *testing.T, jobs []jobRecord) {
				if _, ok := jobs[0].Counters["open"]; ok {
					t.Error("malformed open counter must be dropped")
				}
				if jobs[0].Counters["close"].Samples != 9 {
					t.Errorf("close samples = %d, want 9", jobs[0].Counters["close"].Samples)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			jobs, parseErrors := parseJobStats([]byte(tc.input))
			if len(jobs) != tc.wantJobs {
				t.Fatalf("jobs len = %d, want %d (%+v)", len(jobs), tc.wantJobs, jobs)
			}
			if parseErrors != tc.wantErrors {
				t.Errorf("parse errors = %d, want %d", parseErrors, tc.wantErrors)
			}
			if tc.check != nil {
				tc.check(t, jobs)
			}
		})
	}
}

func TestUnquoteJobID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{`run_fio.1234`, `run_fio.1234`},
		{`"backup rsync.5678"`, `backup rsync.5678`},
		{`'quoted.1'`, `quoted.1`},
		{`"`, `"`},
		{`""`, ``},
		{``, ``},
	}
	for _, tc := range cases {
		if got := unquoteJobID(tc.in); got != tc.want {
			t.Errorf("unquoteJobID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAggregateRecords(t *testing.T) {
	t.Parallel()
	aggs := map[string]*jobAggregate{}

	ostJobs, _ := parseJobStats([]byte(ost0JobStats))
	aggregateRecords(aggs, ostJobs, "testfs-OST0000", false)
	mdtJobs, _ := parseJobStats([]byte(mdtJobStats))
	aggregateRecords(aggs, mdtJobs, "testfs-MDT0000", true)

	if len(aggs) != 3 {
		t.Fatalf("aggregates = %d, want 3", len(aggs))
	}
	fio := aggs["run_fio.1234"]
	if fio == nil {
		t.Fatal("missing run_fio.1234 aggregate")
	}
	if fio.writeBytes != 838860800 || fio.writeOps != 300 {
		t.Errorf("fio write = %d bytes / %d ops", fio.writeBytes, fio.writeOps)
	}
	// OST getattr/punch counters must not leak into metadata ops.
	if fio.totalMDOps != 2200 {
		t.Errorf("fio totalMDOps = %d, want 2200 (MDT files only)", fio.totalMDOps)
	}
	if _, ok := fio.ioTargets["testfs-OST0000"]; !ok {
		t.Error("fio ioTargets missing testfs-OST0000")
	}
	if _, ok := fio.ioTargets["testfs-MDT0000"]; ok {
		t.Error("MDT metadata-only record must not join ioTargets")
	}
	if _, ok := fio.mdTargets["testfs-MDT0000"]; !ok {
		t.Error("fio mdTargets missing testfs-MDT0000")
	}

	idle := aggs["idle_job.999"]
	if idle == nil {
		t.Fatal("missing idle_job.999 aggregate")
	}
	if len(idle.ioTargets) != 0 {
		t.Errorf("idle job with zero-sample counters must have no ioTargets, got %v", idle.ioTargets)
	}
}

func TestBuildIOTop(t *testing.T) {
	t.Parallel()
	aggs := map[string]*jobAggregate{
		"w1": {jobID: "w1", writeBytes: 2 << 20, writeOps: 4, ioTargets: map[string]struct{}{"t2": {}, "t1": {}}},
		"w2": {jobID: "w2", writeBytes: 1 << 20, writeOps: 9, readBytes: 5 << 20, readOps: 3, ioTargets: map[string]struct{}{"t1": {}}},
		"m1": {jobID: "m1", totalMDOps: 50, mdOps: map[string]uint64{"open": 50}, mdTargets: map[string]struct{}{"mdt": {}}},
	}

	writes := buildIOTop(aggs, 10, true)
	if len(writes) != 2 {
		t.Fatalf("write leaderboard len = %d, want 2 (metadata-only m1 excluded)", len(writes))
	}
	if writes[0].JobID != "w1" || writes[1].JobID != "w2" {
		t.Errorf("write order = %s,%s want w1,w2", writes[0].JobID, writes[1].JobID)
	}
	if writes[0].Targets[0] != "t1" || writes[0].Targets[1] != "t2" {
		t.Errorf("targets must be sorted, got %v", writes[0].Targets)
	}

	reads := buildIOTop(aggs, 10, false)
	if len(reads) != 1 || reads[0].JobID != "w2" {
		t.Fatalf("read leaderboard = %+v, want single w2", reads)
	}

	if got := buildIOTop(aggs, 1, true); len(got) != 1 {
		t.Errorf("limit 1 must truncate, got %d entries", len(got))
	}
	if got := buildIOTop(map[string]*jobAggregate{}, 5, true); len(got) != 0 {
		t.Errorf("empty aggregates must yield empty board, got %+v", got)
	}
}

func TestBuildIOTopDeterministicTies(t *testing.T) {
	t.Parallel()
	aggs := map[string]*jobAggregate{
		"b": {jobID: "b", writeBytes: 100, writeOps: 1, ioTargets: map[string]struct{}{"t": {}}},
		"a": {jobID: "a", writeBytes: 100, writeOps: 1, ioTargets: map[string]struct{}{"t": {}}},
	}
	got := buildIOTop(aggs, 10, true)
	if got[0].JobID != "a" || got[1].JobID != "b" {
		t.Errorf("tie order = %s,%s, want a,b (job_id ascending)", got[0].JobID, got[1].JobID)
	}
}

func TestBuildMetaTop(t *testing.T) {
	t.Parallel()
	aggs := map[string]*jobAggregate{
		"m1": {jobID: "m1", totalMDOps: 100, mdOps: map[string]uint64{"open": 60, "close": 40}, mdTargets: map[string]struct{}{"mdt0": {}}},
		"m2": {jobID: "m2", totalMDOps: 300, mdOps: map[string]uint64{"unlink": 300}, mdTargets: map[string]struct{}{"mdt0": {}, "mdt1": {}}},
		"io": {jobID: "io", writeBytes: 999, writeOps: 9, ioTargets: map[string]struct{}{"ost0": {}}},
	}

	got := buildMetaTop(aggs, 10)
	if len(got) != 2 {
		t.Fatalf("metadata leaderboard len = %d, want 2", len(got))
	}
	if got[0].JobID != "m2" || got[0].TotalOps != 300 || got[0].TopOp != "unlink" {
		t.Errorf("got[0] = %+v, want m2/300/unlink", got[0])
	}
	if len(got[0].Targets) != 2 || got[0].Targets[0] != "mdt0" {
		t.Errorf("m2 targets = %v, want sorted [mdt0 mdt1]", got[0].Targets)
	}
	if got[1].JobID != "m1" || got[1].TopOp != "open" {
		t.Errorf("got[1] = %+v, want m1 with top_op open", got[1])
	}

	if got := buildMetaTop(aggs, 1); len(got) != 1 {
		t.Errorf("limit 1 must truncate, got %d", len(got))
	}
}

func TestTopOp(t *testing.T) {
	t.Parallel()
	if got := topOp(map[string]uint64{"open": 5, "close": 9}); got != "close" {
		t.Errorf("topOp = %q, want close", got)
	}
	// Ties break alphabetically for deterministic output.
	if got := topOp(map[string]uint64{"b": 7, "a": 7}); got != "a" {
		t.Errorf("topOp tie = %q, want a", got)
	}
	if got := topOp(map[string]uint64{}); got != "" {
		t.Errorf("topOp(empty) = %q, want empty", got)
	}
}

func TestMbFromBytes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   uint64
		want float64
	}{
		{0, 0},
		{1048576, 1.0},
		{655360, 0.6},
		{838860800, 800.0},
		{262144, 0.3}, // 0.25 MB rounds half away from zero to 0.3
	}
	for _, tc := range cases {
		if got := mbFromBytes(tc.in); math.Abs(got-tc.want) > 0.001 {
			t.Errorf("mbFromBytes(%d) = %f, want %f", tc.in, got, tc.want)
		}
	}
}

func TestSortedTargets(t *testing.T) {
	t.Parallel()
	got := sortedTargets(map[string]struct{}{"z": {}, "a": {}, "m": {}})
	if len(got) != 3 || got[0] != "a" || got[1] != "m" || got[2] != "z" {
		t.Errorf("sortedTargets = %v, want [a m z]", got)
	}
	if got := sortedTargets(nil); len(got) != 0 {
		t.Errorf("sortedTargets(nil) = %v, want empty", got)
	}
}

func TestResolvePathFallback(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sysfsRoot := filepath.Join(dir, "sys")
	procfsRoot := filepath.Join(dir, "proc")
	mustWrite(t, filepath.Join(sysfsRoot, "fs", "lustre", "both"), "sys\n")
	mustWrite(t, filepath.Join(procfsRoot, "fs", "lustre", "both"), "proc\n")
	mustWrite(t, filepath.Join(procfsRoot, "fs", "lustre", "proconly"), "proc\n")

	tool := &Tool{sysfsRoot: sysfsRoot, procfsRoot: procfsRoot}

	if got := tool.resolvePath("both"); got != filepath.Join(sysfsRoot, "fs", "lustre", "both") {
		t.Errorf("resolvePath(both) = %q, want sysfs to win", got)
	}
	if got := tool.resolvePath("proconly"); got != filepath.Join(procfsRoot, "fs", "lustre", "proconly") {
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
	if err := os.MkdirAll(filepath.Join(procfsRoot, "fs", "lustre", "obdfilter", "fs-OST0000"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	tool := &Tool{sysfsRoot: sysfsRoot, procfsRoot: procfsRoot}
	got := tool.getSubdirs("obdfilter")
	if len(got) != 1 || got[0] != "fs-OST0000" {
		t.Errorf("getSubdirs = %v, want [fs-OST0000] via procfs fallback", got)
	}
	if got := tool.getSubdirs("mdt"); got != nil {
		t.Errorf("getSubdirs(mdt) = %v, want nil", got)
	}
}
