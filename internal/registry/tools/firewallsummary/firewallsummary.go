// Package firewallsummary implements the get_firewall_summary diagnostic
// tool.
//
// It summarizes the host packet-filter state (tables, chains, policies,
// rule counts, and traffic counters) using 'nft -j list ruleset' as the
// primary source and 'iptables-save -c' / 'iptables -S' as fallbacks.
package firewallsummary

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

// Package-level injection points. Tests and benchmarks override the copies
// held by each Tool instance; production code uses these defaults.
var (
	execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	}
	execLookPath = exec.LookPath
)

// maxRenderedRules caps the number of individual rules rendered when a
// table+chain filter is requested, keeping LLM-facing output bounded.
const maxRenderedRules = 100

// Args are the optional tool parameters.
type Args struct {
	Table string `json:"table"`
	Chain string `json:"chain"`
}

// ChainSummary describes one chain within a firewall table.
//
// Packets/Bytes semantics differ by backend: for iptables they are the
// chain policy counters from ':CHAIN POLICY [pkts:bytes]'; for nftables
// (which has no per-chain counters) they are the sum of anonymous counter
// expressions across the chain's rules.
type ChainSummary struct {
	Name      string `json:"name"`
	Policy    string `json:"policy,omitempty"`
	RuleCount int    `json:"rule_count"`
	Packets   uint64 `json:"packets"`
	Bytes     uint64 `json:"bytes"`
}

// TableSummary describes one firewall table and its chains.
type TableSummary struct {
	Name   string         `json:"name"`
	Family string         `json:"family"`
	Chains []ChainSummary `json:"chains"`
}

// Output is the tool-specific data payload.
type Output struct {
	Backend             string         `json:"backend"`
	FirewallActive      bool           `json:"firewall_active"`
	TotalRules          int            `json:"total_rules"`
	Tables              []TableSummary `json:"tables"`
	RequestedChain      string         `json:"requested_chain,omitempty"`
	RequestedChainFound *bool          `json:"requested_chain_found,omitempty"`
	Rules               []string       `json:"rules,omitempty"`
	RulesTruncated      bool           `json:"rules_truncated,omitempty"`
	WarningReasons      []string       `json:"warning_reasons,omitempty"`
}

// Tool implements registry.Tool for get_firewall_summary.
type Tool struct {
	execCommand  func(ctx context.Context, name string, args ...string) ([]byte, error)
	execLookPath func(file string) (string, error)
}

// New constructs the tool with the package-level defaults.
func New() *Tool {
	return &Tool{
		execCommand:  execCommand,
		execLookPath: execLookPath,
	}
}

func init() {
	registry.Register(New())
}

func (t *Tool) Name() string {
	return "get_firewall_summary"
}

func (t *Tool) Description() string {
	return "Summarize firewall state: backend, tables, chains with policies, rule counts, and traffic counters."
}

func (t *Tool) Help() string {
	return `Summarizes the host packet-filter configuration without dumping the full
ruleset: which backend is active, every table and chain with its policy,
per-chain rule counts, and packet/byte counters. A factual tool — it reports
state and emits no warnings by default.

Data Sources:
- Primary: 'nft -j list ruleset' (nftables JSON: table/chain/rule objects)
- Fallback: 'iptables-save -c' text output (*table lines, ':CHAIN POLICY
  [pkts:bytes]' policy counters, -A rule lines)
- Last resort: 'iptables -S' (filter table only, no counters)

Parameters:
- table: Optional string. Firewall table name (e.g. 'filter', 'nat').
- chain: Optional string. Chain name (e.g. 'input', 'INPUT'). When BOTH
  table and chain are provided, the matching chain's individual rules are
  rendered compactly (capped at 100) in the 'rules' field. Names are
  case-sensitive: nftables chains are typically lowercase, iptables
  chains uppercase.

Output: {backend: nftables|iptables, firewall_active (any rules present),
total_rules, tables[] {name, family, chains[] {name, policy, rule_count,
packets, bytes}}, rules[]?, rules_truncated?}.

Caveats: packets/bytes are policy counters for iptables but the sum of
anonymous rule counter expressions for nftables (nftables chains carry no
implicit counters). Reading the ruleset requires root or passwordless sudo;
an empty ruleset is a valid result (firewall_active=false).`
}

func (t *Tool) Category() registry.Category {
	return registry.CategoryNetwork
}

func (t *Tool) Parameters() []registry.ToolParam {
	return []registry.ToolParam{
		{
			Name:        "table",
			Type:        "string",
			Description: "Optional: firewall table name (e.g. 'filter', 'nat'). Combine with 'chain' to render that chain's rules.",
			Required:    false,
		},
		{
			Name:        "chain",
			Type:        "string",
			Description: "Optional: chain name (e.g. 'input', 'INPUT'). Combine with 'table' to render that chain's rules (capped at 100).",
			Required:    false,
		},
	}
}

func (t *Tool) Hidden() bool {
	return false
}

func (t *Tool) IsSupported() (bool, string) {
	for _, bin := range []string{"nft", "iptables-save", "iptables"} {
		if _, err := t.execLookPath(bin); err == nil {
			return true, ""
		}
	}
	return false, "no firewall inspection binaries found (nft, iptables-save, iptables)"
}

func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	if err := ctx.Err(); err != nil {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("execution cancelled: %v", err)), nil
	}

	var parsed Args
	if len(args) > 0 {
		if err := json.Unmarshal(args, &parsed); err != nil {
			return registry.NewErrorResult(t.Name(), fmt.Sprintf("invalid arguments: %v", err)), nil
		}
	}

	_, nftErr := t.execLookPath("nft")
	_, saveErr := t.execLookPath("iptables-save")
	_, iptErr := t.execLookPath("iptables")
	hasNft, hasSave, hasIpt := nftErr == nil, saveErr == nil, iptErr == nil
	if !hasNft && !hasSave && !hasIpt {
		return registry.NewErrorResult(t.Name(),
			"no firewall inspection binaries found in $PATH (nft, iptables-save, iptables)"), nil
	}

	var (
		backend   string
		tables    []TableSummary
		total     int
		rules     []string
		truncated bool
		succeeded bool
		lastErr   error
	)

	if hasNft {
		out, err := t.runPrivileged(ctx, "nft", "-j", "list", "ruleset")
		if err == nil {
			var perr error
			tables, total, rules, truncated, perr = parseNftRuleset(out, parsed.Table, parsed.Chain)
			if perr == nil {
				backend = "nftables"
				succeeded = true
			} else {
				lastErr = perr
			}
		} else if isPermissionError(out, err) {
			return registry.NewErrorResult(t.Name(),
				"Unauthorized: root or passwordless sudo privileges required to read the firewall ruleset."), nil
		} else {
			lastErr = fmt.Errorf("'nft -j list ruleset' failed: %w", err)
		}
	}

	if !succeeded && (hasSave || hasIpt) {
		name, cmdArgs := "iptables-save", []string{"-c"}
		if !hasSave {
			name, cmdArgs = "iptables", []string{"-S"}
		}
		out, err := t.runPrivileged(ctx, name, cmdArgs...)
		if err == nil {
			var perr error
			tables, total, rules, truncated, perr = parseIptablesSave(out, parsed.Table, parsed.Chain)
			if perr == nil {
				backend = "iptables"
				succeeded = true
			} else {
				lastErr = perr
			}
		} else if isPermissionError(out, err) {
			return registry.NewErrorResult(t.Name(),
				"Unauthorized: root or passwordless sudo privileges required to read the firewall ruleset."), nil
		} else {
			lastErr = fmt.Errorf("'%s %s' failed: %w", name, strings.Join(cmdArgs, " "), err)
		}
	}

	if !succeeded {
		return registry.NewErrorResult(t.Name(), fmt.Sprintf("failed to read firewall state: %v", lastErr)), nil
	}

	out := Output{
		Backend:        backend,
		FirewallActive: total > 0,
		TotalRules:     total,
		Tables:         tables,
	}

	chainCount := 0
	for _, tb := range tables {
		chainCount += len(tb.Chains)
	}

	summary := fmt.Sprintf("Firewall (%s): %d table(s), %d chain(s), %d rule(s)", backend, len(tables), chainCount, total)
	if total == 0 {
		summary = fmt.Sprintf("Firewall (%s): no rules loaded (inactive)", backend)
	}

	if parsed.Table != "" && parsed.Chain != "" {
		out.RequestedChain = parsed.Table + "/" + parsed.Chain
		found := false
		for _, tb := range tables {
			if tb.Name != parsed.Table {
				continue
			}
			for _, c := range tb.Chains {
				if c.Name == parsed.Chain {
					found = true
					break
				}
			}
		}
		out.RequestedChainFound = &found
		out.Rules = rules
		out.RulesTruncated = truncated
		if found {
			summary += fmt.Sprintf("; rendered %d rule(s) from %s", len(out.Rules), out.RequestedChain)
		} else {
			summary += fmt.Sprintf("; chain %s not found", out.RequestedChain)
		}
	}

	res := registry.NewResult(t.Name(), registry.StatusOK, summary, out)
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()
	return res, nil
}

// runPrivileged executes a firewall inspection command, wrapping with
// 'sudo -n' when not running as root and passwordless sudo is available.
func (t *Tool) runPrivileged(ctx context.Context, name string, args ...string) ([]byte, error) {
	if os.Geteuid() != 0 {
		if _, err := t.execLookPath("sudo"); err == nil {
			return t.execCommand(ctx, "sudo", append([]string{"-n", name}, args...)...)
		}
	}
	return t.execCommand(ctx, name, args...)
}

// nftEnvelope mirrors the top-level 'nft -j list ruleset' JSON document.
type nftEnvelope struct {
	Nftables []nftObject `json:"nftables"`
}

// nftObject is one element of the nftables array. Only table/chain/rule
// objects are consumed; metainfo, sets, named counters etc. are ignored.
type nftObject struct {
	Table *nftTable `json:"table"`
	Chain *nftChain `json:"chain"`
	Rule  *nftRule  `json:"rule"`
}

type nftTable struct {
	Family string `json:"family"`
	Name   string `json:"name"`
}

type nftChain struct {
	Family string `json:"family"`
	Table  string `json:"table"`
	Name   string `json:"name"`
	Policy string `json:"policy"`
}

type nftRule struct {
	Family  string            `json:"family"`
	Table   string            `json:"table"`
	Chain   string            `json:"chain"`
	Handle  int64             `json:"handle"`
	Comment string            `json:"comment"`
	Expr    []json.RawMessage `json:"expr"`
}

// parseNftRuleset decodes 'nft -j list ruleset' JSON into table summaries.
// When both filterTable and filterChain are non-empty, rules of the matching
// chain are rendered compactly (capped at maxRenderedRules).
func parseNftRuleset(data []byte, filterTable, filterChain string) ([]TableSummary, int, []string, bool, error) {
	var env nftEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, 0, nil, false, fmt.Errorf("nft JSON output: %w", err)
	}

	var tables []TableSummary
	tableIdx := make(map[string]int)
	chainIdx := make(map[string]int)
	total := 0
	var rules []string
	truncated := false
	filterActive := filterTable != "" && filterChain != ""

	ensureTable := func(family, name string) int {
		key := family + "|" + name
		if i, ok := tableIdx[key]; ok {
			return i
		}
		tables = append(tables, TableSummary{Name: name, Family: family})
		tableIdx[key] = len(tables) - 1
		return len(tables) - 1
	}
	ensureChain := func(family, table, chain, policy string) (int, int) {
		ti := ensureTable(family, table)
		key := family + "|" + table + "|" + chain
		if ci, ok := chainIdx[key]; ok {
			if policy != "" {
				tables[ti].Chains[ci].Policy = policy
			}
			return ti, ci
		}
		tables[ti].Chains = append(tables[ti].Chains, ChainSummary{Name: chain, Policy: policy})
		chainIdx[key] = len(tables[ti].Chains) - 1
		return ti, len(tables[ti].Chains) - 1
	}

	for i := range env.Nftables {
		obj := &env.Nftables[i]
		switch {
		case obj.Table != nil:
			ensureTable(obj.Table.Family, obj.Table.Name)
		case obj.Chain != nil:
			ensureChain(obj.Chain.Family, obj.Chain.Table, obj.Chain.Name, obj.Chain.Policy)
		case obj.Rule != nil:
			r := obj.Rule
			ti, ci := ensureChain(r.Family, r.Table, r.Chain, "")
			tables[ti].Chains[ci].RuleCount++
			pkts, byts := extractRuleCounter(r.Expr)
			tables[ti].Chains[ci].Packets += pkts
			tables[ti].Chains[ci].Bytes += byts
			total++
			if filterActive && r.Table == filterTable && r.Chain == filterChain {
				if len(rules) < maxRenderedRules {
					rules = append(rules, renderNftRule(r))
				} else {
					truncated = true
				}
			}
		}
	}

	return tables, total, rules, truncated, nil
}

// extractRuleCounter sums anonymous counter expressions
// ({"counter": {"packets": N, "bytes": N}}) within a rule's expr array.
// Named counter references ({"counter": "name"}) are ignored.
func extractRuleCounter(expr []json.RawMessage) (uint64, uint64) {
	var packets, byteCount uint64
	for _, e := range expr {
		var wrapper struct {
			Counter *struct {
				Packets uint64 `json:"packets"`
				Bytes   uint64 `json:"bytes"`
			} `json:"counter"`
		}
		if err := json.Unmarshal(e, &wrapper); err != nil || wrapper.Counter == nil {
			continue
		}
		packets += wrapper.Counter.Packets
		byteCount += wrapper.Counter.Bytes
	}
	return packets, byteCount
}

// renderNftRule renders one nftables rule as a compact single-line string.
func renderNftRule(r *nftRule) string {
	exprStr := ""
	if raw, err := json.Marshal(r.Expr); err == nil {
		exprStr = compactJSON(raw)
	}
	s := fmt.Sprintf("%s %s %s handle %d: %s", r.Family, r.Table, r.Chain, r.Handle, exprStr)
	if r.Comment != "" {
		s += fmt.Sprintf(" comment %q", r.Comment)
	}
	return s
}

// compactJSON compacts a JSON fragment; invalid JSON falls back to the
// whitespace-trimmed raw text.
func compactJSON(raw []byte) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return strings.TrimSpace(string(raw))
	}
	return buf.String()
}

// parseIptablesSave parses 'iptables-save -c' output. It also tolerates
// 'iptables -S' output (-P/-N/-A lines without a *table header, mapped to
// an implicit 'filter' table). The error return is reserved for symmetry
// with parseNftRuleset; the text format is parsed leniently.
func parseIptablesSave(data []byte, filterTable, filterChain string) ([]TableSummary, int, []string, bool, error) {
	var tables []TableSummary
	tableIdx := make(map[string]int)
	chainIdx := make(map[string]int)
	total := 0
	var rules []string
	truncated := false
	filterActive := filterTable != "" && filterChain != ""
	currentTable := ""

	ensureTable := func(name string) int {
		if i, ok := tableIdx[name]; ok {
			return i
		}
		tables = append(tables, TableSummary{Name: name, Family: "ip"})
		tableIdx[name] = len(tables) - 1
		return len(tables) - 1
	}
	ensureChain := func(table, chain, policy string) (int, int) {
		ti := ensureTable(table)
		key := table + "|" + chain
		if ci, ok := chainIdx[key]; ok {
			if policy != "" {
				tables[ti].Chains[ci].Policy = policy
			}
			return ti, ci
		}
		tables[ti].Chains = append(tables[ti].Chains, ChainSummary{Name: chain, Policy: policy})
		chainIdx[key] = len(tables[ti].Chains) - 1
		return ti, len(tables[ti].Chains) - 1
	}

	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || line == "COMMIT" {
			continue
		}

		switch {
		case strings.HasPrefix(line, "*"):
			currentTable = strings.TrimSpace(line[1:])
			ensureTable(currentTable)

		case strings.HasPrefix(line, ":"):
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			name := strings.TrimPrefix(fields[0], ":")
			policy := fields[1]
			if policy == "-" {
				policy = "" // user-defined chain, no policy
			}
			if currentTable == "" {
				currentTable = "filter"
			}
			ti, ci := ensureChain(currentTable, name, policy)
			if len(fields) >= 3 {
				pkts, byts, _ := splitCounterPrefix(fields[2])
				tables[ti].Chains[ci].Packets = pkts
				tables[ti].Chains[ci].Bytes = byts
			}

		default:
			_, _, rest := splitCounterPrefix(line)
			fields := strings.Fields(rest)
			if len(fields) < 2 {
				continue
			}
			if currentTable == "" {
				currentTable = "filter"
			}
			switch fields[0] {
			case "-P": // iptables -S policy line
				policy := ""
				if len(fields) >= 3 {
					policy = fields[2]
				}
				ensureChain(currentTable, fields[1], policy)
			case "-N": // iptables -S user chain declaration
				ensureChain(currentTable, fields[1], "")
			case "-A", "-I":
				ti, ci := ensureChain(currentTable, fields[1], "")
				tables[ti].Chains[ci].RuleCount++
				total++
				if filterActive && currentTable == filterTable && fields[1] == filterChain {
					if len(rules) < maxRenderedRules {
						rules = append(rules, rest)
					} else {
						truncated = true
					}
				}
			}
		}
	}

	return tables, total, rules, truncated, nil
}

// splitCounterPrefix strips a leading '[pkts:bytes]' counter token (as
// produced by 'iptables-save -c') from a line, returning the decoded
// counters and the remainder. Lines without a counter prefix are returned
// unchanged with zero counters.
func splitCounterPrefix(line string) (uint64, uint64, string) {
	if !strings.HasPrefix(line, "[") {
		return 0, 0, line
	}
	end := strings.Index(line, "]")
	if end < 0 {
		return 0, 0, line
	}
	rest := strings.TrimSpace(line[end+1:])
	pStr, bStr, ok := strings.Cut(line[1:end], ":")
	if !ok {
		return 0, 0, rest
	}
	pkts, err1 := strconv.ParseUint(pStr, 10, 64)
	byts, err2 := strconv.ParseUint(bStr, 10, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, rest
	}
	return pkts, byts, rest
}

// isPermissionError classifies command failures caused by missing
// root / passwordless-sudo privileges.
func isPermissionError(out []byte, err error) bool {
	combined := strings.ToLower(string(out))
	if err != nil {
		combined += " " + strings.ToLower(err.Error())
	}
	return strings.Contains(combined, "permission denied") ||
		strings.Contains(combined, "operation not permitted") ||
		strings.Contains(combined, "password is required")
}
