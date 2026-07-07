# Catalog update: `get_hugepage_info` — per-NUMA-node breakdown

Additive enhancement only. Merge the bullets below into the existing
`get_hugepage_info` entry in `docs/TOOL_CATALOG.md`; no existing bullets
change. The tool now surfaces a top-level `per_node` array plus a
`warning_reasons` array in its data payload.

**Data Sources (add):**
- **Per-NUMA-node Pools**: Reads `/sys/devices/system/node/node<N>/hugepages/hugepages-<size>kB/` for `nr_hugepages`, `free_hugepages`, and `surplus_hugepages`, covering every hugepage size exposed per node (e.g. 2 MB and 1 GB pools).

**Mathematical Models / Output Structuring (add):**
- **Per-node Breakdown**: Emits `per_node` as a flat array of `{node, size_kb, total, free, surplus}` objects, sorted by node then numerically by page size, so the LLM can cross-reference NUMA topology without re-sorting (`2048` sorts before `1048576`).
- **Allocation Imbalance Warning**: For each page size, if one node is exhausted (`free == 0` with `total > 0`) while another node of the same size still has free pages, the result status is elevated to `warning` and `warning_reasons` carries `"HugePages exhausted on node N while node M has X free — NUMA-pinned allocations may fail"` (the referenced donor node is the one with the most free pages for that size); the first warning becomes the result summary. Exhaustion on every node simultaneously is capacity pressure, not imbalance, and does not warn.

**Degradation Profile (add):**
- **UMA / Old Kernels**: When no per-node hugepage directories exist (`/sys/devices/system/node` missing, no `node<N>` entries, or nodes without a `hugepages/` subtree), the `per_node` block is omitted entirely (no error, no empty array) and the pre-existing payload is unchanged.
- **Partial Node Data**: Whatever is readable is included — unreadable or malformed individual counter files are reported as `0`, and malformed `hugepages-*` directory names are skipped without aborting the scan.
