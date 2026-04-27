package routing

// bufPool is a GC-resistant, channel-based buffer pool for the forwarding
// hot path. Unlike sync.Pool, buffers are not cleared during garbage
// collection, providing deterministic memory retention under traffic spikes.
//
// The pool uses a buffered channel as a fixed-capacity free list. Get/Put
// are lock-free channel operations. When the pool is empty, Get allocates
// a fresh buffer with make(). When the pool is full, Put silently drops
// the buffer (it will be collected by GC eventually).
//
// This eliminates the sawtooth CPU pattern caused by sync.Pool: during
// traffic spikes, sync.Pool would empty after GC, causing sudden massive
// 32KB heap allocations followed by aggressive GC cleanup. The channel-based
// pool retains its buffers across GC cycles, keeping the forwarding path
// allocation-free.
type bufPool struct {
	ch      chan []byte
	bufSize int
}

// newBufPool creates a buffer pool with the given capacity and buffer size.
// The pool starts empty — buffers are allocated on first Get and recycled
// via Put. The capacity determines the maximum number of buffers retained
// across GC cycles.
//
// Recommended sizing: match expected concurrent stream count. For a node
// forwarding N simultaneous circuits, set capacity to 2*N (each circuit
// uses 2 buffers: one per direction).
func newBufPool(capacity int, bufSize int) *bufPool {
	return &bufPool{
		ch:      make(chan []byte, capacity),
		bufSize: bufSize,
	}
}

// get returns a buffer from the pool, or allocates a fresh one if the
// pool is empty. Never blocks — returns immediately.
func (p *bufPool) get() []byte {
	select {
	case buf := <-p.ch:
		return buf
	default:
		return make([]byte, p.bufSize)
	}
}

// put returns a buffer to the pool. If the pool is at capacity, the
// buffer is silently dropped (GC will reclaim it). Never blocks.
//
// Buffers with incorrect capacity are silently rejected. Legitimate
// resliced buffers are restored to full capacity before being pooled.
func (p *bufPool) put(buf []byte) {
	if cap(buf) != p.bufSize {
		return
	}
	// Restore to full capacity in case caller resliced (e.g. buf[:n])
	buf = buf[:p.bufSize]

	select {
	case p.ch <- buf:
	default:
		// Pool is full — drop the buffer.
	}
}
