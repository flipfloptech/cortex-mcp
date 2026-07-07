package coredumps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

const (
	// µs-epoch timestamps, newest to oldest.
	tsNewUsec = int64(1751888100000000)
	tsMidUsec = int64(1751888050000000)
	tsOldUsec = int64(1751888000000000)
)

func coredumpctlJSON() []byte {
	// Deliberately NOT sorted newest-first: the tool must sort.
	return []byte(fmt.Sprintf(`[
		{"time":%d,"pid":100,"uid":0,"gid":0,"sig":11,"corefile":"present","exe":"/usr/bin/appA"},
		{"time":%d,"pid":300,"uid":1000,"gid":1000,"sig":6,"corefile":"missing","exe":"/usr/bin/appB"},
		{"time":%d,"pid":200,"uid":1000,"gid":1000,"sig":11,"corefile":"present","exe":"/usr/bin/appA"}
	]`, tsOldUsec, tsNewUsec, tsMidUsec))
}

func lookPathHave(available ...string) func(string) (string, error) {
	return func(file string) (string, error) {
		for _, a := range available {
			if file == a {
				return "/mock/bin/" + file, nil
			}
		}
		return "", errors.New("not found")
	}
}

func rfc3339FromUsec(usec int64) string {
	return time.Unix(usec/1000000, (usec%1000000)*1000).UTC().Format(time.RFC3339)
}

// ---------------------------------------------------------------------------
// Contract compliance
// ---------------------------------------------------------------------------

func TestCoredumpsTool_ContractCompliance(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "query_coredumps" {
		t.Errorf("expected name query_coredumps, got %q", tool.Name())
	}
	if tool.Category() != registry.CategorySystem {
		t.Errorf("expected category system, got %q", tool.Category())
	}
	if tool.Description() == "" {
		t.Error("expected non-empty description")
	}
	if tool.Hidden() {
		t.Error("expected Hidden() == false")
	}

	params := tool.Parameters()
	if len(params) != 1 || params[0].Name != "last_n" || params[0].Required {
		t.Errorf("expected single optional last_n parameter, got %+v", params)
	}

	help := tool.Help()
	for _, src := range []string{"coredumpctl", "/var/lib/systemd/coredump"} {
		if !strings.Contains(help, src) {
			t.Errorf("Help() must reference data source %q", src)
		}
	}
}

// ---------------------------------------------------------------------------
// IsSupported
// ---------------------------------------------------------------------------

func TestCoredumpsTool_IsSupported(t *testing.T) {
	t.Parallel()

	t.Run("coredumpctl in PATH", func(t *testing.T) {
		t.Parallel()
		tool := &Tool{coredumpRoot: filepath.Join(t.TempDir(), "nope"), execLookPath: lookPathHave("coredumpctl")}
		if ok, _ := tool.IsSupported(); !ok {
			t.Error("expected supported with coredumpctl in PATH")
		}
	})

	t.Run("coredump dir only", func(t *testing.T) {
		t.Parallel()
		tool := &Tool{coredumpRoot: t.TempDir(), execLookPath: lookPathHave()}
		if ok, _ := tool.IsSupported(); !ok {
			t.Error("expected supported with coredump directory present")
		}
	})

	t.Run("neither", func(t *testing.T) {
		t.Parallel()
		tool := &Tool{coredumpRoot: filepath.Join(t.TempDir(), "nope"), execLookPath: lookPathHave()}
		ok, reason := tool.IsSupported()
		if ok {
			t.Error("expected unsupported")
		}
		if reason == "" {
			t.Error("expected reason when unsupported")
		}
	})
}

// ---------------------------------------------------------------------------
// Execute: coredumpctl primary path
// ---------------------------------------------------------------------------

func TestCoredumpsTool_Execute_Coredumpctl(t *testing.T) {
	t.Parallel()
	tool := &Tool{
		coredumpRoot: filepath.Join(t.TempDir(), "nope"),
		execLookPath: lookPathHave("coredumpctl"),
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name != "coredumpctl" {
				return nil, fmt.Errorf("unexpected binary %q", name)
			}
			joined := strings.Join(args, " ")
			if !strings.Contains(joined, "list") || !strings.Contains(joined, "--json=short") || !strings.Contains(joined, "--no-pager") {
				return nil, fmt.Errorf("unexpected args: %s", joined)
			}
			return coredumpctlJSON(), nil
		},
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("expected ok, got %q (%s)", res.Status, res.Summary)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatal(err)
	}

	if out.Total != 3 {
		t.Errorf("expected total 3, got %d", out.Total)
	}
	if len(out.Dumps) != 3 {
		t.Fatalf("expected 3 dumps, got %d", len(out.Dumps))
	}

	// Newest first.
	if out.Dumps[0].PID != 300 || out.Dumps[1].PID != 200 || out.Dumps[2].PID != 100 {
		t.Errorf("dumps not sorted newest-first: %+v", out.Dumps)
	}

	newest := out.Dumps[0]
	if newest.Time != rfc3339FromUsec(tsNewUsec) {
		t.Errorf("expected time %q, got %q", rfc3339FromUsec(tsNewUsec), newest.Time)
	}
	if newest.SignalName != "SIGABRT" {
		t.Errorf("expected SIGABRT for sig 6, got %q", newest.SignalName)
	}
	if newest.CorefilePresent {
		t.Error("corefile 'missing' must map to corefile_present=false")
	}
	if newest.Executable != "/usr/bin/appB" || newest.UID != 1000 {
		t.Errorf("unexpected newest dump: %+v", newest)
	}

	if out.Dumps[1].SignalName != "SIGSEGV" || !out.Dumps[1].CorefilePresent {
		t.Errorf("unexpected mid dump: %+v", out.Dumps[1])
	}

	if out.CountByExecutable["/usr/bin/appA"] != 2 || out.CountByExecutable["/usr/bin/appB"] != 1 {
		t.Errorf("unexpected count_by_executable: %v", out.CountByExecutable)
	}
}

func TestCoredumpsTool_Execute_LastN(t *testing.T) {
	t.Parallel()

	makeTool := func() *Tool {
		return &Tool{
			coredumpRoot: filepath.Join(t.TempDir(), "nope"),
			execLookPath: lookPathHave("coredumpctl"),
			execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				var entries []string
				for i := 0; i < 30; i++ {
					entries = append(entries, fmt.Sprintf(
						`{"time":%d,"pid":%d,"uid":0,"gid":0,"sig":11,"corefile":"present","exe":"/bin/x"}`,
						tsOldUsec+int64(i)*1000000, i+1))
				}
				return []byte("[" + strings.Join(entries, ",") + "]"), nil
			},
		}
	}

	t.Run("default 20", func(t *testing.T) {
		t.Parallel()
		res, err := makeTool().Execute(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var out Output
		if err := json.Unmarshal(res.Data, &out); err != nil {
			t.Fatal(err)
		}
		if len(out.Dumps) != 20 {
			t.Errorf("expected default cap of 20 dumps, got %d", len(out.Dumps))
		}
		if out.Total != 30 {
			t.Errorf("total must reflect all dumps found (30), got %d", out.Total)
		}
		// Newest first: pid 30 has the newest timestamp.
		if out.Dumps[0].PID != 30 {
			t.Errorf("expected newest dump pid 30 first, got %d", out.Dumps[0].PID)
		}
	})

	t.Run("explicit last_n", func(t *testing.T) {
		t.Parallel()
		res, err := makeTool().Execute(context.Background(), json.RawMessage(`{"last_n": 5}`))
		if err != nil {
			t.Fatal(err)
		}
		var out Output
		if err := json.Unmarshal(res.Data, &out); err != nil {
			t.Fatal(err)
		}
		if len(out.Dumps) != 5 {
			t.Errorf("expected 5 dumps, got %d", len(out.Dumps))
		}
	})

	t.Run("last_n capped at 100", func(t *testing.T) {
		t.Parallel()
		res, err := makeTool().Execute(context.Background(), json.RawMessage(`{"last_n": 5000}`))
		if err != nil {
			t.Fatal(err)
		}
		var out Output
		if err := json.Unmarshal(res.Data, &out); err != nil {
			t.Fatal(err)
		}
		// only 30 exist, but the requested cap must clamp to 100 without error
		if len(out.Dumps) != 30 {
			t.Errorf("expected all 30 dumps under the 100 cap, got %d", len(out.Dumps))
		}
	})
}

func TestCoredumpsTool_Execute_NoCoredumpsFoundIsOK(t *testing.T) {
	t.Parallel()
	tool := &Tool{
		coredumpRoot: filepath.Join(t.TempDir(), "nope"),
		execLookPath: lookPathHave("coredumpctl"),
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return []byte("No coredumps found.\n"), errors.New("exit status 1")
		},
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("'No coredumps found' must be a valid empty OK result, got %q (%s)", res.Status, res.Summary)
	}
	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Total != 0 || len(out.Dumps) != 0 {
		t.Errorf("expected zero dumps, got %+v", out)
	}
}

// ---------------------------------------------------------------------------
// Execute: directory-scan fallback
// ---------------------------------------------------------------------------

func TestCoredumpsTool_Execute_DirFallback(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	files := []string{
		fmt.Sprintf("core.myapp.1000.a1b2c3d4e5f6.4242.%d.zst", tsOldUsec),
		fmt.Sprintf("core.multi.part.app.0.ffeeddccbbaa.99.%d", tsNewUsec),
		"core.incomplete",     // too few fields: skipped
		"not-a-core-file.txt", // wrong prefix: skipped
		fmt.Sprintf("core.badpid.0.aabb.notanum.%d.zst", tsOldUsec), // non-numeric pid: skipped
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	tool := &Tool{
		coredumpRoot: dir,
		execLookPath: lookPathHave(), // no coredumpctl
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("expected ok, got %q (%s)", res.Status, res.Summary)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Total != 2 {
		t.Fatalf("expected 2 parseable dumps, got %d: %+v", out.Total, out.Dumps)
	}

	// Newest first: multi.part.app carries tsNewUsec.
	first := out.Dumps[0]
	if first.Executable != "multi.part.app" || first.PID != 99 || first.UID != 0 {
		t.Errorf("unexpected first dump: %+v", first)
	}
	if first.Time != rfc3339FromUsec(tsNewUsec) {
		t.Errorf("expected time %q, got %q", rfc3339FromUsec(tsNewUsec), first.Time)
	}
	if !first.CorefilePresent {
		t.Error("scanned corefiles exist on disk: corefile_present must be true")
	}

	second := out.Dumps[1]
	if second.Executable != "myapp" || second.PID != 4242 || second.UID != 1000 {
		t.Errorf("unexpected second dump: %+v", second)
	}

	if out.CountByExecutable["myapp"] != 1 || out.CountByExecutable["multi.part.app"] != 1 {
		t.Errorf("unexpected count_by_executable: %v", out.CountByExecutable)
	}
}

func TestCoredumpsTool_Execute_EmptyDirFallback(t *testing.T) {
	t.Parallel()
	tool := &Tool{coredumpRoot: t.TempDir(), execLookPath: lookPathHave()}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("empty coredump dir must be valid, got %q", res.Status)
	}
	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Total != 0 {
		t.Errorf("expected 0 dumps, got %d", out.Total)
	}
}

// ---------------------------------------------------------------------------
// Execute: error paths
// ---------------------------------------------------------------------------

func TestCoredumpsTool_Execute_Errors(t *testing.T) {
	t.Parallel()

	t.Run("invalid args", func(t *testing.T) {
		t.Parallel()
		tool := &Tool{coredumpRoot: t.TempDir(), execLookPath: lookPathHave()}
		res, err := tool.Execute(context.Background(), json.RawMessage(`{invalid`))
		if err != nil {
			t.Fatalf("errors must be encapsulated, got hard error: %v", err)
		}
		if res.Status != registry.StatusError {
			t.Errorf("expected error status, got %q", res.Status)
		}
	})

	t.Run("no source available", func(t *testing.T) {
		t.Parallel()
		tool := &Tool{
			coredumpRoot: filepath.Join(t.TempDir(), "nope"),
			execLookPath: lookPathHave(),
		}
		res, err := tool.Execute(context.Background(), nil)
		if err != nil {
			t.Fatalf("errors must be encapsulated, got hard error: %v", err)
		}
		if res.Status != registry.StatusError {
			t.Errorf("expected error status, got %q", res.Status)
		}
	})

	t.Run("coredumpctl hard failure falls back to dir scan", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		fname := fmt.Sprintf("core.app.0.bootid.1.%d", tsOldUsec)
		if err := os.WriteFile(filepath.Join(dir, fname), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		tool := &Tool{
			coredumpRoot: dir,
			execLookPath: lookPathHave("coredumpctl"),
			execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return nil, errors.New("dbus timeout")
			},
		}
		res, err := tool.Execute(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != registry.StatusOK {
			t.Fatalf("expected fallback to dir scan, got %q (%s)", res.Status, res.Summary)
		}
		var out Output
		if err := json.Unmarshal(res.Data, &out); err != nil {
			t.Fatal(err)
		}
		if out.Total != 1 || out.Dumps[0].Executable != "app" {
			t.Errorf("expected 1 dump from fallback scan, got %+v", out)
		}
	})
}

func TestCoredumpsTool_Execute_ContextCancelled(t *testing.T) {
	t.Parallel()
	tool := &Tool{coredumpRoot: t.TempDir(), execLookPath: lookPathHave()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := tool.Execute(ctx, nil)
	if err != nil {
		t.Fatalf("cancellation must be encapsulated, got hard error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("expected error status for cancelled context, got %q", res.Status)
	}
}

// ---------------------------------------------------------------------------
// Parser tables
// ---------------------------------------------------------------------------

func TestSignalName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		sig  int
		want string
	}{
		{6, "SIGABRT"},
		{11, "SIGSEGV"},
		{7, "SIGBUS"},
		{4, "SIGILL"},
		{8, "SIGFPE"},
		{9, "SIG9"},
		{31, "SIG31"},
	}
	for _, tc := range tests {
		if got := signalName(tc.sig); got != tc.want {
			t.Errorf("signalName(%d): expected %q, got %q", tc.sig, tc.want, got)
		}
	}
}

func TestParseCoreFilename(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		filename string
		wantOK   bool
		wantComm string
		wantPID  int
		wantUID  int
	}{
		{"plain", "core.bash.1000.deadbeef.777.1751888000000000", true, "bash", 777, 1000},
		{"zst compressed", "core.bash.1000.deadbeef.777.1751888000000000.zst", true, "bash", 777, 1000},
		{"comm with dots", "core.my.app.d.0.aa.5.1751888000000000.zst", true, "my.app.d", 5, 0},
		{"wrong prefix", "notcore.bash.1000.a.1.1751888000000000", false, "", 0, 0},
		{"too few fields", "core.bash.1000", false, "", 0, 0},
		{"non-numeric pid", "core.bash.1000.a.xyz.1751888000000000", false, "", 0, 0},
		{"non-numeric ts", "core.bash.1000.a.1.notatime", false, "", 0, 0},
		{"empty", "", false, "", 0, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dump, ok := parseCoreFilename(tc.filename)
			if ok != tc.wantOK {
				t.Fatalf("expected ok=%v, got %v (%+v)", tc.wantOK, ok, dump)
			}
			if !ok {
				return
			}
			if dump.Executable != tc.wantComm || dump.PID != tc.wantPID || dump.UID != tc.wantUID {
				t.Errorf("unexpected dump: %+v", dump)
			}
			if dump.Time == "" {
				t.Error("expected RFC3339 time to be set")
			}
		})
	}
}

func TestParseCoredumpctlJSON(t *testing.T) {
	t.Parallel()

	t.Run("valid", func(t *testing.T) {
		t.Parallel()
		dumps, err := parseCoredumpctlJSON(coredumpctlJSON())
		if err != nil {
			t.Fatal(err)
		}
		if len(dumps) != 3 {
			t.Fatalf("expected 3 dumps, got %d", len(dumps))
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		t.Parallel()
		if _, err := parseCoredumpctlJSON([]byte("not json")); err == nil {
			t.Error("expected error for invalid JSON")
		}
	})

	t.Run("empty array", func(t *testing.T) {
		t.Parallel()
		dumps, err := parseCoredumpctlJSON([]byte("[]"))
		if err != nil {
			t.Fatal(err)
		}
		if len(dumps) != 0 {
			t.Errorf("expected 0 dumps, got %d", len(dumps))
		}
	})
}

// ---------------------------------------------------------------------------
// Benchmarks (benchcov: one per non-test function)
// ---------------------------------------------------------------------------

func benchCoredumpctlTool() *Tool {
	return &Tool{
		coredumpRoot: "/nonexistent",
		execLookPath: lookPathHave("coredumpctl"),
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return coredumpctlJSON(), nil
		},
	}
}

func BenchmarkNew(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = New()
	}
}

func BenchmarkName(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Name()
	}
}

func BenchmarkDescription(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Description()
	}
}

func BenchmarkHelp(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Help()
	}
}

func BenchmarkCategory(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Category()
	}
}

func BenchmarkParameters(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Parameters()
	}
}

func BenchmarkHidden(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Hidden()
	}
}

func BenchmarkIsSupported(b *testing.B) {
	tool := benchCoredumpctlTool()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	tool := benchCoredumpctlTool()
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, nil)
	}
}

func BenchmarkParseCoredumpctlJSON(b *testing.B) {
	data := coredumpctlJSON()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = parseCoredumpctlJSON(data)
	}
}

func BenchmarkScanCoredumpDir(b *testing.B) {
	dir := b.TempDir()
	for i := 0; i < 10; i++ {
		fname := fmt.Sprintf("core.app%d.0.bootid.%d.%d.zst", i, i+1, tsOldUsec+int64(i))
		if err := os.WriteFile(filepath.Join(dir, fname), []byte("x"), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = scanCoredumpDir(dir)
	}
}

func BenchmarkParseCoreFilename(b *testing.B) {
	name := "core.my.app.d.1000.deadbeef.4242.1751888000000000.zst"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = parseCoreFilename(name)
	}
}

func BenchmarkSignalName(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = signalName(11)
	}
}
