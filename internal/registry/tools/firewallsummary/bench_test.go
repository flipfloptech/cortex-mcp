package firewallsummary

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

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
	tool := New()
	tool.execLookPath = mockLookPath("nft")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	tool := mockTool(b, map[string]string{"nft": nftFixture}, "nft")
	ctx := context.Background()
	args := json.RawMessage(`{}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, args)
	}
}

func BenchmarkRunPrivileged(b *testing.B) {
	tool := New()
	tool.execLookPath = mockLookPath()
	tool.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("ok"), nil
	}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.runPrivileged(ctx, "nft", "-j", "list", "ruleset")
	}
}

func BenchmarkParseNftRuleset(b *testing.B) {
	data := []byte(nftFixture)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _, _, _ = parseNftRuleset(data, "", "")
	}
}

func BenchmarkExtractRuleCounter(b *testing.B) {
	var expr []json.RawMessage
	if err := json.Unmarshal([]byte(`[{"counter": {"packets": 5, "bytes": 300}}, {"accept": null}]`), &expr); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = extractRuleCounter(expr)
	}
}

func BenchmarkRenderNftRule(b *testing.B) {
	var expr []json.RawMessage
	if err := json.Unmarshal([]byte(`[{"counter": {"packets": 5, "bytes": 300}}, {"accept": null}]`), &expr); err != nil {
		b.Fatal(err)
	}
	rule := &nftRule{Family: "inet", Table: "filter", Chain: "input", Handle: 4, Comment: "c", Expr: expr}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = renderNftRule(rule)
	}
}

func BenchmarkCompactJSON(b *testing.B) {
	data := []byte("{\n  \"a\": 1,\n  \"b\": [1, 2]\n}")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = compactJSON(data)
	}
}

func BenchmarkParseIptablesSave(b *testing.B) {
	data := []byte(iptablesSaveFixture)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _, _, _ = parseIptablesSave(data, "", "")
	}
}

func BenchmarkSplitCounterPrefix(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _, _ = splitCounterPrefix("[125:7544] -A INPUT -i lo -j ACCEPT")
	}
}

func BenchmarkIsPermissionError(b *testing.B) {
	out := []byte("nft: netlink: Operation not permitted")
	err := errors.New("exit status 1")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = isPermissionError(out, err)
	}
}
