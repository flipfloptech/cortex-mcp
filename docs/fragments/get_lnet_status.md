#### `get_lnet_status`
*Category: `network` · Runs on: Nodes with the LNet kernel module loaded or `lnetctl` installed (Lustre servers, routers, and clients)*

Reports the health of the LNet fabric layer used by Lustre: local network interfaces (NIs) with up/down status and tx credit levels, known peer count, and global message counters (send/recv/route/drop/errors).

**Data Sources:**
- **Primary**: `/sys/kernel/debug/lnet/nis` (NI status, refs, max/tx/min credits), `/sys/kernel/debug/lnet/peers` (peer count), and `/sys/kernel/debug/lnet/stats` (positional message counters). debugfs typically requires root.
- **Fallback**: `lnetctl net show` and `lnetctl stats show` command outputs, parsed by simple `key: value` indentation scanning (no YAML library), wrapped with `sudo -n` when not running as root.

**Mathematical Models / Formatting:**
- **Positional Decoding**: The single-line `stats` file is decoded into named counters (`msgs_alloc msgs_max errors send_count recv_count route_count drop_count send_length recv_length route_length drop_length`).
- **Credit Semantics**: `max/tx/min` credits are emitted as optional fields; a negative minimum tx credit means messages had to queue waiting for credits (fabric congestion). Credits are debugfs-only and omitted on the `lnetctl` path instead of emitting misleading zeros.
- **Warning Reasons**: Deterministic triggers — any NI whose status is not `up` (NID lists capped at 5 entries), negative `min` tx credits (credit starvation), and `drop_count > 0`. Any trigger flips the result status to `warning`.
- **Source Attribution**: The `source` field records whether `debugfs` or `lnetctl` served the data.

**Degradation Profile:**
- `IsSupported()` returns `false` with reason `"LNet not loaded and lnetctl not found"` if `/sys/module/lnet` does not exist and `lnetctl` is not in `PATH`.
- If `/sys/kernel/debug/lnet/nis` is unreadable (non-root debugfs), the tool falls back to `lnetctl net show` / `lnetctl stats show`. Peer count is debugfs-only and reported with `peers_available: false` on the fallback path.
- If debugfs is unreadable and `lnetctl` is missing or denied, the tool returns an error result explaining the root / passwordless-sudo requirement. Missing `peers`/`stats` files degrade gracefully (`peers_available` / `stats_available` set to `false`) rather than failing.
