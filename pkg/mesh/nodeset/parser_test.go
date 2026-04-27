package nodeset

import (
	"fmt"
	"reflect"
	"sort"
	"testing"
)

// --- Bracket Expression Parsing ---

func TestParseBracketExpr_SimpleRange(t *testing.T) {
	t.Parallel()
	got, pad, err := parseBracketExpr("1-3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pad != 0 {
		t.Fatalf("padding = %d, want 0", pad)
	}
	want := []int{1, 2, 3}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseBracketExpr_PaddedRange(t *testing.T) {
	t.Parallel()
	got, pad, err := parseBracketExpr("01-03")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pad != 2 {
		t.Fatalf("padding = %d, want 2", pad)
	}
	want := []int{1, 2, 3}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseBracketExpr_List(t *testing.T) {
	t.Parallel()
	got, _, err := parseBracketExpr("1,3,5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []int{1, 3, 5}
	sort.Ints(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseBracketExpr_MixedRangeAndList(t *testing.T) {
	t.Parallel()
	got, _, err := parseBracketExpr("1-3,5,7-9")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []int{1, 2, 3, 5, 7, 8, 9}
	sort.Ints(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseBracketExpr_StepRange(t *testing.T) {
	t.Parallel()
	got, _, err := parseBracketExpr("1-9/2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []int{1, 3, 5, 7, 9}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseBracketExpr_SingleValue(t *testing.T) {
	t.Parallel()
	got, _, err := parseBracketExpr("5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []int{5}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseBracketExpr_InvalidReversed(t *testing.T) {
	t.Parallel()
	_, _, err := parseBracketExpr("5-2")
	if err == nil {
		t.Fatal("expected error for reversed range 5-2")
	}
}

func TestParseBracketExpr_EmptyExpr(t *testing.T) {
	t.Parallel()
	_, _, err := parseBracketExpr("")
	if err == nil {
		t.Fatal("expected error for empty expression")
	}
}

func TestParseBracketExpr_ZeroStep(t *testing.T) {
	t.Parallel()
	_, _, err := parseBracketExpr("1-9/0")
	if err == nil {
		t.Fatal("expected error for zero step")
	}
}

func TestParseBracketExpr_PaddedMixed(t *testing.T) {
	t.Parallel()
	got, pad, err := parseBracketExpr("01-03,05")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pad != 2 {
		t.Fatalf("padding = %d, want 2", pad)
	}
	want := []int{1, 2, 3, 5}
	sort.Ints(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// --- Expand Term (single pattern piece without operators) ---

func TestExpandTerm_ExactName(t *testing.T) {
	t.Parallel()
	got, err := expandTerm("mds-01", nil, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"mds-01"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExpandTerm_SingleBracket(t *testing.T) {
	t.Parallel()
	got, err := expandTerm("node[1-3]", nil, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sort.Strings(got)
	want := []string{"node1", "node2", "node3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExpandTerm_PaddedBracket(t *testing.T) {
	t.Parallel()
	got, err := expandTerm("node[01-03]", nil, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sort.Strings(got)
	want := []string{"node01", "node02", "node03"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExpandTerm_MultiBracket(t *testing.T) {
	t.Parallel()
	got, err := expandTerm("rack[1-2]node[1-3]", nil, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sort.Strings(got)
	want := []string{
		"rack1node1", "rack1node2", "rack1node3",
		"rack2node1", "rack2node2", "rack2node3",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExpandTerm_TrailingBracket(t *testing.T) {
	t.Parallel()
	got, err := expandTerm("memp-aqr-mvm0[0-3]", nil, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sort.Strings(got)
	want := []string{
		"memp-aqr-mvm00", "memp-aqr-mvm01",
		"memp-aqr-mvm02", "memp-aqr-mvm03",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExpandTerm_UnclosedBracket(t *testing.T) {
	t.Parallel()
	_, err := expandTerm("node[1-3", nil, 0)
	if err == nil {
		t.Fatal("expected error for unclosed bracket")
	}
}

func TestExpandTerm_GlobPassthrough(t *testing.T) {
	t.Parallel()
	got, err := expandTerm("mds-*", nil, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Globs pass through as-is.
	want := []string{"mds-*"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExpandTerm_BracketOnly(t *testing.T) {
	t.Parallel()
	got, err := expandTerm("[1-3]", nil, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sort.Strings(got)
	want := []string{"1", "2", "3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// --- Expand Term: Groups ---

func TestExpandTerm_GroupReference(t *testing.T) {
	t.Parallel()
	resolver := &testGroupResolver{
		groups: map[string][]string{
			":compute": {"node1", "node2", "node3"},
		},
	}
	got, err := expandTerm("@compute", resolver, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sort.Strings(got)
	want := []string{"node1", "node2", "node3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExpandTerm_SourceGroupReference(t *testing.T) {
	t.Parallel()
	resolver := &testGroupResolver{
		groups: map[string][]string{
			"slurm:compute": {"node1", "node5"},
		},
	}
	got, err := expandTerm("@slurm:compute", resolver, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sort.Strings(got)
	want := []string{"node1", "node5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExpandTerm_GroupNoResolver(t *testing.T) {
	t.Parallel()
	_, err := expandTerm("@compute", nil, 0)
	if err == nil {
		t.Fatal("expected error when no group resolver")
	}
}

func TestExpandTerm_GroupUnknown(t *testing.T) {
	t.Parallel()
	resolver := &testGroupResolver{
		groups: map[string][]string{},
	}
	_, err := expandTerm("@nonexistent", resolver, 0)
	if err == nil {
		t.Fatal("expected error for unknown group")
	}
}

// --- Full Expression Parsing (with operators) ---

func TestParseExpr_SimpleCommaUnion(t *testing.T) {
	t.Parallel()
	got, err := parseExpr("mds-01,oss-01", nil, 25000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sort.Strings(got)
	want := []string{"mds-01", "oss-01"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseExpr_CommaWithBrackets(t *testing.T) {
	t.Parallel()
	got, err := parseExpr("mds[1-2],oss[1-3]", nil, 25000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sort.Strings(got)
	want := []string{"mds1", "mds2", "oss1", "oss2", "oss3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseExpr_DifferenceOperator(t *testing.T) {
	t.Parallel()
	got, err := parseExpr("node[1-10]!node[5-7]", nil, 25000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sort.Strings(got)
	want := []string{"node1", "node10", "node2", "node3", "node4", "node8", "node9"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseExpr_IntersectionOperator(t *testing.T) {
	t.Parallel()
	got, err := parseExpr("node[1-10]&node[5-15]", nil, 25000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sort.Strings(got)
	want := []string{"node10", "node5", "node6", "node7", "node8", "node9"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseExpr_SymmetricDifferenceOperator(t *testing.T) {
	t.Parallel()
	got, err := parseExpr("node[1-3]^node[2-4]", nil, 25000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sort.Strings(got)
	want := []string{"node1", "node4"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseExpr_RealWorldHPCPattern(t *testing.T) {
	t.Parallel()
	got, err := parseExpr("memp-aqr-mvm0[0-3],memp-aqs-oss24", nil, 25000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sort.Strings(got)
	want := []string{
		"memp-aqr-mvm00", "memp-aqr-mvm01",
		"memp-aqr-mvm02", "memp-aqr-mvm03",
		"memp-aqs-oss24",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseExpr_MaxExpandExceeded(t *testing.T) {
	t.Parallel()
	_, err := parseExpr("node[1-100000]", nil, 25000)
	if err == nil {
		t.Fatal("expected error when expansion exceeds limit")
	}
}

func TestParseExpr_EmptyPattern(t *testing.T) {
	t.Parallel()
	_, err := parseExpr("", nil, 25000)
	if err == nil {
		t.Fatal("expected error for empty pattern")
	}
}

func TestParseExpr_ChainedOperators(t *testing.T) {
	t.Parallel()
	// node[1-10] - node[8-10] - node[1-2] = node[3-7]
	got, err := parseExpr("node[1-10]!node[8-10]!node[1-2]", nil, 25000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sort.Strings(got)
	want := []string{"node3", "node4", "node5", "node6", "node7"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseExpr_GroupInExpression(t *testing.T) {
	t.Parallel()
	resolver := &testGroupResolver{
		groups: map[string][]string{
			":compute": {"node1", "node2", "node3", "node4", "node5"},
		},
	}
	got, err := parseExpr("@compute!node[4-5]", resolver, 25000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sort.Strings(got)
	want := []string{"node1", "node2", "node3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// --- Test helpers ---

type testGroupResolver struct {
	groups map[string][]string // key is "source:group" or ":group" for default
}

func (r *testGroupResolver) Resolve(source, group string) ([]string, error) {
	key := source + ":" + group
	nodes, ok := r.groups[key]
	if !ok {
		return nil, fmt.Errorf("unknown group: %s", key)
	}
	return nodes, nil
}

func (r *testGroupResolver) List(source string) ([]string, error) {
	var groups []string
	prefix := source + ":"
	for k := range r.groups {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			groups = append(groups, k[len(prefix):])
		}
	}
	return groups, nil
}
