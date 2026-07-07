#### `get_memory_reclaim_stats`
*Category: `memory` · Runs on: Every Linux node with procfs*

Samples `/proc/vmstat` twice across a short window (default 500 ms) and converts the kernel's cumulative reclaim counters into per-second rates. Any non-zero direct reclaim, allocation stall, swap, or compaction stall activity means processes are already paying memory-pressure latency — this is the live early-warning signal that fires long before an OOM kill.

**Data Sources:**
- `/proc/vmstat` (sampled twice; `key value` lines): `pgscan_kswapd`/`pgscan_direct`, `pgsteal_kswapd`/`pgsteal_direct`, `allocstall_*` (all zone variants summed), `compact_stall`/`compact_fail`/`compact_success`, `pswpin`/`pswpout`, `pgmajfault`, `thp_fault_fallback`, `oom_kill`.
- Pre-4.8 kernels export per-zone spellings (e.g. `pgscan_kswapd_dma`); all variants are summed into the modern unsuffixed counter. The `pgscan_direct_throttle` event counter is excluded (it counts throttle events, not pages).

**Mathematical Models / Formatting:**
- `rates_per_sec` (2 decimal places): counter deltas divided by the measured window width — `swap_in`, `swap_out`, `direct_scan`, `kswapd_scan`, `direct_steal`, `kswapd_steal`, `allocstall`, `major_faults`, `compact_stall`. `window_ms` reports the actual measured window.
- `totals_since_boot`: cumulative counters from the second sample (`pswpin`, `pswpout`, `pgscan_direct`, `pgscan_kswapd`, `allocstall`, `compact_stall`, `compact_fail`, `compact_success`, `thp_fault_fallback`, `oom_kill`).
- `reclaim_efficiency_pct` = total `pgsteal`/`pgscan` × 100 since boot (2 decimal places, omitted when `pgscan` is 0); low values mean the kernel scans many pages per page actually freed.
- Pre-evaluated `warning_reasons` on any in-window activity: `direct_scan > 0` (processes are direct-reclaiming — allocation latency impact), `allocstall > 0`, `swap_in`/`swap_out > 0`, `compact_stall > 0`. Any breach flips the result status to `warning`.
- `sample_duration_ms` parameter (integer, optional, default 500) is clamped to [10, 5000].

**Degradation Profile:**
- `IsSupported()` returns `false` when `/proc/vmstat` is missing.
- Counters not exported by this kernel are omitted from `totals_since_boot` and contribute a 0 rate.
- Context cancellation during the sampling window returns an error result promptly instead of waiting the window out.
