package stream

import "container/heap"

// TopK maintains a bounded min-heap to track the K highest-scoring items
// from an unbounded stream. O(n log k) time, O(k) space.
//
// Usage:
//
//	tk := NewTopK[MyItem](10, func(a, b MyItem) bool { return a.Score < b.Score })
//	for _, item := range stream {
//	    tk.Push(item)
//	}
//	result := tk.Results() // top 10, descending by score
type TopK[T any] struct {
	k    int
	less func(a, b T) bool // min-heap: returns true if a < b
	h    *topKHeap[T]
}

// NewTopK creates a Top-K reducer with capacity k.
// The less function defines ordering: items with higher "score" should
// return false when compared to lower items (i.e., less returns true
// for the LEAST important items that get evicted first).
func NewTopK[T any](k int, less func(a, b T) bool) *TopK[T] {
	h := &topKHeap[T]{less: less}
	heap.Init(h)
	return &TopK[T]{k: k, less: less, h: h}
}

// Push adds an item to the reducer. If the heap is full and the item
// scores higher than the minimum, the minimum is evicted.
func (tk *TopK[T]) Push(item T) {
	if tk.h.Len() < tk.k {
		heap.Push(tk.h, item)
		return
	}
	// If the new item is greater than the current min, replace it.
	if tk.less(tk.h.items[0], item) {
		tk.h.items[0] = item
		heap.Fix(tk.h, 0)
	}
}

// Results returns the top-K items sorted descending (highest first).
func (tk *TopK[T]) Results() []T {
	out := make([]T, tk.h.Len())
	copy(out, tk.h.items)

	// Sort descending: reverse the less function.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if tk.less(out[i], out[j]) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// Len returns the current number of items in the reducer.
func (tk *TopK[T]) Len() int {
	return tk.h.Len()
}

// topKHeap implements container/heap.Interface for a min-heap.
type topKHeap[T any] struct {
	items []T
	less  func(a, b T) bool
}

func (h *topKHeap[T]) Len() int           { return len(h.items) }
func (h *topKHeap[T]) Less(i, j int) bool { return h.less(h.items[i], h.items[j]) }
func (h *topKHeap[T]) Swap(i, j int)      { h.items[i], h.items[j] = h.items[j], h.items[i] }
func (h *topKHeap[T]) Push(x interface{}) { h.items = append(h.items, x.(T)) }
func (h *topKHeap[T]) Pop() interface{} {
	old := h.items
	n := len(old)
	item := old[n-1]
	h.items = old[:n-1]
	return item
}
