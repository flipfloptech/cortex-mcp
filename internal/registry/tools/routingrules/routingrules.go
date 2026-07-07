package routingrules

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

type RoutingRule struct {
	Priority int    `json:"priority"`
	Src      string `json:"src,omitempty"`
	Dst      string `json:"dst,omitempty"`
	Table    string `json:"table,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Iif      string `json:"iif,omitempty"`
	Oif      string `json:"oif,omitempty"`
	Fwmark   string `json:"fwmark,omitempty"`
	Tos      string `json:"tos,omitempty"`
}

type RoutingRulesData struct {
	Rules []RoutingRule `json:"rules"`
}

type RoutingRulesTool struct {
	execCommand func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func New() *RoutingRulesTool {
	return &RoutingRulesTool{
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		},
	}
}

func init() {
	registry.Register(registry.WithCache(5*time.Second, New()))
}

func (t *RoutingRulesTool) Name() string {
	return "get_routing_rules"
}

func (t *RoutingRulesTool) Category() registry.Category {
	return registry.CategoryNetwork
}

func (t *RoutingRulesTool) Help() string {
	return `Query the system Routing Policy Database (RPDB) rules (i.e. policy routing).

Exposes active rules determining route table lookup priorities, matching on source/destination IPs, interfaces, firewall marks, and target tables.

Data Sources:
- ip -j rule (primary JSON execution)
- ip rule (fallback plain text parsing)`
}

func (t *RoutingRulesTool) Description() string {
	return "Active policy routing rules (ip rules)"
}

func (t *RoutingRulesTool) Parameters() []registry.ToolParam {
	return nil
}

func (t *RoutingRulesTool) Hidden() bool { return false }

func (t *RoutingRulesTool) IsSupported() (bool, string) {
	_, err := exec.LookPath("ip")
	if err != nil {
		return false, "Routing rules not supported (missing ip binary)"
	}
	return true, ""
}

func (t *RoutingRulesTool) Execute(ctx context.Context, args json.RawMessage) (*registry.ToolResult, error) {
	start := time.Now()

	var rules []RoutingRule
	var parseErr error

	// 1. Try JSON output of ip -j rule
	output, err := t.execCommand(ctx, "ip", "-j", "rule")
	if err == nil {
		rules, parseErr = parseIpRuleJSON(output)
	}

	// 2. Fall back to plain text ip rule
	if err != nil || parseErr != nil {
		textOutput, textErr := t.execCommand(ctx, "ip", "rule")
		if textErr == nil {
			rules = parseIpRuleText(textOutput)
		}
	}

	summaryStr := fmt.Sprintf("Routing Policy Database: %d policy routing rules active", len(rules))
	data := RoutingRulesData{Rules: rules}

	res := registry.NewResult(t.Name(), registry.StatusOK, summaryStr, data)
	res.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()

	return res, nil
}

type rawIpRule struct {
	Priority int    `json:"priority"`
	Src      string `json:"src"`
	Dst      string `json:"dst"`
	Table    any    `json:"table"` // can be string or float64 in JSON
	Proto    string `json:"proto"`
	Iif      string `json:"iif"`
	Oif      string `json:"oif"`
	Fwmark   string `json:"fwmark"`
	Tos      string `json:"tos"`
}

func parseIpRuleJSON(data []byte) ([]RoutingRule, error) {
	var raw []rawIpRule
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	var list []RoutingRule
	for _, r := range raw {
		tableStr := ""
		if r.Table != nil {
			switch v := r.Table.(type) {
			case string:
				tableStr = v
			case float64:
				tableStr = strconv.FormatFloat(v, 'f', -1, 64)
			}
		}

		list = append(list, RoutingRule{
			Priority: r.Priority,
			Src:      r.Src,
			Dst:      r.Dst,
			Table:    tableStr,
			Protocol: r.Proto,
			Iif:      r.Iif,
			Oif:      r.Oif,
			Fwmark:   r.Fwmark,
			Tos:      r.Tos,
		})
	}
	return list, nil
}

func parseIpRuleText(data []byte) []RoutingRule {
	var list []RoutingRule
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Example line: "48:     from 10.2.0.11 lookup 302 proto static"
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}

		prio, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil {
			continue
		}

		rule := RoutingRule{Priority: prio}
		tokens := strings.Fields(parts[1])
		for i := 0; i < len(tokens); i++ {
			switch tokens[i] {
			case "from":
				if i+1 < len(tokens) {
					rule.Src = tokens[i+1]
					i++
				}
			case "to":
				if i+1 < len(tokens) {
					rule.Dst = tokens[i+1]
					i++
				}
			case "lookup", "table":
				if i+1 < len(tokens) {
					rule.Table = tokens[i+1]
					i++
				}
			case "proto":
				if i+1 < len(tokens) {
					rule.Protocol = tokens[i+1]
					i++
				}
			case "iif":
				if i+1 < len(tokens) {
					rule.Iif = tokens[i+1]
					i++
				}
			case "oif":
				if i+1 < len(tokens) {
					rule.Oif = tokens[i+1]
					i++
				}
			case "fwmark":
				if i+1 < len(tokens) {
					rule.Fwmark = tokens[i+1]
					i++
				}
			case "tos":
				if i+1 < len(tokens) {
					rule.Tos = tokens[i+1]
					i++
				}
			}
		}

		list = append(list, rule)
	}
	return list
}
