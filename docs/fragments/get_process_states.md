#### `get_process_states`
*Category: `compute` · Runs on: Every Linux node*

Classifies every process on the node by scheduler state — running, sleeping, disk_sleep (uninterruptible), zombie, stopped, traced, idle — and turns the two states that actually indicate trouble into actionable detail: zombies are itemized with the parent that is failing to reap them, and D-state processes are itemized with the kernel function they are blocked in, so fork-leak bugs and I/O stalls can be attributed to a specific process instead of a raw `ps` dump.

**Data Sources:**
- `/proc/[pid]/stat` — state (field 3) and PPID (field 4), parsed relative to the *last* `)` so comm values containing spaces or parentheses cannot shift the fields.
- `/proc/[pid]/comm` — process name, falling back to the comm embedded in stat when unreadable.
- `/proc/[pid]/wchan` — kernel wait channel for D-state processes (`?` when restricted or empty).
- `/proc/[pid]/cmdline` — command line for D-state processes (NUL separators joined with spaces, truncated to 100 chars).

**Mathematical Models / Formatting:**
- Deterministic `O(N)` single scan: state characters map to named counters (`R`→running, `S`→sleeping, `D`→disk_sleep, `Z`→zombie, `T`→stopped, `t`→traced, `I`→idle) with unrecognized characters aggregated under `other`; `counts.total` is always the untruncated population.
- Zombies are joined against the scanned process table to resolve `parent_comm`/`parent_state`, then grouped into `parents_with_zombies` with a per-parent `zombie_count` and an `is_init` flag (PPID 1 means init will reap them — expected, not a bug).
- Heuristic `warning_reasons` flag every group of zombies whose parent is alive and not PID 1 (`"N zombie(s) not being reaped by <comm> (pid P)"` — a wait()/SIGCHLD bug in that parent) and more than 5 concurrent D-state processes (possible I/O or lock stall); any warning elevates the result status to `warning`.
- Itemized lists are capped for token economy: `zombies` at 25, `d_state` at 20; the counts remain exact.

**Degradation Profile:**
- `IsSupported()` returns `false` when `<procfs>/self/stat` is not readable.
- PIDs that vanish between the directory scan and the stat read are skipped silently — a scan can never fail because processes exited mid-flight.
- Unreadable `wchan` degrades to `?`; unreadable `cmdline`/`comm` degrade to empty and the stat-embedded comm respectively.
- Cancellation is checked between pid iterations; a canceled context returns an encapsulated error result instead of a hard failure.
