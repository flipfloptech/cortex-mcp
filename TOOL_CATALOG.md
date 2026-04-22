# Cortex MCP Tool Catalog

This catalog documents every tool in the Cortex MCP Server, detailing their data sources, MCP visibility, and degradation profiles.

## MCP Exposure Model

The Cortex MCP gateway exposes tools to LLMs through a **meta-tool pattern**. Instead of registering hundreds of tools directly with the MCP protocol (which would overwhelm context windows), the gateway exposes exactly **4 meta-tools**:

| Meta-Tool | Purpose | MCP Exposed |
|---|---|---|
| `list_tools` | Discover available tools across the mesh | ✅ Direct |
| `tool_help` | Get JSON schema for a specific tool | ✅ Direct |
| `call_tool` | Invoke any tool (unicast, fan-out, auto-route) | ✅ Direct |
| `mesh_overview` | Aggregate fleet topology, roles, and Mermaid graph | ✅ Direct |

All other tools are **indirectly accessible** through `call_tool`. The LLM uses `list_tools` to discover them and `call_tool` to invoke them.

### Tool Visibility

Tools have a `Hidden` flag in their `ToolDefinition`. Hidden tools:
- **Do NOT appear** in `list_tools` output (invisible to the LLM's tool browsing)
- **Are still callable** via `call_tool` if the LLM (or gateway) knows their name
- **Are still documented** via `tool_help`

This is used for lifecycle management tools — they need to be callable by the gateway and test harness, but should not clutter the LLM's diagnostic tool namespace.

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

## Visible Tools (LLM-Discoverable)

These tools appear in `list_tools` output and are the primary interface for LLM-driven diagnostics.

### Plugin Tools (registry.Tool)

These follow the `registry.Tool` interface and are auto-registered via `init()`.

#### `system_info`
*Category: `system` · Runs on: Every node*

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

#### `uptime`
*Category: `system` · Runs on: Every Linux node*

Reads and calculates system uptime and CPU idle time. Formats the data into pre-processed human-readable strings to reduce the mathematical overhead for LLMs consuming the API.

**Data Sources:**
- **Uptime/Idle**: Read from `/proc/uptime`. The first value is the total system uptime, and the second is the total idle time across all CPUs.
- **CPU Count**: Number of logical cores reported by the Go runtime (`runtime.NumCPU()`).

**Mathematical Models:**
- **Idle Percentage**: Calculated dynamically as `(IdleSeconds / (UptimeSeconds * CPUCount)) * 100.0`.
- **Human Readable Format**: Converts seconds into a comma-separated duration string (e.g., `4 days, 1 hours, 25 minutes, 35 seconds`).

**Degradation Profile:**
- `IsSupported()` returns `false` if the host OS is not Linux, or if `/proc/uptime` is unreadable/missing.

#### `loadavg`
*Category: `system` · Runs on: Every Linux node*

Reads the system load averages and scheduling entity statistics to provide a snapshot of system CPU and I/O pressure.

**Data Sources:**
- Read directly from `/proc/loadavg`.

**Mathematical Models:**
- Directly extracts the 1-minute, 5-minute, and 15-minute load averages.
- Parses the scheduling entity ratio (e.g., `1/863`) into two distinct integers: `RunnableEntities` and `TotalEntities`.

**Degradation Profile:**
- `IsSupported()` returns `false` if the host OS is not Linux, or if `/proc/loadavg` is unreadable/missing.

---

### Mesh Infrastructure Tools

#### `mesh_topology`
*Category: `mesh` · Runs on: Every node*

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

---

## Hidden Tools (Gateway/Harness Only)

These tools have `Hidden: true` — they do **not** appear in `list_tools` and are invisible to the LLM during tool browsing. They remain callable via `call_tool` by the gateway, test harness, and any component that knows their name.

### Lifecycle Management Tools

> **⚠️ Operational Impact**: These tools have real infrastructure side effects. They are hidden to prevent accidental LLM invocation during diagnostic sessions.

#### `node_install`
*Category: `lifecycle` · Hidden: ✅*

Converts an ephemeral node (running from `/tmp`) into persistent infrastructure.

**Operations:**
1. Copies the running binary to `/opt/cortex-mesh/bin/cortex-mcp`
2. Writes a systemd unit file (`cortex-mesh.service`)
3. Runs `systemctl daemon-reload`, `enable`, and `start`

#### `node_uninstall`
*Category: `lifecycle` · Hidden: ✅*

Removes the node — handles both persistent (systemd) and ephemeral (`/tmp`) nodes.

**Operations:**
- **Persistent nodes**: Stops and disables the systemd service, removes the unit file and installed binary.
- **Ephemeral nodes**: Removes the `/tmp` binary and exits the process.

#### `node_restart`
*Category: `lifecycle` · Hidden: ✅*

Restarts the local cortex-mesh systemd service (`systemctl restart cortex-mesh`).

#### `node_stop`
*Category: `lifecycle` · Hidden: ✅*

Gracefully stops the cortex-mesh systemd service without uninstalling (`systemctl stop cortex-mesh`).

#### `node_upgrade`
*Category: `lifecycle` · Hidden: ✅*

Upgrades the node binary and restarts the service.

**Parameters:**
- `path` (string, required): Path to the new binary on the local filesystem.

**Operations:**
1. Copies the new binary from the specified path over `/opt/cortex-mesh/bin/cortex-mcp`
2. Runs `systemctl daemon-reload` and `restart`

#### `node_deploy`
*Category: `lifecycle` · Hidden: ✅*

Deploys the mesh binary to another host via SSH. Any node in the fabric can act as a jumphost.

**Parameters:**
- `target` (string, required): Target host address (e.g., `10.0.1.5` or `host:port`).

---

## Meta-Tools (MCP Gateway Level)

These tools operate at the MCP gateway, not on individual nodes. They are **directly MCP-exposed** — the LLM calls them without going through `call_tool`.

### `list_tools`
*Category: `meta` · MCP Access: Direct*

Discovers all available **visible** tools across the mesh. Hidden tools are excluded. Returns name, description, and category for each tool.

### `tool_help`
*Category: `meta` · MCP Access: Direct*

Returns the full JSON schema, parameters, and long description for a specific tool. Works for both visible and hidden tools.

### `call_tool`
*Category: `meta` · MCP Access: Direct*

Invokes any tool in the mesh (visible or hidden). Supports three dispatch modes:
- **Auto-route** (no `node_name`): Routes to the lowest-impedance node offering the tool.
- **Unicast** (exact `node_name`): Sends to a specific node.
- **Fan-out** (pattern `node_name`): Executes on all matching nodes. Supports `*`, nodeset ranges (`node[1-10]`), exclusions (`oss[01-72]!oss[10-15]`), and groups (`@storage`).

### `mesh_overview`
*Category: `meta` · MCP Access: Direct*

Provides a complete cluster topology in a single call. Fans out `system_info` and `mesh_topology` to every node in the mesh, then aggregates the results at the gateway.

**Algorithm:**
1. **Fan-out `system_info`** to `*` (all nodes) — collects hostname, OS, arch, CPUs, kernel, distro, and detected storage roles from every node.
2. **Fan-out `mesh_topology`** to `*` (all nodes) — collects each node's direct peers, impedance costs, next-hop routing, and capabilities.
3. **Edge deduplication**: Unions all `IsDirect=true` relationships across all nodes to produce the complete mesh graph.
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
