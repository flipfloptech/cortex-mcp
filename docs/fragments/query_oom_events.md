#### `query_oom_events`
*Category: `memory` · Runs on: Every Linux node with ring buffer access (klogctl or dmesg)*

Extracts OOM-killer incidents from the kernel ring buffer as structured events — victim pid/comm, memory footprint at kill time, OOM constraint, memcg and global-vs-cgroup scope, with boot-relative timestamps converted to wallclock RFC3339 — so "what got killed, when, and was it a cgroup limit or true system exhaustion" is answered without grepping raw dmesg text.

**Data Sources:**
- **Primary**: kernel ring buffer read natively via the `klogctl` syscall (`SYSLOG_ACTION_READ_ALL`, non-destructive; buffer sized via `SYSLOG_ACTION_SIZE_BUFFER` with a 1 MB fallback).
- **Fallback**: `dmesg -r` when the syscall is blocked (e.g. `kernel.dmesg_restrict=1` without `CAP_SYSLOG`).
- **Wallclock anchor**: the `btime` line of `/proc/stat` (boot time, seconds since epoch).

**Mathematical Models / Formatting:**
- Parses both kernel record formats: `Out of memory: Killed process PID (comm) total-vm:...kB, anon-rss:...kB, file-rss:...kB, shmem-rss:...kB` (global scope) and the `Memory cgroup out of memory: Killed process ...` variant (cgroup scope); the adjacent `oom-kill:constraint=...,oom_memcg=...,task=...,pid=...` context record is paired with its kill record by pid, contributing `constraint` and `memcg` (`oom_memcg` preferred, `task_memcg` fallback). Unpaired records of either kind still surface as standalone events.
- Event wallclock time = `btime` + the boot-relative `[offset]` prefix, rendered RFC3339 UTC; when the offset prefix or btime is unavailable the event is preserved with `time: null` rather than dropped.
- Pre-computes `total_vm_mb`, `anon_rss_mb`, `file_rss_mb`, `shmem_rss_mb` (kB → MB, rounded to one decimal) so the LLM never does unit math.
- Events are returned newest-first, capped by `last_n` (default 10, max 50); `total_found` and `count_by_comm` aggregate every event in the buffer, and a fixed `note` records that the ring buffer covers recent history only. Any event found elevates the result status to `warning`.

**Degradation Profile:**
- `IsSupported()` returns `false` only when the klogctl read fails *and* no `dmesg` binary is in `PATH`.
- When both sources fail at execution time (typically permission denied), returns an encapsulated error result explaining the required privilege (`CAP_SYSLOG`/root, `kernel.dmesg_restrict`) instead of a hard Go error.
- Zero OOM events is a healthy `ok` result with an empty `events` array; malformed or truncated records are skipped without aborting the parse.
- Command execution respects context cancellation.
