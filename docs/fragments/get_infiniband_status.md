#### `get_infiniband_status`
*Category: `network` · Runs on: Nodes with InfiniBand/RoCE HCAs (sysfs `class/infiniband` present)*

Audits every InfiniBand/RoCE HCA port natively from sysfs, decoding link state, physical state, rate, LID, and per-port error counters, and raising deterministic warnings for non-ACTIVE links or nonzero error counters.

**Data Sources:**
- **Primary**: `/sys/class/infiniband/<hca>/ports/<n>/{state,phys_state,rate,lid,link_layer}` sysfs attributes.
- **Primary**: `/sys/class/infiniband/<hca>/ports/<n>/counters/` error counter files: `symbol_error`, `link_downed`, `link_error_recovery`, `port_rcv_errors`, `port_xmit_discards`.
- No external binaries are executed.

**Mathematical Models / Formatting:**
- **State Decoding**: Strips numeric prefixes from kernel state strings (`"4: ACTIVE"` → `ACTIVE`, `"5: LinkUp"` → `LinkUp`).
- **Rate Decoding**: Preserves the raw rate string (`"100 Gb/sec (4X EDR)"`) and precomputes a numeric `rate_gbps` field for the leading speed value.
- **LID Decoding**: Hex-decodes the sysfs `lid` attribute (`0x3` → `3`) into an integer.
- **Warning Reasons**: Per-port deterministic warnings when `state != ACTIVE`, `phys_state != LinkUp`, or any error counter is nonzero.
- **System Summary**: Precomputes `hca_count`, `total_ports`, `active_ports`, and `ports_with_errors`; the result status escalates to `warning` when any warning reason exists.

**Degradation Profile:**
- `IsSupported()` returns `false` with reason "no InfiniBand devices (sysfs path missing)" when `<sysfs>/class/infiniband` does not exist.
- Absent counter files (common on RoCE ports) are omitted from the `counters` map rather than zero-filled; a fully absent `counters/` directory omits the map entirely.
- RoCE ports (`link_layer: Ethernet`) are reported factually with the same fields and warning logic; no special-casing.
- HCAs without a readable `ports/` directory and non-numeric entries in `ports/` are skipped; missing attribute files degrade to empty/zero fields instead of failing.
- A canceled context or an unreadable `class/infiniband` directory returns an encapsulated `error` result (never a hard Go error).
