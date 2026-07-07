#### `get_bond_status`
*Category: `network` · Runs on: Nodes with the Linux bonding module loaded (`/proc/net/bonding` present)*

Audits every configured bonding (link aggregation) device by parsing the kernel bonding driver's procfs report, extracting bond mode, MII status, the currently active slave, per-slave link health, and 802.3ad (LACP) aggregator details when present.

**Data Sources:**
- **Primary**: `/proc/net/bonding/<bond>` text files (one per configured bond) exposed by the bonding kernel module.
- Bond-level fields parsed: `Bonding Mode:`, `MII Status:`, `Currently Active Slave:`, and 802.3ad info (`LACP rate:`, Active Aggregator `Partner Mac Address:`).
- Per-slave sections parsed: `Slave Interface:`, `MII Status:`, `Speed:`, `Duplex:`, `Link Failure Count:`.
- No external binaries are executed.

**Mathematical Models / Formatting:**
- **Speed Decoding**: Converts the kernel `Speed:` field (`"25000 Mbps"`) into an integer `speed_mbps`; `Unknown`, negative, or malformed speeds decode to `0`.
- **Active Slave Normalization**: `Currently Active Slave: None` is decoded to an empty field instead of the literal string `None`.
- **Warning Reasons**: Per-bond deterministic warnings when the bond MII status is not `up`, any slave MII status is not `up`, or any slave has a nonzero `Link Failure Count`.
- **System Summary**: Precomputes `total_bonds`, `bonds_up`, `total_slaves`, `slaves_up`, and `bonds_with_warnings`; the result status escalates to `warning` when any warning reason exists.

**Degradation Profile:**
- `IsSupported()` returns `false` when `<procfs>/net/bonding` does not exist (bonding module not loaded).
- Unparseable or missing fields degrade to empty strings / zero values — parsing never fails on garbage content.
- Indented per-slave LACP PDU detail lines (e.g. `system mac address:`) are deliberately not matched, so partner data cannot leak into slave fields.
- An empty bonding directory is a valid result (`bonds: []`, status `ok`); unreadable individual bond files are skipped.
- A canceled context or an unreadable bonding directory returns an encapsulated `error` result (never a hard Go error).
