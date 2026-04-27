package nodeset

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// parseBracketExpr parses the content between [ and ] in a nodeset pattern.
// Returns the expanded integers, the zero-pad width, and any error.
//
// Supported formats:
//
//	"1-3"       → [1,2,3], pad=0
//	"01-03"     → [1,2,3], pad=2
//	"1,3,5"     → [1,3,5], pad=0
//	"1-3,5,7-9" → [1,2,3,5,7,8,9], pad=0
//	"1-9/2"     → [1,3,5,7,9], pad=0
func parseBracketExpr(expr string) ([]int, int, error) {
	if expr == "" {
		return nil, 0, fmt.Errorf("empty bracket expression")
	}

	padding := 0
	rs := newRangeSet(0)

	// Split on commas to handle mixed range/list/step expressions.
	parts := strings.Split(expr, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		// Check for step: N-M/S
		var stepStr string
		if slashIdx := strings.Index(part, "/"); slashIdx >= 0 {
			stepStr = part[slashIdx+1:]
			part = part[:slashIdx]
		}

		// Check for range: N-M
		if dashIdx := strings.Index(part, "-"); dashIdx >= 0 {
			loStr := part[:dashIdx]
			hiStr := part[dashIdx+1:]

			// Detect padding from the first numeric token.
			pad := detectPadding(loStr)
			if pad > padding {
				padding = pad
			}

			lo, err := strconv.Atoi(loStr)
			if err != nil {
				return nil, 0, fmt.Errorf("invalid range start %q: %w", loStr, err)
			}
			hi, err := strconv.Atoi(hiStr)
			if err != nil {
				return nil, 0, fmt.Errorf("invalid range end %q: %w", hiStr, err)
			}
			if lo > hi {
				return nil, 0, fmt.Errorf("invalid range: start %d > end %d", lo, hi)
			}

			step := 1
			if stepStr != "" {
				step, err = strconv.Atoi(stepStr)
				if err != nil {
					return nil, 0, fmt.Errorf("invalid step %q: %w", stepStr, err)
				}
				if step <= 0 {
					return nil, 0, fmt.Errorf("step must be positive, got %d", step)
				}
			}

			if step == 1 {
				rs.Add(lo, hi)
			} else {
				for v := lo; v <= hi; v += step {
					rs.Add(v, v)
				}
			}
		} else {
			// Single value.
			pad := detectPadding(part)
			if pad > padding {
				padding = pad
			}

			v, err := strconv.Atoi(part)
			if err != nil {
				return nil, 0, fmt.Errorf("invalid value %q: %w", part, err)
			}
			rs.Add(v, v)
		}
	}

	return rs.Expand(), padding, nil
}

// detectPadding returns the zero-pad width for a numeric string.
// "03" → 2, "3" → 0, "001" → 3.
func detectPadding(s string) int {
	if len(s) > 1 && s[0] == '0' {
		return len(s)
	}
	return 0
}

// expandTerm expands a single pattern term (no set operators).
// Handles:
//   - Plain names: "mds-01" → ["mds-01"]
//   - Bracket expansion: "node[1-3]" → ["node1", "node2", "node3"]
//   - Multi-dimensional: "rack[1-2]node[1-3]" → 6 nodes (Cartesian product)
//   - Glob passthrough: "mds-*" → ["mds-*"]
//   - Group references: "@compute" → resolver.Resolve("", "compute")
func expandTerm(term string, resolver GroupResolver, maxExpand int) ([]string, error) {
	// Handle group references: @group or @source:group
	if len(term) > 0 && term[0] == '@' {
		return expandGroup(term[1:], resolver)
	}

	// Parse bracket expressions.
	segments, err := splitBrackets(term)
	if err != nil {
		return nil, err
	}

	// If no brackets, return the term as-is (including globs).
	if len(segments) == 1 && segments[0].kind == segLiteral {
		return []string{term}, nil
	}

	// Expand bracket segments and compute Cartesian product.
	return cartesianExpand(segments, maxExpand)
}

// segmentKind indicates whether a segment is a literal string or a bracket expansion.
type segmentKind int

const (
	segLiteral segmentKind = iota
	segBracket
)

// segment represents a piece of a pattern term.
type segment struct {
	kind    segmentKind
	literal string   // for segLiteral
	values  []string // for segBracket (expanded bracket values)
}

// splitBrackets parses a term into alternating literal and bracket segments.
// Returns an error if brackets are malformed.
func splitBrackets(term string) ([]segment, error) {
	var segments []segment
	i := 0

	for i < len(term) {
		// Find next '['.
		bracketStart := strings.IndexByte(term[i:], '[')
		if bracketStart < 0 {
			// No more brackets — rest is literal.
			segments = append(segments, segment{kind: segLiteral, literal: term[i:]})
			break
		}
		bracketStart += i

		// Add literal before bracket if any.
		if bracketStart > i {
			segments = append(segments, segment{kind: segLiteral, literal: term[i:bracketStart]})
		}

		// Find matching ']'.
		bracketEnd := strings.IndexByte(term[bracketStart:], ']')
		if bracketEnd < 0 {
			return nil, fmt.Errorf("unclosed bracket in %q", term)
		}
		bracketEnd += bracketStart

		// Parse bracket content.
		content := term[bracketStart+1 : bracketEnd]
		values, padding, err := parseBracketExpr(content)
		if err != nil {
			return nil, fmt.Errorf("invalid bracket expression [%s]: %w", content, err)
		}

		// Format values as strings with padding.
		strs := make([]string, len(values))
		for j, v := range values {
			if padding > 0 {
				strs[j] = fmt.Sprintf("%0*d", padding, v)
			} else {
				strs[j] = strconv.Itoa(v)
			}
		}
		segments = append(segments, segment{kind: segBracket, values: strs})

		i = bracketEnd + 1
	}

	return segments, nil
}

// cartesianExpand computes the Cartesian product of all segments.
//
// Uses strings.Builder to construct each result string in a single pass,
// avoiding the O(segments × results) intermediate string allocations from
// += concatenation. Each output string requires exactly one allocation
// (the final Builder.String() call).
//
// Algorithm:
//  1. Compute totalCount (product of all bracket dimensions).
//  2. Pre-compute estimated string length from literal segments.
//  3. For each output index, determine which bracket value to use via
//     modular arithmetic, and build the full string with a Builder.
func cartesianExpand(segments []segment, maxExpand int) ([]string, error) {
	if len(segments) == 0 {
		return []string{""}, nil
	}

	// Compute the total number of output strings and estimate the
	// per-string byte length for Builder pre-allocation.
	//
	// SECURITY: Validate totals BEFORE allocating the result slice.
	// Without this check, malicious patterns like rack[1-10000]node[1-10000]
	// would allocate 100M strings and OOM the process.
	totalCount := 1
	estimatedLen := 0
	for _, seg := range segments {
		if seg.kind == segBracket {
			if len(seg.values) == 0 {
				return []string{}, nil
			}
			// Overflow-safe multiplication: check before multiplying.
			if maxExpand > 0 && len(seg.values) > 0 && totalCount > maxExpand/len(seg.values) {
				return nil, fmt.Errorf("nodeset: cartesian product %d × %d exceeds limit of %d",
					totalCount, len(seg.values), maxExpand)
			}
			totalCount *= len(seg.values)
			// Use the first value's length as a representative estimate.
			estimatedLen += len(seg.values[0])
		} else {
			estimatedLen += len(seg.literal)
		}
	}

	// Final check after all dimensions are accumulated.
	if maxExpand > 0 && totalCount > maxExpand {
		return nil, fmt.Errorf("nodeset: expansion of %d nodes exceeds limit of %d",
			totalCount, maxExpand)
	}

	// For each output string, determine the bracket combination via
	// modular arithmetic and build it in one pass with a Builder.
	//
	// The "stride" for bracket dimension k is the product of all
	// subsequent bracket dimensions. For output index i, the value
	// chosen from bracket k is: values[(i / stride) % len(values)].
	//
	// Pre-compute strides for all bracket segments.
	strides := make([]int, len(segments))
	stride := 1
	for k := len(segments) - 1; k >= 0; k-- {
		if segments[k].kind == segBracket {
			strides[k] = stride
			stride *= len(segments[k].values)
		}
	}

	results := make([]string, totalCount)
	var b strings.Builder
	b.Grow(estimatedLen)

	for i := 0; i < totalCount; i++ {
		b.Reset()
		// Grow is a no-op if the builder's capacity already suffices,
		// which it will after the first iteration.
		b.Grow(estimatedLen)

		for k, seg := range segments {
			if seg.kind == segLiteral {
				b.WriteString(seg.literal)
			} else {
				// Pick the value for this bracket dimension.
				idx := (i / strides[k]) % len(seg.values)
				b.WriteString(seg.values[idx])
			}
		}

		results[i] = b.String()
	}

	return results, nil
}

// expandGroup resolves a group reference (after the @ prefix).
// Format: "group" or "source:group"
func expandGroup(ref string, resolver GroupResolver) ([]string, error) {
	if resolver == nil {
		return nil, fmt.Errorf("group reference @%s but no GroupResolver configured", ref)
	}

	source := ""
	group := ref
	if colonIdx := strings.IndexByte(ref, ':'); colonIdx >= 0 {
		source = ref[:colonIdx]
		group = ref[colonIdx+1:]
	}

	nodes, err := resolver.Resolve(source, group)
	if err != nil {
		return nil, fmt.Errorf("resolving group @%s: %w", ref, err)
	}
	return nodes, nil
}

// parseExpr parses a full nodeset expression with set operators.
//
// Operators (evaluated left-to-right):
//   - , (comma): union
//   - ! : difference
//   - & : intersection
//   - ^ : symmetric difference
//
// Returns the expanded, deduplicated, sorted set of node names.
func parseExpr(expr string, resolver GroupResolver, maxExpand int) ([]string, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, fmt.Errorf("empty pattern")
	}

	// Tokenize into operands and operators.
	tokens, ops, err := tokenize(expr)
	if err != nil {
		return nil, err
	}

	// Expand the first operand.
	result, err := expandTerm(tokens[0], resolver, maxExpand)
	if err != nil {
		return nil, err
	}
	resultSet := sliceToSet(result)

	// Apply operators left-to-right.
	for i, op := range ops {
		rhs, expandErr := expandTerm(tokens[i+1], resolver, maxExpand)
		if expandErr != nil {
			return nil, expandErr
		}
		rhsSet := sliceToSet(rhs)

		switch op {
		case ',':
			// Union.
			for k := range rhsSet {
				resultSet[k] = struct{}{}
			}
		case '!':
			// Difference.
			for k := range rhsSet {
				delete(resultSet, k)
			}
		case '&':
			// Intersection.
			for k := range resultSet {
				if _, ok := rhsSet[k]; !ok {
					delete(resultSet, k)
				}
			}
		case '^':
			// Symmetric difference.
			newSet := make(map[string]struct{})
			for k := range resultSet {
				if _, ok := rhsSet[k]; !ok {
					newSet[k] = struct{}{}
				}
			}
			for k := range rhsSet {
				if _, ok := resultSet[k]; !ok {
					newSet[k] = struct{}{}
				}
			}
			resultSet = newSet
		}
	}

	// Check expansion cap.
	if len(resultSet) > maxExpand {
		return nil, fmt.Errorf("expansion of %d nodes exceeds limit of %d", len(resultSet), maxExpand)
	}

	return setToSortedSlice(resultSet), nil
}

// tokenize splits an expression into operand terms and operators.
// Commas inside brackets are NOT treated as operators.
func tokenize(expr string) (terms []string, ops []byte, err error) {
	depth := 0
	start := 0

	for i := 0; i < len(expr); i++ {
		switch expr[i] {
		case '[':
			depth++
		case ']':
			depth--
			if depth < 0 {
				return nil, nil, fmt.Errorf("unmatched ']' in %q", expr)
			}
		case ',', '!', '&', '^':
			if depth == 0 {
				term := strings.TrimSpace(expr[start:i])
				if term == "" {
					return nil, nil, fmt.Errorf("empty operand near position %d in %q", i, expr)
				}
				terms = append(terms, term)
				ops = append(ops, expr[i])
				start = i + 1
			}
		}
	}

	if depth != 0 {
		return nil, nil, fmt.Errorf("unclosed bracket in %q", expr)
	}

	// Capture the last term.
	term := strings.TrimSpace(expr[start:])
	if term == "" {
		return nil, nil, fmt.Errorf("trailing operator in %q", expr)
	}
	terms = append(terms, term)

	return terms, ops, nil
}

// sliceToSet converts a string slice to a set.
func sliceToSet(slice []string) map[string]struct{} {
	m := make(map[string]struct{}, len(slice))
	for _, s := range slice {
		m[s] = struct{}{}
	}
	return m
}

// setToSortedSlice converts a set to a sorted slice.
func setToSortedSlice(m map[string]struct{}) []string {
	result := make([]string, 0, len(m))
	for k := range m {
		result = append(result, k)
	}
	sort.Strings(result)
	return result
}
