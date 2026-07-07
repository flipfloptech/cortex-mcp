package listeningservices

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
	tool := writeFixture(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.IsSupported()
	}
}

func BenchmarkExecute(b *testing.B) {
	tool := writeFixture(b)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, nil)
	}
}

func BenchmarkCollectSockets(b *testing.B) {
	tool := writeFixture(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.collectSockets()
	}
}

func BenchmarkBuildInodePIDMap(b *testing.B) {
	tool := writeFixture(b)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = tool.buildInodePIDMap(ctx)
	}
}

func BenchmarkReadComm(b *testing.B) {
	tool := writeFixture(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.readComm(1234)
	}
}

func BenchmarkReadCmdline(b *testing.B) {
	tool := writeFixture(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tool.readCmdline(1234)
	}
}

func BenchmarkParseSocketFile(b *testing.B) {
	content := []byte(fixtureTCP)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseSocketFile(content, "tcp")
	}
}

func BenchmarkParseSocketLine(b *testing.B) {
	line := "   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 10001 1 0000000000000000 100 0 0 10 0"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = parseSocketLine(line, "tcp")
	}
}

func BenchmarkParseHexIPv4(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = parseHexIPv4("0100007F")
	}
}

func BenchmarkParseHexIPv6(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = parseHexIPv6("000080FE000000000000000001000000")
	}
}

func BenchmarkParseHexPort(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = parseHexPort("1F90")
	}
}
