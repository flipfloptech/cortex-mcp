package routingrules

import (
	"context"
	"testing"
)

func BenchmarkNew(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = New()
	}
}

func BenchmarkName(b *testing.B) {
	t := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Name()
	}
}

func BenchmarkDescription(b *testing.B) {
	t := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Description()
	}
}

func BenchmarkHelp(b *testing.B) {
	t := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Help()
	}
}

func BenchmarkCategory(b *testing.B) {
	t := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Category()
	}
}

func BenchmarkParameters(b *testing.B) {
	t := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Parameters()
	}
}

func BenchmarkHidden(b *testing.B) {
	t := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.Hidden()
	}
}

func BenchmarkIsSupported(b *testing.B) {
	t := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = t.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	t := New()
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = t.Execute(ctx, nil)
	}
}

func BenchmarkParseIpRuleJSON(b *testing.B) {
	content := []byte(`[
		{"priority":0,"src":"all","table":"local"},
		{"priority":48,"src":"10.2.0.11","table":"302","proto":"static"},
		{"priority":32766,"src":"all","table":"main"}
	]`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = parseIpRuleJSON(content)
	}
}

func BenchmarkParseIpRuleText(b *testing.B) {
	content := []byte(`0:      from all lookup local
48:     from 10.2.0.11 lookup 302 proto static
49:     from 10.1.0.11 lookup 301 proto static
32766:  from all lookup main
`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseIpRuleText(content)
	}
}
