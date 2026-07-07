#### `get_open_files`
*Category: `storage` · Runs on: Every Linux node*

Finds every process holding open file descriptors under a path prefix — the native (binary-free) answer to "why can't this filesystem unmount?" and "what is still writing to this mount?".

**Data Sources:**
- **FD Tables**: `/proc/<pid>/fd/*` symlinks resolved via `readlink` for every numeric `/proc` entry (no `lsof`/`fuser` binaries).
- **Process Names**: `/proc/<pid>/comm`.
- **Deleted Files**: readlink targets ending in `" (deleted)"` mark files that hold disk space until closed.

**Mathematical Models / Formatting:**
- **Prefix Matching**: fd targets are matched with a plain prefix comparison against the required `path` parameter (so `/mnt/lustre` also matches `/mnt/lustre2`; pass a trailing slash to bind to a directory). The deleted suffix is stripped before matching.
- **Ranking & Caps**: processes are ranked by matching `fd_count` (ties broken by pid) and truncated to `limit` (default 20, cap 100); each entry carries up to 3 `example_paths` and a `has_deleted` flag.
- **Totals**: `total_matching_fds` and `deleted_open_count` cover the full scan, not just the truncated process list.

**Degradation Profile:**
- `IsSupported()` returns `false` off-Linux or when `<procfs>/self/fd` is not readable.
- fd directories unreadable due to permissions (other users' processes when unprivileged) are counted in `processes_skipped` — never an error.
- Processes that vanish mid-scan (fd dir or individual fds gone) are skipped silently; dead-pid comm reads degrade to `"unknown"`.
- Missing/empty `path`, malformed arguments, and cancelled contexts (checked between pid iterations) are encapsulated as error results (never hard Go errors).
