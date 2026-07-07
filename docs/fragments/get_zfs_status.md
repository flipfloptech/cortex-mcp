#### `get_zfs_status`
*Category: `storage` · Runs on: Nodes with the ZFS kernel module loaded or `zpool` installed*

Audits the ZFS storage stack: per-pool health, capacity, fragmentation, scrub/resilver progress, and device error counters, plus ARC cache efficiency — combining native kernel counters with the zpool CLI's stable parseable output.

**Data Sources:**
- **ARC (native)**: `/proc/spl/kstat/zfs/arcstats` 3-column kstat rows (`size`, `c_max`, `hits`, `misses`).
- **Module Version (native)**: `/sys/module/zfs/version`.
- **Pool Inventory**: `zpool list -Hp -o name,size,alloc,free,frag,cap,health` (script-friendly: no header, exact byte values). `zpool` JSON output requires ZFS ≥ 2.3, so the stable text/parseable formats are used instead.
- **Pool Detail**: `zpool status <pool>` text — the `scan:` line for scrub/resilver state and the config table for per-device READ/WRITE/CKSUM counters.

**Mathematical Models / Formatting:**
- **ARC Efficiency**: `hit_ratio_pct = hits / (hits + misses) × 100`, rounded to 2 decimal places (0 when there are no lookups); `size_mb`/`max_mb` converted from bytes (MiB).
- **Capacity Conversion**: `size_gb`/`alloc_gb` converted from exact bytes to GiB, rounded to 1 decimal place; `frag_pct`/`capacity_pct` taken from zpool's precomputed percentages.
- **Scan Classification**: The `scan:` line is normalized to `scrub in progress`, `resilver in progress`, `scrub repaired`, `resilvered`, or `none`; in-progress scans include `scan_pct` extracted from the `NN.NN% done` progress figure.
- **Error Accounting**: READ/WRITE/CKSUM counters are summed across every row of the config tree (pool, vdevs, and leaf devices) with human-readable magnitude suffixes (`1.05K`, `3M`, ...) expanded; dashes and garbage degrade to 0.
- **Per-Pool Warning Heuristics**: Populates `warning_reasons []string` from three rules — health ≠ `ONLINE`, any error counter > 0, and `capacity_pct > 90` (ZFS performance degrades when nearly full). Any warning flips the result status to `warning`.

**Degradation Profile:**
- `IsSupported()` returns `false` only when `/sys/module/zfs` is absent **and** `zpool` is not in `PATH`.
- **No Pools**: Zero imported pools is a valid state — returns `pools: []` with an `ok` status.
- **ARC Missing**: If `arcstats` is unreadable (module unloaded, procfs restricted), the `arc` block is omitted entirely rather than reporting zeros.
- **Binary Missing / CLI Failure**: If the kernel module is loaded but `zpool` is missing or `zpool list` fails, the tool degrades to an ARC-only report with an explanatory `note` and a `degraded` status. A per-pool `zpool status` failure keeps the list-derived health/capacity data (scan state falls back to `none`).
- **Cancellation**: A cancelled context returns an encapsulated error result, never a hard Go error.
