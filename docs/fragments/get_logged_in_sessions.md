#### `get_logged_in_sessions`
*Category: `system` · Runs on: Every Linux node with /run/utmp or systemd-logind*

Enumerates active interactive login sessions — local TTYs and remote SSH/PTY logins — revealing who is on the node, from where, and since when. Useful for correlating performance anomalies or configuration drift with human activity.

**Data Sources:**
- **Native**: `/run/utmp` binary session database. Each 384-byte record (little-endian x86_64 glibc layout) is decoded with `encoding/binary`; NUL-padded C strings are trimmed. Only `USER_PROCESS` (type 7) records — real interactive logins — are reported; reboot, runlevel, and dead-process records are filtered out.
- **Fallback**: `loginctl list-sessions --output=json` (systemd-logind) when the utmp database is missing or unreadable. This path carries no login timestamp or leader PID, so those fields degrade to empty/`0`.

**Mathematical Models / Formatting:**
- **Timestamp Normalization**: utmp `timeval` (32-bit sec/usec) is converted to RFC3339 UTC `login_time`.
- **Local vs Remote**: `remote_host` is the origin host/IP for remote logins and empty for local console sessions — no reverse-DNS heuristics are applied.
- **List Capping**: The session list is capped at 100 entries; `count` reflects the returned list. The `source` field (`utmp`|`loginctl`) tells the LLM which fidelity level was used.

**Degradation Profile:**
- `IsSupported()` returns `false` only when `/run/utmp` is absent AND no `loginctl` binary exists.
- Malformed or truncated utmp records are skipped individually, never fatal.
- Zero active sessions is a valid `ok` result (headless/compute nodes).
- loginctl execution or JSON parse failures return an encapsulated error result, never a hard Go error.
