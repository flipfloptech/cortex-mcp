// Package nodeset provides ClusterShell-compatible node set manipulation
// for pattern expansion, folding, matching, and set operations.
//
// # Pattern Syntax
//
// The nodeset package supports the full ClusterShell pattern language:
//
//	node[1-3]         → node1, node2, node3
//	node[01-03]       → node01, node02, node03 (zero-padded)
//	node[1,3,5]       → node1, node3, node5
//	node[1-3,5,7-9]   → node1, node2, node3, node5, node7, node8, node9
//	node[1-9/2]       → node1, node3, node5, node7, node9 (step)
//	rack[1-2]node[1-3] → 6 nodes (Cartesian product)
//	mds-*, node?      → glob patterns (filepath.Match semantics)
//
// # Set Operations
//
// Operators are applied left-to-right in the pattern string:
//
//	node[1-3],oss[1-5] → union (comma)
//	node[1-10]!node[5-7] → difference (!)
//	node[1-10]&node[5-15] → intersection (&)
//	node[1-5]^node[3-8]   → symmetric difference (^)
//
// # Group References
//
// Groups are resolved via a pluggable GroupResolver interface:
//
//	@compute          → default source, group "compute"
//	@slurm:compute    → source "slurm", group "compute"
//
// # Quick Start
//
//	// Expand a pattern
//	nodes, _ := nodeset.Expand("node[1-3],oss[01-05]")
//	// → ["node1", "node2", "node3", "oss01", ..., "oss05"]
//
//	// Fold nodes into compact notation
//	compact := nodeset.Fold([]string{"node1", "node2", "node3"})
//	// → "node[1-3]"
//
//	// Match a hostname against a pattern
//	ok, _ := nodeset.Match("node[1-100]", "node50")
//	// → true
//
//	// Full NodeSet with set operations
//	ns, _ := nodeset.New("node[1-10]!node[5-7]")
//	ns.Contains("node3")  // true
//	ns.Contains("node5")  // false
//	ns.Len()              // 7
//	ns.String()           // "node[1-4,8-10]"
package nodeset
