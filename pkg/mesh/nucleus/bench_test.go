package nucleus

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// --- ImpedanceCalculator Benchmarks ---

func BenchmarkNewImpedanceCalculator(b *testing.B) {
	m := NewManifest("node-a")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewImpedanceCalculator(m)
	}
}

func BenchmarkWithMetricSource(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = WithMetricSource(&defaultMetricSource{})
	}
}

func BenchmarkCalculate(b *testing.B) {
	m := NewManifest("node-a")
	calc := NewImpedanceCalculator(m)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = calc.Calculate()
	}
}

func BenchmarkUpdate(b *testing.B) {
	m := NewManifest("node-a")
	calc := NewImpedanceCalculator(m)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		calc.Update()
	}
}

func BenchmarkAddStreams(b *testing.B) {
	m := NewManifest("node-a")
	calc := NewImpedanceCalculator(m)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		calc.AddStreams(1)
	}
}

func BenchmarkRemoveStreams(b *testing.B) {
	m := NewManifest("node-a")
	calc := NewImpedanceCalculator(m)
	calc.AddStreams(b.N)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		calc.RemoveStreams(1)
	}
}

func BenchmarkStart(b *testing.B) {
	m := NewManifest("node-a")
	calc := NewImpedanceCalculator(m)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		calc.Start(ctx, 1*time.Hour)
	}
}

func BenchmarkClampNorm(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = clampNorm(0.5)
	}
}

func BenchmarkCPUPercent(b *testing.B) {
	src := &defaultMetricSource{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = src.CPUPercent()
	}
}

func BenchmarkMemoryPercent(b *testing.B) {
	src := &defaultMetricSource{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = src.MemoryPercent()
	}
}

// --- Manifest Benchmarks ---

func BenchmarkNewManifest(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = NewManifest("node-a")
	}
}

func BenchmarkNodeID(b *testing.B) {
	m := NewManifest("node-a")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.NodeID()
	}
}

func BenchmarkImpedance(b *testing.B) {
	m := NewManifest("node-a")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.Impedance()
	}
}

func BenchmarkSetImpedance(b *testing.B) {
	m := NewManifest("node-a")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.SetImpedance(50.0)
	}
}

func BenchmarkSetAddresses(b *testing.B) {
	m := NewManifest("node-a")
	addrs := []string{"192.168.1.1:8080", "10.0.0.1:9090"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.SetAddresses(addrs)
	}
}

func BenchmarkRegisterCapability(b *testing.B) {
	m := NewManifest("node-a")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.RegisterCapability("test.capability")
	}
}

func BenchmarkHasCapability(b *testing.B) {
	m := NewManifest("node-a")
	m.RegisterCapability("test.capability")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.HasCapability("test.capability")
	}
}

func BenchmarkCapabilities(b *testing.B) {
	m := NewManifest("node-a")
	m.RegisterCapability("test.capability1")
	m.RegisterCapability("test.capability2")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.Capabilities()
	}
}

func BenchmarkSnapshot(b *testing.B) {
	m := NewManifest("node-a")
	m.SetAddresses([]string{"127.0.0.1:8080"})
	m.RegisterCapability("test.cap")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.Snapshot()
	}
}

// --- Resolver Benchmarks ---

func BenchmarkNewResolver(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = NewResolver()
	}
}

func BenchmarkAddEntry(b *testing.B) {
	r := NewResolver()
	addrs := []string{"127.0.0.1:8080"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.AddEntry("node-a", addrs, 10*time.Second)
	}
}

func BenchmarkAddEntryWithTransport(b *testing.B) {
	r := NewResolver()
	addrs := []string{"127.0.0.1:8080"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.AddEntryWithTransport("node-a", addrs, 10*time.Second, "tls")
	}
}

func BenchmarkAddEntryFull(b *testing.B) {
	r := NewResolver()
	addrs := []string{"127.0.0.1:8080"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.AddEntryFull("node-a", addrs, 10*time.Second, "tls", "")
	}
}

func BenchmarkResolve(b *testing.B) {
	r := NewResolver()
	r.AddEntry("node-a", []string{"127.0.0.1:8080"}, 10*time.Second)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.Resolve("node-a")
	}
}

func BenchmarkAllEntries(b *testing.B) {
	r := NewResolver()
	r.AddEntry("node-a", []string{"127.0.0.1:8080"}, 10*time.Second)
	r.AddEntry("node-b", []string{"127.0.0.1:8081"}, 10*time.Second)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.AllEntries()
	}
}

func BenchmarkAllDialTargets(b *testing.B) {
	r := NewResolver()
	r.AddEntry("node-a", []string{"127.0.0.1:8080"}, 10*time.Second)
	r.AddEntry("node-b", []string{"127.0.0.1:8081"}, 10*time.Second)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.AllDialTargets()
	}
}

func BenchmarkMerge(b *testing.B) {
	r := NewResolver()
	entries := []ResolverEntry{
		{Hostname: "node-a", Addresses: []string{"127.0.0.1:8080"}, Transport: "tls", ExpiresAt: time.Now().Add(1 * time.Hour)},
		{Hostname: "node-b", Addresses: []string{"127.0.0.1:8081"}, Transport: "tls", ExpiresAt: time.Now().Add(1 * time.Hour)},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Merge(entries)
	}
}

func BenchmarkParseHostsContent(b *testing.B) {
	content := []byte("node-a 127.0.0.1:8080\nnode-b 127.0.0.1:8081\n")
	r := NewResolver()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reader := bytes.NewReader(content)
		_, _ = r.ParseHostsContent(reader)
	}
}

func BenchmarkSeedKnownHosts(b *testing.B) {
	r := NewResolver()
	hosts := map[string][]string{
		"node-a": {"127.0.0.1:8080"},
		"node-b": {"127.0.0.1:8081"},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.SeedKnownHosts(hosts)
	}
}

func BenchmarkIsExpired(b *testing.B) {
	entry := &resolverRecord{expiresAt: time.Now().Add(-1 * time.Hour)}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = entry.isExpired()
	}
}
