#### `get_time_sync_status`
*Category: `system` · Runs on: Every Linux node*

Audits system clock discipline: whether the clock is synchronized, by how much it drifts, which kernel clocksource is active, and which NTP daemon (if any) is steering it. Clock skew silently breaks TLS, Kerberos, distributed locks, log correlation, and lease logic.

**Data Sources:**
- Native `adjtimex(2)` syscall in read-only mode (`modes=0`): `STA_UNSYNC` synchronization flag, clock offset (nanoseconds when `STA_NANO` is set, microseconds otherwise), estimated/maximum error bounds.
- `/sys/devices/system/clocksource/clocksource0/current_clocksource` and `available_clocksource`.
- Enrichment via binaries when present: `chronyc -c tracking` (stratum, reference source, daemon offset, leap status) or `timedatectl show` (`NTP=`/`NTPSynchronized=` properties, attributed to `systemd-timesyncd`).

**Mathematical Models / Formatting:**
- Kernel offset normalized to milliseconds with 2 decimal places, sign preserved (`STA_NANO` unit handling).
- `warning_reasons`: clock not synchronized; absolute offset > 100 ms; current clocksource is not `tsc` on amd64 (caveat: paravirtual clocksources such as `kvm-clock`/`hyperv_clocksource` are normal on VMs).

**Degradation Profile:**
- `adjtimex` denied (`EPERM`, common in unprivileged containers): falls back to daemon queries only and reports `"kernel_status_available": false`; synchronization is then judged from the daemon (chrony leap status / `NTPSynchronized`).
- No `chronyc`/`timedatectl`: the `ntp_daemon` object is omitted; kernel + clocksource data still returned.
- Missing clocksource sysfs files: the `clocksource` object is omitted.
