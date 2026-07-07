#### `get_lustre_job_stats`
*Category: `storage` · Runs on: Lustre Server nodes (OSS/MDS)*

Attributes Lustre server load to the jobs generating it ("who is hammering the filesystem?") by parsing the YAML-ish per-target `job_stats` files, aggregating each job's counters across every target, and condensing them into three leaderboards: top jobs by write volume, by read volume, and by metadata operations.

**Data Sources:**
- **Target Discovery**: subdirectories of `/sys/fs/lustre/{obdfilter,mdt}/` (or `/proc/fs/lustre/...`); each `job_stats` file is then resolved sysfs-first with a per-file procfs fallback, matching the split layout of real Lustre releases. MGS carries no `job_stats` and is not scanned.
- **OSS Targets**: `obdfilter/<target>/job_stats` — per-job `read_bytes` / `write_bytes` blocks (`{ samples, unit, min, max, sum }`).
- **MDS Targets**: `mdt/<target>/job_stats` — per-job metadata op counters (`open`, `close`, `getattr`, `setattr`, `mkdir`, `rmdir`, `unlink`, `rename`, `statfs`, ...).
- **Parser Tolerance**: indent-based line-by-line parsing; `job_id` values may be quoted and contain dots, spaces, or user names; `snapshot_time` lines are skipped; malformed blocks (empty `job_id`, unparseable brace bodies) are skipped and surfaced via `parse_errors`.

**Mathematical Models / Formatting:**
- **Cross-Target Aggregation**: per job_id, `read/write` samples and byte sums are summed across all scanned targets; every other counter is treated as a metadata op and accumulated from MDT targets only (zero-sample ops excluded).
- **Volume Conversion**: `write_mb` / `read_mb` = byte sum ÷ 2²⁰, rounded to 1 decimal place, so the LLM never divides.
- **Leaderboards**: `top_jobs_by_write` / `top_jobs_by_read` sort by bytes desc → ops desc → job_id asc (deterministic ties); `top_jobs_by_metadata_ops` sorts by `total_ops` desc → job_id asc and names the dominant op as `top_op` (ties broken alphabetically). Jobs with zero activity in a dimension are excluded from that board; each entry lists the (sorted) targets where the job was observed in that dimension.
- **Limit**: optional `limit` parameter sizes each leaderboard (default 15, capped at 50); `total_jobs_tracked` still counts every unique job.
- **Warnings**: `warning_reasons` is present but empty by default — this is an attribution tool, not a threshold monitor.

**Degradation Profile:**
- `IsSupported()` returns `false` via `registry.DetectNodeRoles()` when the node is neither OSS nor MDS ("node has no Lustre server targets (not OSS/MDS)").
- **Job Stats Disabled**: targets exist but no job is tracked (files absent or header-only) → `degraded` status with an enablement note: set `jobid_var`, e.g. `lctl conf_param <fs>.sys.jobid_var=procname_uid`.
- **Unknown Target**: an unmatched `target` parameter returns an error result naming the requested target; zero discovered targets returns a `warning` status.
- **Metadata Board Omission**: `top_jobs_by_metadata_ops` is omitted entirely when no MDT was scanned.
- **Recency Caveat**: entries expire after `job_cleanup_interval`, so leaderboards reflect recent activity; cancelled contexts and malformed arguments are encapsulated as error results.
