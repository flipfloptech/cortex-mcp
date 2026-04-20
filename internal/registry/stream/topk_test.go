package stream_test

import (
	"testing"

	"github.com/flipfloptech/cortex-mcp/internal/registry/stream"
)

// TestTopK_BasicUsage verifies basic top-K selection.
func TestTopK_BasicUsage(t *testing.T) {
	t.Parallel()

	tk := stream.NewTopK[int](3, func(a, b int) bool { return a < b })

	for _, v := range []int{10, 50, 30, 80, 20, 90, 40} {
		tk.Push(v)
	}

	results := tk.Results()
	if len(results) != 3 {
		t.Fatalf("Results() returned %d items, want 3", len(results))
	}

	// Top 3 should be 90, 80, 50 (descending).
	expected := []int{90, 80, 50}
	for i, want := range expected {
		if results[i] != want {
			t.Errorf("Results()[%d] = %d, want %d", i, results[i], want)
		}
	}
}

// TestTopK_FewerThanK verifies behavior when fewer items than K are pushed.
func TestTopK_FewerThanK(t *testing.T) {
	t.Parallel()

	tk := stream.NewTopK[int](10, func(a, b int) bool { return a < b })
	tk.Push(5)
	tk.Push(3)

	if tk.Len() != 2 {
		t.Errorf("Len() = %d, want 2", tk.Len())
	}

	results := tk.Results()
	if len(results) != 2 {
		t.Fatalf("Results() returned %d items, want 2", len(results))
	}
}

// TestTopK_SingleItem verifies K=1 returns the maximum.
func TestTopK_SingleItem(t *testing.T) {
	t.Parallel()

	tk := stream.NewTopK[int](1, func(a, b int) bool { return a < b })
	for _, v := range []int{3, 7, 1, 9, 5} {
		tk.Push(v)
	}

	results := tk.Results()
	if len(results) != 1 || results[0] != 9 {
		t.Errorf("Results() = %v, want [9]", results)
	}
}

// TestTopK_Duplicates verifies that duplicate values are handled correctly.
func TestTopK_Duplicates(t *testing.T) {
	t.Parallel()

	tk := stream.NewTopK[int](3, func(a, b int) bool { return a < b })
	for _, v := range []int{5, 5, 5, 5, 5} {
		tk.Push(v)
	}

	results := tk.Results()
	if len(results) != 3 {
		t.Fatalf("Results() returned %d items, want 3", len(results))
	}
	for i, v := range results {
		if v != 5 {
			t.Errorf("Results()[%d] = %d, want 5", i, v)
		}
	}
}

// TestTopK_StructType verifies that TopK works with struct types.
func TestTopK_StructType(t *testing.T) {
	t.Parallel()

	type record struct {
		name  string
		score float64
	}

	tk := stream.NewTopK[record](2, func(a, b record) bool { return a.score < b.score })
	tk.Push(record{"a", 1.0})
	tk.Push(record{"b", 5.0})
	tk.Push(record{"c", 3.0})

	results := tk.Results()
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].name != "b" || results[1].name != "c" {
		t.Errorf("Results = %v, want [{b 5} {c 3}]", results)
	}
}
