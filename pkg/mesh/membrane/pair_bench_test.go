package membrane

import (
	"net"
	"testing"
	"time"
)

func BenchmarkNewPendingPairs(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		pp := NewPendingPairs()
		pp.Close()
	}
}

func BenchmarkNewPendingPairsWithTimeout(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		pp := NewPendingPairsWithTimeout(100 * time.Millisecond)
		pp.Close()
	}
}

func BenchmarkRegisterControl(b *testing.B) {
	pp := NewPendingPairs()
	defer pp.Close()

	token := make([]byte, 32)
	c, _ := net.Pipe()
	defer func() { _ = c.Close() }()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		pp.RegisterControl("node-a", token, c)
	}
}

func BenchmarkCompleteData(b *testing.B) {
	pp := NewPendingPairs()
	defer pp.Close()

	token := make([]byte, 32)
	c, _ := net.Pipe()
	defer func() { _ = c.Close() }()
	d, _ := net.Pipe()
	defer func() { _ = d.Close() }()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		pp.RegisterControl("node-a", token, c)
		_, _ = pp.CompleteData("node-a", token, d)
	}
}

func BenchmarkPurgeExpired(b *testing.B) {
	pp := NewPendingPairs()
	defer pp.Close()

	token := make([]byte, 32)
	c, _ := net.Pipe()
	defer func() { _ = c.Close() }()
	pp.RegisterControl("node-a", token, c)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		pp.purgeExpired()
	}
}

func BenchmarkCleanupLoop(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		// Don't use NewPendingPairs because it starts the loop automatically.
		pp := &PendingPairs{
			pending: make(map[string]*pendingEntry),
			timeout: 10 * time.Millisecond,
			closeCh: make(chan struct{}),
		}
		pp.closeWg.Add(1)

		go pp.cleanupLoop()
		pp.Close()
	}
}
