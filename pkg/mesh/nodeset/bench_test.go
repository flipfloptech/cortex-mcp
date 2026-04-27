package nodeset

import (
	"fmt"
	"strconv"
	"testing"
)

func BenchmarkExpand(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, err := Expand("node[1-100]")
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMatch(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, err := Match("node[1-1000]", "node500")
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFold(b *testing.B) {
	nodes := make([]string, 1000)
	for i := range nodes {
		nodes[i] = fmt.Sprintf("node%04d", i+1)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Fold(nodes)
	}
}

func BenchmarkDifference(b *testing.B) {
	a, _ := New("node[1-500]")
	bset, _ := New("node[200-300]")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.Difference(bset)
	}
}

func BenchmarkUnion(b *testing.B) {
	a, _ := New("node[1-500]")
	c, _ := New("node[300-800]")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.Union(c)
	}
}

func BenchmarkNew(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = New("node[1-100]")
	}
}

func BenchmarkString(b *testing.B) {
	a, _ := New("node[1-500]")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.String()
	}
}

func BenchmarkNodes(b *testing.B) {
	a, _ := New("node[1-500]")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.Nodes()
	}
}

func BenchmarkIntersection(b *testing.B) {
	a, _ := New("node[1-500]")
	c, _ := New("node[300-800]")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.Intersection(c)
	}
}

func BenchmarkSymmetricDifference(b *testing.B) {
	a, _ := New("node[1-500]")
	c, _ := New("node[300-800]")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.SymmetricDifference(c)
	}
}

func BenchmarkIsSubset(b *testing.B) {
	a, _ := New("node[1-500]")
	c, _ := New("node[300-400]")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.IsSubset(a)
	}
}

func BenchmarkIsSuperset(b *testing.B) {
	a, _ := New("node[1-500]")
	c, _ := New("node[300-400]")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.IsSuperset(c)
	}
}

func BenchmarkEqual(b *testing.B) {
	a, _ := New("node[1-500]")
	c, _ := New("node[1-500]")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.Equal(c)
	}
}

func BenchmarkCopy(b *testing.B) {
	a, _ := New("node[1-500]")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.Copy()
	}
}

func BenchmarkAdd(b *testing.B) {
	a := NewFromSlice([]string{})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.Add("node[1-100]")
	}
}

func BenchmarkRemove(b *testing.B) {
	a, _ := New("node[1-500]")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.Remove("node[100-200]")
	}
}

func BenchmarkAddNode(b *testing.B) {
	nodeNames := make([]string, 100)
	for j := 0; j < 100; j++ {
		nodeNames[j] = "node" + strconv.Itoa(j)
	}
	a := NewFromSlice([]string{})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < 100; j++ {
			a.AddNode(nodeNames[j])
		}
	}
}

func BenchmarkContains(b *testing.B) {
	a, _ := New("node[1-500]")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.Contains("node250")
	}
}

func BenchmarkSplit(b *testing.B) {
	a, _ := New("node[1-500]")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.Split(5)
	}
}

func BenchmarkContiguous(b *testing.B) {
	a, _ := New("node[1-500,600-700,800-900]")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.Contiguous()
	}
}

func BenchmarkLen(b *testing.B) {
	a, _ := New("node[1-500]")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.Len()
	}
}

func BenchmarkNewFromSlice(b *testing.B) {
	nodes := make([]string, 500)
	for i := range nodes {
		nodes[i] = fmt.Sprintf("node%d", i)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewFromSlice(nodes)
	}
}

func BenchmarkWithGroupResolver(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = WithGroupResolver(nil)
	}
}

func BenchmarkWithMaxExpand(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = WithMaxExpand(100)
	}
}

func BenchmarkDefaultOptions(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = defaultOptions()
	}
}

func BenchmarkError(b *testing.B) {
	err := &parseError{s: "test error"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = err.Error()
	}
}

func BenchmarkParseInt(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = parseInt("12345")
	}
}

func BenchmarkParseIntAtoi(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = parseIntAtoi("12345")
	}
}

func BenchmarkFormatPadded(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = formatPadded(5, 3)
	}
}

func BenchmarkParseExpr(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = parseExpr("node[1-100]", nil, 1000000)
	}
}

func BenchmarkTokenize(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _, _ = tokenize("rack[1-4]node[1-100]")
	}
}

func BenchmarkDetectStep(b *testing.B) {
	values := []int{1, 3, 5, 7, 9}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = detectStep(values)
	}
}

func BenchmarkDetectPadding(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = detectPadding("005")
	}
}

func BenchmarkParseBracketExpr(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _, _ = parseBracketExpr("1-100")
	}
}

func BenchmarkSplitBrackets(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = splitBrackets("rack[1-4]node[1-100]")
	}
}

func BenchmarkCartesianExpand(b *testing.B) {
	segs := []segment{
		{kind: segLiteral, literal: "rack"},
		{kind: 1, values: []string{"1", "2"}}, // segBracket = 1
		{kind: segLiteral, literal: "node"},
		{kind: 1, values: []string{"1", "2"}},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = cartesianExpand(segs, 1000000)
	}
}

func BenchmarkExpandTerm(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = expandTerm("node[1-100]", nil, 1000000)
	}
}

func BenchmarkExpandGroup(b *testing.B) {
	// Need a resolver to test this
	for i := 0; i < b.N; i++ {
		_, _ = expandGroup("@test", nil)
	}
}

func BenchmarkFoldGroup(b *testing.B) {
	nodes := []string{"1", "2", "3", "4", "5"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = foldGroup("node", nodes)
	}
}

func BenchmarkGroupByPrefix(b *testing.B) {
	nodes := []string{"node1", "node2", "rack1", "rack2"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = groupByPrefix(nodes)
	}
}

func BenchmarkFoldRanges(b *testing.B) {
	rs := newRangeSet(0)
	rs.Add(1, 100)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = rs.foldRanges()
	}
}

func BenchmarkNewRangeSet(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = newRangeSet(3)
	}
}

func BenchmarkIsEmpty(b *testing.B) {
	rs := newRangeSet(0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = rs.IsEmpty()
	}
}

func BenchmarkExpandStrings(b *testing.B) {
	rs := newRangeSet(3)
	rs.Add(1, 100)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = rs.ExpandStrings()
	}
}

func BenchmarkFormatInt(b *testing.B) {
	rs := newRangeSet(3)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = rs.formatInt(5)
	}
}

func BenchmarkSetToSortedSlice(b *testing.B) {
	m := map[string]struct{}{
		"node1": {}, "node2": {}, "node3": {},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = setToSortedSlice(m)
	}
}

func BenchmarkSliceToSet(b *testing.B) {
	s := []string{"node1", "node2", "node3"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = sliceToSet(s)
	}
}

func BenchmarkContainsGlob(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = containsGlob("node*")
	}
}

func BenchmarkContainsBracket(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = containsBracket("node[1-100]")
	}
}

func BenchmarkContainsSetOrGroup(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = containsSetOrGroup("node[1-100],rack[1-4]")
	}
}

func BenchmarkSplitTrailingNumber(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = splitTrailingNumber("node005")
	}
}

func BenchmarkContainsComplex(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = containsComplex("node[1-100]")
	}
}

func BenchmarkMatchFast(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = matchFast("node[1-100]", "node50")
	}
}

func BenchmarkMatchSimplePattern(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = matchSimplePattern("node*", "node50")
	}
}

func BenchmarkCheckNumStr(b *testing.B) {
	ints := []int{1, 2, 3, 4, 5}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = checkNumStr("005", ints, 3)
	}
}
