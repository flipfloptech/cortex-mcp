#### `get_process_memory_detail`
*Category: `memory` · Runs on: Every Linux node*

Produces an accurate memory footprint for a single process (required `target_pid` parameter), distinguishing truly-owned memory (PSS, private pages) from shared library pages that naive RSS readings double-count, plus swap pressure, transparent huge page usage, and the rlimits bounding the process.

**Data Sources:**
- **Primary**: `/proc/<pid>/smaps_rollup` — kernel-precomputed totals for `Rss`, `Pss`, `Shared_Clean`, `Shared_Dirty`, `Private_Clean`, `Private_Dirty`, `Swap`, `SwapPss`, and `AnonHugePages` (all `kB` lines).
- **Fallback**: `/proc/<pid>/smaps` — the same keys summed across every mapping on kernels older than 4.14 that lack `smaps_rollup`.
- **Identity**: `/proc/<pid>/status` for `Name` (comm), `Threads`, and `VmSwap` (used only when smaps carries no Swap key).
- **Limits**: `/proc/<pid>/limits` rows `Max address space` and `Max locked memory` (soft limit reported; `unlimited` preserved as a string).

**Mathematical Models / Formatting:**
- **Unit Conversion**: all sizes converted from kB to MB, rounded to 1 decimal place.
- **Composite Fields**: `shared_mb = Shared_Clean + Shared_Dirty`; `private_mb = Private_Clean + Private_Dirty`; `thp_mb = AnonHugePages`.
- **Headroom Ratio**: `rss_pct_of_address_limit = RSS / soft address-space limit × 100` (1dp), emitted **only** when that limit is finite — never against `unlimited`.

**Degradation Profile:**
- `IsSupported()` returns `false` when `/proc/self/status` is unreadable (non-Linux).
- Nonexistent `target_pid` returns a `process not found` error result; missing/invalid `target_pid` returns a parameter error result.
- Missing `smaps_rollup` (old kernels) silently falls back to summing `smaps`.
- Permission denied on smaps data (other users' processes without root/CAP_SYS_PTRACE) returns an error result explicitly naming the privilege requirement.
- An unreadable `limits` file degrades to `"unknown"` limit values, never fatal.
