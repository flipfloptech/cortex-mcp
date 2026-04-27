package nodeset

import (
	"reflect"
	"sort"
	"testing"
)

// --- Construction & Add ---

func TestRangeSet_Empty(t *testing.T) {
	t.Parallel()
	rs := newRangeSet(0)
	if rs.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", rs.Len())
	}
	if !rs.IsEmpty() {
		t.Fatal("expected empty")
	}
}

func TestRangeSet_AddSingle(t *testing.T) {
	t.Parallel()
	rs := newRangeSet(0)
	rs.Add(5, 5)
	if rs.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", rs.Len())
	}
	if !rs.Contains(5) {
		t.Fatal("should contain 5")
	}
	if rs.Contains(4) {
		t.Fatal("should not contain 4")
	}
}

func TestRangeSet_AddRange(t *testing.T) {
	t.Parallel()
	rs := newRangeSet(0)
	rs.Add(1, 5)
	if rs.Len() != 5 {
		t.Fatalf("Len() = %d, want 5", rs.Len())
	}
	for i := 1; i <= 5; i++ {
		if !rs.Contains(i) {
			t.Fatalf("should contain %d", i)
		}
	}
	if rs.Contains(0) || rs.Contains(6) {
		t.Fatal("should not contain values outside range")
	}
}

func TestRangeSet_AddOverlapping(t *testing.T) {
	t.Parallel()
	rs := newRangeSet(0)
	rs.Add(1, 5)
	rs.Add(3, 8) // overlaps [1,5]
	if rs.Len() != 8 {
		t.Fatalf("Len() = %d, want 8", rs.Len())
	}
	// Should have merged into a single range [1,8]
	for i := 1; i <= 8; i++ {
		if !rs.Contains(i) {
			t.Fatalf("should contain %d", i)
		}
	}
}

func TestRangeSet_AddAdjacent(t *testing.T) {
	t.Parallel()
	rs := newRangeSet(0)
	rs.Add(1, 3)
	rs.Add(4, 6) // adjacent to [1,3]
	if rs.Len() != 6 {
		t.Fatalf("Len() = %d, want 6", rs.Len())
	}
	// Should merge into [1,6]
	got := rs.Expand()
	want := []int{1, 2, 3, 4, 5, 6}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Expand() = %v, want %v", got, want)
	}
}

func TestRangeSet_AddDisjoint(t *testing.T) {
	t.Parallel()
	rs := newRangeSet(0)
	rs.Add(1, 3)
	rs.Add(7, 9) // gap between [1,3] and [7,9]
	if rs.Len() != 6 {
		t.Fatalf("Len() = %d, want 6", rs.Len())
	}
	if rs.Contains(5) {
		t.Fatal("should not contain 5 (in gap)")
	}
}

func TestRangeSet_AddMultipleOverlapping(t *testing.T) {
	t.Parallel()
	rs := newRangeSet(0)
	rs.Add(1, 3)
	rs.Add(5, 7)
	rs.Add(9, 11)
	// Now add a range that spans all three
	rs.Add(2, 10)
	if rs.Len() != 11 {
		t.Fatalf("Len() = %d, want 11", rs.Len())
	}
	for i := 1; i <= 11; i++ {
		if !rs.Contains(i) {
			t.Fatalf("should contain %d after merge", i)
		}
	}
}

func TestRangeSet_AddDuplicate(t *testing.T) {
	t.Parallel()
	rs := newRangeSet(0)
	rs.Add(1, 5)
	rs.Add(1, 5)
	if rs.Len() != 5 {
		t.Fatalf("Len() = %d, want 5 (no duplicates)", rs.Len())
	}
}

// --- Remove ---

func TestRangeSet_RemoveAll(t *testing.T) {
	t.Parallel()
	rs := newRangeSet(0)
	rs.Add(1, 5)
	rs.Remove(1, 5)
	if rs.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", rs.Len())
	}
	if !rs.IsEmpty() {
		t.Fatal("expected empty after removing all")
	}
}

func TestRangeSet_RemoveMiddle(t *testing.T) {
	t.Parallel()
	rs := newRangeSet(0)
	rs.Add(1, 10)
	rs.Remove(4, 6) // remove middle portion
	if rs.Len() != 7 {
		t.Fatalf("Len() = %d, want 7", rs.Len())
	}
	// Should have [1-3] and [7-10]
	if rs.Contains(4) || rs.Contains(5) || rs.Contains(6) {
		t.Fatal("should not contain removed values")
	}
	if !rs.Contains(3) || !rs.Contains(7) {
		t.Fatal("should contain values outside removed range")
	}
}

func TestRangeSet_RemoveFromStart(t *testing.T) {
	t.Parallel()
	rs := newRangeSet(0)
	rs.Add(1, 10)
	rs.Remove(1, 3)
	if rs.Len() != 7 {
		t.Fatalf("Len() = %d, want 7", rs.Len())
	}
	if rs.Contains(1) || rs.Contains(3) {
		t.Fatal("should not contain removed start values")
	}
	if !rs.Contains(4) {
		t.Fatal("should contain 4")
	}
}

func TestRangeSet_RemoveFromEnd(t *testing.T) {
	t.Parallel()
	rs := newRangeSet(0)
	rs.Add(1, 10)
	rs.Remove(8, 10)
	if rs.Len() != 7 {
		t.Fatalf("Len() = %d, want 7", rs.Len())
	}
	if !rs.Contains(7) {
		t.Fatal("should contain 7")
	}
	if rs.Contains(8) {
		t.Fatal("should not contain 8")
	}
}

func TestRangeSet_RemoveNotPresent(t *testing.T) {
	t.Parallel()
	rs := newRangeSet(0)
	rs.Add(1, 5)
	rs.Remove(10, 20) // nothing to remove
	if rs.Len() != 5 {
		t.Fatalf("Len() = %d, want 5", rs.Len())
	}
}

func TestRangeSet_RemoveFromEmpty(t *testing.T) {
	t.Parallel()
	rs := newRangeSet(0)
	rs.Remove(1, 5) // no-op
	if rs.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", rs.Len())
	}
}

// --- Expand ---

func TestRangeSet_ExpandEmpty(t *testing.T) {
	t.Parallel()
	rs := newRangeSet(0)
	got := rs.Expand()
	if len(got) != 0 {
		t.Fatalf("Expand() = %v, want empty", got)
	}
}

func TestRangeSet_ExpandSorted(t *testing.T) {
	t.Parallel()
	rs := newRangeSet(0)
	rs.Add(7, 9)
	rs.Add(1, 3)
	got := rs.Expand()
	want := []int{1, 2, 3, 7, 8, 9}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Expand() = %v, want %v", got, want)
	}
}

// --- ExpandStrings (with padding) ---

func TestRangeSet_ExpandStringsNoPad(t *testing.T) {
	t.Parallel()
	rs := newRangeSet(0)
	rs.Add(1, 3)
	got := rs.ExpandStrings()
	want := []string{"1", "2", "3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExpandStrings() = %v, want %v", got, want)
	}
}

func TestRangeSet_ExpandStringsWithPad(t *testing.T) {
	t.Parallel()
	rs := newRangeSet(2) // 2-digit padding
	rs.Add(1, 3)
	got := rs.ExpandStrings()
	want := []string{"01", "02", "03"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExpandStrings() = %v, want %v", got, want)
	}
}

func TestRangeSet_ExpandStringsWidePad(t *testing.T) {
	t.Parallel()
	rs := newRangeSet(4)
	rs.Add(1, 3)
	got := rs.ExpandStrings()
	want := []string{"0001", "0002", "0003"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExpandStrings() = %v, want %v", got, want)
	}
}

// --- Set Operations: Union ---

func TestRangeSet_Union(t *testing.T) {
	t.Parallel()
	a := newRangeSet(0)
	a.Add(1, 5)
	b := newRangeSet(0)
	b.Add(3, 8)

	c := a.Union(b)
	if c.Len() != 8 {
		t.Fatalf("Union Len() = %d, want 8", c.Len())
	}
	for i := 1; i <= 8; i++ {
		if !c.Contains(i) {
			t.Fatalf("union should contain %d", i)
		}
	}
}

func TestRangeSet_UnionDisjoint(t *testing.T) {
	t.Parallel()
	a := newRangeSet(0)
	a.Add(1, 3)
	b := newRangeSet(0)
	b.Add(7, 9)

	c := a.Union(b)
	if c.Len() != 6 {
		t.Fatalf("Union Len() = %d, want 6", c.Len())
	}
}

func TestRangeSet_UnionEmpty(t *testing.T) {
	t.Parallel()
	a := newRangeSet(0)
	a.Add(1, 5)
	b := newRangeSet(0)

	c := a.Union(b)
	if c.Len() != 5 {
		t.Fatalf("Union with empty Len() = %d, want 5", c.Len())
	}
}

// --- Set Operations: Difference ---

func TestRangeSet_Difference(t *testing.T) {
	t.Parallel()
	a := newRangeSet(0)
	a.Add(1, 10)
	b := newRangeSet(0)
	b.Add(5, 15)

	c := a.Difference(b) // [1-4]
	if c.Len() != 4 {
		t.Fatalf("Difference Len() = %d, want 4", c.Len())
	}
	got := c.Expand()
	want := []int{1, 2, 3, 4}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Difference Expand() = %v, want %v", got, want)
	}
}

func TestRangeSet_DifferenceNoOverlap(t *testing.T) {
	t.Parallel()
	a := newRangeSet(0)
	a.Add(1, 5)
	b := newRangeSet(0)
	b.Add(10, 15)

	c := a.Difference(b) // no change
	if c.Len() != 5 {
		t.Fatalf("Difference Len() = %d, want 5", c.Len())
	}
}

func TestRangeSet_DifferenceCompleteOverlap(t *testing.T) {
	t.Parallel()
	a := newRangeSet(0)
	a.Add(3, 7)
	b := newRangeSet(0)
	b.Add(1, 10)

	c := a.Difference(b) // empty
	if c.Len() != 0 {
		t.Fatalf("Difference Len() = %d, want 0", c.Len())
	}
}

// --- Set Operations: Intersection ---

func TestRangeSet_Intersection(t *testing.T) {
	t.Parallel()
	a := newRangeSet(0)
	a.Add(1, 10)
	b := newRangeSet(0)
	b.Add(5, 15)

	c := a.Intersection(b) // [5-10]
	if c.Len() != 6 {
		t.Fatalf("Intersection Len() = %d, want 6", c.Len())
	}
	got := c.Expand()
	want := []int{5, 6, 7, 8, 9, 10}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Intersection Expand() = %v, want %v", got, want)
	}
}

func TestRangeSet_IntersectionNoOverlap(t *testing.T) {
	t.Parallel()
	a := newRangeSet(0)
	a.Add(1, 5)
	b := newRangeSet(0)
	b.Add(10, 15)

	c := a.Intersection(b)
	if c.Len() != 0 {
		t.Fatalf("Intersection Len() = %d, want 0", c.Len())
	}
}

// --- Set Operations: Symmetric Difference ---

func TestRangeSet_SymmetricDifference(t *testing.T) {
	t.Parallel()
	a := newRangeSet(0)
	a.Add(1, 10)
	b := newRangeSet(0)
	b.Add(5, 15)

	c := a.SymmetricDifference(b) // [1-4,11-15]
	if c.Len() != 9 {
		t.Fatalf("SymmetricDifference Len() = %d, want 9", c.Len())
	}
	got := c.Expand()
	want := []int{1, 2, 3, 4, 11, 12, 13, 14, 15}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SymmetricDifference Expand() = %v, want %v", got, want)
	}
}

// --- Subset / Superset ---

func TestRangeSet_IsSubset(t *testing.T) {
	t.Parallel()
	a := newRangeSet(0)
	a.Add(3, 5)
	b := newRangeSet(0)
	b.Add(1, 10)

	if !a.IsSubset(b) {
		t.Fatal("[3-5] should be subset of [1-10]")
	}
	if b.IsSubset(a) {
		t.Fatal("[1-10] should NOT be subset of [3-5]")
	}
}

func TestRangeSet_IsSubsetEqual(t *testing.T) {
	t.Parallel()
	a := newRangeSet(0)
	a.Add(1, 5)
	b := newRangeSet(0)
	b.Add(1, 5)

	if !a.IsSubset(b) {
		t.Fatal("equal sets should be subsets of each other")
	}
}

func TestRangeSet_IsSuperset(t *testing.T) {
	t.Parallel()
	a := newRangeSet(0)
	a.Add(1, 10)
	b := newRangeSet(0)
	b.Add(3, 5)

	if !a.IsSuperset(b) {
		t.Fatal("[1-10] should be superset of [3-5]")
	}
}

// --- Equal ---

func TestRangeSet_Equal(t *testing.T) {
	t.Parallel()
	a := newRangeSet(0)
	a.Add(1, 5)
	a.Add(8, 10)
	b := newRangeSet(0)
	b.Add(1, 5)
	b.Add(8, 10)

	if !a.Equal(b) {
		t.Fatal("identical range sets should be equal")
	}
}

func TestRangeSet_NotEqual(t *testing.T) {
	t.Parallel()
	a := newRangeSet(0)
	a.Add(1, 5)
	b := newRangeSet(0)
	b.Add(1, 6)

	if a.Equal(b) {
		t.Fatal("different range sets should not be equal")
	}
}

// --- Fold ---

func TestRangeSet_FoldEmpty(t *testing.T) {
	t.Parallel()
	rs := newRangeSet(0)
	got := rs.Fold()
	if got != "" {
		t.Fatalf("Fold() = %q, want empty", got)
	}
}

func TestRangeSet_FoldSingle(t *testing.T) {
	t.Parallel()
	rs := newRangeSet(0)
	rs.Add(5, 5)
	got := rs.Fold()
	if got != "5" {
		t.Fatalf("Fold() = %q, want %q", got, "5")
	}
}

func TestRangeSet_FoldContiguousRange(t *testing.T) {
	t.Parallel()
	rs := newRangeSet(0)
	rs.Add(1, 5)
	got := rs.Fold()
	if got != "1-5" {
		t.Fatalf("Fold() = %q, want %q", got, "1-5")
	}
}

func TestRangeSet_FoldDisjointRanges(t *testing.T) {
	t.Parallel()
	rs := newRangeSet(0)
	rs.Add(1, 3)
	rs.Add(7, 9)
	got := rs.Fold()
	if got != "1-3,7-9" {
		t.Fatalf("Fold() = %q, want %q", got, "1-3,7-9")
	}
}

func TestRangeSet_FoldMixed(t *testing.T) {
	t.Parallel()
	rs := newRangeSet(0)
	rs.Add(1, 3)
	rs.Add(5, 5)
	rs.Add(7, 9)
	got := rs.Fold()
	if got != "1-3,5,7-9" {
		t.Fatalf("Fold() = %q, want %q", got, "1-3,5,7-9")
	}
}

func TestRangeSet_FoldPadded(t *testing.T) {
	t.Parallel()
	rs := newRangeSet(2)
	rs.Add(1, 3)
	got := rs.Fold()
	if got != "01-03" {
		t.Fatalf("Fold() = %q, want %q", got, "01-03")
	}
}

func TestRangeSet_FoldPaddedSingle(t *testing.T) {
	t.Parallel()
	rs := newRangeSet(3)
	rs.Add(5, 5)
	got := rs.Fold()
	if got != "005" {
		t.Fatalf("Fold() = %q, want %q", got, "005")
	}
}

// --- Step detection in Fold ---

func TestRangeSet_FoldWithStep(t *testing.T) {
	t.Parallel()
	rs := newRangeSet(0)
	// Add 1,3,5,7,9 — should fold as 1-9/2
	for i := 1; i <= 9; i += 2 {
		rs.Add(i, i)
	}
	got := rs.Fold()
	if got != "1-9/2" {
		t.Fatalf("Fold() = %q, want %q", got, "1-9/2")
	}
}

func TestRangeSet_FoldNoStep(t *testing.T) {
	t.Parallel()
	rs := newRangeSet(0)
	// 1,2,4 — no consistent step
	rs.Add(1, 2)
	rs.Add(4, 4)
	got := rs.Fold()
	if got != "1-2,4" {
		t.Fatalf("Fold() = %q, want %q", got, "1-2,4")
	}
}

// --- Copy ---

func TestRangeSet_Copy(t *testing.T) {
	t.Parallel()
	a := newRangeSet(0)
	a.Add(1, 5)
	b := a.Copy()
	b.Add(10, 15)

	if a.Len() != 5 {
		t.Fatalf("original was mutated: Len() = %d, want 5", a.Len())
	}
	if b.Len() != 11 {
		t.Fatalf("copy Len() = %d, want 11", b.Len())
	}
}

// --- Concurrent Access ---

func TestRangeSet_ConcurrentExpand(t *testing.T) {
	t.Parallel()
	rs := newRangeSet(0)
	rs.Add(1, 100)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			_ = rs.Expand()
		}
	}()
	for i := 0; i < 100; i++ {
		_ = rs.Expand()
	}
	<-done
}

// --- Table-driven edge cases ---

func TestRangeSet_ExpandTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		adds    [][2]int
		removes [][2]int
		want    []int
	}{
		{
			name: "single value",
			adds: [][2]int{{5, 5}},
			want: []int{5},
		},
		{
			name:    "add then remove all",
			adds:    [][2]int{{1, 10}},
			removes: [][2]int{{1, 10}},
			want:    []int{},
		},
		{
			name:    "add then remove middle",
			adds:    [][2]int{{1, 10}},
			removes: [][2]int{{4, 6}},
			want:    []int{1, 2, 3, 7, 8, 9, 10},
		},
		{
			name: "three disjoint ranges",
			adds: [][2]int{{1, 2}, {5, 6}, {9, 10}},
			want: []int{1, 2, 5, 6, 9, 10},
		},
		{
			name: "merge three into one",
			adds: [][2]int{{1, 2}, {5, 6}, {9, 10}, {1, 10}},
			want: []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rs := newRangeSet(0)
			for _, a := range tt.adds {
				rs.Add(a[0], a[1])
			}
			for _, r := range tt.removes {
				rs.Remove(r[0], r[1])
			}
			got := rs.Expand()
			sort.Ints(got)
			if len(got) == 0 {
				got = []int{} // normalize nil to empty
			}
			if len(tt.want) == 0 {
				tt.want = []int{}
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Expand() = %v, want %v", got, tt.want)
			}
		})
	}
}
