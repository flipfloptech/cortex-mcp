package packageaudit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// rpmMock simulates `rpm -q --queryformat ...` behavior: installed packages
// print NAME\tVERSION\tRELEASE\tARCH lines, missing packages print
// "package X is not installed" and exit 1 (returned as an error alongside
// the stdout payload, matching exec.Cmd.Output semantics).
func rpmMock(installed map[string]string) func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "rpm" {
			return nil, fmt.Errorf("unexpected binary %q", name)
		}
		pkg := args[len(args)-1]
		if line, ok := installed[pkg]; ok {
			return []byte(line), nil
		}
		return []byte(fmt.Sprintf("package %s is not installed\n", pkg)), errors.New("exit status 1")
	}
}

func dpkgMock(installed map[string]string) func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "dpkg-query" {
			return nil, fmt.Errorf("unexpected binary %q", name)
		}
		pkg := args[len(args)-1]
		if line, ok := installed[pkg]; ok {
			return []byte(line), nil
		}
		return []byte(fmt.Sprintf("dpkg-query: no packages found matching %s\n", pkg)), errors.New("exit status 1")
	}
}

func lookPathOnly(available ...string) func(string) (string, error) {
	return func(file string) (string, error) {
		for _, a := range available {
			if file == a {
				return "/mock/bin/" + file, nil
			}
		}
		return "", errors.New("not found")
	}
}

func mustArgs(t testing.TB, v interface{}) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// ---------------------------------------------------------------------------
// Contract compliance
// ---------------------------------------------------------------------------

func TestPackageAuditTool_ContractCompliance(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "get_package_audit" {
		t.Errorf("expected name get_package_audit, got %q", tool.Name())
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
	if len(params) != 1 {
		t.Fatalf("expected exactly 1 parameter, got %d", len(params))
	}
	if params[0].Name != "packages" || !params[0].Required || params[0].Type != "array" {
		t.Errorf("expected required array parameter 'packages', got %+v", params[0])
	}

	help := tool.Help()
	for _, src := range []string{"rpm", "dpkg-query"} {
		if !strings.Contains(help, src) {
			t.Errorf("Help() must reference data source %q", src)
		}
	}
}

// ---------------------------------------------------------------------------
// IsSupported
// ---------------------------------------------------------------------------

func TestPackageAuditTool_IsSupported(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		available []string
		want      bool
	}{
		{"rpm present", []string{"rpm"}, true},
		{"dpkg-query present", []string{"dpkg-query"}, true},
		{"both present", []string{"rpm", "dpkg-query"}, true},
		{"neither present", nil, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tool := &Tool{execLookPath: lookPathOnly(tc.available...)}
			ok, reason := tool.IsSupported()
			if ok != tc.want {
				t.Errorf("expected supported=%v, got %v (%s)", tc.want, ok, reason)
			}
			if !tc.want && reason == "" {
				t.Error("expected reason when unsupported")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Execute: rpm path
// ---------------------------------------------------------------------------

func TestPackageAuditTool_Execute_RPM(t *testing.T) {
	t.Parallel()
	tool := &Tool{
		execLookPath: lookPathOnly("rpm"),
		execCommand: rpmMock(map[string]string{
			"bash":   "bash\t5.2.26\t3.el9\tx86_64\n",
			"kernel": "kernel\t5.14.0\t427.el9\tx86_64\nkernel\t5.14.1\t430.el9\tx86_64\n",
		}),
	}

	res, err := tool.Execute(context.Background(), mustArgs(t, map[string]interface{}{
		"packages": []string{"bash", "kernel", "nosuchpkg"},
	}))
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

	if out.Manager != "rpm" {
		t.Errorf("expected manager rpm, got %q", out.Manager)
	}
	// bash(1) + kernel(2 installed versions) + nosuchpkg(1 missing entry) = 4
	if len(out.Packages) != 4 {
		t.Fatalf("expected 4 package entries, got %d: %+v", len(out.Packages), out.Packages)
	}
	if out.InstalledCount != 3 {
		t.Errorf("expected installed_count 3, got %d", out.InstalledCount)
	}
	if len(out.Missing) != 1 || out.Missing[0] != "nosuchpkg" {
		t.Errorf("expected missing [nosuchpkg], got %v", out.Missing)
	}

	bash := out.Packages[0]
	if !bash.Installed || bash.Query != "bash" || bash.Name != "bash" ||
		bash.Version != "5.2.26" || bash.Release != "3.el9" || bash.Arch != "x86_64" {
		t.Errorf("unexpected bash entry: %+v", bash)
	}

	for _, p := range out.Packages {
		if p.Query == "nosuchpkg" {
			if p.Installed {
				t.Errorf("nosuchpkg must be installed=false: %+v", p)
			}
			if p.Version != "" || p.Name != "" {
				t.Errorf("missing package must not carry name/version: %+v", p)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Execute: dpkg path
// ---------------------------------------------------------------------------

func TestPackageAuditTool_Execute_Dpkg(t *testing.T) {
	t.Parallel()
	tool := &Tool{
		execLookPath: lookPathOnly("dpkg-query"),
		execCommand: dpkgMock(map[string]string{
			"bash":     "bash\t5.2.21-2ubuntu4\tamd64\tinstalled\n",
			"removed":  "removed\t1.0-1\tamd64\tconfig-files\n",
			"libssl3*": "libssl3\t3.0.13-0ubuntu3\tamd64\tinstalled\nlibssl3t64\t3.0.14-1\tamd64\tinstalled\n",
		}),
	}

	res, err := tool.Execute(context.Background(), mustArgs(t, map[string]interface{}{
		"packages": []string{"bash", "removed", "libssl3*", "ghost"},
	}))
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatal(err)
	}

	if out.Manager != "dpkg" {
		t.Errorf("expected manager dpkg, got %q", out.Manager)
	}
	if out.InstalledCount != 3 {
		t.Errorf("expected installed_count 3 (bash + 2 glob matches), got %d", out.InstalledCount)
	}
	// "removed" (config-files status) and "ghost" (no match) are both missing.
	if len(out.Missing) != 2 {
		t.Errorf("expected 2 missing queries, got %v", out.Missing)
	}

	bash := out.Packages[0]
	if !bash.Installed || bash.Name != "bash" || bash.Version != "5.2.21-2ubuntu4" || bash.Arch != "amd64" {
		t.Errorf("unexpected bash entry: %+v", bash)
	}
	if bash.Release != "" {
		t.Errorf("dpkg entries have no release field, got %q", bash.Release)
	}
}

// ---------------------------------------------------------------------------
// Execute: manager preference and error paths
// ---------------------------------------------------------------------------

func TestPackageAuditTool_Execute_PrefersRPMOverDpkg(t *testing.T) {
	t.Parallel()
	tool := &Tool{
		execLookPath: lookPathOnly("rpm", "dpkg-query"),
		execCommand:  rpmMock(map[string]string{"bash": "bash\t5.2\t1\tx86_64\n"}),
	}
	res, err := tool.Execute(context.Background(), mustArgs(t, map[string]interface{}{"packages": []string{"bash"}}))
	if err != nil {
		t.Fatal(err)
	}
	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Manager != "rpm" {
		t.Errorf("rpm must win detection when both managers exist, got %q", out.Manager)
	}
}

func TestPackageAuditTool_Execute_ArgErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args json.RawMessage
	}{
		{"nil args", nil},
		{"empty object", json.RawMessage(`{}`)},
		{"empty packages array", json.RawMessage(`{"packages": []}`)},
		{"wrong type", json.RawMessage(`{"packages": "bash"}`)},
		{"invalid json", json.RawMessage(`{nope`)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tool := &Tool{
				execLookPath: lookPathOnly("rpm"),
				execCommand:  rpmMock(nil),
			}
			res, err := tool.Execute(context.Background(), tc.args)
			if err != nil {
				t.Fatalf("errors must be encapsulated, got hard error: %v", err)
			}
			if res.Status != registry.StatusError {
				t.Errorf("expected error status, got %q", res.Status)
			}
		})
	}
}

func TestPackageAuditTool_Execute_NoManager(t *testing.T) {
	t.Parallel()
	tool := &Tool{execLookPath: lookPathOnly()}
	res, err := tool.Execute(context.Background(), mustArgs(t, map[string]interface{}{"packages": []string{"bash"}}))
	if err != nil {
		t.Fatalf("errors must be encapsulated, got hard error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("expected error status when no manager found, got %q", res.Status)
	}
}

func TestPackageAuditTool_Execute_CapsAt50(t *testing.T) {
	t.Parallel()
	calls := 0
	tool := &Tool{
		execLookPath: lookPathOnly("rpm"),
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			calls++
			return []byte("p\t1\t1\tnoarch\n"), nil
		},
	}
	pkgs := make([]string, 75)
	for i := range pkgs {
		pkgs[i] = fmt.Sprintf("pkg%d", i)
	}
	res, err := tool.Execute(context.Background(), mustArgs(t, map[string]interface{}{"packages": pkgs}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("expected ok, got %q", res.Status)
	}
	if calls != 50 {
		t.Errorf("expected the query list to be capped at 50, got %d manager invocations", calls)
	}
}

// Per-package command failure with unrecognizable output must degrade to an
// installed=false entry, never abort the batch.
func TestPackageAuditTool_Execute_PerPackageFailureDoesNotAbort(t *testing.T) {
	t.Parallel()
	tool := &Tool{
		execLookPath: lookPathOnly("rpm"),
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			pkg := args[len(args)-1]
			if pkg == "broken" {
				return nil, errors.New("rpmdb corruption")
			}
			return []byte("bash\t5.2\t1\tx86_64\n"), nil
		},
	}
	res, err := tool.Execute(context.Background(), mustArgs(t, map[string]interface{}{
		"packages": []string{"broken", "bash"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("batch must survive per-package failure, got %q (%s)", res.Status, res.Summary)
	}
	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatal(err)
	}
	if out.InstalledCount != 1 {
		t.Errorf("expected 1 installed, got %d", out.InstalledCount)
	}
	if len(out.Missing) != 1 || out.Missing[0] != "broken" {
		t.Errorf("expected broken in missing, got %v", out.Missing)
	}
}

func TestPackageAuditTool_Execute_ContextCancelled(t *testing.T) {
	t.Parallel()
	tool := &Tool{
		execLookPath: lookPathOnly("rpm"),
		execCommand:  rpmMock(map[string]string{"bash": "bash\t5.2\t1\tx86_64\n"}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := tool.Execute(ctx, mustArgs(t, map[string]interface{}{"packages": []string{"bash"}}))
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

func TestParseRPMOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		out           string
		wantInstalled int
	}{
		{"single package", "bash\t5.2.26\t3.el9\tx86_64\n", 1},
		{"multiple versions", "kernel\t5.14.0\t427\tx86_64\nkernel\t5.14.1\t430\tx86_64\n", 2},
		{"not installed", "package foo is not installed\n", 0},
		{"empty output", "", 0},
		{"garbage line skipped", "some random text without tabs\n", 0},
		{"mixed valid and garbage", "bash\t5.2\t1\tx86_64\nnoise\n", 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			entries := parseRPMOutput("q", []byte(tc.out))
			if len(entries) != tc.wantInstalled {
				t.Errorf("expected %d installed entries, got %d: %+v", tc.wantInstalled, len(entries), entries)
			}
			for _, e := range entries {
				if !e.Installed || e.Query != "q" {
					t.Errorf("bad entry: %+v", e)
				}
			}
		})
	}
}

func TestParseDpkgOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		out           string
		wantInstalled int
	}{
		{"installed", "bash\t5.2.21\tamd64\tinstalled\n", 1},
		{"config-files residue", "old\t1.0\tamd64\tconfig-files\n", 0},
		{"not-installed status", "gone\t\tamd64\tnot-installed\n", 0},
		{"glob multi-match", "a\t1\tamd64\tinstalled\nb\t2\tamd64\tinstalled\n", 2},
		{"no packages found stderr-ish", "dpkg-query: no packages found matching x\n", 0},
		{"empty", "", 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			entries := parseDpkgOutput("q", []byte(tc.out))
			if len(entries) != tc.wantInstalled {
				t.Errorf("expected %d installed entries, got %d: %+v", tc.wantInstalled, len(entries), entries)
			}
		})
	}
}

func TestDetectManager(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		available []string
		want      string
	}{
		{"rpm only", []string{"rpm"}, "rpm"},
		{"dpkg only", []string{"dpkg-query"}, "dpkg"},
		{"both prefers rpm", []string{"rpm", "dpkg-query"}, "rpm"},
		{"none", nil, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tool := &Tool{execLookPath: lookPathOnly(tc.available...)}
			if got := tool.detectManager(); got != tc.want {
				t.Errorf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Benchmarks (benchcov: one per non-test function)
// ---------------------------------------------------------------------------

func benchTool() *Tool {
	return &Tool{
		execLookPath: lookPathOnly("rpm"),
		execCommand:  rpmMock(map[string]string{"bash": "bash\t5.2.26\t3.el9\tx86_64\n"}),
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
	tool := benchTool()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	tool := benchTool()
	args := json.RawMessage(`{"packages":["bash","missing"]}`)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, args)
	}
}

func BenchmarkDetectManager(b *testing.B) {
	tool := benchTool()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.detectManager()
	}
}

func BenchmarkParseRPMOutput(b *testing.B) {
	out := []byte("bash\t5.2.26\t3.el9\tx86_64\nkernel\t5.14.0\t427.el9\tx86_64\n")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseRPMOutput("q", out)
	}
}

func BenchmarkParseDpkgOutput(b *testing.B) {
	out := []byte("bash\t5.2.21-2ubuntu4\tamd64\tinstalled\n")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseDpkgOutput("q", out)
	}
}
