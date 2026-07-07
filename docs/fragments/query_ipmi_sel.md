#### `query_ipmi_sel`
*Category: `hardware` · Runs on: Bare-metal nodes with a BMC exposing an IPMI character device*

Queries the baseboard management controller's System Event Log to surface out-of-band hardware events — temperature excursions, fan failures, ECC faults, PSU state changes — recorded by the BMC independently of the host OS, along with the SEL's remaining capacity. `ipmitool` is the justified primary source since SEL access requires the vendor IPMI protocol over the kernel's BMC character device.

**Data Sources:**
- **Primary records**: `ipmitool sel elist` (wrapped in `sudo -n` when the effective UID is not 0 and sudo is available), parsed row-by-row into `{id, timestamp, sensor, event, direction}`.
- **Capacity metadata**: `ipmitool sel info`, parsed tolerantly (`Entries`, `Free Space`, `Percent Used` labels vary slightly between BMC firmwares; the first integer in each value is extracted).
- **Device gate**: a BMC character device must exist at `/dev/ipmi0`, `/dev/ipmi/0` or `/dev/ipmidev/0`.

**Mathematical Models / Formatting:**
- Converts `MM/DD/YYYY HH:MM:SS` date/time columns to RFC3339; events logged before BMC clock initialization keep their raw `Pre-Init` marker.
- Aggregates the entire SEL into `counts_by_sensor_type` (e.g. `Temperature: 3`) by stripping the `#0xNN` sensor suffix, while `records` returns only the `last_n` most recent rows (default 25, capped at 200) to bound token payload.
- Heuristic `warning_reasons` flag any record whose event contains `Critical` or `Non-recoverable` (case-sensitive, so IPMI's warning-level `Non-critical` does not false-positive) and SEL usage above 75%; itemized critical warnings are capped at 10 with an aggregate overflow entry.

**Degradation Profile:**
- `IsSupported()` returns `false` when `ipmitool` is missing from `PATH` or no BMC character device exists — the reason string distinguishes which prerequisite failed.
- Permission failures (sudo password required, device permission denied, insufficient privilege level) return the encapsulated error result `Unauthorized: Root or passwordless sudo privileges required for BMC access.` instead of a hard Go error.
- A failing `sel info` degrades gracefully: records are still returned and `sel_info` is omitted from the payload. An empty SEL ("SEL has no entries") yields an `ok` result with an empty records list.
