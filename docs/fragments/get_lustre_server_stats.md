#### `get_lustre_server_stats`
*Category: `storage` · Runs on: Lustre Server nodes (OSS/MDS/MGS)*

Collects per-target statistics for Lustre server roles: OSS (`obdfilter`), MDS (`mdt`), and MGS (`mgs`) — export counts, capacity/inode utilization, top RPC/IO counters, and the OSS disk-I/O-size distribution.

**Data Sources:**
- **Target Discovery & Roles**: subdirectories of `/sys/fs/lustre/{obdfilter,mdt,mgs}/` (or `/proc/fs/lustre/...`); each file is resolved sysfs-first with a per-file procfs fallback, matching the split layout of real Lustre releases.
- **OSS Targets**: `obdfilter/<target>/{num_exports,kbytestotal,kbytesfree,filestotal,filesfree,stats,brw_stats}`.
- **MDS Targets**: `mdt/<target>/{num_exports,stats,md_stats}` (open/close/getattr/setattr counters), plus capacity/inode files when exposed.
- **MGS Target**: `mgs/MGS/{num_exports,stats}`.

**Mathematical Models / Formatting:**
- **Utilization**: precomputes `used_pct = (total - free) / total * 100` (rounded to 2 decimals) for both capacity (`kbytestotal`/`kbytesfree`) and inodes (`filestotal`/`filesfree`).
- **Top Counters**: parses `name count samples [unit] min max sum` lines from `stats` (merged with `md_stats` on MDTs) and keeps the top-10 counters by count, ties broken by name for deterministic output.
- **brw_stats Condensation** (OSS only): parses only the `disk I/O size` histogram section and emits the top-5 non-zero buckets per direction as `{size, pct}` (e.g. `1M` / `43%`), dropping zero-sample buckets.
- **Role Summary**: `role_summary {is_oss, is_mds, is_mgs}` derived from which target-type directories contain entries.
- **Target Filter**: optional `target` parameter restricts output to a single target name; an unknown name returns an error result listing the requested target.

**Degradation Profile:**
- `IsSupported()` returns `false` via `registry.DetectNodeRoles()` when the node has no OSS/MDS/MGS role ("node has no Lustre server targets").
- Missing per-target files (e.g. `num_exports`, capacity counters, `stats`) degrade to zero values / empty `top_stats` — a partial entry is always returned instead of failing.
- Missing or unparseable `brw_stats` omits the `brw_io_sizes` block entirely.
- Zero discovered targets returns a `warning` status ("no Lustre server targets found") rather than an error; cancelled contexts and malformed arguments are encapsulated as error results.
