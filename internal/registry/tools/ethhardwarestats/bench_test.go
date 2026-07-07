package ethhardwarestats

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

func BenchmarkParseRingParams(b *testing.B) {
	content := []byte(`Ring parameters for eth0:
Pre-set maximums:
RX:		4096
TX:		4096
Current hardware settings:
RX:		512
TX:		512
`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseRingParams(content)
	}
}

func BenchmarkParseCoalesceParams(b *testing.B) {
	content := []byte(`Coalesce parameters for eth0:
Adaptive RX: on  TX: on
stats-block-usecs: 0
sample-interval: 0
pkt-rate-low: 0
pkt-rate-high: 0

rx-usecs: 20
rx-frames: 10
rx-usecs-irq: 0
rx-frames-irq: 0

tx-usecs: 20
tx-frames: 10
tx-usecs-irq: 0
tx-frames-irq: 0
`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseCoalesceParams(content)
	}
}

func BenchmarkParseDriverStats(b *testing.B) {
	content := []byte(`NIC statistics:
     rx_packets: 4300229
     rx_bytes: 4894518924
     rx_dropped: 167
`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseDriverStats(content)
	}
}
