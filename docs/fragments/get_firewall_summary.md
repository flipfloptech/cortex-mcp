#### `get_firewall_summary`
*Category: `network` · Runs on: Every Linux node with `nft`, `iptables-save`, or `iptables` in `PATH`*

Summarizes the host packet-filter configuration without dumping the full ruleset: the active backend, every table and chain with its policy, per-chain rule counts, and packet/byte counters. A factual tool — it reports state and emits no warnings by default. When both `table` and `chain` parameters are provided, the matching chain's individual rules are rendered compactly (capped at 100).

**Data Sources:**
- **Primary**: `nft -j list ruleset` JSON output (`nftables[]` array of `{table}`, `{chain}`, and `{rule}` objects; `metainfo`, sets, and named counters are ignored).
- **Fallback**: `iptables-save -c` text output (`*table` headers, `:CHAIN POLICY [pkts:bytes]` policy counters, `-A` rule lines). As a last resort, `iptables -S` is parsed (filter table only, no counters). Commands are wrapped with `sudo -n` when not running as root.

**Mathematical Models / Formatting:**
- **Counter Semantics**: `packets`/`bytes` per chain are the policy counters from `:CHAIN POLICY [pkts:bytes]` for iptables, but the **sum of anonymous rule counter expressions** for nftables (nftables chains carry no implicit counters). Named counter references (`{"counter": "name"}`) are skipped.
- **Activity Heuristic**: `firewall_active = total_rules > 0`; an empty ruleset is a valid `ok` result with `firewall_active: false`.
- **Rule Rendering**: With `table` + `chain` given, nftables rules are rendered as `family table chain handle N: <compact expr JSON>` (plus comment); iptables rules are the raw `-A ...` lines. Output is capped at 100 rules with `rules_truncated: true` beyond that, and `requested_chain_found` reports whether the chain exists. Chain names are matched case-sensitively (nftables lowercase, iptables uppercase).

**Degradation Profile:**
- `IsSupported()` returns `false` if none of `nft`, `iptables-save`, or `iptables` are in `PATH`.
- If `nft` fails for a non-permission reason (e.g. no nf_tables kernel support), the tool falls back to `iptables-save -c`, then `iptables -S`.
- Permission-denied output from any backend (`permission denied`, `operation not permitted`, sudo password prompts) produces an error result explaining the root / passwordless-sudo requirement instead of a partial answer.
