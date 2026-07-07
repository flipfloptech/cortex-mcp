package networkthroughput

import (
	"context"
	"encoding/json"
	"testing"
	"time"
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
	args := json.RawMessage(`{"sample_duration_ms": 10}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.Execute(ctx, args)
	}
}

func BenchmarkParseArgs(b *testing.B) {
	args := json.RawMessage(`{"sample_duration_ms": 250}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = parseArgs(args)
	}
}

func BenchmarkParseNetDev(b *testing.B) {
	content := []byte(fixtureNetDev)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseNetDev(content)
	}
}

func BenchmarkReadSnapshot(b *testing.B) {
	tool := writeFixture(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.readSnapshot()
	}
}

func BenchmarkReadLinkSpeed(b *testing.B) {
	tool := writeFixture(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = tool.readLinkSpeed("eth0")
	}
}

func BenchmarkComputeRates(b *testing.B) {
	first := map[string]ifCounters{
		"eth0": {rxBytes: 1000, rxPackets: 10},
		"eth1": {rxBytes: 2000, rxPackets: 20},
	}
	second := map[string]ifCounters{
		"eth0": {rxBytes: 1251000, rxPackets: 1010, txBytes: 625000, txPackets: 505},
		"eth1": {rxBytes: 802000, rxPackets: 620, txBytes: 400000, txPackets: 300},
	}
	speeds := map[string]float64{"eth0": 1000}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = computeRates(first, second, 500*time.Millisecond, speeds)
	}
}

func BenchmarkBuildWarnings(b *testing.B) {
	speed := 1000.0
	util := 95.0
	ifaces := []InterfaceRate{
		{Name: "eth0", RxMbps: 950, RxDropsPerS: 2, LinkSpeedMbps: &speed, UtilizationPct: &util},
		{Name: "eth1", RxMbps: 1},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buildWarnings(ifaces)
	}
}

func BenchmarkBuildSummary(b *testing.B) {
	ifaces := []InterfaceRate{
		{Name: "eth0", RxMbps: 10.5, TxMbps: 2.25},
		{Name: "eth1", RxMbps: 0.25, TxMbps: 40.0},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buildSummary(ifaces)
	}
}

func BenchmarkWrapped(b *testing.B) {
	first := ifCounters{rxBytes: 5000, rxPackets: 50, txBytes: 100, txPackets: 1}
	second := ifCounters{rxBytes: 400, rxPackets: 60, txBytes: 200, txPackets: 2}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = first.wrapped(second)
	}
}

func BenchmarkRound1(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = round1(2.333333)
	}
}

func BenchmarkRound2(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = round2(0.098765)
	}
}
