package nodeset

import (
	"fmt"
	"sort"
	"strings"
)

// rang represents an inclusive integer range [lo, hi].
type rang struct {
	lo, hi int
}

// rangeSet manages a sorted set of non-negative integers stored as
// non-overlapping [lo, hi] inclusive ranges. All operations maintain
// the sorted, non-overlapping invariant.
//
// The padding field controls zero-padded string formatting in
// ExpandStrings and Fold.
type rangeSet struct {
	ranges  []rang
	padding int
}

// newRangeSet creates an empty RangeSet with the given zero-pad width.
// Pass 0 for no padding.
func newRangeSet(padding int) *rangeSet {
	return &rangeSet{padding: padding}
}

// Len returns the total number of integers in the set.
func (rs *rangeSet) Len() int {
	n := 0
	for _, r := range rs.ranges {
		n += r.hi - r.lo + 1
	}
	return n
}

// IsEmpty reports whether the set contains no elements.
func (rs *rangeSet) IsEmpty() bool {
	return len(rs.ranges) == 0
}

// Contains reports whether v is in the set.
func (rs *rangeSet) Contains(v int) bool {
	// Binary search for the range that could contain v.
	i := sort.Search(len(rs.ranges), func(i int) bool {
		return rs.ranges[i].hi >= v
	})
	return i < len(rs.ranges) && rs.ranges[i].lo <= v
}

// Add inserts the inclusive range [lo, hi] into the set,
// merging with any overlapping or adjacent ranges.
func (rs *rangeSet) Add(lo, hi int) {
	if lo > hi {
		return
	}

	// Find insertion point and merge.
	newR := rang{lo, hi}
	merged := make([]rang, 0, len(rs.ranges)+1)
	inserted := false

	for _, r := range rs.ranges {
		if r.hi < newR.lo-1 {
			// r is entirely before newR (not adjacent).
			merged = append(merged, r)
		} else if r.lo > newR.hi+1 {
			// r is entirely after newR (not adjacent).
			if !inserted {
				merged = append(merged, newR)
				inserted = true
			}
			merged = append(merged, r)
		} else {
			// Overlapping or adjacent — merge.
			if r.lo < newR.lo {
				newR.lo = r.lo
			}
			if r.hi > newR.hi {
				newR.hi = r.hi
			}
		}
	}

	if !inserted {
		merged = append(merged, newR)
	}

	rs.ranges = merged
}

// Remove removes the inclusive range [lo, hi] from the set,
// splitting ranges as needed.
func (rs *rangeSet) Remove(lo, hi int) {
	if lo > hi || len(rs.ranges) == 0 {
		return
	}

	var result []rang

	for _, r := range rs.ranges {
		if r.hi < lo || r.lo > hi {
			// No overlap — keep entire range.
			result = append(result, r)
		} else {
			// Overlap — may need to split.
			if r.lo < lo {
				// Keep the part before [lo, hi].
				result = append(result, rang{r.lo, lo - 1})
			}
			if r.hi > hi {
				// Keep the part after [lo, hi].
				result = append(result, rang{hi + 1, r.hi})
			}
		}
	}

	rs.ranges = result
}

// Expand returns all integers in the set in sorted order.
func (rs *rangeSet) Expand() []int {
	result := make([]int, 0, rs.Len())
	for _, r := range rs.ranges {
		for v := r.lo; v <= r.hi; v++ {
			result = append(result, v)
		}
	}
	return result
}

// ExpandStrings returns all integers formatted as strings,
// zero-padded to the configured width.
func (rs *rangeSet) ExpandStrings() []string {
	ints := rs.Expand()
	result := make([]string, len(ints))
	for i, v := range ints {
		result[i] = rs.formatInt(v)
	}
	return result
}

// Fold returns a compact string representation of the ranges
// (e.g., "1-3,5,7-9"). Uses autostep detection (e.g., "1-9/2").
func (rs *rangeSet) Fold() string {
	if len(rs.ranges) == 0 {
		return ""
	}

	// First, try to detect a global step pattern across all values.
	values := rs.Expand()
	if len(values) == 0 {
		return ""
	}

	if len(values) == 1 {
		return rs.formatInt(values[0])
	}

	// Check if all values follow a consistent step pattern.
	if step := detectStep(values); step > 0 {
		if step == 1 {
			return rs.foldRanges()
		}
		// Step pattern detected.
		return fmt.Sprintf("%s-%s/%d",
			rs.formatInt(values[0]),
			rs.formatInt(values[len(values)-1]),
			step)
	}

	return rs.foldRanges()
}

// foldRanges folds the stored ranges into compact notation without step detection.
func (rs *rangeSet) foldRanges() string {
	parts := make([]string, 0, len(rs.ranges))
	for _, r := range rs.ranges {
		if r.lo == r.hi {
			parts = append(parts, rs.formatInt(r.lo))
		} else {
			parts = append(parts, fmt.Sprintf("%s-%s",
				rs.formatInt(r.lo), rs.formatInt(r.hi)))
		}
	}
	return strings.Join(parts, ",")
}

// detectStep checks if a sorted slice of ints has a consistent step.
// Returns the step if found, 0 if no consistent step.
func detectStep(values []int) int {
	if len(values) < 2 {
		return 1
	}

	step := values[1] - values[0]
	if step <= 0 {
		return 0
	}

	for i := 2; i < len(values); i++ {
		if values[i]-values[i-1] != step {
			return 0
		}
	}

	return step
}

// formatInt formats an integer with zero-padding.
func (rs *rangeSet) formatInt(v int) string {
	if rs.padding <= 0 {
		return fmt.Sprintf("%d", v)
	}
	return fmt.Sprintf("%0*d", rs.padding, v)
}

// Union returns a new RangeSet containing all elements from both sets.
func (rs *rangeSet) Union(other *rangeSet) *rangeSet {
	result := rs.Copy()
	for _, r := range other.ranges {
		result.Add(r.lo, r.hi)
	}
	return result
}

// Difference returns a new RangeSet containing elements in rs but not in other.
func (rs *rangeSet) Difference(other *rangeSet) *rangeSet {
	result := rs.Copy()
	for _, r := range other.ranges {
		result.Remove(r.lo, r.hi)
	}
	return result
}

// Intersection returns a new RangeSet containing elements in both sets.
func (rs *rangeSet) Intersection(other *rangeSet) *rangeSet {
	result := newRangeSet(rs.padding)
	for _, a := range rs.ranges {
		for _, b := range other.ranges {
			lo := a.lo
			if b.lo > lo {
				lo = b.lo
			}
			hi := a.hi
			if b.hi < hi {
				hi = b.hi
			}
			if lo <= hi {
				result.Add(lo, hi)
			}
		}
	}
	return result
}

// SymmetricDifference returns a new RangeSet containing elements in
// either set but not both.
func (rs *rangeSet) SymmetricDifference(other *rangeSet) *rangeSet {
	// A ^ B = (A ∪ B) - (A ∩ B)
	union := rs.Union(other)
	inter := rs.Intersection(other)
	return union.Difference(inter)
}

// IsSubset reports whether all elements of rs are in other.
func (rs *rangeSet) IsSubset(other *rangeSet) bool {
	diff := rs.Difference(other)
	return diff.IsEmpty()
}

// IsSuperset reports whether rs contains all elements of other.
func (rs *rangeSet) IsSuperset(other *rangeSet) bool {
	return other.IsSubset(rs)
}

// Equal reports whether rs and other contain the same elements.
func (rs *rangeSet) Equal(other *rangeSet) bool {
	if len(rs.ranges) != len(other.ranges) {
		return false
	}
	for i, r := range rs.ranges {
		if r != other.ranges[i] {
			return false
		}
	}
	return true
}

// Copy returns a deep copy of the RangeSet.
func (rs *rangeSet) Copy() *rangeSet {
	c := &rangeSet{
		padding: rs.padding,
		ranges:  make([]rang, len(rs.ranges)),
	}
	copy(c.ranges, rs.ranges)
	return c
}
