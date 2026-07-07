#### `get_sensor_readings`
*Category: `hardware` · Runs on: Bare-metal Linux nodes exposing hwmon chips*

Collects every hardware monitoring sensor the kernel exposes — temperatures, fan tachometers, power draw, and voltage rails — normalized into human units and classified against hardware-defined thermal thresholds. Identifies overheating packages, dead fans, and sagging rails without requiring `lm-sensors` to be installed.

**Data Sources:**
- Chip Discovery: `/sys/class/hwmon/hwmon*/` iteration; attributes are resolved from the chip directory first, then the nested `device/` directory used by older kernel layouts.
- Chip Name: `hwmon*/name`.
- Temperatures: `temp<N>_input` (millidegrees C) with `temp<N>_label`, `temp<N>_max`, `temp<N>_crit`.
- Fans: `fan<N>_input` (RPM) with `fan<N>_label`.
- Power: `power<N>_average` preferred over `power<N>_input` (microwatts) with `power<N>_label`.
- Voltages: `in<N>_input` (millivolts) with `in<N>_label`.

**Mathematical Models / Formatting:**
- **Unit Normalization**: m°C → °C (1 decimal), µW → W (1 decimal), mV → V (3 decimals); labels fall back to the sysfs index (`temp1`) when no `_label` file exists.
- **Threshold Classification** (deterministic, per temperature): reading ≥ `crit` → `critical`, reading ≥ `max` → `warning`, otherwise `ok`; each breach is written to a top-level `warning_reasons[]` entry naming the chip, sensor, reading, and threshold.
- **Status Escalation**: any `critical` sensor → result status `error`, any `warning` sensor → `warning`, mirroring the EDAC tool's health mapping.
- **Payload Caps**: 64 sensors per type per chip and 20 warning reasons (overflow noted as `(+N more ...)`).

**Degradation Profile:**
- `IsSupported()` returns `false` with reason `no hwmon sensors exposed (common in VMs)` when `/sys/class/hwmon` is missing or contains no `hwmon*` directories.
- Individual unreadable or unparseable sensor files are skipped silently; chips exposing zero readable sensors are omitted from the output.
- Missing `temp<N>_max`/`temp<N>_crit` files omit the thresholds and leave the sensor status `ok` — never escalating on absent data.
