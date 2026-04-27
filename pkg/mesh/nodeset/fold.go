package nodeset

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Fold compacts a list of node names into ClusterShell-compatible notation.
// Example: ["node1","node2","node3"] → "node[1-3]"
func Fold(nodes []string) string {
	if len(nodes) == 0 {
		return ""
	}
	if len(nodes) == 1 {
		return nodes[0]
	}

	// Group nodes by their alpha prefix.
	groups := groupByPrefix(nodes)

	// Sort group keys for deterministic output.
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var parts []string
	for _, prefix := range keys {
		entries := groups[prefix]
		if prefix == "" {
			// No numeric suffix — output as-is (sorted).
			sort.Strings(entries)
			parts = append(parts, entries...)
			continue
		}
		part := foldGroup(prefix, entries)
		parts = append(parts, part)
	}

	return strings.Join(parts, ",")
}

// nodeEntry holds a node's numeric suffix info for folding.
type nodeEntry struct {
	value   int
	padding int
	raw     string // the original suffix string
}

// groupByPrefix groups node names by their alphabetic prefix,
// extracting numeric suffixes.
func groupByPrefix(nodes []string) map[string][]string {
	groups := make(map[string][]string)

	for _, node := range nodes {
		prefix, _ := splitTrailingNumber(node)
		groups[prefix] = append(groups[prefix], node)
	}

	return groups
}

// splitTrailingNumber splits a node name into its alpha prefix and
// numeric suffix. Returns ("node", "01") for "node01".
// Returns ("gateway", "") for "gateway" (no numeric suffix).
func splitTrailingNumber(name string) (prefix, suffix string) {
	i := len(name)
	for i > 0 && name[i-1] >= '0' && name[i-1] <= '9' {
		i--
	}
	if i == len(name) {
		return name, ""
	}
	return name[:i], name[i:]
}

// foldGroup folds a set of nodes sharing the same prefix into compact notation.
func foldGroup(prefix string, nodes []string) string {
	// Parse numeric suffixes.
	var entries []nodeEntry
	var nonNumeric []string

	for _, node := range nodes {
		_, suffix := splitTrailingNumber(node)
		if suffix == "" {
			nonNumeric = append(nonNumeric, node)
			continue
		}

		v, err := strconv.Atoi(suffix)
		if err != nil {
			nonNumeric = append(nonNumeric, node)
			continue
		}

		pad := 0
		if len(suffix) > 1 && suffix[0] == '0' {
			pad = len(suffix)
		}
		entries = append(entries, nodeEntry{value: v, padding: pad, raw: suffix})
	}

	if len(entries) == 0 {
		sort.Strings(nonNumeric)
		return strings.Join(nonNumeric, ",")
	}

	// Sort by value.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].value < entries[j].value
	})

	// Determine consistent padding (use max padding found).
	padding := 0
	for _, e := range entries {
		if e.padding > padding {
			padding = e.padding
		}
	}

	// Build a RangeSet directly from sorted values to avoid Add allocations
	rs := newRangeSet(padding)
	var ranges []rang
	for _, e := range entries {
		if len(ranges) > 0 && ranges[len(ranges)-1].hi >= e.value-1 {
			if e.value > ranges[len(ranges)-1].hi {
				ranges[len(ranges)-1].hi = e.value
			}
		} else {
			ranges = append(ranges, rang{lo: e.value, hi: e.value})
		}
	}
	rs.ranges = ranges

	folded := rs.Fold()

	var result string
	if len(entries) == 1 {
		// Single value — no brackets needed.
		result = prefix + formatPadded(entries[0].value, padding)
	} else {
		result = prefix + "[" + folded + "]"
	}

	if len(nonNumeric) > 0 {
		sort.Strings(nonNumeric)
		result = result + "," + strings.Join(nonNumeric, ",")
	}

	return result
}

// formatPadded formats an integer with optional zero-padding.
func formatPadded(v, padding int) string {
	if padding > 0 {
		return fmt.Sprintf("%0*d", padding, v)
	}
	return strconv.Itoa(v)
}
