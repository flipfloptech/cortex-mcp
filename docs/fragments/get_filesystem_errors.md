#### `get_filesystem_errors`
*Category: `storage` · Runs on: All Linux nodes with a readable `/proc/self/mountinfo`*

Surfaces filesystem-level error state that usually goes unnoticed until a mount flips read-only: kernel-recorded ext4 error counters, unexpected read-only block-device mounts (the classic `errors=remount-ro` trip signature), and per-device btrfs error counters.

**Data Sources:**
- **ext4 (native)**: `/sys/fs/ext4/<dev>/{errors_count,first_error_time,last_error_time,first_error_func}` per device; the non-device `features` directory is excluded and devices are mapped to mount points via mountinfo source basenames.
- **Read-Only Detection (native)**: `/proc/self/mountinfo` — per-mount options (field 6) and superblock options (third field after the `-` separator).
- **btrfs (optional enrichment)**: `btrfs device stats <mount>` for each btrfs filesystem when the `btrfs` binary is in `PATH` (`[<device>].write_io_errs / read_io_errs / flush_io_errs / corruption_errs / generation_errs` lines) — btrfs exposes these counters only through its own tooling, not sysfs. Filesystems mounted at multiple points (subvolumes) are queried once per source device.

**Mathematical Models / Formatting:**
- **Timestamps**: `first_error_time` / `last_error_time` unix epochs are rendered as RFC3339 UTC (`first_error` / `last_error`), omitted when 0 or absent (never an error recorded).
- **Read-Only Rule**: a mount is flagged when `ro` appears as a whole comma-separated token in the per-mount **or** superblock options (so `errors=remount-ro` never matches), the fstype is not read-only by design (`squashfs`, `iso9660`, `erofs`, `cramfs`, `romfs`, `udf`), and the source starts with `/dev/`.
- **Warning Heuristics**: `warning_reasons` collects — `errors_count > 0` ("ext4 `<dev>` has recorded N filesystem errors since last fsck — check dmesg"), each unexpected read-only mount ("`<mount>` is mounted read-only — possible error-triggered remount"), and any nonzero btrfs counter. Any warning flips the result status to `warning`.
- **Summary**: precomputed `{ext4_devices_checked, devices_with_errors, readonly_count}` where `devices_with_errors` counts ext4 devices with `errors_count > 0` plus btrfs devices with any nonzero counter.

**Degradation Profile:**
- `IsSupported()` returns `false` only when `<procfs>/self/mountinfo` is missing.
- **No ext4/btrfs**: empty `ext4` / `readonly_mounts` arrays with an `ok` status — a node without these filesystems is healthy, not broken.
- **Healthy ext4**: a missing `errors_count` file means no error was ever recorded (reported with count 0); an unreadable or unparseable counter skips that device.
- **btrfs CLI Missing / Failing**: without the binary the `btrfs` block is omitted and an explanatory `note` is added (status unaffected); a per-filesystem `btrfs device stats` failure skips that filesystem only.
- **XFS Limitation**: XFS exposes no cumulative error counters in sysfs; use `query_dmesg` for XFS corruption events (documented in Help). LVM/device-mapper ext4 devices appear under their `dm-N` sysfs name, which may not match the `/dev/mapper` mountinfo source — the `mount` field is then omitted.
- **Cancellation**: a cancelled context returns an encapsulated error result, never a hard Go error.
