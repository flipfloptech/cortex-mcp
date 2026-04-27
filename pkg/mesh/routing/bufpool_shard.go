package routing

import (
	"runtime"
	"sync/atomic"
)

// shardedBufPool distributes buffer get/put operations across multiple
// independent bufPool shards to reduce channel contention under extreme
// goroutine concurrency (1000+ circuits).
//
// At low concurrency, the single-channel bufPool is already optimal.
// Under extreme contention, the channel's internal lock becomes a
// serialization point. Sharding by goroutine ID distributes the
// contention across GOMAXPROCS independent channels, each with its
// own lock — classic lock striping.
//
// Shard selection uses an atomic counter with round-robin distribution.
// This is cheaper than runtime.GOMAXPROCS-based hashing and provides
// even distribution regardless of goroutine scheduling.
type shardedBufPool struct {
	shards     []*bufPool
	getCounter atomic.Uint64
	putCounter atomic.Uint64
}

// newShardedBufPool creates a buffer pool with GOMAXPROCS shards.
// The total capacity is distributed evenly across shards.
// Each shard is an independent channel-based bufPool.
func newShardedBufPool(totalCapacity int, bufSize int) *shardedBufPool {
	numShards := runtime.GOMAXPROCS(0)
	if numShards < 1 {
		numShards = 1
	}

	perShard := totalCapacity / numShards
	if perShard < 1 {
		perShard = 1
	}

	shards := make([]*bufPool, numShards)
	for i := range shards {
		shards[i] = newBufPool(perShard, bufSize)
	}

	return &shardedBufPool{
		shards: shards,
	}
}

// get returns a buffer from the pool, trying shards in a round-robin
// manner, stealing from other shards if the primary is empty, before
// allocating a fresh one. Never blocks.
func (sp *shardedBufPool) get() []byte {
	numShards := len(sp.shards)
	idx := int(sp.getCounter.Add(1) % uint64(numShards))

	// Fast path
	select {
	case buf := <-sp.shards[idx].ch:
		return buf
	default:
	}

	// Work stealing: try other shards to avoid unnecessary allocation.
	for i := 1; i < numShards; i++ {
		nextIdx := (idx + i) % numShards
		select {
		case buf := <-sp.shards[nextIdx].ch:
			return buf
		default:
		}
	}

	return make([]byte, sp.shards[0].bufSize)
}

// put returns a buffer to the pool, trying shards in a round-robin
// manner, putting into other shards if the primary is full. Never blocks.
func (sp *shardedBufPool) put(buf []byte) {
	if cap(buf) != sp.shards[0].bufSize {
		return
	}
	buf = buf[:sp.shards[0].bufSize]

	numShards := len(sp.shards)
	idx := int(sp.putCounter.Add(1) % uint64(numShards))

	// Fast path
	select {
	case sp.shards[idx].ch <- buf:
		return
	default:
	}

	// Try other shards to avoid dropping the buffer.
	for i := 1; i < numShards; i++ {
		nextIdx := (idx + i) % numShards
		select {
		case sp.shards[nextIdx].ch <- buf:
			return
		default:
		}
	}
}
