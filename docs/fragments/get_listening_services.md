#### `get_listening_services`
*Category: `network` · Runs on: Every Linux node*

Maps every listening TCP socket and bound UDP socket to its owning process — the native answer to "what is exposed on this node?", "which daemon holds this port?", and port-conflict triage. No `ss`/`netstat` binaries are involved: socket tables are parsed straight from procfs and joined to processes through a single `/proc/[pid]/fd` symlink walk.

**Data Sources:**
- **Primary**: `/proc/net/tcp` and `/proc/net/tcp6` — rows with `st == 0A` (LISTEN) only; `/proc/net/udp` and `/proc/net/udp6` — all bound sockets with a non-zero local port. Columns consumed: `local_address` (HEXIP:HEXPORT), `st`, `uid`, `inode`.
- **Process join**: `/proc/[pid]/fd/*` readlink targets of the form `socket:[<inode>]` build a one-pass inode→pid map, then `/proc/[pid]/comm` and `/proc/[pid]/cmdline` (NUL-separated argv joined with spaces, truncated to 80 characters) name the owner.

**Mathematical Models / Formatting:**
- **Hex decoding**: IPv4 addresses are little-endian hex (`0100007F` → `127.0.0.1`); IPv6 addresses are four 32-bit groups, each byte-swapped, reassembled into canonical form (`000080FE…01000000` → `fe80::1`); ports are big-endian hex. Filtering is deterministic.
- **Wildcard detection**: listeners bound to `0.0.0.0` or `::` carry `wildcard: true` so full-exposure sockets stand out.
- **Sorting & capping**: listeners are sorted ascending by port and capped at 200 entries with a `truncated` flag (lowest — best-known — ports are kept). Summary counters (`tcp_listeners`, `udp_sockets`, `resolved`, `unresolved`, `processes_scanned`, `fd_dirs_skipped`) always reflect the full scan.
- **Unresolved digest**: sockets whose inode matched no fd keep their listener entry (pid/comm/cmdline omitted) and are additionally digested into `unresolved[]` as `{proto, port, inode, uid}`.

**Degradation Profile:**
- `IsSupported()` returns `false` when `/proc/net/tcp` is missing (non-Linux or procfs unavailable).
- Missing `tcp6`/`udp6`/`udp` tables (e.g. IPv6 disabled) degrade silently to the tables that exist; only an unreadable primary `/proc/net/tcp` produces an error result.
- Without root, other users' `/proc/[pid]/fd` directories are unreadable: each is counted in `fd_dirs_skipped`, the affected sockets land in `unresolved[]`, and a note advises "run as root for complete socket→process mapping".
- Vanished pids, closed fds and malformed table rows are skipped, never fatal; unreadable `comm`/`cmdline` files simply omit those fields. The context is checked between pid iterations, and cancellation returns an encapsulated error result.
