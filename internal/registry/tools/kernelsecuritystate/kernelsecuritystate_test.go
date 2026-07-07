package kernelsecuritystate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// fakeTree describes which files to materialize in the fake proc/sys roots.
// Empty string values mean "do not create the file".
type fakeTree struct {
	taint    string
	lockdown string
	selinux  string
	apparmor string
	vulns    map[string]string
}

// buildTree materializes a fakeTree and returns (procfsRoot, sysfsRoot).
func buildTree(t *testing.T, tree fakeTree) (string, string) {
	t.Helper()
	procRoot := t.TempDir()
	sysRoot := t.TempDir()

	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if tree.taint != "" {
		write(filepath.Join(procRoot, "sys", "kernel", "tainted"), tree.taint)
	}
	if tree.lockdown != "" {
		write(filepath.Join(sysRoot, "kernel", "security", "lockdown"), tree.lockdown)
	}
	if tree.selinux != "" {
		write(filepath.Join(sysRoot, "fs", "selinux", "enforce"), tree.selinux)
	}
	if tree.apparmor != "" {
		write(filepath.Join(sysRoot, "module", "apparmor", "parameters", "enabled"), tree.apparmor)
	}
	for name, content := range tree.vulns {
		write(filepath.Join(sysRoot, "devices", "system", "cpu", "vulnerabilities", name), content)
	}
	return procRoot, sysRoot
}

func newTestTool(t *testing.T, tree fakeTree) *Tool {
	t.Helper()
	tool := New()
	tool.procfsRoot, tool.sysfsRoot = buildTree(t, tree)
	return tool
}

func TestKernelSecurityStateTool_ContractCompliance(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "get_kernel_security_state" {
		t.Errorf("Name() = %q, want get_kernel_security_state", tool.Name())
	}
	if tool.Category() != registry.CategorySystem {
		t.Errorf("Category() = %q, want system", tool.Category())
	}
	if tool.Description() == "" {
		t.Error("Description() must not be empty")
	}
	help := tool.Help()
	for _, src := range []string{"/proc/sys/kernel/tainted", "lockdown", "selinux", "apparmor", "vulnerabilities"} {
		if !strings.Contains(help, src) {
			t.Errorf("Help() must reference data source %q", src)
		}
	}
	if tool.Parameters() != nil {
		t.Error("Parameters() must be nil (no parameters)")
	}
	if tool.Hidden() {
		t.Error("Hidden() must be false")
	}
}

func TestKernelSecurityStateTool_IsSupported(t *testing.T) {
	t.Parallel()
	tool := New()
	ok, reason := tool.IsSupported()
	if registry.IsLinux() && !ok {
		t.Errorf("IsSupported() = false (%s) on Linux, want true", reason)
	}
}

func TestDecodeTaint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value uint64
		want  []string
	}{
		{
			name:  "untainted",
			value: 0,
			want:  nil,
		},
		{
			name:  "proprietary module only (bit 0)",
			value: 1,
			want:  []string{"P: proprietary module loaded"},
		},
		{
			name:  "proprietary and out-of-tree (bits 0 and 12)",
			value: 1 | 1<<12,
			want:  []string{"P: proprietary module loaded", "O: out-of-tree module loaded"},
		},
		{
			name:  "unsigned module (bit 13)",
			value: 1 << 13,
			want:  []string{"E: unsigned module loaded"},
		},
		{
			name:  "force loaded (bit 1)",
			value: 1 << 1,
			want:  []string{"F: module force loaded"},
		},
		{
			name:  "all documented low bits decode",
			value: 1<<2 | 1<<3 | 1<<4 | 1<<5 | 1<<6 | 1<<7 | 1<<8 | 1<<9 | 1<<10 | 1<<11 | 1<<14 | 1<<15 | 1<<16 | 1<<17 | 1<<18,
			want: []string{
				"S: SMP kernel on out-of-spec CPU",
				"R: module force unloaded",
				"M: machine check exception occurred",
				"B: bad page referenced",
				"U: userspace forced taint",
				"D: kernel oops/die occurred",
				"A: ACPI table overridden by user",
				"W: kernel issued warning",
				"C: staging driver loaded",
				"I: firmware workaround applied",
				"L: soft lockup occurred",
				"K: kernel live patched",
				"X: auxiliary taint (distro-defined)",
				"T: built with struct randomization plugin",
				"N: in-kernel test has run",
			},
		},
		{
			name:  "unknown high bit",
			value: 1 << 19,
			want:  []string{"bit 19: unknown taint flag"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := decodeTaint(tt.value)
			if len(got) != len(tt.want) {
				t.Fatalf("decodeTaint(%d) = %v, want %v", tt.value, got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("reason[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestParseLockdown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"none active", "[none] integrity confidentiality\n", "none"},
		{"integrity active", "none [integrity] confidentiality\n", "integrity"},
		{"confidentiality active", "none integrity [confidentiality]\n", "confidentiality"},
		{"no brackets", "none integrity confidentiality\n", "unknown"},
		{"garbage", "wat\n", "unknown"},
		{"empty", "", "unknown"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := parseLockdown([]byte(tt.input)); got != tt.want {
				t.Errorf("parseLockdown(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestClassifyVulnerability(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status string
		want   string
	}{
		{"Not affected", "not_affected"},
		{"Mitigation: PTI", "mitigated"},
		{"Mitigation: Clear CPU buffers; SMT disabled", "mitigated"},
		{"Vulnerable", "vulnerable"},
		{"Vulnerable: Clear CPU buffers attempted, no microcode", "vulnerable"},
		{"KVM: Mitigation: Split huge pages", "mitigated"},
		{"KVM: Vulnerable", "vulnerable"},
		{"Unknown: Dependent on hypervisor status", "unknown"},
		{"", "unknown"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.status, func(t *testing.T) {
			t.Parallel()
			if got := classifyVulnerability(tt.status); got != tt.want {
				t.Errorf("classifyVulnerability(%q) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

func TestKernelSecurityStateTool_Execute_FullTree(t *testing.T) {
	t.Parallel()

	tool := newTestTool(t, fakeTree{
		taint:    "4097\n", // bits 0 (P) and 12 (O)
		lockdown: "none [integrity] confidentiality\n",
		selinux:  "1\n",
		apparmor: "Y\n",
		vulns: map[string]string{
			"meltdown":   "Mitigation: PTI\n",
			"spectre_v2": "Vulnerable: eIBRS with unprivileged eBPF\n",
			"l1tf":       "Not affected\n",
		},
	})

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute returned hard error: %v", err)
	}
	if res.Status != registry.StatusWarning {
		t.Fatalf("Status = %q, want warning (P taint + vulnerable CPU)", res.Status)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}

	if !out.Tainted || out.TaintValue != 4097 {
		t.Errorf("tainted=%v value=%d, want true/4097", out.Tainted, out.TaintValue)
	}
	joinedReasons := strings.Join(out.TaintReasons, " | ")
	if !strings.Contains(joinedReasons, "proprietary") || !strings.Contains(joinedReasons, "out-of-tree") {
		t.Errorf("TaintReasons = %v, want P and O decoded", out.TaintReasons)
	}
	if out.LockdownMode != "integrity" {
		t.Errorf("LockdownMode = %q, want integrity", out.LockdownMode)
	}
	if out.LSM.SELinux != "enforcing" {
		t.Errorf("selinux = %q, want enforcing", out.LSM.SELinux)
	}
	if out.LSM.AppArmor != "enabled" {
		t.Errorf("apparmor = %q, want enabled", out.LSM.AppArmor)
	}
	if len(out.Vulnerabilities) != 3 {
		t.Fatalf("Vulnerabilities = %v, want 3 entries", out.Vulnerabilities)
	}
	if out.VulnerableCount != 1 {
		t.Errorf("VulnerableCount = %d, want 1", out.VulnerableCount)
	}
	states := map[string]string{}
	for _, v := range out.Vulnerabilities {
		states[v.Name] = v.State
	}
	if states["meltdown"] != "mitigated" || states["spectre_v2"] != "vulnerable" || states["l1tf"] != "not_affected" {
		t.Errorf("vulnerability states = %v", states)
	}

	joinedWarnings := strings.ToLower(strings.Join(out.WarningReasons, " | "))
	if !strings.Contains(joinedWarnings, "proprietary") {
		t.Errorf("warnings %v must flag proprietary module taint", out.WarningReasons)
	}
	if !strings.Contains(joinedWarnings, "spectre_v2") {
		t.Errorf("warnings %v must flag vulnerable spectre_v2", out.WarningReasons)
	}
}

func TestKernelSecurityStateTool_Execute_CleanSystem(t *testing.T) {
	t.Parallel()

	tool := newTestTool(t, fakeTree{
		taint:    "0\n",
		apparmor: "N\n",
		vulns: map[string]string{
			"meltdown": "Not affected\n",
		},
	})

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute returned hard error: %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("Status = %q, want ok (summary: %s)", res.Status, res.Summary)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if out.Tainted {
		t.Error("Tainted = true, want false")
	}
	if len(out.TaintReasons) != 0 {
		t.Errorf("TaintReasons = %v, want none", out.TaintReasons)
	}
	if out.LockdownMode != "" {
		t.Errorf("LockdownMode = %q, want omitted (no lockdown file)", out.LockdownMode)
	}
	if out.LSM.SELinux != "not_present" {
		t.Errorf("selinux = %q, want not_present", out.LSM.SELinux)
	}
	if out.LSM.AppArmor != "disabled" {
		t.Errorf("apparmor = %q, want disabled", out.LSM.AppArmor)
	}
	if out.VulnerableCount != 0 {
		t.Errorf("VulnerableCount = %d, want 0", out.VulnerableCount)
	}
	if len(out.WarningReasons) != 0 {
		t.Errorf("WarningReasons = %v, want none", out.WarningReasons)
	}
}

func TestKernelSecurityStateTool_Execute_SELinuxPermissive(t *testing.T) {
	t.Parallel()

	tool := newTestTool(t, fakeTree{
		taint:   "0\n",
		selinux: "0\n",
	})

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute returned hard error: %v", err)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if out.LSM.SELinux != "permissive" {
		t.Errorf("selinux = %q, want permissive", out.LSM.SELinux)
	}
}

func TestKernelSecurityStateTool_Execute_AllSourcesMissing(t *testing.T) {
	t.Parallel()

	// Completely empty roots: every source degrades, none errors.
	tool := New()
	tool.procfsRoot = t.TempDir()
	tool.sysfsRoot = t.TempDir()

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute returned hard error: %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("Status = %q, want ok — missing sources must never error", res.Status)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if out.Tainted {
		t.Error("Tainted = true, want false when taint file missing")
	}
	if out.LSM.SELinux != "not_present" || out.LSM.AppArmor != "not_present" {
		t.Errorf("LSM = %+v, want both not_present", out.LSM)
	}
	if len(out.Vulnerabilities) != 0 {
		t.Errorf("Vulnerabilities = %v, want empty", out.Vulnerabilities)
	}
}

func TestKernelSecurityStateTool_Execute_UnsignedModuleWarning(t *testing.T) {
	t.Parallel()

	tool := newTestTool(t, fakeTree{
		taint: "8192\n", // bit 13 (E)
	})

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute returned hard error: %v", err)
	}
	if res.Status != registry.StatusWarning {
		t.Fatalf("Status = %q, want warning for unsigned module taint", res.Status)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if len(out.WarningReasons) != 1 || !strings.Contains(out.WarningReasons[0], "unsigned") {
		t.Errorf("WarningReasons = %v, want single unsigned-module warning", out.WarningReasons)
	}
}

func TestKernelSecurityStateTool_Execute_MalformedTaintDegrades(t *testing.T) {
	t.Parallel()

	tool := newTestTool(t, fakeTree{
		taint: "not-a-number\n",
	})

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute returned hard error: %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("Status = %q, want ok (malformed taint degrades to untainted)", res.Status)
	}

	var out Output
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if out.Tainted {
		t.Error("Tainted = true, want false for unparseable taint value")
	}
}

func TestKernelSecurityStateTool_Execute_ContextCanceled(t *testing.T) {
	t.Parallel()

	tool := newTestTool(t, fakeTree{taint: "0\n"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := tool.Execute(ctx, nil)
	if err != nil {
		t.Fatalf("Execute must encapsulate cancellation, got hard error: %v", err)
	}
	if res.Status != registry.StatusError {
		t.Errorf("Status = %q, want error on canceled context", res.Status)
	}
}
