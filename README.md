# cortex-mcp

**A self-deploying diagnostic mesh for Linux fleets, exposed to LLMs through the [Model Context Protocol](https://modelcontextprotocol.io).**

`cortex-mcp` is a single static Go binary that deploys itself across a fleet of Linux hosts over SSH, wires the hosts into an encrypted peer-to-peer mesh, and presents the entire fleet to an MCP client (Claude, IDEs, agents) as one server with four meta-tools. Behind those meta-tools sit **36 read-only diagnostic tools** covering CPU, memory, storage, network, kernel, and HPC/Lustre state — routed automatically to the right node, or fanned out across the fleet with ClusterShell-style nodeset patterns.

Built with HPC storage clusters in mind (Lustre, DDN EXAScaler role detection, NVMe health, NUMA/EDAC auditing), but useful on any Linux fleet.

## How it works

```
MCP client (LLM / IDE)
        │  Streamable HTTP  /mcp
        ▼
┌───────────────────┐   get_tool_list · get_tool_help · call_tool · get_mesh_overview
│  Gateway (neuron) │   ← only 4 meta-tools exposed, so the LLM tool list never overflows
└─────────┬─────────┘
          │  mTLS (Ed25519 site CA) + zstd + yamux, over TCP / SSH / HTTP CONNECT
   ┌──────┼───────────┐
   ▼      ▼           ▼
 node1  node2  …  node[N]     ← each node runs the same binary ("neuron")
 36 diagnostic tools each, advertised as capabilities via gossip
```

- The **gateway is itself a mesh node** — it joins the mesh like any other peer, it just also speaks MCP.
- Nodes gossip capabilities and routes every ~3 s. Tool calls are **auto-routed to the least-loaded node** that has the capability (impedance-based cost routing), unicast to a named node, or fanned out to a nodeset (`*`, `oss[01-72]!oss[10-15]`, `@mdt`, …).
- If two nodes have no direct link, calls are relayed through multi-hop **circuits** stitched by intermediate peers.
- The gateway compares build versions on connect and **self-upgrades outdated fleet nodes** by streaming its own binary over the mesh.

## Quick start

Requires Go 1.26+, [Task](https://taskfile.dev), and SSH access to your targets.

```sh
task build                      # build ./cortex-mcp (CGO_ENABLED=0, static)

# 1. Describe your fleet
cat > mesh.toml <<'EOF'
[hosts.oss1]
addresses = ["10.0.2.19"]

[hosts.oss2]
addresses = ["10.0.2.94"]

[[credentials]]
pattern  = "10.0.2.*"
type     = "ssh_key"
username = "root"
key_file = "~/.ssh/id_ed25519"
EOF

# 2. Start the MCP server — deploys ephemeral agents to all hosts over SSH,
#    generates the site CA + node certs automatically, then serves MCP.
./cortex-mcp mcp                # Streamable HTTP on localhost:8080, endpoint /mcp

# 3. (optional) Make the fleet persistent as systemd services
./cortex-mcp install
```

Point your MCP client at `http://localhost:8080/mcp`. Start with the `get_mesh_overview` tool — it aggregates roles, versions, and topology across the fleet and renders a Mermaid diagram. `get_tool_list` accepts an optional `category` filter (`system`, `compute`, `memory`, `network`, `storage`, `hardware`). The server also ships a `system_introduction` prompt that teaches the LLM the meta-tool workflow.

Running `./cortex-mcp` with no arguments also works with **zero configuration**: it becomes a local-only node exposing this machine's diagnostics.

## CLI

| Command | Purpose |
|---|---|
| `cortex-mcp` | Run a gateway node (deploys to configured hosts, or local-only with no config) |
| `cortex-mcp mcp [ip:port]` | Gateway + MCP Streamable HTTP server (default `localhost:8080`) |
| `cortex-mcp daemon` | Run as a fleet node — persistent under systemd/terminal, ephemeral when SSH-deployed |
| `cortex-mcp bridge` | Lightweight relay node with no diagnostic tools |
| `cortex-mcp install [target]` | Persist fleet nodes as systemd services (`--force`, `--regenerate-keys`) |
| `cortex-mcp uninstall [target]` | Remove fleet nodes — mesh-first, SSH fallback |
| `cortex-mcp start` / `stop [target]` | Start/stop persistent services without uninstalling |
| `cortex-mcp pki generate\|issue\|show` | Manage the mesh CA and issue node certificates |
| `cortex-mcp import-exa <exascaler.toml>` | Convert a DDN EXAScaler config into `mesh.toml` (hosts + role groups) |
| `cortex-mcp version` | Print the injected build version |

Global flags: `--config <path>` (config file), `--skip-deploy` (reuse already-deployed binaries).

## Configuration (`mesh.toml`)

Search order: `./mesh.toml` → `/opt/cortex-mcp/etc/mesh.toml` → `/opt/cortex-mcp/bin/mesh.toml` → `<exe dir>/mesh.toml` → `~/.cortex-mcp/mesh.toml`.

```toml
[node]
id        = "gateway-01"   # default: $CORTEX_NODE_ID, then hostname
mesh_port = 4443           # default P2P port
ssh_port  = 22             # default deploy port

[hosts.oss1]               # deployment/connection targets
addresses = ["10.0.2.19"]  # per-host mesh_port / ssh_port overrides supported

[[credentials]]            # ssh_key | ssh_password | tls_cert; glob pattern,
pattern  = "10.0.2.*"      # most-specific match wins
type     = "ssh_key"
username = "root"
key_file = "~/.ssh/id_ed25519"

[[proxies]]                # optional HTTP CONNECT fallback per host pattern
pattern = "*.dmz.corp"
url     = "http://proxy:3128"

[groups]                   # nodeset groups for @group targeting
mdt = ["mds1", "mds2"]
```

Dialing falls back automatically: direct TCP → configured proxy → gossiped proxy → SSH tunnel using vault credentials. **Credentials never leave the gateway** — the config is stripped before being pushed to fleet nodes.

## Diagnostic tools

37 visible read-only tools (36 host diagnostics + 1 mesh introspection), all returning a uniform envelope `{tool_name, node_id, status: ok|warning|error|degraded, summary, data, metadata}`. Tools self-detect support at startup (`IsSupported`) so only applicable tools are advertised per node.

<details>
<summary><strong>System (10)</strong></summary>

`get_system_info`, `get_uptime`, `get_loadavg`, `query_dmesg`, `query_journalctl`, `get_systemd_status`, `get_kernel_modules`, `get_kernel_module_info`, `get_open_file_limits`, `get_sysctl_tuning_state`
</details>

<details>
<summary><strong>Compute (7)</strong></summary>

`get_cpu_topology`, `get_cpu_power_state`, `get_irq_affinity`, `get_cgroup_limits`, `get_process_list`, `get_process_tree`, `get_thread_wchan`
</details>

<details>
<summary><strong>Memory (5)</strong></summary>

`get_memory_info`, `get_numa_stats`, `get_buddy_info`, `get_hugepage_info`, `get_slab_info`
</details>

<details>
<summary><strong>Network (6)</strong></summary>

`get_network_interfaces`, `get_socket_stats`, `get_routing_table`, `get_routing_rules`, `get_eth_hardware_stats`, `get_nic_ethtool_stats`
</details>

<details>
<summary><strong>Storage (7)</strong></summary>

`get_disk_io_stats`, `get_block_topology`, `get_block_scheduler_info`, `get_mount_usage`, `get_nvme_smart_log`, `get_nfs_client_stats`, `get_lustre_client_stats`
</details>

<details>
<summary><strong>Hardware (1)</strong></summary>

`get_numa_edac_errors`
</details>

<details>
<summary><strong>Mesh (1)</strong></summary>

`get_mesh_topology` — zero-network snapshot of the mesh as seen by the node it runs on (direct peers, gossip-learned routes, impedance, capabilities)
</details>

Six additional **lifecycle tools** (`node_install`, `node_uninstall`, `node_restart`, `node_stop`, `node_upgrade`, `node_deploy`) are hidden from discovery and dry-run by default; host mutations execute only whitelisted declarative operations — no arbitrary shell. `node_deploy` lets any node act as a jumphost to deploy hosts the gateway can't reach directly.

### Targeting

`call_tool` accepts a `node_name` using ClusterShell-compatible nodeset syntax:

```
oss1                     exact node          node[01-72]        zero-padded range
*                        every node          node[1-9/2]        stepped range
mds-*                    glob                rack[1-2]node[1-3] Cartesian product
oss[1-10]!oss[5-7]       difference          @mdt, @slurm:io    group references
```

Omit `node_name` entirely to auto-route to the least-loaded capable node.

## Security model

- **Ed25519 PKI, generated automatically.** A self-signed site CA (`mesh_ca.json`) issues per-node certs; every mesh link is TLS 1.3 mTLS trusting only the site CA, then zstd-compressed and yamux-multiplexed.
- **Bootstrap over SSH.** Ephemeral nodes receive their config and cert bundle via the SSH stdin pipe before the mesh handshake — no secrets on disk, node state lives under `/opt/cortex-mcp/etc/` (root) or `~/.cortex-mcp/` (user), mode 0600.
- **Encrypted credential vault.** SSH/TLS credentials are sealed with AES-256-GCM (key HKDF-derived from the node's Ed25519 key); peer-to-peer credential delegation uses NaCl box with nonce binding and 5-minute grant TTLs.
- **Read-only by design.** Diagnostic tools only read procfs/sysfs or invoke read-only commands (`journalctl`, `ethtool`, `nvme smart-log`, …). Mutation is confined to the hidden lifecycle tools behind an explicit live-mode gate.

## Development

```sh
task check          # fmt-check + vet + lint + tests + benchmark coverage
task test           # fmt + vet + go test ./...
task bench          # all benchmarks; `task benchtrack` saves dated results
task build:prod     # trimmed, UPX-compressed release binary
task proto          # regenerate pkg/mesh/proto from mesh.proto
```

The project follows a strict TDD workflow (see `.agents/skills/`): behavior-defining tests are committed before implementations, and `task benchcov` fails the build if any package lacks benchmark coverage.

### Layout

| Path | Contents |
|---|---|
| `cmd/cortex-mcp` | Entry point |
| `internal/mesh` | CLI (cobra), PKI, identity, deploy/upgrade orchestration |
| `internal/mcp` | MCP server (official `modelcontextprotocol/go-sdk`), meta-tools, mesh overview |
| `internal/registry` | Tool contract, plugin registry, single-item result cache, support detection |
| `internal/registry/tools/*` | One package per diagnostic tool (registered via `init()`) |
| `internal/registry/stream` | Bounded-memory streaming primitives (top-k, dedup, ring buffer, aggregation) |
| `internal/sys` | Pure parsers/collectors for procfs & sysfs (cpu, memory, process, storage, EDAC) |
| `pkg/mesh` | Reusable mesh stack: `api` (neuron), `membrane` (mTLS/zstd/yamux), `routing` (sonar, gradient, circuits), `nucleus` (identity/impedance), `nodeset`, `vault`, `transport`, `gateway`, `proto` |
| `testutil` | Benchmark coverage & tracking tools, in-memory pipes, test certs |

## Further reading

- [docs/QUICK_START.md](docs/QUICK_START.md) — step-by-step setup
- [docs/TOOL_CATALOG.md](docs/TOOL_CATALOG.md) — full tool reference
- [docs/PKI.md](docs/PKI.md) — certificate management details
