#### `query_coredumps`
*Category: `system` · Runs on: Nodes with systemd-coredump (coredumpctl or /var/lib/systemd/coredump)*

Queries recent application crashes captured by systemd-coredump, surfacing crash-looping executables and the fatal signals that killed them. Accepts an optional `last_n` parameter (default 20, capped at 100).

**Data Sources:**
- **Primary**: `coredumpctl list --json=short --no-pager` — a JSON array with µs-epoch `time`, `pid`, `uid`, `sig`, `corefile` state (`present`|`missing`|`none`), and executable path. Exit status 1 with `No coredumps found` is treated as a valid empty result, not a failure.
- **Fallback**: filename scan of `/var/lib/systemd/coredump`, parsing `core.<comm>.<uid>.<boot-id>.<pid>.<timestamp>[.zst]` entries when the `coredumpctl` binary is unavailable or fails. The comm segment may contain dots, so fixed fields are anchored from the right.

**Mathematical Models / Formatting:**
- **Signal Decoding**: Common fatal signals are decoded to names (4 → `SIGILL`, 6 → `SIGABRT`, 7 → `SIGBUS`, 8 → `SIGFPE`, 11 → `SIGSEGV`); anything else renders as `SIG<n>`. Filename-fallback entries omit the signal (not encoded in the name).
- **Temporal Ordering**: Dumps are sorted newest-first on the raw µs timestamp before the `last_n` cut; µs-epoch values are emitted as RFC3339 UTC.
- **Crash-Loop Aggregation**: `count_by_executable` is computed over ALL dumps found (not just the returned window) alongside `total`, so repeat offenders are visible even beyond the cap.

**Degradation Profile:**
- `IsSupported()` returns `false` only when there is no `coredumpctl` binary and no coredump spool directory.
- "No coredumps found" and an empty spool directory both return `ok` with zero dumps.
- Unparseable spool filenames are skipped individually, never fatal.
- A hard `coredumpctl` failure silently falls back to the directory scan when the spool directory exists.
