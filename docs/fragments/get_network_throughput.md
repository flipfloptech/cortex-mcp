#### `get_network_throughput`
*Category: `network` · Runs on: Every Linux node*

Measures live per-interface network throughput by sampling the kernel's cumulative interface counters twice over a short, context-cancellable window — the native answer to "is this link saturated?" and "are we dropping packets right now?". Loopback is excluded; every physical and virtual NIC with counters is measured.

**Data Sources:**
- **Primary**: `/proc/net/dev` sampled twice over `sample_duration_ms` (default 500 ms, clamped to 10–5000 ms). Per interface, the rx `bytes/packets/errs/drop` and tx `bytes/packets/errs/drop` columns are consumed.
- **Link speed**: `/sys/class/net/<iface>/speed` (negotiated Mbps; absent, unreadable, or `-1` on virtual interfaces).

**Mathematical Models / Formatting:**
- **Rates from deltas** (deterministic): `rx_mbps`/`tx_mbps` = bytes-delta × 8 / 10⁶ / elapsed seconds, rounded to 2 decimal places; `rx_pps`/`tx_pps` and the per-second drop/error rates are rounded to 1 decimal place. `window_ms` reports the actually elapsed window, not the requested one.
- **Utilization**: `utilization_pct` = max(`rx_mbps`, `tx_mbps`) / link speed × 100 at 1 decimal place; omitted together with `link_speed_mbps` whenever sysfs reports no usable speed, so the LLM never divides by an unknown.
- **Summary**: precomputes `total_rx_mbps`, `total_tx_mbps`, `busiest_interface` (highest combined rx+tx Mbps) and `interfaces_measured`; interfaces are listed sorted by name.
- **Warnings** (result status elevates to `warning`): any packet drops or interface errors observed during the window, and utilization strictly above 90% of the link speed.

**Degradation Profile:**
- `IsSupported()` returns `false` when `/proc/net/dev` is missing (non-Linux or procfs unavailable).
- A counter that wraps or resets mid-window (second sample below the first) zeroes that interface's rates for this run and records a note in `notes[]` instead of reporting a bogus negative rate.
- Interfaces appearing or disappearing between the two samples are skipped and noted; malformed rows are ignored.
- Context cancellation during the sampling window returns a prompt encapsulated error result (the tool never waits out the window after cancellation); unreadable snapshots and malformed arguments also return error results, never hard Go errors.
