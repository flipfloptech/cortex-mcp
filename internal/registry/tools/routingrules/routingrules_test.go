package routingrules

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry"
)

func TestRoutingRulesTool_ContractCompliance(t *testing.T) {
	t.Parallel()
	tool := New()

	var _ registry.Tool = tool

	if tool.Name() != "get_routing_rules" {
		t.Errorf("expected Name() == 'get_routing_rules', got %q", tool.Name())
	}
	if tool.Category() != "network" {
		t.Errorf("expected Category() == 'network', got %q", tool.Category())
	}
	if tool.Parameters() != nil {
		t.Errorf("expected Parameters() to be nil")
	}
}

func TestParseIpRuleJSON(t *testing.T) {
	t.Parallel()

	input := `[
		{"priority":0,"src":"all","table":"local"},
		{"priority":48,"src":"10.2.0.11","table":"302","proto":"static"},
		{"priority":32766,"src":"all","table":"main"}
	]`

	rules, err := parseIpRuleJSON([]byte(input))
	if err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if len(rules) != 3 {
		t.Fatalf("expected 3 rules, got %d", len(rules))
	}

	if rules[0].Priority != 0 || rules[0].Src != "all" || rules[0].Table != "local" {
		t.Errorf("unexpected rule 0: %+v", rules[0])
	}

	if rules[1].Priority != 48 || rules[1].Src != "10.2.0.11" || rules[1].Table != "302" || rules[1].Protocol != "static" {
		t.Errorf("unexpected rule 1: %+v", rules[1])
	}
}

func TestParseIpRuleText(t *testing.T) {
	t.Parallel()

	input := `0:      from all lookup local
48:     from 10.2.0.11 lookup 302 proto static
49:     from 10.1.0.11 lookup 301 proto static
32766:  from all lookup main
`

	rules := parseIpRuleText([]byte(input))
	if len(rules) != 4 {
		t.Fatalf("expected 4 rules, got %d", len(rules))
	}

	if rules[0].Priority != 0 || rules[0].Src != "all" || rules[0].Table != "local" {
		t.Errorf("unexpected rule 0: %+v", rules[0])
	}

	if rules[1].Priority != 48 || rules[1].Src != "10.2.0.11" || rules[1].Table != "302" || rules[1].Protocol != "static" {
		t.Errorf("unexpected rule 1: %+v", rules[1])
	}

	if rules[2].Priority != 49 || rules[2].Src != "10.1.0.11" || rules[2].Table != "301" {
		t.Errorf("unexpected rule 2: %+v", rules[2])
	}
}

func TestRoutingRulesTool_Execute(t *testing.T) {
	t.Parallel()

	tool := &RoutingRulesTool{
		execCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if len(args) >= 2 && args[0] == "-j" {
				return []byte(`[
					{"priority":0,"src":"all","table":"local"},
					{"priority":32766,"src":"all","table":"main"}
				]`), nil
			}
			return nil, errors.New("command failed")
		},
	}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.Status != registry.StatusOK {
		t.Fatalf("expected status OK, got %s. Summary: %s", res.Status, res.Summary)
	}

	var data RoutingRulesData
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("failed to unmarshal output: %v", err)
	}

	if len(data.Rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(data.Rules))
	}

	if data.Rules[0].Priority != 0 || data.Rules[1].Priority != 32766 {
		t.Errorf("unexpected rule results: %+v", data.Rules)
	}
}
