package stream

import (
	"crypto/rand"
	"testing"
)

// Dedup Benchmarks
func BenchmarkNewDedup(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewDedup(100)
	}
}

func BenchmarkIsDuplicate(b *testing.B) {
	d := NewDedup(1000)
	data := make([]byte, 64)
	rand.Read(data)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = d.IsDuplicate(data)
	}
}

func BenchmarkIsDuplicateString(b *testing.B) {
	d := NewDedup(1000)
	str := "test-duplicate-string-data"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = d.IsDuplicateString(str)
	}
}

func BenchmarkIsDuplicateHash(b *testing.B) {
	d := NewDedup(1000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = d.isDuplicateHash(123456789)
	}
}

func BenchmarkReset(b *testing.B) {
	d := NewDedup(1000)
	d.IsDuplicateString("test")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d.Reset()
	}
}

func BenchmarkHashFNV(b *testing.B) {
	data := make([]byte, 64)
	rand.Read(data)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = hashFNV(data)
	}
}

func BenchmarkHashFNVString(b *testing.B) {
	str := "test-duplicate-string-data"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = hashFNVString(str)
	}
}

// Aggregator Benchmarks
func BenchmarkNewAggregator(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewAggregator(100)
	}
}

func BenchmarkAdd(b *testing.B) {
	agg := NewAggregator(100)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		agg.Add("cpu_usage", 55.5)
	}
}

func BenchmarkAvg(b *testing.B) {
	agg := NewAggregator(100)
	for i := 0; i < 100; i++ {
		agg.Add("cpu_usage", float64(i))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = agg.Get("cpu_usage").Avg()
	}
}

func BenchmarkGroups(b *testing.B) {
	agg := NewAggregator(100)
	for i := 0; i < 100; i++ {
		agg.Add("cpu_usage", float64(i))
		agg.Add("mem_usage", float64(i*2))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = agg.Groups()
	}
}

func BenchmarkGet(b *testing.B) {
	agg := NewAggregator(100)
	agg.Add("cpu_usage", 55.5)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = agg.Get("cpu_usage")
	}
}

func BenchmarkAll(b *testing.B) {
	agg := NewAggregator(100)
	agg.Add("cpu_usage", 55.5)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = agg.All()
	}
}

func BenchmarkStats(b *testing.B) {
	agg := NewAggregator(100)
	for i := 0; i < 100; i++ {
		agg.Add("cpu_usage", float64(i))
		agg.Add("mem_usage", float64(i*2))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = agg.All()
	}
}

// RingBuffer Benchmarks
func BenchmarkNewRingBuffer(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewRingBuffer[int](100)
	}
}

func BenchmarkPush(b *testing.B) {
	rb := NewRingBuffer[int](100)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rb.Push(i)
	}
}

func BenchmarkLen(b *testing.B) {
	rb := NewRingBuffer[int](100)
	for i := 0; i < 100; i++ {
		rb.Push(i)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = rb.Len()
	}
}

func BenchmarkLatest(b *testing.B) {
	rb := NewRingBuffer[int](100)
	rb.Push(42)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = rb.Latest()
	}
}

func BenchmarkSnapshot(b *testing.B) {
	rb := NewRingBuffer[int](100)
	for i := 0; i < 50; i++ {
		rb.Push(i)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = rb.Snapshot()
	}
}

// TopK Benchmarks
func BenchmarkNewTopK(b *testing.B) {
	less := func(a, b int) bool { return a < b }
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewTopK(10, less)
	}
}

func BenchmarkResults(b *testing.B) {
	tk := NewTopK(100, func(a, b int) bool { return a < b })
	for i := 0; i < 150; i++ {
		tk.Push(i)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tk.Results()
	}
}

func BenchmarkLess(b *testing.B) {
	h := &topKHeap[int]{less: func(a, b int) bool { return a < b }}
	h.items = []int{10, 20}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = h.Less(0, 1)
	}
}

func BenchmarkSwap(b *testing.B) {
	h := &topKHeap[int]{less: func(a, b int) bool { return a < b }}
	h.items = []int{10, 20}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.Swap(0, 1)
	}
}

func BenchmarkPop(b *testing.B) {
	h := &topKHeap[int]{less: func(a, b int) bool { return a < b }}
	h.items = []int{10}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.items = []int{10} // refill quickly
		_ = h.Pop()
	}
}

// ThresholdEmitter Benchmarks
func BenchmarkNewThresholdEmitter(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewThresholdEmitter(80.0, 95.0)
	}
}

func BenchmarkEvaluate(b *testing.B) {
	te := NewThresholdEmitter(80.0, 95.0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = te.Evaluate(90.0)
	}
}

// Additional missing TopK Benchmarks
func BenchmarkTopKPush(b *testing.B) {
	tk := NewTopK(100, func(a, b int) bool { return a < b })
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tk.Push(i)
	}
}

func BenchmarkTopKLen(b *testing.B) {
	tk := NewTopK(100, func(a, b int) bool { return a < b })
	tk.Push(10)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tk.Len()
	}
}
