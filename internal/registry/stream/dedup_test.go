package stream_test

import (
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry/stream"
)

// TestDedup_BasicDedup verifies that duplicates are detected.
func TestDedup_BasicDedup(t *testing.T) {
	t.Parallel()

	dd := stream.NewDedup(100)

	if dd.IsDuplicateString("hello") {
		t.Error("first occurrence should not be duplicate")
	}
	if !dd.IsDuplicateString("hello") {
		t.Error("second occurrence should be duplicate")
	}
	if dd.IsDuplicateString("world") {
		t.Error("different string should not be duplicate")
	}
}

// TestDedup_ByteInput verifies byte slice dedup.
func TestDedup_ByteInput(t *testing.T) {
	t.Parallel()

	dd := stream.NewDedup(100)

	data := []byte("test data")
	if dd.IsDuplicate(data) {
		t.Error("first occurrence should not be duplicate")
	}
	if !dd.IsDuplicate(data) {
		t.Error("second occurrence should be duplicate")
	}
}

// TestDedup_Eviction verifies FIFO eviction when capacity is exceeded.
func TestDedup_Eviction(t *testing.T) {
	t.Parallel()

	dd := stream.NewDedup(3)

	dd.IsDuplicateString("a") // a
	dd.IsDuplicateString("b") // a, b
	dd.IsDuplicateString("c") // a, b, c — full

	if dd.Len() != 3 {
		t.Errorf("Len() = %d, want 3", dd.Len())
	}

	dd.IsDuplicateString("d") // evicts "a", now b, c, d

	// "a" should no longer be tracked (evicted).
	if dd.IsDuplicateString("a") {
		t.Error("'a' should not be duplicate after eviction")
	}
}

// TestDedup_Reset verifies that Reset clears all state.
func TestDedup_Reset(t *testing.T) {
	t.Parallel()

	dd := stream.NewDedup(100)
	dd.IsDuplicateString("x")
	dd.Reset()

	if dd.Len() != 0 {
		t.Errorf("Len() after Reset = %d, want 0", dd.Len())
	}
	if dd.IsDuplicateString("x") {
		t.Error("after Reset, 'x' should not be duplicate")
	}
}

// TestDedup_PanicOnZeroCapacity verifies the panic guard.
func TestDedup_PanicOnZeroCapacity(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r == nil {
			t.Error("NewDedup(0) should panic")
		}
	}()
	stream.NewDedup(0)
}
