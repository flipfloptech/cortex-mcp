package nodeset

import (
	"sort"
	"testing"
)

// --- Construction ---

func TestNodeSet_NewExact(t *testing.T) {
	t.Parallel()
	ns, err := New("mds-01")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ns.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", ns.Len())
	}
	if !ns.Contains("mds-01") {
		t.Fatal("should contain mds-01")
	}
}

func TestNodeSet_NewBracketRange(t *testing.T) {
	t.Parallel()
	ns, err := New("node[1-5]")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ns.Len() != 5 {
		t.Fatalf("Len() = %d, want 5", ns.Len())
	}
	for i := 1; i <= 5; i++ {
		if !ns.Contains("node" + string(rune('0'+i))) {
			t.Fatalf("should contain node%d", i)
		}
	}
}

func TestNodeSet_NewCommaUnion(t *testing.T) {
	t.Parallel()
	ns, err := New("mds[1-2],oss[1-3]")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ns.Len() != 5 {
		t.Fatalf("Len() = %d, want 5", ns.Len())
	}
}

func TestNodeSet_NewDifference(t *testing.T) {
	t.Parallel()
	ns, err := New("node[1-10]!node[5-7]")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ns.Len() != 7 {
		t.Fatalf("Len() = %d, want 7", ns.Len())
	}
	if ns.Contains("node5") || ns.Contains("node6") || ns.Contains("node7") {
		t.Fatal("should not contain excluded nodes")
	}
}

func TestNodeSet_NewFromSlice(t *testing.T) {
	t.Parallel()
	ns := NewFromSlice([]string{"a", "b", "c", "a"})
	if ns.Len() != 3 {
		t.Fatalf("Len() = %d, want 3 (deduped)", ns.Len())
	}
}

func TestNodeSet_NewEmpty(t *testing.T) {
	t.Parallel()
	_, err := New("")
	if err == nil {
		t.Fatal("expected error for empty pattern")
	}
}

func TestNodeSet_NewWithGroup(t *testing.T) {
	t.Parallel()
	resolver := &testGroupResolver{
		groups: map[string][]string{
			":mds": {"mds1", "mds2", "mds3"},
		},
	}
	ns, err := New("@mds", WithGroupResolver(resolver))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ns.Len() != 3 {
		t.Fatalf("Len() = %d, want 3", ns.Len())
	}
}

func TestNodeSet_NewMaxExpandExceeded(t *testing.T) {
	t.Parallel()
	_, err := New("node[1-100000]", WithMaxExpand(100))
	if err == nil {
		t.Fatal("expected error when expansion exceeds limit")
	}
}

// --- Nodes (sorted) ---

func TestNodeSet_Nodes(t *testing.T) {
	t.Parallel()
	ns, _ := New("node[3,1,2]")
	got := ns.Nodes()
	want := []string{"node1", "node2", "node3"}
	if len(got) != len(want) {
		t.Fatalf("Nodes() len = %d, want %d", len(got), len(want))
	}
	for i, g := range got {
		if g != want[i] {
			t.Fatalf("Nodes()[%d] = %q, want %q", i, g, want[i])
		}
	}
}

// --- Set Operations (method API) ---

func TestNodeSet_Union(t *testing.T) {
	t.Parallel()
	a, _ := New("node[1-3]")
	b, _ := New("node[2-5]")
	c := a.Union(b)
	if c.Len() != 5 {
		t.Fatalf("Union Len() = %d, want 5", c.Len())
	}
}

func TestNodeSet_Difference(t *testing.T) {
	t.Parallel()
	a, _ := New("node[1-5]")
	b, _ := New("node[3-5]")
	c := a.Difference(b)
	if c.Len() != 2 {
		t.Fatalf("Difference Len() = %d, want 2", c.Len())
	}
	got := c.Nodes()
	want := []string{"node1", "node2"}
	if len(got) != len(want) {
		t.Fatalf("Nodes() = %v, want %v", got, want)
	}
}

func TestNodeSet_Intersection(t *testing.T) {
	t.Parallel()
	a, _ := New("node[1-5]")
	b, _ := New("node[3-8]")
	c := a.Intersection(b)
	if c.Len() != 3 {
		t.Fatalf("Intersection Len() = %d, want 3", c.Len())
	}
}

func TestNodeSet_SymmetricDifference(t *testing.T) {
	t.Parallel()
	a, _ := New("node[1-5]")
	b, _ := New("node[3-8]")
	c := a.SymmetricDifference(b)
	// a-only: 1,2; b-only: 6,7,8 → 5 nodes.
	if c.Len() != 5 {
		t.Fatalf("SymmetricDifference Len() = %d, want 5", c.Len())
	}
}

// --- Subset/Superset/Equal ---

func TestNodeSet_IsSubset(t *testing.T) {
	t.Parallel()
	a, _ := New("node[2-3]")
	b, _ := New("node[1-5]")
	if !a.IsSubset(b) {
		t.Fatal("node[2-3] should be subset of node[1-5]")
	}
	if b.IsSubset(a) {
		t.Fatal("node[1-5] should NOT be subset of node[2-3]")
	}
}

func TestNodeSet_IsSuperset(t *testing.T) {
	t.Parallel()
	a, _ := New("node[1-5]")
	b, _ := New("node[2-3]")
	if !a.IsSuperset(b) {
		t.Fatal("node[1-5] should be superset of node[2-3]")
	}
}

func TestNodeSet_Equal(t *testing.T) {
	t.Parallel()
	a, _ := New("node[1-3]")
	b, _ := New("node[1,2,3]")
	if !a.Equal(b) {
		t.Fatal("node[1-3] should equal node[1,2,3]")
	}
}

func TestNodeSet_NotEqual(t *testing.T) {
	t.Parallel()
	a, _ := New("node[1-3]")
	b, _ := New("node[1-4]")
	if a.Equal(b) {
		t.Fatal("node[1-3] should not equal node[1-4]")
	}
}

// --- Copy ---

func TestNodeSet_Copy(t *testing.T) {
	t.Parallel()
	a, _ := New("node[1-3]")
	b := a.Copy()
	b.AddNode("node99")
	if a.Contains("node99") {
		t.Fatal("original was mutated by copy")
	}
	if !b.Contains("node99") {
		t.Fatal("copy should contain added node")
	}
}

// --- Add / Remove ---

func TestNodeSet_AddPattern(t *testing.T) {
	t.Parallel()
	ns, _ := New("node[1-3]")
	err := ns.Add("node[5-7]")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ns.Len() != 6 {
		t.Fatalf("Len() = %d, want 6", ns.Len())
	}
}

func TestNodeSet_RemovePattern(t *testing.T) {
	t.Parallel()
	ns, _ := New("node[1-10]")
	err := ns.Remove("node[5-7]")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ns.Len() != 7 {
		t.Fatalf("Len() = %d, want 7", ns.Len())
	}
}

// --- String (fold) ---

func TestNodeSet_String(t *testing.T) {
	t.Parallel()
	ns, _ := New("node[1-3]")
	got := ns.String()
	if got != "node[1-3]" {
		t.Fatalf("String() = %q, want %q", got, "node[1-3]")
	}
}

func TestNodeSet_StringComplex(t *testing.T) {
	t.Parallel()
	ns, _ := New("mds[1-2],oss[1-3]")
	got := ns.String()
	want := "mds[1-2],oss[1-3]"
	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

// --- Contiguous ---

func TestNodeSet_Contiguous(t *testing.T) {
	t.Parallel()
	ns, _ := New("node[1-3,7-9]")
	parts := ns.Contiguous()
	if len(parts) != 2 {
		t.Fatalf("Contiguous() = %d parts, want 2", len(parts))
	}
	sort.Slice(parts, func(i, j int) bool {
		return parts[i].Nodes()[0] < parts[j].Nodes()[0]
	})
	if parts[0].Len() != 3 || parts[1].Len() != 3 {
		t.Fatalf("parts have %d and %d nodes, want 3 and 3", parts[0].Len(), parts[1].Len())
	}
}

// --- Split ---

func TestNodeSet_Split(t *testing.T) {
	t.Parallel()
	ns, _ := New("node[1-10]")
	parts := ns.Split(3)
	if len(parts) != 3 {
		t.Fatalf("Split(3) = %d parts, want 3", len(parts))
	}
	total := 0
	for _, p := range parts {
		total += p.Len()
	}
	if total != 10 {
		t.Fatalf("total nodes across splits = %d, want 10", total)
	}
}

// --- Match function ---

func TestMatch_Exact(t *testing.T) {
	t.Parallel()
	ok, err := Match("node1", "node1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("exact match should succeed")
	}
}

func TestMatch_Range(t *testing.T) {
	t.Parallel()
	ok, err := Match("node[1-5]", "node3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("range match should succeed")
	}
}

func TestMatch_Glob(t *testing.T) {
	t.Parallel()
	ok, err := Match("mds-*", "mds-01")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("glob match should succeed")
	}
}

func TestMatch_GlobQuestion(t *testing.T) {
	t.Parallel()
	ok, err := Match("node?", "node5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("? glob should match single char")
	}
}

func TestMatch_NoMatch(t *testing.T) {
	t.Parallel()
	ok, err := Match("node[1-5]", "node6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("should not match node6 for node[1-5]")
	}
}

func TestMatch_Difference(t *testing.T) {
	t.Parallel()
	ok, err := Match("node[1-10]!node[5-7]", "node5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("node5 should be excluded by difference")
	}
	ok, err = Match("node[1-10]!node[5-7]", "node3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("node3 should match")
	}
}

func TestMatch_IPPattern(t *testing.T) {
	t.Parallel()
	ok, err := Match("10.0.1.*", "10.0.1.25")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("IP glob should match")
	}
}

func TestMatch_IPBracketRange(t *testing.T) {
	t.Parallel()
	ok, err := Match("10.0.1.[1-50]", "10.0.1.25")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("IP bracket range should match")
	}
	ok, err = Match("10.0.1.[1-50]", "10.0.1.51")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("10.0.1.51 should NOT match 10.0.1.[1-50]")
	}
}

// --- Expand convenience ---

func TestExpand_Basic(t *testing.T) {
	t.Parallel()
	got, err := Expand("node[1-3]")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"node1", "node2", "node3"}
	if len(got) != len(want) {
		t.Fatalf("Expand() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Expand()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestExpand_RealWorldHPC(t *testing.T) {
	t.Parallel()
	got, err := Expand("memp-aqr-mvm0[0-3],memp-aqs-oss24")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{
		"memp-aqr-mvm00", "memp-aqr-mvm01",
		"memp-aqr-mvm02", "memp-aqr-mvm03",
		"memp-aqs-oss24",
	}
	if len(got) != len(want) {
		t.Fatalf("Expand() len = %d, want %d", len(got), len(want))
	}
}

// --- Complex combined expression ---

func TestNodeSet_ComplexExpression(t *testing.T) {
	t.Parallel()
	resolver := &testGroupResolver{
		groups: map[string][]string{
			":compute": {"node1", "node2", "node3", "node4", "node5",
				"node6", "node7", "node8", "node9", "node10"},
		},
	}
	ns, err := New("@compute!node[8-10]", WithGroupResolver(resolver))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ns.Len() != 7 {
		t.Fatalf("Len() = %d, want 7", ns.Len())
	}
	if ns.Contains("node8") || ns.Contains("node9") || ns.Contains("node10") {
		t.Fatal("should not contain excluded nodes")
	}
}
