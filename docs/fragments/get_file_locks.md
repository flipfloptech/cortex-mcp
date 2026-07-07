#### `get_file_locks`
*Category: `system` · Runs on: Every Linux node*

Summarizes the kernel file-lock table to diagnose lock contention: counts by lock type and mode, the processes holding the most locks, and blocked waiters queued behind held locks.

**Data Sources:**
- **Lock Table**: `/proc/locks` text file, including `->` blocked-waiter lines (`ID: [->] TYPE MODE KIND PID MAJ:MIN:INO START END`).
- **Process Names**: `/proc/<pid>/comm` for holder and waiter identity resolution.

**Mathematical Models / Formatting:**
- **Aggregation**: `total_locks` counts held locks only (waiter lines excluded); `by_type` buckets POSIX / FLOCK / OFDLCK / LEASE and `by_mode` buckets READ / WRITE.
- **Top Holders**: held locks grouped per pid, ranked by lock count (ties broken by pid) and capped at the top 15, each with resolved `comm`.
- **Blocked Waiters**: exact `count` plus a detail list (`{pid, comm, type, mode}`) capped at 15 entries.
- **Warning Heuristic**: any blocked waiter appends a `warning_reasons` entry ("N process(es) blocked waiting on file locks") and flips the result status from `ok` to `warning`.

**Degradation Profile:**
- `IsSupported()` returns `false` if `/proc/locks` is missing.
- Dead pids (comm unreadable) degrade to `comm: "unknown"`; OFD locks carry pid `-1` and report `"OFD (no owner pid)"` instead of a process name.
- Malformed lock-table lines are skipped rather than failing the parse; an empty table is a valid `ok` result with zeroed aggregates.
- A missing locks file at execution time and cancelled contexts are encapsulated as error results (never hard Go errors).
