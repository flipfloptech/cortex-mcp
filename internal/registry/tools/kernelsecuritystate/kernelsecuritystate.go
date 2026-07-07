// Package kernelsecuritystate implements the get_kernel_security_state tool.
//
// It audits the kernel's integrity and hardening posture from procfs/sysfs:
// the taint bitmask (decoded into words), lockdown mode, LSM status
// (SELinux/AppArmor), and per-CPU-vulnerability mitigation state.
package kernelsecuritystate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// taintBits maps kernel taint bit positions (0..18) to their canonical flag
// letter and a human-readable reason. Order matches
// Documentation/admin-guide/tainted-kernels.rst.
var taintBits = []struct {
	Letter string
	Reason string
}{
	{"P", "proprietary module loaded"},              // bit 0
	{"F", "module force loaded"},                    // bit 1
	{"S", "SMP kernel on out-of-spec CPU"},          // bit 2
	{"R", "module force unloaded"},                  // bit 3
	{"M", "machine check exception occurred"},       // bit 4
	{"B", "bad page referenced"},                    // bit 5
	{"U", "userspace forced taint"},                 // bit 6
	{"D", "kernel oops/die occurred"},               // bit 7
	{"A", "ACPI table overridden by user"},          // bit 8
	{"W", "kernel issued warning"},                  // bit 9
	{"C", "staging driver loaded"},                  // bit 10
	{"I", "firmware workaround applied"},            // bit 11
	{"O", "out-of-tree module loaded"},              // bit 12
	{"E", "unsigned module loaded"},                 // bit 13
	{"L", "soft lockup occurred"},                   // bit 14
	{"K", "kernel live patched"},                    // bit 15
	{"X", "auxiliary taint (distro-defined)"},       // bit 16
	{"T", "built with struct randomization plugin"}, // bit 17
	{"N", "in-kernel test has run"},                 // bit 18
}

// warnTaintBits are the taint bits that indicate a module-trust problem
// worth a warning: P (proprietary), F (force loaded), E (unsigned).
var warnTaintBits = map[int]bool{0: true, 1: true, 13: true}

// LSMState reports the Linux Security Module status.
type LSMState struct {
	// SELinux is "enforcing", "permissive", or "not_present".
	SELinux string `json:"selinux"`

	// AppArmor is "enabled", "disabled", or "not_present".
	AppArmor string `json:"apparmor"`
}

// Vulnerability is one CPU vulnerability sysfs entry.
type Vulnerability struct {
	Name string `json:"name"`

	// Status is the raw kernel string (e.g. "Mitigation: PTI").
	Status string `json:"status"`

	// State classifies Status: "not_affected", "mitigated", "vulnerable",
	// or "unknown".
	State string `json:"state"`
}

// Output is the tool's data payload.
type Output struct {
	Tainted      bool     `json:"tainted"`
	TaintValue   uint64   `json:"taint_value"`
	TaintReasons []string `json:"taint_reasons,omitempty"`

	// LockdownMode is "none", "integrity", or "confidentiality"; omitted
	// when the kernel has no lockdown support (file missing).
	LockdownMode string `json:"lockdown_mode,omitempty"`

	LSM LSMState `json:"lsm"`

	Vulnerabilities []Vulnerability `json:"vulnerabilities"`
	VulnerableCount int             `json:"vulnerable_count"`

	// WarningReasons lists pre-evaluated problems in plain words.
	WarningReasons []string `json:"warning_reasons"`
}

// Tool implements registry.Tool for get_kernel_security_state.
type Tool struct {
	// procfsRoot and sysfsRoot are the procfs/sysfs mount points,
	// injectable for hermetic tests.
	procfsRoot string
	sysfsRoot  string
}

// New returns a kernel security state tool bound to the real /proc and /sys.
func New() *Tool {
	return &Tool{procfsRoot: "/proc", sysfsRoot: "/sys"}
}

func init() {
	registry.Register(New())
}

// Name returns the unique tool identifier.
func (t *Tool) Name() string { return "get_kernel_security_state" }

// Description returns the one-line summary for tool discovery.
func (t *Tool) Description() string {
	return "Audit kernel integrity: taint flags, lockdown mode, LSM status, and CPU vulnerability mitigations"
}

// Help returns the full tool documentation.
func (t *Tool) Help() string {
	return `get_kernel_security_state — Kernel Integrity & Hardening Audit

Reports whether the kernel is trustworthy (taint state), how locked-down it
is, which LSM is active, and whether known CPU vulnerabilities are mitigated.
A tainted kernel invalidates vendor support and makes crash dumps suspect;
unmitigated CPU vulnerabilities are a compliance/security finding.

Data Sources (all native, each independent):
  - /proc/sys/kernel/tainted: decimal bitmask, decoded bit-by-bit (bits 0-18)
    into flag letters and plain-language reasons (P proprietary module,
    F force loaded, O out-of-tree, E unsigned, L soft lockup, ...).
  - /sys/kernel/security/lockdown: active mode is the bracketed word in
    "none [integrity] confidentiality".
  - SELinux: /sys/fs/selinux/enforce (1 = enforcing, 0 = permissive; file
    absent = not_present).
  - AppArmor: /sys/module/apparmor/parameters/enabled (Y/N; file absent =
    not_present).
  - CPU vulnerabilities: /sys/devices/system/cpu/vulnerabilities/* — each
    file classified as not_affected ("Not affected"), mitigated
    ("Mitigation: ..."), or vulnerable ("Vulnerable...").

Warning Heuristics:
  - Any vulnerability classified as vulnerable (vulnerable_count > 0).
  - Trust-relevant taint bits set: P (proprietary), F (force loaded),
    E (unsigned module).

Degradation Profile:
  - Every source is independent: a missing file yields "not_present" (LSM),
    an omitted lockdown_mode, an empty vulnerabilities list, or an untainted
    default — never an execution error.
  - An unparseable taint value degrades to untainted (0).

Parameters: None
Supported on: Linux`
}

// Category classifies this tool under system.
func (t *Tool) Category() registry.Category { return registry.CategorySystem }

// Parameters returns nil — this tool takes no arguments.
func (t *Tool) Parameters() []registry.ToolParam { return nil }

// Hidden returns false — this tool is LLM-discoverable.
func (t *Tool) Hidden() bool { return false }

// IsSupported requires Linux; individual sources degrade at execution time.
func (t *Tool) IsSupported() (bool, string) {
	if !registry.IsLinux() {
		return false, "requires Linux"
	}
	return true, ""
}

// Execute gathers taint, lockdown, LSM, and vulnerability state.
func (t *Tool) Execute(ctx context.Context, _ json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	if err := ctx.Err(); err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("context canceled: %v", err)), nil
	}

	out := Output{
		Vulnerabilities: []Vulnerability{},
		WarningReasons:  []string{},
	}

	// Taint bitmask (degrades to untainted when missing or malformed).
	if raw, ok := readTrimmedFile(filepath.Join(t.procfsRoot, "sys", "kernel", "tainted")); ok {
		if value, err := strconv.ParseUint(raw, 10, 64); err == nil {
			out.TaintValue = value
			out.Tainted = value != 0
			out.TaintReasons = decodeTaint(value)
		}
	}

	// Lockdown mode (omitted when unsupported).
	if raw, err := os.ReadFile(filepath.Join(t.sysfsRoot, "kernel", "security", "lockdown")); err == nil {
		out.LockdownMode = parseLockdown(raw)
	}

	// SELinux.
	out.LSM.SELinux = "not_present"
	if raw, ok := readTrimmedFile(filepath.Join(t.sysfsRoot, "fs", "selinux", "enforce")); ok {
		if raw == "1" {
			out.LSM.SELinux = "enforcing"
		} else {
			out.LSM.SELinux = "permissive"
		}
	}

	// AppArmor.
	out.LSM.AppArmor = "not_present"
	if raw, ok := readTrimmedFile(filepath.Join(t.sysfsRoot, "module", "apparmor", "parameters", "enabled")); ok {
		if strings.EqualFold(raw, "Y") {
			out.LSM.AppArmor = "enabled"
		} else {
			out.LSM.AppArmor = "disabled"
		}
	}

	// CPU vulnerabilities.
	vulnDir := filepath.Join(t.sysfsRoot, "devices", "system", "cpu", "vulnerabilities")
	out.Vulnerabilities, out.VulnerableCount = collectVulnerabilities(vulnDir)

	// Warnings: unmitigated vulnerabilities.
	if out.VulnerableCount > 0 {
		var names []string
		for _, v := range out.Vulnerabilities {
			if v.State == "vulnerable" {
				names = append(names, v.Name)
			}
		}
		out.WarningReasons = append(out.WarningReasons, fmt.Sprintf(
			"%d CPU vulnerabilities lack mitigation: %s", out.VulnerableCount, strings.Join(names, ", ")))
	}

	// Warnings: trust-relevant taint bits (P/F/E).
	for bit := range warnTaintBits {
		if out.TaintValue&(1<<uint(bit)) != 0 {
			out.WarningReasons = append(out.WarningReasons, fmt.Sprintf(
				"kernel tainted: %s (%s)", taintBits[bit].Reason, taintBits[bit].Letter))
		}
	}
	sort.Strings(out.WarningReasons)

	status := registry.StatusOK
	summary := fmt.Sprintf("Kernel security: tainted=%v, lockdown=%s, selinux=%s, apparmor=%s, %d/%d vulnerabilities unmitigated",
		out.Tainted, orUnavailable(out.LockdownMode), out.LSM.SELinux, out.LSM.AppArmor, out.VulnerableCount, len(out.Vulnerabilities))
	if len(out.WarningReasons) > 0 {
		status = registry.StatusWarning
		summary += fmt.Sprintf(" — %d warnings: %s", len(out.WarningReasons), strings.Join(out.WarningReasons, "; "))
	}

	result := registry.NewResult(t.Name(), status, summary, out)
	result.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	result.Metadata.FilteringMethod = "deterministic"
	return result, nil
}

// decodeTaint expands the kernel taint bitmask into human-readable reasons.
// Bits beyond the documented range decode as "bit N: unknown taint flag".
func decodeTaint(value uint64) []string {
	var reasons []string
	for bit := 0; bit < 64; bit++ {
		if value&(1<<uint(bit)) == 0 {
			continue
		}
		if bit < len(taintBits) {
			reasons = append(reasons, fmt.Sprintf("%s: %s", taintBits[bit].Letter, taintBits[bit].Reason))
		} else {
			reasons = append(reasons, fmt.Sprintf("bit %d: unknown taint flag", bit))
		}
	}
	return reasons
}

// parseLockdown extracts the active (bracketed) mode from a lockdown file,
// e.g. "none [integrity] confidentiality" => "integrity".
func parseLockdown(raw []byte) string {
	for _, field := range strings.Fields(string(raw)) {
		if strings.HasPrefix(field, "[") && strings.HasSuffix(field, "]") {
			return strings.Trim(field, "[]")
		}
	}
	return "unknown"
}

// classifyVulnerability maps a raw vulnerability status string to
// not_affected / mitigated / vulnerable / unknown. Some entries carry a
// context prefix (e.g. "KVM: Mitigation: ..."), so matching is by substring
// precedence: "Vulnerable" wins over "Mitigation" only when it appears first.
func classifyVulnerability(status string) string {
	if strings.HasPrefix(status, "Not affected") {
		return "not_affected"
	}
	vulnIdx := strings.Index(status, "Vulnerable")
	mitIdx := strings.Index(status, "Mitigation")
	switch {
	case vulnIdx >= 0 && (mitIdx < 0 || vulnIdx < mitIdx):
		return "vulnerable"
	case mitIdx >= 0:
		return "mitigated"
	default:
		return "unknown"
	}
}

// collectVulnerabilities reads every file in the sysfs vulnerabilities
// directory. Returns the decoded entries (sorted by name) and the number
// classified as vulnerable. A missing directory yields an empty list.
func collectVulnerabilities(dir string) ([]Vulnerability, int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []Vulnerability{}, 0
	}
	vulns := make([]Vulnerability, 0, len(entries))
	vulnerable := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		raw, ok := readTrimmedFile(filepath.Join(dir, e.Name()))
		if !ok {
			continue
		}
		state := classifyVulnerability(raw)
		if state == "vulnerable" {
			vulnerable++
		}
		vulns = append(vulns, Vulnerability{Name: e.Name(), Status: raw, State: state})
	}
	sort.Slice(vulns, func(i, j int) bool { return vulns[i].Name < vulns[j].Name })
	return vulns, vulnerable
}

// readTrimmedFile reads a small file and returns its whitespace-trimmed
// content; ok is false when the file cannot be read.
func readTrimmedFile(path string) (string, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(raw)), true
}

// orUnavailable renders empty optional strings as "not_available" in the
// one-line summary.
func orUnavailable(s string) string {
	if s == "" {
		return "not_available"
	}
	return s
}
