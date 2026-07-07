# Catalog update: `get_cpu_power_state` — thermal throttle counters

Additive enhancement only. Merge the bullets below into the existing
`get_cpu_power_state` entry in `docs/TOOL_CATALOG.md`; no existing bullets
change. The tool now surfaces a top-level `thermal_throttle` block plus a
`warning_reasons` array in its data payload.

**Data Sources (add):**
- **Thermal Throttle Events**: Reads `/sys/devices/system/cpu/cpu*/thermal_throttle/` for `core_throttle_count` and `package_throttle_count` — cumulative throttle event counts since boot exposed by the x86 thermal interrupt driver (Intel-specific; absent on AMD and most VMs).
- **Package Topology**: Reads `cpu*/topology/physical_package_id` to attribute package throttle counters to distinct physical packages.

**Mathematical Models / Output Structuring (add):**
- **Core Event Sum**: `core_events_total` is the sum of `core_throttle_count` across all logical CPUs; `cpus_with_core_events` counts the CPUs reporting a non-zero core counter.
- **Distinct Package Sum**: Every CPU in a physical package reports the same `package_throttle_count`, so `package_events_total` sums one counter per distinct `physical_package_id` (avoiding N-way overcounting on SMT/multicore parts).
- **Throttle Warning**: When `core_events_total > 0` or `package_events_total > 0`, the result status is elevated to `warning` and `warning_reasons` carries `"CPU has thermally throttled since boot (core events: N, package events: M)"`; the warning text becomes the result summary.
- The block is collected independently of cpufreq visibility, so it is also present on BIOS/Hypervisor-managed systems that expose the interface.

**Degradation Profile (add):**
- **Interface Absent (AMD / VMs)**: When `cpu0/thermal_throttle/` does not exist, the entire `thermal_throttle` block is omitted from the payload (no error, no empty object) and status behavior is unchanged.
- **Unreadable Counters**: Individually unreadable or malformed counter files are treated as `0` events rather than failing the scan.
- **Topology Missing**: When `topology/physical_package_id` is unavailable, `package_events_total` degrades to the maximum package counter observed across CPUs and the payload is flagged with `"package_count_approximate": true`.
