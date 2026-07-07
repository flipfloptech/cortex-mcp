#### `get_multipath_status`
*Category: `storage` · Runs on: SAN-attached nodes with dm-multipath maps or multipath-tools installed*

Audits device-mapper multipath (dm-multipath) maps and their individual SAN paths, flagging offline/failed paths and — critically — maps that have lost every active path (all I/O to that LUN fails).

**Data Sources:**
- **Map Discovery (native)**: Scans `/sys/block/dm-*/dm/uuid` for the `mpath-` prefix; LVM, dm-crypt, and other dm targets are excluded. Friendly names come from `dm/name`.
- **Path Membership (native)**: The map's `slaves/` directory lists its component path devices.
- **Per-Path State (native)**: `/sys/block/<slave>/device/state` (`running` / `offline`).
- **Enrichment (optional)**: `multipathd show maps raw format "%n %w %N %t"` adds the device-mapper state (`dm_state`, e.g. `active`/`suspend`) per map when the daemon is reachable.

**Mathematical Models / Formatting:**
- **Path Accounting**: `active_paths` counts paths in the `running` state; `failed_paths = total_paths − active_paths`. A missing per-path state file degrades to `unknown` and is counted as failed.
- **Per-Map Warning Heuristics**: Populates `warning_reasons []string` with one entry per non-running path, plus an explicit `CRITICAL: no active paths remaining` entry when a map with paths has zero active ones.
- **Status Escalation**: Any map with zero active paths drives the result status to `error`; any failed path (with redundancy remaining) drives `warning`; otherwise `ok`.
- **Summary Aggregation**: `summary` precomputes `total_maps`, `maps_with_failed_paths`, and `maps_with_no_active_paths`. Maps and paths are sorted by name for stable output.

**Degradation Profile:**
- `IsSupported()` returns `false` only when there is no `mpath-` dm device in sysfs **and** neither `multipath` nor `multipathd` is in `PATH` (reason: "no multipath devices and multipathd not installed").
- **No Maps**: Zero multipath maps is a valid state — returns `maps: []` with an `ok` status.
- **Daemon Failures Ignored**: multipathd socket/permission errors never affect results — native sysfs is the source of truth and enrichment is best-effort (`dm_state` is simply omitted).
- **Cancellation**: A cancelled context returns an encapsulated error result, never a hard Go error.
