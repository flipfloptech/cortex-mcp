package openfiles

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func mustMkdir(tb testing.TB, dir string) {
	tb.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		tb.Fatalf("mkdir %s: %v", dir, err)
	}
}

func mustWrite(tb testing.TB, path, content string) {
	tb.Helper()
	mustMkdir(tb, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		tb.Fatalf("write %s: %v", path, err)
	}
}

func mustSymlink(tb testing.TB, target, link string) {
	tb.Helper()
	mustMkdir(tb, filepath.Dir(link))
	if err := os.Symlink(target, link); err != nil {
		tb.Fatalf("symlink %s -> %s: %v", link, target, err)
	}
}

// writeFDFixture builds a fake procfs:
//   - pid 1234 (writerA): 4 fds under /mnt/lustre + 1 elsewhere
//   - pid 5678 (writerB): 2 fds under /mnt/lustre, one deleted
//   - pid 9999 (no comm file): 1 fd under /mnt/lustre
//   - pid 4321: no fd dir at all (exited mid-scan)
//   - "self" and "notapid": non-numeric entries that must be ignored
func writeFDFixture(tb testing.TB) *Tool {
	tb.Helper()
	procRoot := filepath.Join(tb.TempDir(), "proc")

	mustSymlink(tb, "/mnt/lustre/self-trap", filepath.Join(procRoot, "self", "fd", "0"))
	mustMkdir(tb, filepath.Join(procRoot, "notapid", "fd"))

	mustWrite(tb, filepath.Join(procRoot, "1234", "comm"), "writerA\n")
	mustSymlink(tb, "/mnt/lustre/fileA", filepath.Join(procRoot, "1234", "fd", "0"))
	mustSymlink(tb, "/mnt/lustre/fileB", filepath.Join(procRoot, "1234", "fd", "1"))
	mustSymlink(tb, "/mnt/lustre/dir/fileC", filepath.Join(procRoot, "1234", "fd", "2"))
	mustSymlink(tb, "/mnt/lustre/fileD", filepath.Join(procRoot, "1234", "fd", "3"))
	mustSymlink(tb, "/var/log/other", filepath.Join(procRoot, "1234", "fd", "4"))

	mustWrite(tb, filepath.Join(procRoot, "5678", "comm"), "writerB\n")
	mustSymlink(tb, "/mnt/lustre/fileA", filepath.Join(procRoot, "5678", "fd", "0"))
	mustSymlink(tb, "/mnt/lustre/gone (deleted)", filepath.Join(procRoot, "5678", "fd", "1"))

	mustSymlink(tb, "/mnt/lustre/orphan", filepath.Join(procRoot, "9999", "fd", "0"))

	mustMkdir(tb, filepath.Join(procRoot, "4321"))

	return &Tool{procfsRoot: procRoot}
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

	if tool.Name() != "get_open_files" {
		t.Errorf("Name() = %q, want get_open_files", tool.Name())
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
	for _, src := range []string{"/proc/<pid>/fd", "comm", "deleted"} {
		if !strings.Contains(help, src) {
			t.Errorf("Help() missing data source reference %q", src)
		}
	}

	params := tool.Parameters()
	if len(params) != 2 {
		t.Fatalf("Parameters() len = %d, want 2", len(params))
	}
	if params[0].Name != "path" || params[0].Type != "string" || !params[0].Required {
		t.Errorf("param[0] = %+v, want required string 'path'", params[0])
	}
	if params[1].Name != "limit" || params[1].Type != "integer" || params[1].Required || params[1].Default != "20" {
		t.Errorf("param[1] = %+v, want optional integer 'limit' defaulting to 20", params[1])
	}
}

func TestIsSupported(t *testing.T) {
	t.Parallel()
	if !registry.IsLinux() {
		t.Skip("procfs fixture semantics require Linux")
	}

	tool := writeFDFixture(t)
	if ok, reason := tool.IsSupported(); !ok {
		t.Errorf("IsSupported() = false (%s), want true with readable self/fd", reason)
	}

	missing := &Tool{procfsRoot: filepath.Join(t.TempDir(), "nope")}
	ok, reason := missing.IsSupported()
	if ok {
		t.Error("IsSupported() = true without self/fd, want false")
	}
	if reason == "" {
		t.Error("unsupported verdict must carry a reason")
	}
}

func TestExecuteHappyPath(t *testing.T) {
	t.Parallel()
	tool := writeFDFixture(t)

	res, data := executeJSON(t, tool, `{"path":"/mnt/lustre"}`)
	if res.Status != registry.StatusOK {
		t.Fatalf("Status = %s (summary %q), want ok", res.Status, res.Summary)
	}

	if data.Path != "/mnt/lustre" {
		t.Errorf("path = %q, want /mnt/lustre", data.Path)
	}
	if data.TotalMatchingFDs != 7 {
		t.Errorf("total_matching_fds = %d, want 7", data.TotalMatchingFDs)
	}
	if data.DeletedOpenCount != 1 {
		t.Errorf("deleted_open_count = %d, want 1", data.DeletedOpenCount)
	}
	if data.ProcessesSkipped != 0 {
		t.Errorf("processes_skipped = %d, want 0", data.ProcessesSkipped)
	}
	if len(data.Processes) != 3 {
		t.Fatalf("processes len = %d, want 3", len(data.Processes))
	}

	first := data.Processes[0]
	if first.PID != 1234 || first.Comm != "writerA" || first.FDCount != 4 {
		t.Errorf("processes[0] = %+v, want pid 1234/writerA/4", first)
	}
	if len(first.ExamplePaths) != 3 {
		t.Errorf("example_paths len = %d, want cap 3", len(first.ExamplePaths))
	}
	for _, p := range first.ExamplePaths {
		if !strings.HasPrefix(p, "/mnt/lustre") {
			t.Errorf("example path %q outside requested prefix", p)
		}
	}
	if first.HasDeleted {
		t.Error("pid 1234 must not be flagged has_deleted")
	}

	second := data.Processes[1]
	if second.PID != 5678 || second.FDCount != 2 || !second.HasDeleted {
		t.Errorf("processes[1] = %+v, want pid 5678 with 2 fds incl. deleted", second)
	}

	third := data.Processes[2]
	if third.PID != 9999 || third.Comm != "unknown" || third.FDCount != 1 {
		t.Errorf("processes[2] = %+v, want pid 9999/unknown/1", third)
	}
}

func TestExecuteLimit(t *testing.T) {
	t.Parallel()
	tool := writeFDFixture(t)

	res, data := executeJSON(t, tool, `{"path":"/mnt/lustre","limit":2}`)
	if res.Status != registry.StatusOK {
		t.Fatalf("Status = %s, want ok", res.Status)
	}
	if len(data.Processes) != 2 {
		t.Fatalf("processes len = %d, want limit 2", len(data.Processes))
	}
	if data.Processes[0].PID != 1234 || data.Processes[1].PID != 5678 {
		t.Errorf("limit must keep the highest fd counts, got %+v", data.Processes)
	}
	// Totals still reflect the full scan, not the truncated list.
	if data.TotalMatchingFDs != 7 {
		t.Errorf("total_matching_fds = %d, want 7 despite limit", data.TotalMatchingFDs)
	}
}

func TestExecuteNoMatches(t *testing.T) {
	t.Parallel()
	tool := writeFDFixture(t)

	res, data := executeJSON(t, tool, `{"path":"/does/not/exist"}`)
	if res.Status != registry.StatusOK {
		t.Fatalf("Status = %s, want ok for zero matches", res.Status)
	}
	if data.TotalMatchingFDs != 0 || len(data.Processes) != 0 || data.DeletedOpenCount != 0 {
		t.Errorf("expected empty result, got %+v", data)
	}
}

func TestExecuteMissingPathParam(t *testing.T) {
	t.Parallel()
	tool := writeFDFixture(t)

	for _, args := range []string{`{}`, `{"limit":5}`, `{"path":""}`, ``} {
		res, err := tool.Execute(context.Background(), json.RawMessage(args))
		if err != nil {
			t.Fatalf("args %q: hard error %v", args, err)
		}
		if res.Status != registry.StatusError {
			t.Errorf("args %q: Status = %s, want error for missing path", args, res.Status)
		}
		if !strings.Contains(res.Summary, "path") {
			t.Errorf("args %q: summary %q should mention the missing 'path' param", args, res.Summary)
		}
	}
}

func TestExecuteBadArgs(t *testing.T) {
	t.Parallel()
	tool := writeFDFixture(t)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{not json`))
	if err != nil {
		t.Fatalf("Execute must not return hard error, got %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %s, want error for malformed args", res.Status)
	}
}

func TestExecutePermissionSkipped(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}

	tool := writeFDFixture(t)
	lockedFD := filepath.Join(tool.procfsRoot, "777", "fd")
	mustSymlink(t, "/mnt/lustre/locked", filepath.Join(lockedFD, "0"))
	if err := os.Chmod(lockedFD, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(lockedFD, 0o755) })

	res, data := executeJSON(t, tool, `{"path":"/mnt/lustre"}`)
	if res.Status != registry.StatusOK {
		t.Fatalf("Status = %s, want ok despite unreadable fd dir", res.Status)
	}
	if data.ProcessesSkipped != 1 {
		t.Errorf("processes_skipped = %d, want 1", data.ProcessesSkipped)
	}
	// The unreadable process must not appear in results.
	for _, p := range data.Processes {
		if p.PID == 777 {
			t.Errorf("permission-denied pid leaked into results: %+v", p)
		}
	}
}

func TestExecuteContextCancelled(t *testing.T) {
	t.Parallel()
	tool := writeFDFixture(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := tool.Execute(ctx, json.RawMessage(`{"path":"/mnt/lustre"}`))
	if err != nil {
		t.Fatalf("Execute must encapsulate cancellation, got hard error %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %s, want error on cancelled context", res.Status)
	}
}

func TestParseArgs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		args      string
		wantPath  string
		wantLimit int
		wantErr   bool
	}{
		{"path only uses default limit", `{"path":"/mnt"}`, "/mnt", 20, false},
		{"explicit limit", `{"path":"/mnt","limit":5}`, "/mnt", 5, false},
		{"limit capped at 100", `{"path":"/mnt","limit":500}`, "/mnt", 100, false},
		{"zero limit falls back to default", `{"path":"/mnt","limit":0}`, "/mnt", 20, false},
		{"negative limit falls back to default", `{"path":"/mnt","limit":-3}`, "/mnt", 20, false},
		{"missing path", `{}`, "", 0, true},
		{"empty path", `{"path":""}`, "", 0, true},
		{"nil args", ``, "", 0, true},
		{"malformed json", `{not json`, "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var raw json.RawMessage
			if tc.args != "" {
				raw = json.RawMessage(tc.args)
			}
			path, limit, err := parseArgs(raw)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %t", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if path != tc.wantPath || limit != tc.wantLimit {
				t.Errorf("parseArgs = %q,%d, want %q,%d", path, limit, tc.wantPath, tc.wantLimit)
			}
		})
	}
}

func TestScanFDDir(t *testing.T) {
	t.Parallel()
	tool := writeFDFixture(t)

	scan, err := scanFDDir(filepath.Join(tool.procfsRoot, "5678", "fd"), "/mnt/lustre")
	if err != nil {
		t.Fatalf("scanFDDir: %v", err)
	}
	if scan.count != 2 {
		t.Errorf("count = %d, want 2", scan.count)
	}
	if scan.deleted != 1 {
		t.Errorf("deleted = %d, want 1", scan.deleted)
	}
	if len(scan.examples) != 2 {
		t.Fatalf("examples = %v, want 2 entries", scan.examples)
	}
	for _, p := range scan.examples {
		if strings.HasSuffix(p, " (deleted)") {
			t.Errorf("example %q must have the deleted suffix stripped", p)
		}
	}

	if _, err := scanFDDir(filepath.Join(tool.procfsRoot, "4321", "fd"), "/mnt"); err == nil {
		t.Error("scanFDDir on missing dir must return an error")
	}
}

func TestReadComm(t *testing.T) {
	t.Parallel()
	tool := writeFDFixture(t)

	if got := tool.readComm("1234"); got != "writerA" {
		t.Errorf("readComm(1234) = %q, want writerA", got)
	}
	if got := tool.readComm("9999"); got != "unknown" {
		t.Errorf("readComm(9999) = %q, want unknown", got)
	}
}
