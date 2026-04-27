package routing

import (
	"runtime"
	"sync"
	"testing"
)

// --- BufPool: basic Get/Put ---

func TestBufPool_Get_ReturnsCorrectSize(t *testing.T) {
	t.Parallel()

	pool := newBufPool(4, stitchBufSize)
	buf := pool.get()
	defer pool.put(buf)

	if len(buf) != stitchBufSize {
		t.Fatalf("buffer size = %d, want %d", len(buf), stitchBufSize)
	}
	if cap(buf) < stitchBufSize {
		t.Fatalf("buffer cap = %d, want >= %d", cap(buf), stitchBufSize)
	}
}

func TestBufPool_PutGet_ReusesBuffer(t *testing.T) {
	t.Parallel()

	pool := newBufPool(4, stitchBufSize)
	buf := pool.get()
	// Write a marker to verify identity.
	buf[0] = 0xAB
	pool.put(buf)

	reused := pool.get()
	if reused[0] != 0xAB {
		t.Fatal("expected buffer to be reused from pool")
	}
}

// --- BufPool: capacity limit ---

func TestBufPool_Put_DropsWhenFull(t *testing.T) {
	t.Parallel()

	const poolSize = 2
	pool := newBufPool(poolSize, stitchBufSize)

	// Fill the pool.
	for i := 0; i < poolSize; i++ {
		buf := make([]byte, stitchBufSize)
		buf[0] = byte(i + 1)
		pool.put(buf)
	}

	// This buffer should be silently dropped — pool is full.
	dropped := make([]byte, stitchBufSize)
	dropped[0] = 0xFF
	pool.put(dropped)

	// Drain the pool — should get exactly poolSize buffers.
	gotten := 0
	for i := 0; i < poolSize+1; i++ {
		buf := pool.get()
		if buf[0] == 0xFF {
			t.Fatal("pool retained a buffer beyond its capacity")
		}
		if buf[0] > 0 {
			gotten++
		}
	}
	if gotten != poolSize {
		t.Fatalf("got %d marked buffers, want %d", gotten, poolSize)
	}
}

// --- BufPool: empty pool fallback ---

func TestBufPool_Get_EmptyPoolAllocates(t *testing.T) {
	t.Parallel()

	pool := newBufPool(4, stitchBufSize)
	// Pool starts empty — should allocate fresh.
	buf := pool.get()
	if len(buf) != stitchBufSize {
		t.Fatalf("fallback buffer size = %d, want %d", len(buf), stitchBufSize)
	}
}

// --- BufPool: GC retention ---

func TestBufPool_SurvivesGC(t *testing.T) {
	t.Parallel()

	const poolSize = 8
	pool := newBufPool(poolSize, stitchBufSize)

	// Fill the pool with marked buffers.
	for i := 0; i < poolSize; i++ {
		buf := make([]byte, stitchBufSize)
		buf[0] = byte(i + 1)
		pool.put(buf)
	}

	// Force GC — sync.Pool would clear here, channel-based pool must not.
	runtime.GC()
	runtime.GC()

	// All buffers should still be available.
	retained := 0
	for i := 0; i < poolSize; i++ {
		buf := pool.get()
		if buf[0] > 0 {
			retained++
		}
	}
	if retained != poolSize {
		t.Fatalf("retained %d buffers after GC, want %d", retained, poolSize)
	}
}

// --- BufPool: concurrent access ---

func TestBufPool_ConcurrentGetPut(t *testing.T) {
	t.Parallel()

	const poolSize = 16
	pool := newBufPool(poolSize, stitchBufSize)

	const goroutines = 64
	const iterations = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				buf := pool.get()
				if len(buf) != stitchBufSize {
					// Can't call t.Fatal from goroutine—use panic.
					panic("buffer size mismatch in concurrent test")
				}
				// Simulate work.
				buf[0] = 0x42
				pool.put(buf)
			}
		}()
	}

	wg.Wait()
}

// --- BufPool: capacity checking ---

func TestBufPool_Put_RejectsWrongCapacity(t *testing.T) {
	t.Parallel()

	pool := newBufPool(4, stitchBufSize)

	// Put a buffer that's too small capacity — should be silently dropped.
	tooSmall := make([]byte, 1024)
	tooSmall[0] = 0xBB
	pool.put(tooSmall)

	// Get should return a fresh buffer of the correct capacity, not the small one.
	buf := pool.get()
	if len(buf) != stitchBufSize {
		t.Fatalf("got buffer of size %d after putting wrong-capacity buffer", len(buf))
	}
	if buf[0] == 0xBB {
		t.Fatal("pool accepted a wrong-capacity buffer")
	}
}

func TestBufPool_Put_AcceptsReslicedBuffer(t *testing.T) {
	t.Parallel()

	pool := newBufPool(4, stitchBufSize)

	buf := pool.get()
	// Reslice the buffer to simulate a partial read
	resliced := buf[:100]
	resliced[0] = 0xCC
	pool.put(resliced)

	// Get should return the buffer restored to full size
	got := pool.get()
	if len(got) != stitchBufSize {
		t.Fatalf("got buffer of size %d after putting resliced buffer, want %d", len(got), stitchBufSize)
	}
	if got[0] != 0xCC {
		t.Fatal("pool did not return the resliced buffer")
	}
}

// --- Benchmarks ---

func BenchmarkBufPool_GetPut(b *testing.B) {
	pool := newBufPool(16, stitchBufSize)
	// Pre-fill one buffer so we're testing the reuse path.
	pool.put(make([]byte, stitchBufSize))

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		buf := pool.get()
		pool.put(buf)
	}
}

func BenchmarkBufPool_GetPut_Contended(b *testing.B) {
	pool := newBufPool(64, stitchBufSize)
	// Pre-fill.
	for i := 0; i < 64; i++ {
		pool.put(make([]byte, stitchBufSize))
	}

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			buf := pool.get()
			pool.put(buf)
		}
	})
}

func BenchmarkSyncPool_GetPut(b *testing.B) {
	// Baseline comparison: standard sync.Pool.
	sp := sync.Pool{
		New: func() interface{} {
			buf := make([]byte, stitchBufSize)
			return &buf
		},
	}
	sp.Put(sp.Get()) // warm up

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		bufp := sp.Get().(*[]byte)
		sp.Put(bufp)
	}
}

func BenchmarkSyncPool_GetPut_AfterGC(b *testing.B) {
	// Shows sync.Pool weakness: after GC, allocations spike.
	sp := sync.Pool{
		New: func() interface{} {
			buf := make([]byte, stitchBufSize)
			return &buf
		},
	}
	sp.Put(sp.Get()) // put one in

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if i%1000 == 0 {
			runtime.GC()
		}
		bufp := sp.Get().(*[]byte)
		sp.Put(bufp)
	}
}

func BenchmarkBufPool_GetPut_AfterGC(b *testing.B) {
	pool := newBufPool(16, stitchBufSize)
	pool.put(make([]byte, stitchBufSize))

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if i%1000 == 0 {
			runtime.GC()
		}
		buf := pool.get()
		pool.put(buf)
	}
}
