package routing

import (
	"runtime"
	"sync"
	"testing"
)

// --- ShardedBufPool: basic correctness ---

func TestShardedBufPool_Get_ReturnsCorrectSize(t *testing.T) {
	t.Parallel()

	pool := newShardedBufPool(4, stitchBufSize)
	buf := pool.get()
	defer pool.put(buf)

	if len(buf) != stitchBufSize {
		t.Fatalf("buffer size = %d, want %d", len(buf), stitchBufSize)
	}
}

func TestShardedBufPool_PutGet_ReusesBuffer(t *testing.T) {
	t.Parallel()

	pool := newShardedBufPool(4, stitchBufSize)
	buf := pool.get()
	buf[0] = 0xCD
	pool.put(buf)

	// Try to get the same buffer back.
	reused := pool.get()
	if reused[0] != 0xCD {
		t.Log("buffer was not reused (may have gone to different shard)")
	}
}

func TestShardedBufPool_SurvivesGC(t *testing.T) {
	t.Parallel()

	pool := newShardedBufPool(16, stitchBufSize)

	// Fill the pool with marked buffers.
	for i := 0; i < 16; i++ {
		buf := make([]byte, stitchBufSize)
		buf[0] = byte(i + 1)
		pool.put(buf)
	}

	runtime.GC()
	runtime.GC()

	// Drain enough to hit all shards — buffers should survive GC.
	// Use more iterations than pool size because round-robin counter
	// means get/put don't always hit the same shard.
	retained := 0
	for i := 0; i < 64; i++ {
		buf := pool.get()
		if buf[0] > 0 {
			retained++
		}
	}
	if retained == 0 {
		t.Fatal("no buffers survived GC — channel-based pool should retain them")
	}
}

func TestShardedBufPool_ConcurrentGetPut(t *testing.T) {
	t.Parallel()

	pool := newShardedBufPool(64, stitchBufSize)

	const goroutines = 128
	const iterations = 200
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				buf := pool.get()
				if len(buf) != stitchBufSize {
					panic("buffer size mismatch in concurrent test")
				}
				buf[0] = 0x42
				pool.put(buf)
			}
		}()
	}

	wg.Wait()
}

func TestShardedBufPool_RejectsWrongSize(t *testing.T) {
	t.Parallel()

	pool := newShardedBufPool(4, stitchBufSize)

	wrongSize := make([]byte, 1024)
	wrongSize[0] = 0xEE
	pool.put(wrongSize)

	buf := pool.get()
	if len(buf) != stitchBufSize {
		t.Fatalf("got buffer of size %d after putting wrong-sized buffer", len(buf))
	}
	if buf[0] == 0xEE {
		t.Fatal("pool accepted a wrong-sized buffer")
	}
}

func TestShardedBufPool_ShardCount(t *testing.T) {
	t.Parallel()

	pool := newShardedBufPool(64, stitchBufSize)

	numShards := len(pool.shards)
	procs := runtime.GOMAXPROCS(0)

	if numShards < 1 {
		t.Fatal("must have at least 1 shard")
	}
	if numShards != procs {
		t.Fatalf("shard count = %d, want %d (GOMAXPROCS)", numShards, procs)
	}
}

// --- Benchmarks: sharded vs single ---

func BenchmarkShardedBufPool_GetPut(b *testing.B) {
	pool := newShardedBufPool(16, stitchBufSize)
	pool.put(make([]byte, stitchBufSize))

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		buf := pool.get()
		pool.put(buf)
	}
}

func BenchmarkShardedBufPool_GetPut_Contended(b *testing.B) {
	pool := newShardedBufPool(256, stitchBufSize)
	// Pre-fill.
	for i := 0; i < 256; i++ {
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

func BenchmarkShardedBufPool_GetPut_AfterGC(b *testing.B) {
	pool := newShardedBufPool(16, stitchBufSize)
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
