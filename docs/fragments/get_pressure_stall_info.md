#### `get_pressure_stall_info`
*Category: `system` · Runs on: Every Linux node with PSI enabled (kernel ≥ 4.20, not booted with `psi=0`)*

Reads the kernel's Pressure Stall Information (PSI) accounting to quantify how much wall-clock time tasks spend stalled waiting for CPU, memory, I/O, and IRQ. This is the canonical saturation signal: non-zero `full` pressure means every non-idle task was blocked simultaneously — pure lost throughput.

**Data Sources:**
- Read directly from `/proc/pressure/cpu`, `/proc/pressure/memory`, `/proc/pressure/io`.
- `/proc/pressure/irq` (optional; kernels ≥ 6.1 only).

**Mathematical Models / Formatting:**
- Decodes each `some`/`full` record into `avg10`/`avg60`/`avg300` percentages plus the cumulative stall total in microseconds (`total_usec`).
- Pre-evaluated `warning_reasons` on the 10-second averages: `cpu some avg10 > 40%` (CPU contention), `memory full avg10 > 10%` (reclaim/thrashing stalls), `io full avg10 > 10%` (storage saturation). Any breach flips the result status to `warning`.

**Degradation Profile:**
- `IsSupported()` returns `false` when `/proc/pressure/cpu` is missing ("PSI not available (kernel < 4.20 or psi=0)").
- A missing `irq` file is normal on kernels < 6.1 and is silently omitted; the `cpu` resource has no `full` record on kernels < 5.13.
- A malformed resource file is skipped and reported in `notes`; if no resource is readable at all, an error result is returned.
