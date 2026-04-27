package nodeset

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// defaultMaxExpand is the maximum number of nodes from a single Expand call.
const defaultMaxExpand = 25000

// options holds configuration for NodeSet operations.
type options struct {
	resolver  GroupResolver
	maxExpand int
}

// Option configures NodeSet behavior.
type Option func(*options)

// WithGroupResolver sets the GroupResolver for @group expansion.
func WithGroupResolver(r GroupResolver) Option {
	return func(o *options) {
		o.resolver = r
	}
}

// WithMaxExpand sets the maximum number of expanded nodes.
// Default: 25000.
func WithMaxExpand(n int) Option {
	return func(o *options) {
		o.maxExpand = n
	}
}

func defaultOptions() *options {
	return &options{
		maxExpand: defaultMaxExpand,
	}
}

// NodeSet is a set-like container for node names, providing
// ClusterShell-compatible pattern expansion, folding, and set operations.
//
// Patterns supported:
//   - Brackets: node[1-3,5,7-9/2]
//   - Multi-dimensional: rack[1-2]node[1-3]
//   - Globs: mds-*, node?
//   - Set operations: node[1-10]!node[5-7], &, ^
//   - Groups: @compute, @source:group
//   - Union: node[1-3],oss[1-5]
type NodeSet struct {
	nodes map[string]struct{}
}

// New creates a NodeSet by parsing and expanding a pattern string.
func New(pattern string, opts ...Option) (*NodeSet, error) {
	o := defaultOptions()
	for _, opt := range opts {
		opt(o)
	}

	nodes, err := parseExpr(pattern, o.resolver, o.maxExpand)
	if err != nil {
		return nil, err
	}

	ns := &NodeSet{nodes: make(map[string]struct{}, len(nodes))}
	for _, n := range nodes {
		ns.nodes[n] = struct{}{}
	}
	return ns, nil
}

// NewFromSlice creates a NodeSet from an explicit list of node names.
// Duplicates are silently deduplicated.
func NewFromSlice(nodes []string) *NodeSet {
	ns := &NodeSet{nodes: make(map[string]struct{}, len(nodes))}
	for _, n := range nodes {
		ns.nodes[n] = struct{}{}
	}
	return ns
}

// Contains reports whether the given node name is in the set.
func (ns *NodeSet) Contains(node string) bool {
	_, ok := ns.nodes[node]
	return ok
}

// Len returns the number of nodes in the set.
func (ns *NodeSet) Len() int {
	return len(ns.nodes)
}

// Nodes returns all node names in sorted order.
func (ns *NodeSet) Nodes() []string {
	result := make([]string, 0, len(ns.nodes))
	for n := range ns.nodes {
		result = append(result, n)
	}
	sort.Strings(result)
	return result
}

// String returns the folded compact representation of the NodeSet.
func (ns *NodeSet) String() string {
	return Fold(ns.Nodes())
}

// Add expands a pattern and adds all resulting nodes to the set.
func (ns *NodeSet) Add(pattern string, opts ...Option) error {
	o := defaultOptions()
	for _, opt := range opts {
		opt(o)
	}
	nodes, err := parseExpr(pattern, o.resolver, o.maxExpand)
	if err != nil {
		return err
	}
	for _, n := range nodes {
		ns.nodes[n] = struct{}{}
	}
	return nil
}

// AddNode adds a single node name to the set.
func (ns *NodeSet) AddNode(node string) {
	ns.nodes[node] = struct{}{}
}

// Remove expands a pattern and removes all resulting nodes from the set.
func (ns *NodeSet) Remove(pattern string, opts ...Option) error {
	o := defaultOptions()
	for _, opt := range opts {
		opt(o)
	}
	nodes, err := parseExpr(pattern, o.resolver, o.maxExpand)
	if err != nil {
		return err
	}
	for _, n := range nodes {
		delete(ns.nodes, n)
	}
	return nil
}

// Union returns a new NodeSet containing all nodes from both sets.
func (ns *NodeSet) Union(other *NodeSet) *NodeSet {
	result := ns.Copy()
	for n := range other.nodes {
		result.nodes[n] = struct{}{}
	}
	return result
}

// Difference returns a new NodeSet with nodes in ns but not in other.
func (ns *NodeSet) Difference(other *NodeSet) *NodeSet {
	result := ns.Copy()
	for n := range other.nodes {
		delete(result.nodes, n)
	}
	return result
}

// Intersection returns a new NodeSet with nodes in both sets.
func (ns *NodeSet) Intersection(other *NodeSet) *NodeSet {
	result := &NodeSet{nodes: make(map[string]struct{})}
	for n := range ns.nodes {
		if _, ok := other.nodes[n]; ok {
			result.nodes[n] = struct{}{}
		}
	}
	return result
}

// SymmetricDifference returns a new NodeSet with nodes in either set but not both.
func (ns *NodeSet) SymmetricDifference(other *NodeSet) *NodeSet {
	result := &NodeSet{nodes: make(map[string]struct{})}
	for n := range ns.nodes {
		if _, ok := other.nodes[n]; !ok {
			result.nodes[n] = struct{}{}
		}
	}
	for n := range other.nodes {
		if _, ok := ns.nodes[n]; !ok {
			result.nodes[n] = struct{}{}
		}
	}
	return result
}

// IsSubset reports whether all nodes in ns are in other.
func (ns *NodeSet) IsSubset(other *NodeSet) bool {
	for n := range ns.nodes {
		if _, ok := other.nodes[n]; !ok {
			return false
		}
	}
	return true
}

// IsSuperset reports whether ns contains all nodes in other.
func (ns *NodeSet) IsSuperset(other *NodeSet) bool {
	return other.IsSubset(ns)
}

// Equal reports whether ns and other contain the same nodes.
func (ns *NodeSet) Equal(other *NodeSet) bool {
	if len(ns.nodes) != len(other.nodes) {
		return false
	}
	return ns.IsSubset(other)
}

// Copy returns a deep copy of the NodeSet.
func (ns *NodeSet) Copy() *NodeSet {
	result := &NodeSet{nodes: make(map[string]struct{}, len(ns.nodes))}
	for n := range ns.nodes {
		result.nodes[n] = struct{}{}
	}
	return result
}

// Contiguous splits the NodeSet into contiguous sub-sets.
// Each sub-set contains nodes with consecutive numeric suffixes
// sharing the same prefix.
func (ns *NodeSet) Contiguous() []*NodeSet {
	groups := groupByPrefix(ns.Nodes())

	var result []*NodeSet
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, prefix := range keys {
		entries := groups[prefix]
		if prefix == "" {
			// Non-numeric nodes — each is its own contiguous group.
			for _, node := range entries {
				result = append(result, NewFromSlice([]string{node}))
			}
			continue
		}

		// Parse and sort numeric suffixes.
		type suffixEntry struct {
			value int
			node  string
		}
		var se []suffixEntry
		for _, node := range entries {
			_, suffix := splitTrailingNumber(node)
			if suffix == "" {
				result = append(result, NewFromSlice([]string{node}))
				continue
			}
			v, err := parseInt(suffix)
			if err != nil {
				result = append(result, NewFromSlice([]string{node}))
				continue
			}
			se = append(se, suffixEntry{value: v, node: node})
		}

		sort.Slice(se, func(i, j int) bool { return se[i].value < se[j].value })

		// Split into contiguous runs.
		if len(se) == 0 {
			continue
		}
		var current []string
		current = append(current, se[0].node)
		for i := 1; i < len(se); i++ {
			if se[i].value == se[i-1].value+1 {
				current = append(current, se[i].node)
			} else {
				result = append(result, NewFromSlice(current))
				current = []string{se[i].node}
			}
		}
		result = append(result, NewFromSlice(current))
	}

	return result
}

// Split divides the NodeSet into n roughly equal parts.
func (ns *NodeSet) Split(n int) []*NodeSet {
	nodes := ns.Nodes()
	if n <= 0 || n > len(nodes) {
		n = len(nodes)
	}

	result := make([]*NodeSet, n)
	chunkSize := len(nodes) / n
	remainder := len(nodes) % n

	offset := 0
	for i := 0; i < n; i++ {
		size := chunkSize
		if i < remainder {
			size++
		}
		result[i] = NewFromSlice(nodes[offset : offset+size])
		offset += size
	}

	return result
}

// parseInt is a local helper that wraps strconv.Atoi.
func parseInt(s string) (int, error) {
	return parseIntAtoi(s)
}

// parseIntAtoi wraps strconv.Atoi to avoid import cycle issues.
func parseIntAtoi(s string) (int, error) {
	v := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, &parseError{s}
		}
		v = v*10 + int(c-'0')
	}
	return v, nil
}

type parseError struct {
	s string
}

func (e *parseError) Error() string {
	return "not a number: " + e.s
}

// --- Package-level convenience functions ---

// Expand expands a nodeset pattern string into individual node names.
func Expand(pattern string, opts ...Option) ([]string, error) {
	ns, err := New(pattern, opts...)
	if err != nil {
		return nil, err
	}
	return ns.Nodes(), nil
}

// Match reports whether hostname matches the nodeset pattern.
// This is the primary API for credential matching and fan-out filtering.
//
// For patterns containing globs (* or ?), the hostname is matched against
// each expanded glob pattern using filepath.Match semantics.
// For patterns with bracket ranges, the hostname is checked for set membership.
func Match(pattern, hostname string, opts ...Option) (bool, error) {
	// Fast path: exact match.
	if pattern == hostname {
		return true, nil
	}

	// Check if pattern contains expression metacharacters that require expansion.
	hasGlob := containsGlob(pattern)
	hasExpansion := hasGlob || containsBracket(pattern) || containsSetOrGroup(pattern)

	if !hasExpansion {
		// Plain string — exact match only.
		return pattern == hostname, nil
	}

	// Fast path for patterns without complex set operations or globs.
	if !hasGlob && !containsComplex(pattern) {
		return matchFast(pattern, hostname)
	}

	if hasGlob && !containsBracket(pattern) && !containsSetOrGroup(pattern) {
		// Pure glob — use filepath.Match.
		return filepath.Match(pattern, hostname)
	}

	// Has brackets, groups, or set ops — expand and check membership.
	ns, err := New(pattern, opts...)
	if err != nil {
		return false, err
	}

	// If the expanded set contains glob patterns, match each.
	for _, node := range ns.Nodes() {
		if containsGlob(node) {
			matched, matchErr := filepath.Match(node, hostname)
			if matchErr != nil {
				return false, matchErr
			}
			if matched {
				return true, nil
			}
		} else if node == hostname {
			return true, nil
		}
	}

	return false, nil
}

// containsGlob reports whether s contains glob metacharacters.
func containsGlob(s string) bool {
	for _, c := range s {
		if c == '*' || c == '?' {
			return true
		}
	}
	return false
}

// containsBracket reports whether s contains bracket expressions.
func containsBracket(s string) bool {
	for _, c := range s {
		if c == '[' {
			return true
		}
	}
	return false
}

// containsSetOrGroup reports whether s contains set operators,
// group prefixes, or comma separators.
func containsSetOrGroup(s string) bool {
	for _, c := range s {
		if c == '@' || c == ',' || c == '!' || c == '&' || c == '^' {
			return true
		}
	}
	return false
}

// containsComplex reports whether s contains set operators other than union (comma).
func containsComplex(s string) bool {
	for _, c := range s {
		if c == '@' || c == '!' || c == '&' || c == '^' {
			return true
		}
	}
	return false
}

// matchFast matches a pattern against a hostname without expanding the cartesian product.
// Supports comma-separated unions of brackets and literals.
func matchFast(pattern, hostname string) (bool, error) {
	parts := strings.Split(pattern, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		matched, err := matchSimplePattern(part, hostname)
		if err != nil {
			return false, err
		}
		if matched {
			return true, nil
		}
	}
	return false, nil
}

// matchSimplePattern matches a single pattern (no commas) against a hostname.
// It parses the pattern into literals and brackets on the fly, avoiding string generation.
func matchSimplePattern(pattern, hostname string) (bool, error) {
	pIdx := 0
	hIdx := 0

	for pIdx < len(pattern) {
		bracketStart := strings.IndexByte(pattern[pIdx:], '[')
		if bracketStart < 0 {
			return hostname[hIdx:] == pattern[pIdx:], nil
		}
		bracketStart += pIdx

		literal := pattern[pIdx:bracketStart]
		if !strings.HasPrefix(hostname[hIdx:], literal) {
			return false, nil
		}
		hIdx += len(literal)

		bracketEnd := strings.IndexByte(pattern[bracketStart:], ']')
		if bracketEnd < 0 {
			return false, fmt.Errorf("unclosed bracket in %q", pattern)
		}
		bracketEnd += bracketStart

		content := pattern[bracketStart+1 : bracketEnd]
		ints, padding, err := parseBracketExpr(content)
		if err != nil {
			return false, fmt.Errorf("invalid bracket expr: %w", err)
		}

		pIdx = bracketEnd + 1

		if pIdx == len(pattern) {
			numStr := hostname[hIdx:]
			return checkNumStr(numStr, ints, padding)
		}

		nextBracketStart := strings.IndexByte(pattern[pIdx:], '[')
		var nextLiteral string
		if nextBracketStart < 0 {
			nextLiteral = pattern[pIdx:]
		} else {
			nextLiteral = pattern[pIdx : pIdx+nextBracketStart]
		}

		litIdx := strings.Index(hostname[hIdx:], nextLiteral)
		if litIdx < 0 {
			return false, nil
		}
		numStr := hostname[hIdx : hIdx+litIdx]

		matched, err := checkNumStr(numStr, ints, padding)
		if err != nil || !matched {
			return false, nil
		}
		hIdx += len(numStr)
	}

	return hIdx == len(hostname), nil
}

func checkNumStr(numStr string, ints []int, padding int) (bool, error) {
	if padding > 0 && len(numStr) != padding {
		return false, nil
	}
	if numStr == "" {
		return false, nil
	}
	val, err := parseInt(numStr)
	if err != nil {
		return false, nil
	}
	idx := sort.SearchInts(ints, val)
	return idx < len(ints) && ints[idx] == val, nil
}
