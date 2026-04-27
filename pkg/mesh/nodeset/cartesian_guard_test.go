package nodeset

import (
	"strings"
	"testing"
)

// --- C-4: Pre-allocation OOM guard in cartesianExpand ---
//
// These tests verify that cartesianExpand rejects expansions exceeding
// maxExpand BEFORE allocating the result slice, preventing OOM from
// malicious patterns like rack[1-10000]node[1-10000].

func TestCartesianExpand_WithinLimit(t *testing.T) {
	t.Parallel()
	// 3 × 3 = 9 results, well within limit of 100.
	segs := []segment{
		{kind: segLiteral, literal: "rack"},
		{kind: segBracket, values: []string{"1", "2", "3"}},
		{kind: segLiteral, literal: "node"},
		{kind: segBracket, values: []string{"a", "b", "c"}},
	}
	got, err := cartesianExpand(segs, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 9 {
		t.Fatalf("got %d results, want 9", len(got))
	}
	// Verify one expected entry exists.
	found := false
	for _, s := range got {
		if s == "rack2nodeb" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected 'rack2nodeb' in results: %v", got)
	}
}

func TestCartesianExpand_ExactlyAtLimit(t *testing.T) {
	t.Parallel()
	// 5 × 4 = 20, limit is 20 — should succeed.
	segs := []segment{
		{kind: segBracket, values: []string{"a", "b", "c", "d", "e"}},
		{kind: segBracket, values: []string{"1", "2", "3", "4"}},
	}
	got, err := cartesianExpand(segs, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 20 {
		t.Fatalf("got %d results, want 20", len(got))
	}
}

func TestCartesianExpand_ExceedsLimit(t *testing.T) {
	t.Parallel()
	// 10 × 10 = 100, limit is 50 — must fail BEFORE allocation.
	segs := []segment{
		{kind: segBracket, values: makeValues(10)},
		{kind: segBracket, values: makeValues(10)},
	}
	_, err := cartesianExpand(segs, 50)
	if err == nil {
		t.Fatal("expected error when product exceeds limit")
	}
	if !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("error should mention 'exceeds limit', got: %v", err)
	}
}

func TestCartesianExpand_MassiveProduct_NoOOM(t *testing.T) {
	t.Parallel()
	// 10000 × 10000 = 100,000,000 — the critical exploit case.
	// Must fail immediately, not allocate 100M strings.
	segs := []segment{
		{kind: segBracket, values: makeValues(10000)},
		{kind: segBracket, values: makeValues(10000)},
	}
	_, err := cartesianExpand(segs, 25000)
	if err == nil {
		t.Fatal("expected error for massive product 10000×10000, must not OOM")
	}
	if !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("error should mention 'exceeds limit', got: %v", err)
	}
}

func TestCartesianExpand_ThreeDimensionOverflow(t *testing.T) {
	t.Parallel()
	// 100 × 100 × 100 = 1,000,000 — overflow check across 3 dimensions.
	segs := []segment{
		{kind: segBracket, values: makeValues(100)},
		{kind: segBracket, values: makeValues(100)},
		{kind: segBracket, values: makeValues(100)},
	}
	_, err := cartesianExpand(segs, 25000)
	if err == nil {
		t.Fatal("expected error for 100×100×100 product")
	}
}

func TestCartesianExpand_SingleDimensionWithinLimit(t *testing.T) {
	t.Parallel()
	// Single bracket with 50 values, limit 100 — should succeed.
	segs := []segment{
		{kind: segLiteral, literal: "node"},
		{kind: segBracket, values: makeValues(50)},
	}
	got, err := cartesianExpand(segs, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 50 {
		t.Fatalf("got %d results, want 50", len(got))
	}
}

func TestCartesianExpand_EmptySegments_WithLimit(t *testing.T) {
	t.Parallel()
	got, err := cartesianExpand(nil, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0] != "" {
		t.Fatalf("empty segments should return [\"\"], got %v", got)
	}
}

func TestCartesianExpand_EmptyBracketValues(t *testing.T) {
	t.Parallel()
	segs := []segment{
		{kind: segLiteral, literal: "node"},
		{kind: segBracket, values: []string{}},
	}
	got, err := cartesianExpand(segs, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty bracket should return [], got %v", got)
	}
}

func TestCartesianExpand_LiteralOnly(t *testing.T) {
	t.Parallel()
	segs := []segment{
		{kind: segLiteral, literal: "node-static"},
	}
	got, err := cartesianExpand(segs, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0] != "node-static" {
		t.Fatalf("literal-only should return [\"node-static\"], got %v", got)
	}
}

// TestExpandTerm_MaxExpandThreaded verifies that expandTerm correctly
// threads maxExpand to cartesianExpand, rejecting large products.
func TestExpandTerm_MaxExpandThreaded(t *testing.T) {
	t.Parallel()
	// rack[1-200]node[1-200] = 40,000 — exceeds limit of 25,000.
	_, err := expandTerm("rack[1-200]node[1-200]", nil, 25000)
	if err == nil {
		t.Fatal("expected error when expandTerm product exceeds maxExpand")
	}
	if !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("error should mention 'exceeds limit', got: %v", err)
	}
}

// TestExpandTerm_MaxExpandWithinLimit verifies expandTerm succeeds when
// the product is within the limit.
func TestExpandTerm_MaxExpandWithinLimit(t *testing.T) {
	t.Parallel()
	got, err := expandTerm("rack[1-5]node[1-5]", nil, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 25 {
		t.Fatalf("got %d results, want 25", len(got))
	}
}

// TestExpandTerm_GroupBypassesMaxExpand verifies that group references
// are not affected by maxExpand (groups are pre-computed).
func TestExpandTerm_GroupBypassesMaxExpand(t *testing.T) {
	t.Parallel()
	resolver := &testGroupResolver{
		groups: map[string][]string{
			":big": makeValues(100),
		},
	}
	got, err := expandTerm("@big", resolver, 10)
	if err != nil {
		t.Fatalf("group references should bypass maxExpand: %v", err)
	}
	if len(got) != 100 {
		t.Fatalf("got %d, want 100", len(got))
	}
}

// TestExpandTerm_ExactNameIgnoresMaxExpand verifies that exact names
// (no brackets) always succeed regardless of maxExpand.
func TestExpandTerm_ExactNameIgnoresMaxExpand(t *testing.T) {
	t.Parallel()
	got, err := expandTerm("mds-01", nil, 0)
	if err != nil {
		t.Fatalf("exact name should succeed with any maxExpand: %v", err)
	}
	if len(got) != 1 || got[0] != "mds-01" {
		t.Fatalf("got %v, want [\"mds-01\"]", got)
	}
}

// --- Benchmark: verify no OOM for large rejected patterns ---

func BenchmarkCartesianExpand_Rejected(b *testing.B) {
	segs := []segment{
		{kind: segBracket, values: makeValues(10000)},
		{kind: segBracket, values: makeValues(10000)},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = cartesianExpand(segs, 25000)
	}
}

// makeValues generates n string values: "0", "1", ..., "n-1".
func makeValues(n int) []string {
	vals := make([]string, n)
	for i := range vals {
		vals[i] = strings.Repeat("x", 1) // minimal string
	}
	return vals
}
