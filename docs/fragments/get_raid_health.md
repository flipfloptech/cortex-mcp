#### `get_raid_health`
*Category: `storage` · Runs on: Nodes with Linux software RAID (md driver loaded)*

Audits Linux software RAID (md) array health natively from the kernel — no mdadm dependency — reporting per-array state, degradation, rebuild/resync progress, mismatch counts, and per-member device states, and flagging faulty members and arrays running at reduced redundancy.

**Data Sources:**
- **Support Gate**: `/proc/mdstat` presence indicates the md driver is loaded.
- **Array Discovery**: Entries under `/sys/block/md*` that contain an `md/` subdirectory (the definitive array marker, which also excludes partitions like `md0p1`).
- **Per-Array Attributes**: `/sys/block/md*/md/{array_state,degraded,sync_action,sync_completed,mismatch_cnt,raid_disks,level}`.
- **Per-Member State**: `/sys/block/md*/md/dev-*/state` (`in_sync`, `faulty`, `spare`, ...).

**Mathematical Models / Formatting:**
- **Rebuild Progress**: `rebuild_pct` is computed from the `sync_completed` fraction (`N / M` sectors) as `N / M × 100`, rounded to 1 decimal place; `none`, a zero denominator, or unparseable content yield `0`.
- **Per-Array Warning Heuristics**: Populates `warning_reasons []string` from four rules — `degraded=1`, any member whose state contains `faulty` (also promoted into `failed_members[]`), an active `sync_action` of `recover`/`resync` (redundancy not at full strength; `check`/`repair` scrubs are not warnings), and `mismatch_cnt > 0` (blocks inconsistent between mirrors/parity).
- **Summary Aggregation**: `summary` counts `total`, `degraded_count`, and `rebuilding_count` (arrays with `sync_action` of `recover`/`resync`); any warning flips the result status from `ok` to `warning`.
- **Determinism**: Arrays and members are sorted by name for stable, diff-friendly output.

**Degradation Profile:**
- `IsSupported()` returns `false` only when `/proc/mdstat` is missing (md driver not loaded).
- **No Arrays**: A node with the md driver loaded but zero arrays is a valid state — returns `arrays: []` with an `ok` status.
- **Missing Attributes**: Individual missing/unreadable sysfs attributes degrade to zero values (`""`, `0`, `false`) instead of failing the array or the execution.
- **Cancellation**: A cancelled context returns an encapsulated error result, never a hard Go error.
