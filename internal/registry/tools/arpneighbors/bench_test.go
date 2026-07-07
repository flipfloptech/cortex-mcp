package arpneighbors

import (
	"context"
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

func BenchmarkHidden(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Hidden()
	}
}

func BenchmarkParameters(b *testing.B) {
	tool := New()
	for i := 0; i < b.N; i++ {
		_ = tool.Parameters()
	}
}

func BenchmarkIsSupported(b *testing.B) {
	tool := newProcfsOnlyTool(writeArpFile(b, procArpContent))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	tool := newEnrichedTool(writeArpFile(b, procArpContent), []byte(ipNeighJSON), nil)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, nil)
	}
}

func BenchmarkParseProcArp(b *testing.B) {
	content := []byte(procArpContent)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseProcArp(content)
	}
}

func BenchmarkFlagsToState(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = flagsToState("0x2")
	}
}

func BenchmarkParseIPNeighJSON(b *testing.B) {
	payload := []byte(ipNeighJSON)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = parseIPNeighJSON(payload)
	}
}

func BenchmarkMergeNeighbors(b *testing.B) {
	base := parseProcArp([]byte(procArpContent))
	extra, _ := parseIPNeighJSON([]byte(ipNeighJSON))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = mergeNeighbors(base, extra)
	}
}

func BenchmarkStatePriority(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = statePriority("failed")
	}
}
