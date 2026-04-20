package stream_test

import (
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry/stream"
)

// TestRingBuffer_BasicPushAndSnapshot verifies basic push and snapshot ordering.
func TestRingBuffer_BasicPushAndSnapshot(t *testing.T) {
	t.Parallel()

	rb := stream.NewRingBuffer[int](3)
	rb.Push(1)
	rb.Push(2)
	rb.Push(3)

	snap := rb.Snapshot()
	expected := []int{1, 2, 3}
	if len(snap) != 3 {
		t.Fatalf("Snapshot() returned %d items, want 3", len(snap))
	}
	for i, want := range expected {
		if snap[i] != want {
			t.Errorf("Snapshot()[%d] = %d, want %d", i, snap[i], want)
		}
	}
}

// TestRingBuffer_Overflow verifies that oldest items are evicted on overflow.
func TestRingBuffer_Overflow(t *testing.T) {
	t.Parallel()

	rb := stream.NewRingBuffer[int](3)
	for _, v := range []int{1, 2, 3, 4, 5} {
		rb.Push(v)
	}

	snap := rb.Snapshot()
	expected := []int{3, 4, 5}
	if len(snap) != 3 {
		t.Fatalf("Snapshot() returned %d items, want 3", len(snap))
	}
	for i, want := range expected {
		if snap[i] != want {
			t.Errorf("Snapshot()[%d] = %d, want %d", i, snap[i], want)
		}
	}
}

// TestRingBuffer_Len verifies length tracking.
func TestRingBuffer_Len(t *testing.T) {
	t.Parallel()

	rb := stream.NewRingBuffer[int](5)
	if rb.Len() != 0 {
		t.Errorf("Len() = %d, want 0", rb.Len())
	}

	rb.Push(1)
	if rb.Len() != 1 {
		t.Errorf("Len() = %d, want 1", rb.Len())
	}

	for i := 0; i < 10; i++ {
		rb.Push(i)
	}
	if rb.Len() != 5 {
		t.Errorf("Len() = %d, want 5 (capped at capacity)", rb.Len())
	}
}

// TestRingBuffer_Latest verifies Latest returns the most recent item.
func TestRingBuffer_Latest(t *testing.T) {
	t.Parallel()

	rb := stream.NewRingBuffer[string](3)

	_, ok := rb.Latest()
	if ok {
		t.Error("Latest() on empty buffer should return false")
	}

	rb.Push("a")
	rb.Push("b")
	val, ok := rb.Latest()
	if !ok || val != "b" {
		t.Errorf("Latest() = (%q, %v), want (\"b\", true)", val, ok)
	}

	rb.Push("c")
	rb.Push("d") // evicts "a"
	val, ok = rb.Latest()
	if !ok || val != "d" {
		t.Errorf("Latest() = (%q, %v), want (\"d\", true)", val, ok)
	}
}

// TestRingBuffer_PanicOnZeroCapacity verifies the panic guard.
func TestRingBuffer_PanicOnZeroCapacity(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r == nil {
			t.Error("NewRingBuffer(0) should panic")
		}
	}()
	stream.NewRingBuffer[int](0)
}
