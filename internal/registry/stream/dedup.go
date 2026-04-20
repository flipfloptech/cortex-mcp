package stream

import "hash/fnv"

// Dedup is a streaming deduplicator that uses signature hashing to
// filter duplicate records. Maintains a bounded set of seen hashes
// with FIFO eviction when capacity is exceeded.
//
// Usage:
//
//	dd := NewDedup(10000) // track up to 10k unique signatures
//	for _, line := range stream {
//	    if dd.IsDuplicate([]byte(line)) {
//	        continue // skip duplicate
//	    }
//	    process(line)
//	}
type Dedup struct {
	seen  map[uint64]struct{}
	order []uint64 // FIFO eviction order
	cap   int
}

// NewDedup creates a deduplicator with the given capacity.
// When capacity is exceeded, the oldest signature is evicted.
// Panics if cap <= 0.
func NewDedup(cap int) *Dedup {
	if cap <= 0 {
		panic("stream: Dedup capacity must be > 0")
	}
	return &Dedup{
		seen:  make(map[uint64]struct{}, cap),
		order: make([]uint64, 0, cap),
		cap:   cap,
	}
}

// IsDuplicate returns true if the data has been seen before.
// If the data is new, it is recorded and false is returned.
func (d *Dedup) IsDuplicate(data []byte) bool {
	h := hashFNV(data)
	return d.isDuplicateHash(h)
}

// IsDuplicateString is a convenience method for string input.
func (d *Dedup) IsDuplicateString(s string) bool {
	h := hashFNVString(s)
	return d.isDuplicateHash(h)
}

func (d *Dedup) isDuplicateHash(h uint64) bool {
	if _, ok := d.seen[h]; ok {
		return true
	}

	// Evict oldest if at capacity.
	if len(d.order) >= d.cap {
		oldest := d.order[0]
		d.order = d.order[1:]
		delete(d.seen, oldest)
	}

	d.seen[h] = struct{}{}
	d.order = append(d.order, h)
	return false
}

// Len returns the number of unique signatures currently tracked.
func (d *Dedup) Len() int {
	return len(d.seen)
}

// Reset clears all tracked signatures.
func (d *Dedup) Reset() {
	d.seen = make(map[uint64]struct{}, d.cap)
	d.order = d.order[:0]
}

func hashFNV(data []byte) uint64 {
	h := fnv.New64a()
	_, _ = h.Write(data)
	return h.Sum64()
}

func hashFNVString(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}
