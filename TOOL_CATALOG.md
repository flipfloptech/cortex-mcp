# Cortex MCP Tool Catalog

This catalog documents every tool exposed by the Cortex MCP Server, detailing their data sources, mathematical models, MCP exposure, and degradation profiles.

## MCP Exposure Model

The Cortex MCP gateway exposes tools to LLMs through a **meta-tool pattern**. Instead of registering hundreds of tools directly with the MCP protocol (which would overwhelm context windows), the gateway exposes exactly **4 meta-tools**:

| Meta-Tool | Purpose | MCP Exposed |
|---|---|---|
| `list_tools` | Discover available tools across the mesh | ✅ Direct |
| `tool_help` | Get JSON schema for a specific tool | ✅ Direct |
| `call_tool` | Invoke any tool (unicast, fan-out, auto-route) | ✅ Direct |
| `cluster_overview` | Aggregate fleet topology, roles, and Mermaid graph | ✅ Direct |

All other tools are **indirectly accessible** through `call_tool`. The LLM uses `list_tools` to discover them and `call_tool` to invoke them. This means every tool in this catalog is reachable by the LLM — just through the meta-tool layer.

---

## Core Philosophy

### Graceful Degradation
Tools must prioritize availability over absolute completeness. If a tool expects secondary data (e.g., `/etc/os-release` for the OS name) but cannot find it, it should return `"unknown"` or an empty string rather than failing the execution. Hard failures (`IsSupported() = false`) should be reserved for scenarios where the fundamental operation of the tool is impossible (e.g., a required binary is missing, or the core sysfs tree does not exist).

### Role-Based Tool Activation
Tools are dynamically registered based on the specialized storage roles of the node they run on. The `internal/registry.DetectNodeRoles()` engine determines these capabilities by inspecting sysfs:

- **SFA Controllers**: Detected via the presence of `/sys/module/jsysdd`, `/sys/class/jsys`, `/sys/module/jnvme`, or `/sys/class/jnvme`.
- **Lustre MGS**: Detected via active instances in `/sys/fs/lustre/mgs/`.
- **Lustre MDS**: Detected via active targets in `/sys/fs/lustre/mdt/`.
- **Lustre OSS**: Detected via active targets in `/sys/fs/lustre/obdfilter/`.
- **Lustre Client**: Detected via active mounts in `/sys/fs/lustre/llite/`.

A single physical node can represent any combination of these roles (e.g., an SFA storage controller running hyperconverged MGS + MDS + OSS targets). If no specialized roles are detected, the node is classified as **`generic`** and only core diagnostic tools will be advertised.

---

## Plugin Tools (registry.Tool)

These tools follow the `registry.Tool` interface, are auto-registered via `init()`, and run locally on each fleet node. They are **indirectly MCP-exposed** via `call_tool`.

### `system_info`
*Category: `system` · MCP Access: `call_tool` · Runs on: Every node*

Gathers foundational telemetry about the host system. This tool is designed to run universally on any Linux environment, including minimal containers and heavily stripped OS deployments.

**Data Sources:**
- **Hostname**: Retrieved via standard OS calls (`os.Hostname()`).
- **CPUs**: Number of logical cores reported by the Go runtime (`runtime.NumCPU()`).
- **Kernel Version**: Read directly from `/proc/sys/kernel/osrelease`.
- **OS Distribution**: Parsed from `/etc/os-release`, prioritizing the `PRETTY_NAME` field and falling back to `ID`.
- **Node Roles**: Detected via `registry.DetectNodeRoles()`, which inspects sysfs paths for SFA controllers, Lustre MGS/MDS/OSS targets, and mounted Lustre clients. Returns `["generic"]` when no specialized roles are detected.
- **Role Info**: Detailed `NodeRoleInfo` struct with per-target lists (e.g., active MDTs, OSTs, mounted filesystems).

**Degradation Profile:**
- `IsSupported()` will only return `false` if the host operating system is not Linux.
- If `/proc/sys/kernel/osrelease` or `/etc/os-release` are missing or unreadable, the tool degrades gracefully by returning `"unknown"` or empty strings. If no Lustre/SFA sysfs paths exist, `roles` returns `["generic"]` — the tool never errors.

### `uptime`
*Category: `system` · MCP Access: `call_tool` · Runs on: Every Linux node*

Reads and calculates system uptime and CPU idle time. Formats the data into pre-processed human-readable strings to reduce the mathematical overhead for LLMs consuming the API.

**Data Sources:**
- **Uptime/Idle**: Read from `/proc/uptime`. The first value is the total system uptime, and the second is the total idle time across all CPUs.
- **CPU Count**: Number of logical cores reported by the Go runtime (`runtime.NumCPU()`).

**Mathematical Models:**
- **Idle Percentage**: Calculated dynamically as `(IdleSeconds / (UptimeSeconds * CPUCount)) * 100.0`.
- **Human Readable Format**: Converts seconds into a comma-separated duration string (e.g., `4 days, 1 hours, 25 minutes, 35 seconds`).

**Degradation Profile:**
- `IsSupported()` returns `false` if the host OS is not Linux, or if `/proc/uptime` is unreadable/missing.
- If the system is so fresh or constrained that uptime/CPU math would cause a division by zero, the tool fails gracefully or reports 0% idle.

### `loadavg`
*Category: `system` · MCP Access: `call_tool` · Runs on: Every Linux node*

Reads the system load averages and scheduling entity statistics to provide a snapshot of system CPU and I/O pressure.

**Data Sources:**
- Read directly from `/proc/loadavg`.

**Mathematical Models:**
- Directly extracts the 1-minute, 5-minute, and 15-minute load averages.
- Parses the scheduling entity ratio (e.g., `1/863`) into two distinct integers: `RunnableEntities` and `TotalEntities`.

**Degradation Profile:**
- `IsSupported()` returns `false` if the host OS is not Linux, or if `/proc/loadavg` is unreadable/missing.

---

## Mesh Infrastructure Tools

These tools are registered directly with the cortex-mesh `tools.Registry` (not via the plugin system). They are **indirectly MCP-exposed** via `call_tool`.

### `mesh_topology`
*Category: `mesh` · MCP Access: `call_tool` · Runs on: Every node*

Returns a point-in-time snapshot of the mesh topology **as seen by the node it runs on**. All data is sourced locally — zero network traffic. When fanned out to `*`, the union of all nodes' direct-peer relationships produces the complete mesh graph.

**Data Sources:**
- **Gradient Routing Table**: `node.MeshTopology()` reads `AllRoutes()` for all known paths with cost and next-hop.
- **Peer Manager**: Identifies directly connected peers via active yamux sessions.
- **Capability Index**: Aggregates tool/capability registrations learned via gossip.
- **Resolver Cache**: Reports the number of hostname-to-address mappings.

**Output Fields:**
- `node_id`: The identity of the reporting node.
- `direct_peers`: Count of currently connected peers.
- `known_nodes`: Total number of distinct nodes in the routing table (direct + gossip-learned).
- `resolver_entries`: Number of hosts in the resolver cache.
- `node_details[]`: Per-node detail including `node_id`, `impedance` (total path cost), `next_hop` (routing next hop), `capabilities`, and `is_direct` (whether this is a direct peer).

**Degradation Profile:**
- Always available on any mesh node. Returns empty `node_details` if the mesh has no peers.

### `hello`
*Category: `demo` · MCP Access: `call_tool` · Runs on: Every node*

Returns a greeting message from the node. Used for verifying connectivity and tool invocation.

**Parameters:**
- `name` (string, optional, default: `"world"`): Who to greet.

**Degradation Profile:**
- Always available. Cannot fail.

---

## Lifecycle Management Tools

These tools manage the node's systemd service lifecycle. They are **indirectly MCP-exposed** via `call_tool` and are critical for fleet operations.

> **⚠️ Operational Impact**: These tools have real infrastructure side effects. `node_uninstall` removes binaries and services. `node_stop` halts the node. The LLM should understand these consequences before invoking them.

### `node_install`
*Category: `lifecycle` · MCP Access: `call_tool` · Runs on: Fleet nodes*

Converts an ephemeral node (running from `/tmp`) into persistent infrastructure.

**Operations:**
1. Copies the running binary to `/opt/cortex-mesh/bin/cortex-mcp`
2. Writes a systemd unit file (`cortex-mesh.service`)
3. Runs `systemctl daemon-reload`, `enable`, and `start`

**Degradation Profile:**
- Requires write access to `/opt/cortex-mesh/bin/` and `/etc/systemd/system/`. Fails if the node is already installed.

### `node_uninstall`
*Category: `lifecycle` · MCP Access: `call_tool` · Runs on: Fleet nodes*

Removes the node — handles both persistent (systemd) and ephemeral (`/tmp`) nodes.

**Operations:**
- **Persistent nodes**: Stops and disables the systemd service, removes the unit file and installed binary.
- **Ephemeral nodes**: Removes the `/tmp` binary and exits the process.

**Degradation Profile:**
- Detects the installation type automatically. Safe to call on either ephemeral or persistent nodes.

### `node_restart`
*Category: `lifecycle` · MCP Access: `call_tool` · Runs on: Fleet nodes*

Restarts the local cortex-mesh systemd service. Use after binary upgrades or configuration changes.

**Operations:**
- Runs `systemctl restart cortex-mesh`.

**Degradation Profile:**
- Fails if the systemd service is not installed.

### `node_stop`
*Category: `lifecycle` · MCP Access: `call_tool` · Runs on: Fleet nodes*

Gracefully stops the cortex-mesh systemd service without uninstalling. The node remains installed and can be restarted. Use for maintenance windows.

**Operations:**
- Runs `systemctl stop cortex-mesh`.

**Degradation Profile:**
- Fails if the systemd service is not installed.

### `node_upgrade`
*Category: `lifecycle` · MCP Access: `call_tool` · Runs on: Fleet nodes*

Upgrades the node binary and restarts the service.

**Parameters:**
- `path` (string, required): Path to the new binary on the local filesystem (e.g., `/tmp/cortex-mesh-new`).

**Operations:**
1. Copies the new binary from the specified path over `/opt/cortex-mesh/bin/cortex-mcp`
2. Runs `systemctl daemon-reload`
3. Runs `systemctl restart cortex-mesh`

**Degradation Profile:**
- Fails if the source binary doesn't exist or the systemd service is not installed.

### `node_deploy`
*Category: `lifecycle` · MCP Access: `call_tool` · Runs on: Every node*

Deploys the mesh binary to another host via SSH. Any node in the fabric can act as a jumphost, enabling deployment to hosts unreachable from the gateway.

**Parameters:**
- `target` (string, required): Target host address (e.g., `10.0.1.5` or `host:port`).

**Degradation Profile:**
- Requires SSH access (credentials delegated via the mesh's vault system).

---

## Auto-Detected Tools

These tools are dynamically registered based on the presence of specific binaries in `PATH`. They are **indirectly MCP-exposed** via `call_tool`. Availability varies by node.

### `pg_isready`
*Category: `database` · Requires: `pg_isready` binary*

Checks PostgreSQL server status by running `pg_isready`.

### `mysql_ping`
*Category: `database` · Requires: `mysqladmin` binary*

Checks MySQL/MariaDB server status by running `mysqladmin ping`.

### `lctl_dl`
*Category: `storage` · Requires: `lctl` binary*

Shows configured Lustre devices by running `lctl dl`.

### `gpu_status`
*Category: `compute` · Requires: `nvidia-smi` binary*

Shows NVIDIA GPU memory status by running `nvidia-smi -q -d MEMORY`.

---

## Meta-Tools (MCP Gateway Level)

These tools operate at the MCP gateway, not on individual nodes. They are **directly MCP-exposed** — the LLM calls them without going through `call_tool`.

### `list_tools`
*Category: `meta` · MCP Access: Direct*

Discovers all available tools across the mesh. Returns name, description, and category for each tool. Optionally filtered by category.

### `tool_help`
*Category: `meta` · MCP Access: Direct*

Returns the full JSON schema, parameters, and long description for a specific tool.

### `call_tool`
*Category: `meta` · MCP Access: Direct*

Invokes any tool in the mesh. Supports three dispatch modes:
- **Auto-route** (no `node_name`): Routes to the lowest-impedance node offering the tool.
- **Unicast** (exact `node_name`): Sends to a specific node.
- **Fan-out** (pattern `node_name`): Executes on all matching nodes. Supports `*`, nodeset ranges (`node[1-10]`), exclusions (`oss[01-72]!oss[10-15]`), and groups (`@storage`).

### `cluster_overview`
*Category: `meta` · MCP Access: Direct*

Provides a complete cluster topology in a single call. Fans out `system_info` and `mesh_topology` to every node in the mesh, then aggregates the results at the gateway.

**Algorithm:**
1. **Fan-out `system_info`** to `*` (all nodes) — collects hostname, OS, arch, CPUs, kernel, distro, and detected storage roles from every node.
2. **Fan-out `mesh_topology`** to `*` (all nodes) — collects each node's direct peers, impedance costs, next-hop routing, and capabilities.
3. **Edge deduplication**: For every node's report, extracts `IsDirect=true` entries. If node A reports B as a direct peer and B reports A as a direct peer, they collapse to a single undirected edge. The union of all direct-peer relationships produces the **complete mesh graph**.
4. **Role count aggregation**: Counts occurrences of each role (SFA, MGS, MDS, OSS, Client, Generic) across all nodes.
5. **Mermaid rendering**: Generates a `graph TD` diagram from the real edge set, with nodes colored by primary role.

**Output includes:**
- `total_nodes`: Number of nodes in the fleet.
- `role_counts`: Map of role → count (e.g., `{"mgs": 4, "mds": 10, "oss": 100, "client": 1000, "generic": 2}`).
- `nodes[]`: Per-node detail (hostname, roles, OS, CPUs, impedance from gateway, direct/transitive status, next-hop, tools).
- `edges[]`: Deduplicated direct connections between nodes.
- `mermaid_graph`: Pre-rendered Mermaid diagram string.

**Degradation Profile:**
- Requires a live mesh with at least one connected node to produce meaningful results.
- Nodes that fail to respond to the fan-out are omitted from the topology (no error propagation).
- Returns sensible defaults (`total_nodes: 0`, empty arrays) for an empty mesh.
