#### `get_scheduled_jobs`
*Category: `system` · Runs on: Linux nodes with systemd and/or cron*

Inventories all recurring background work on the node: systemd timers and cron entries. Essential for explaining periodic load spikes, tracing unexpected file changes, and auditing active automation.

**Data Sources:**
- systemd timers: `systemctl list-timers --all --no-pager --output=json`; on older systemd without JSON support, the plain-text table is parsed as a fallback (unit/activates always recovered, timestamps best-effort).
- System cron parsed natively: `/etc/crontab` and `/etc/cron.d/*` (`m h dom mon dow user command` format).
- User crontabs parsed natively: `/var/spool/cron/crontabs/*` (Debian) and `/var/spool/cron/*` (RHEL) — one file per user, no user column.

**Mathematical Models / Formatting:**
- Timer microsecond-epoch fields (`next`, `last`) converted to RFC3339 UTC (`next_iso`, `last_iso`), `null` preserved for never/none.
- Comments and environment lines skipped; `@reboot`/`@daily` specials preserved verbatim as the schedule.
- Token caps: timers limited to 50, cron entries to 100, commands truncated to 120 characters; `summary` always carries the total discovered counts (`timers`, `cron_entries`, `crontabs_skipped`).

**Degradation Profile:**
- `IsSupported()` returns `false` only when `systemctl` is absent from `$PATH` **and** no cron path exists.
- `systemctl` missing or failing: timers list empty, cron still reported.
- Unreadable user crontab files/dirs (running unprivileged): counted in `summary.crontabs_skipped`, execution still returns status `ok`.
