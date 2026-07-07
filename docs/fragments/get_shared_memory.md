#### `get_shared_memory`
*Category: `memory` · Runs on: Every Linux node with SysV IPC (`/proc/sysvipc/shm` present)*

Inventories all three shared-memory surfaces on the node — SysV IPC segments, POSIX `/dev/shm` files, and tmpfs mount usage — and pre-evaluates the classic failure signatures: orphaned SysV segments (created, then the owner died without `shmctl(IPC_RMID)` — the canonical crashed-MPI-job leak) and tmpfs mounts filling up, which consume RAM and evict page cache.

**Data Sources:**
- `/proc/sysvipc/shm` — SysV segments, parsed by header column names so varying kernel column layouts (with/without `rss`+`swap`) are handled.
- `/dev/shm` — recursive listing of POSIX shared memory files with size, owner uid (`syscall.Stat_t`), and mtime.
- `/proc/self/mountinfo` — tmpfs mounts, sized via `statfs` with a strict 2-second hung-mount guard per mount.

**Mathematical Models / Formatting:**
- `sysv`: top 20 segments by size `{shmid, size_mb, nattch, creator_pid, creator_alive, last_pid, orphaned}` plus `segment_count`, `total_mb`, `orphaned_count`, `orphaned_mb`. A segment is `orphaned` when `nattch == 0`; `creator_alive` checks `/proc/<cpid>` existence. All sizes in megabytes (1 decimal place).
- `posix`: top 20 `/dev/shm` files by size `{name, size_mb, uid, mtime}` plus `file_count` and `total_mb`.
- `tmpfs[]`: every tmpfs mount `{mount, used_mb, total_mb, used_pct, status}` with `used_pct` = used/total × 100 (1 decimal place).
- Pre-evaluated `warning_reasons`: `orphaned_count > 0` ("N orphaned SysV segments (M MB) — likely leaked by exited processes (common MPI failure)") and any tmpfs `used_pct > 80%`. Any breach flips the result status to `warning`.

**Degradation Profile:**
- `IsSupported()` returns `false` when `/proc/sysvipc/shm` is missing ("SysV IPC not available").
- Unreadable `/dev/shm`: the `posix` block is omitted and explained in `notes`; status becomes `degraded`.
- A `statfs` timeout marks that mount `status: "hung"`, excludes it from `used_pct` math and warnings, and degrades the result instead of wedging the tool.
- Missing `/proc/self/mountinfo`: empty `tmpfs` list plus a note.
