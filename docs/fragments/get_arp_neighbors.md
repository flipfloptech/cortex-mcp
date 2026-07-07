#### `get_arp_neighbors`
*Category: `network` · Runs on: Every Linux node (procfs ARP table or `ip` binary)*

Collects the kernel neighbor (ARP/NDP) tables, classifies every entry by resolution state, and surfaces broken address resolution first: FAILED and INCOMPLETE neighbors are prioritized ahead of healthy ones, and every FAILED neighbor is always listed in full.

**Data Sources:**
- **Primary**: `/proc/net/arp` text table (IPv4; columns IP / HWtype / Flags / HWaddress / Mask / Device).
- **Fallback/Enrichment**: `ip -j neigh` JSON output (adds IPv6 neighbors and granular NUD states: `reachable`, `stale`, `failed`, `incomplete`, `permanent`, `delay`, `probe`).

**Mathematical Models / Formatting:**
- **Flag Decoding**: procfs hex flags decode by bit — `ATF_PERM` (`0x4`, thus also `0x6`) → `permanent`, `ATF_COM` (`0x2`) → `reachable`, `0x0` → `incomplete`; unparseable flags → `unknown`.
- **Enrichment Merge**: `ip neigh` entries are overlaid onto procfs entries keyed by `(ip, device)`; granular NUD states win over coarse flag mappings, MACs are filled in when missing, and unmatched entries (e.g., IPv6) are appended.
- **MAC Normalization**: The all-zero placeholder MAC (`00:00:00:00:00:00`) of incomplete entries is blanked.
- **Prioritized Capping**: Entries are stably sorted `failed` → `incomplete` → all other states, then capped at 50 (`entries_shown`, `truncated`); `total_entries` and `counts_by_state` always reflect the full table, and the `failed` list is never truncated.
- **Warning Reasons**: A warning is raised (status `warning`) when any neighbor is in the FAILED state.

**Degradation Profile:**
- `IsSupported()` returns `false` only when `/proc/net/arp` is missing **and** the `ip` binary is not in `PATH`.
- If `ip` is missing, fails to execute, or returns unparseable JSON, the tool degrades to procfs-only IPv4 data, sets `ipv6_included: false`, and records the reason in a `note` field.
- If `/proc/net/arp` is unreadable but `ip -j neigh` works, the ip output alone is used.
- When neither source is usable, or the context is canceled, an encapsulated `error` result is returned (never a hard Go error).
