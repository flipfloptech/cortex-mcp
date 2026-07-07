#### `get_conntrack_summary`
*Category: `network` · Runs on: Every Linux node with the nf_conntrack module loaded*

Summarizes netfilter connection-tracking table pressure: current entry count vs table capacity with a precomputed usage percentage, plus per-CPU failure counters summed across all CPUs. This is the go-to check when a busy node starts logging `nf_conntrack: table full, dropping packet`.

**Data Sources:**
- **Primary**: `/proc/sys/net/netfilter/nf_conntrack_count` (current tracked connections), `/proc/sys/net/netfilter/nf_conntrack_max` (table capacity), and `/proc/net/stat/nf_conntrack` (header line plus one row of hexadecimal counters per CPU).
- **Fallback**: None required — all sources are world-readable procfs files.

**Mathematical Models / Formatting:**
- **Usage Percentage**: `usage_pct = round(count / max × 100, 2dp)`, guarded to `0` when `max` is unavailable.
- **Header-Driven Hex Decoding**: `/proc/net/stat/nf_conntrack` columns vary across kernel versions (`searched`/`delete_list` vs `clashres`/`chainlength`), so columns are resolved by header name rather than position. Each per-CPU row is parsed as base-16 and summed column-wise; the row count is reported as `cpu_count`.
- **Counter Selection**: Only failure-relevant sums are emitted (`invalid`, `insert_failed`, `drop`, `early_drop`, `search_restart`) instead of the full raw matrix.
- **Warning Reasons**: Deterministic triggers — `usage_pct > 80`, and non-zero `drop`, `early_drop`, or `insert_failed` sums. Any trigger flips the result status to `warning`.

**Degradation Profile:**
- `IsSupported()` returns `false` with reason `"conntrack not loaded"` if `nf_conntrack_count` does not exist (conntrack module absent).
- If `/proc/net/stat/nf_conntrack` is missing or unparsable, the tool degrades to counts only and sets `counters_available: false` (status stays `ok`).
- If `nf_conntrack_max` is unreadable, `max` and `usage_pct` are reported as `0` rather than failing. Only an unreadable `nf_conntrack_count` produces an error result.
