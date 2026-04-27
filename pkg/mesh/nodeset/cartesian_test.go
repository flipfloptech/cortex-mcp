package nodeset

import (
	"fmt"
	"testing"
)

// --- Correctness: cartesianExpand with strings.Builder ---

// TestCartesianExpand_SingleLiteral verifies a single literal segment
// passes through unchanged.
func TestCartesianExpand_SingleLiteral(t *testing.T) {
	t.Parallel()

	segs := []segment{{kind: segLiteral, literal: "hello"}}
	got, err := cartesianExpand(segs, 0)
	if err != nil {
		t.Fatalf("cartesianExpand: %v", err)
	}
	if len(got) != 1 || got[0] != "hello" {
		t.Fatalf("got %v, want [hello]", got)
	}
}

// TestCartesianExpand_SingleBracket verifies a single bracket segment
// expands correctly.
func TestCartesianExpand_SingleBracket(t *testing.T) {
	t.Parallel()

	segs := []segment{{kind: segBracket, values: []string{"1", "2", "3"}}}
	got, err := cartesianExpand(segs, 0)
	if err != nil {
		t.Fatalf("cartesianExpand: %v", err)
	}
	want := []string{"1", "2", "3"}
	if len(got) != len(want) {
		t.Fatalf("got %d items, want %d", len(got), len(want))
	}
	for i, g := range got {
		if g != want[i] {
			t.Fatalf("got[%d] = %q, want %q", i, g, want[i])
		}
	}
}

// TestCartesianExpand_LiteralThenBracket verifies literal+bracket concatenation.
func TestCartesianExpand_LiteralThenBracket(t *testing.T) {
	t.Parallel()

	segs := []segment{
		{kind: segLiteral, literal: "node"},
		{kind: segBracket, values: []string{"1", "2", "3"}},
	}
	got, err := cartesianExpand(segs, 0)
	if err != nil {
		t.Fatalf("cartesianExpand: %v", err)
	}
	want := []string{"node1", "node2", "node3"}
	if len(got) != len(want) {
		t.Fatalf("got %d items, want %d", len(got), len(want))
	}
	for i, g := range got {
		if g != want[i] {
			t.Fatalf("got[%d] = %q, want %q", i, g, want[i])
		}
	}
}

// TestCartesianExpand_MultiDimensional verifies the Cartesian product
// across multiple bracket segments interleaved with literals.
func TestCartesianExpand_MultiDimensional(t *testing.T) {
	t.Parallel()

	// rack[1-2]node[a,b] → rack1nodea, rack1nodeb, rack2nodea, rack2nodeb
	segs := []segment{
		{kind: segLiteral, literal: "rack"},
		{kind: segBracket, values: []string{"1", "2"}},
		{kind: segLiteral, literal: "node"},
		{kind: segBracket, values: []string{"a", "b"}},
	}
	got, err := cartesianExpand(segs, 0)
	if err != nil {
		t.Fatalf("cartesianExpand: %v", err)
	}
	want := []string{"rack1nodea", "rack1nodeb", "rack2nodea", "rack2nodeb"}
	if len(got) != len(want) {
		t.Fatalf("got %d items, want %d", len(got), len(want))
	}
	for i, g := range got {
		if g != want[i] {
			t.Fatalf("got[%d] = %q, want %q", i, g, want[i])
		}
	}
}

// TestCartesianExpand_TrailingLiteral verifies bracket+literal suffix.
func TestCartesianExpand_TrailingLiteral(t *testing.T) {
	t.Parallel()

	// [1,2].example.com → 1.example.com, 2.example.com
	segs := []segment{
		{kind: segBracket, values: []string{"1", "2"}},
		{kind: segLiteral, literal: ".example.com"},
	}
	got, err := cartesianExpand(segs, 0)
	if err != nil {
		t.Fatalf("cartesianExpand: %v", err)
	}
	want := []string{"1.example.com", "2.example.com"}
	if len(got) != len(want) {
		t.Fatalf("got %d items, want %d", len(got), len(want))
	}
	for i, g := range got {
		if g != want[i] {
			t.Fatalf("got[%d] = %q, want %q", i, g, want[i])
		}
	}
}

// TestCartesianExpand_EmptySegments verifies no segments produces
// a single empty string.
func TestCartesianExpand_EmptySegments(t *testing.T) {
	t.Parallel()

	got, err := cartesianExpand(nil, 0)
	if err != nil {
		t.Fatalf("cartesianExpand: %v", err)
	}
	if len(got) != 1 || got[0] != "" {
		t.Fatalf("got %v, want [\"\"]", got)
	}
}

// TestCartesianExpand_ConsecutiveLiterals verifies multiple consecutive
// literal segments are concatenated correctly.
func TestCartesianExpand_ConsecutiveLiterals(t *testing.T) {
	t.Parallel()

	segs := []segment{
		{kind: segLiteral, literal: "hello"},
		{kind: segLiteral, literal: "-"},
		{kind: segLiteral, literal: "world"},
	}
	got, err := cartesianExpand(segs, 0)
	if err != nil {
		t.Fatalf("cartesianExpand: %v", err)
	}
	if len(got) != 1 || got[0] != "hello-world" {
		t.Fatalf("got %v, want [hello-world]", got)
	}
}

// TestCartesianExpand_ThreeDimensions verifies 3-way Cartesian product.
func TestCartesianExpand_ThreeDimensions(t *testing.T) {
	t.Parallel()

	// [a,b][1,2][x,y] → 8 combinations
	segs := []segment{
		{kind: segBracket, values: []string{"a", "b"}},
		{kind: segBracket, values: []string{"1", "2"}},
		{kind: segBracket, values: []string{"x", "y"}},
	}
	got, err := cartesianExpand(segs, 0)
	if err != nil {
		t.Fatalf("cartesianExpand: %v", err)
	}
	want := []string{
		"a1x", "a1y", "a2x", "a2y",
		"b1x", "b1y", "b2x", "b2y",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d items, want %d", len(got), len(want))
	}
	for i, g := range got {
		if g != want[i] {
			t.Fatalf("got[%d] = %q, want %q", i, g, want[i])
		}
	}
}

// --- Integration: full Expand pipeline produces correct results ---

// TestExpand_MatchesPreviousOutput verifies the full Expand pipeline
// produces identical output with the strings.Builder refactor.
func TestExpand_MatchesPreviousOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pattern string
		want    []string
	}{
		{
			name:    "simple range",
			pattern: "node[1-3]",
			want:    []string{"node1", "node2", "node3"},
		},
		{
			name:    "multi-dimensional",
			pattern: "rack[1-2]sw[01-02]",
			want:    []string{"rack1sw01", "rack1sw02", "rack2sw01", "rack2sw02"},
		},
		{
			name:    "padded",
			pattern: "oss[001-003]",
			want:    []string{"oss001", "oss002", "oss003"},
		},
		{
			name:    "stepped range",
			pattern: "node[1-9/3]",
			want:    []string{"node1", "node4", "node7"},
		},
		{
			name:    "plain name",
			pattern: "single-host",
			want:    []string{"single-host"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := Expand(tt.pattern)
			if err != nil {
				t.Fatalf("Expand(%q): %v", tt.pattern, err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d", len(got), len(tt.want))
			}
			for i, g := range got {
				if g != tt.want[i] {
					t.Fatalf("got[%d] = %q, want %q", i, g, tt.want[i])
				}
			}
		})
	}
}

// --- Benchmarks: allocation tracking ---

// BenchmarkCartesianExpand_MultiDim measures the allocation cost
// of the multi-dimensional Cartesian product (the hot path).
func BenchmarkCartesianExpand_MultiDim(b *testing.B) {
	// Simulate rack[1-10]node[1-100] — 1000 result strings.
	rackValues := make([]string, 10)
	for i := range rackValues {
		rackValues[i] = fmt.Sprintf("%d", i+1)
	}
	nodeValues := make([]string, 100)
	for i := range nodeValues {
		nodeValues[i] = fmt.Sprintf("%d", i+1)
	}

	segs := []segment{
		{kind: segLiteral, literal: "rack"},
		{kind: segBracket, values: rackValues},
		{kind: segLiteral, literal: "node"},
		{kind: segBracket, values: nodeValues},
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, _ = cartesianExpand(segs, 0)
	}
}

// BenchmarkCartesianExpand_LargeLinear measures single-dimension expansion.
func BenchmarkCartesianExpand_LargeLinear(b *testing.B) {
	// Simulate node[1-10000] — 10000 result strings.
	values := make([]string, 10000)
	for i := range values {
		values[i] = fmt.Sprintf("%d", i+1)
	}

	segs := []segment{
		{kind: segLiteral, literal: "node"},
		{kind: segBracket, values: values},
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, _ = cartesianExpand(segs, 0)
	}
}
