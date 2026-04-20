package stream

// RingBuffer is a fixed-capacity circular buffer that evicts the oldest
// entry on overflow. Used for sliding-window analysis.
//
// Usage:
//
//	rb := NewRingBuffer[Event](100) // keep last 100 events
//	rb.Push(event)
//	snapshot := rb.Snapshot() // ordered oldest → newest
type RingBuffer[T any] struct {
	items []T
	cap   int
	head  int // next write position
	full  bool
}

// NewRingBuffer creates a ring buffer with the given capacity.
// Panics if cap <= 0.
func NewRingBuffer[T any](cap int) *RingBuffer[T] {
	if cap <= 0 {
		panic("stream: RingBuffer capacity must be > 0")
	}
	return &RingBuffer[T]{
		items: make([]T, cap),
		cap:   cap,
	}
}

// Push adds an item to the buffer. If full, the oldest item is overwritten.
func (rb *RingBuffer[T]) Push(item T) {
	rb.items[rb.head] = item
	rb.head = (rb.head + 1) % rb.cap
	if rb.head == 0 && !rb.full {
		rb.full = true
	}
}

// Len returns the current number of items in the buffer.
func (rb *RingBuffer[T]) Len() int {
	if rb.full {
		return rb.cap
	}
	return rb.head
}

// Snapshot returns all items in order from oldest to newest.
// Returns a new slice — safe to modify.
func (rb *RingBuffer[T]) Snapshot() []T {
	n := rb.Len()
	out := make([]T, n)
	if rb.full {
		// head points to the oldest item when full.
		copy(out, rb.items[rb.head:])
		copy(out[rb.cap-rb.head:], rb.items[:rb.head])
	} else {
		copy(out, rb.items[:rb.head])
	}
	return out
}

// Latest returns the most recently pushed item and true, or the zero
// value and false if the buffer is empty.
func (rb *RingBuffer[T]) Latest() (T, bool) {
	if rb.Len() == 0 {
		var zero T
		return zero, false
	}
	idx := (rb.head - 1 + rb.cap) % rb.cap
	return rb.items[idx], true
}
