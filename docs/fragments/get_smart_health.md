#### `get_smart_health`
*Category: `storage` · Runs on: Nodes with `smartctl` (smartmontools) installed*

Audits the SMART health of SATA/SAS drives (`sd*`) to catch failing spinning disks and SSDs before data loss, reducing full smartctl output to the failure-predictive core: overall self-assessment, temperature, power-on hours, and the four canonical defect/link counters. Accepts an optional `target_device` parameter (e.g., `sda`) to audit a single drive; audits all discovered drives when omitted. NVMe devices are excluded — they are covered by `get_nvme_smart_log`.

**Data Sources:**
- **Device Discovery**: Enumerates `/sys/class/block` entries matching `^sd[a-z]+$` that have a physical `device/` entry (partitions and virtual devices excluded) — no external binary needed for discovery. `device/vendor` + `device/model` provide the identity fallback when smartctl reports no `model_name`.
- **Primary**: `smartctl -a -j /dev/<dev>`, parsing `smart_status.passed`, `temperature.current`, `power_on_time.hours`, and `ata_smart_attributes.table[]` raw values for IDs 5 (Reallocated_Sector_Ct), 197 (Current_Pending_Sector), 198 (Offline_Uncorrectable), and 199 (UDMA_CRC_Error_Count).
- **Privilege Escalation**: SMART ioctls require root; when running unprivileged, commands are automatically wrapped in non-interactive `sudo -n` if `sudo` is in `PATH` (same contract as `get_nvme_smart_log`).

**Mathematical Models / Formatting:**
- **Exit-Bitmask Tolerance**: smartctl exits non-zero when SMART checks fail even though its JSON is valid, so output is parsed regardless of exit status; only unparseable output counts as a per-drive failure.
- **Per-Drive Warning Heuristics**: Populates `warning_reasons []string` from six rules — failed self-assessment, `reallocated_sectors > 0` (grown defects), `pending_sectors > 0` (unstable sectors), `uncorrectable_sectors > 0`, `crc_errors > 0` (cabling/backplane link integrity), and temperature `> 60°C`. Any warning flips the drive's `status` from `healthy` to `critical`.
- **Status Escalation**: A failed SMART self-assessment drives the result status to `error`; attribute-level findings alone drive `warning`; otherwise `ok`.
- **System Summary**: Aggregates `drives_audited`, `drives_failing` (drives in `critical` status), and `total_reallocated` (summed across drives).

**Degradation Profile:**
- `IsSupported()` returns `false` only if `smartctl` is not in `PATH`.
- **No SATA/SAS Hardware**: Zero matching drives is a valid state — returns `drives: []` with `drives_audited: 0` rather than failing.
- **Permission Lockout**: If the command is refused (`permission denied`, `operation not permitted`, or sudo demanding a password), returns an explicit `Unauthorized` error instructing that root or passwordless sudo is required — rather than silently returning partial data.
- **Per-Drive Skip**: A drive whose output fails to parse (USB bridge without SAT, dead device) is skipped and the remaining drives are still audited.
- **Cancellation**: A cancelled context returns an encapsulated error result, never a hard Go error.
