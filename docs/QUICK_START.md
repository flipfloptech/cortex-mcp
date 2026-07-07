# Quick Start Guide

This guide will walk you through setting up `cortex-mcp` from scratch, deploying fleet nodes to remote hosts, and connecting an LLM client (like Cursor or Claude) to interact with the decentralized mesh.

---

## Prerequisites

1. **Operating System**: Linux (systemd required for persistent daemons).
2. **Go**: Version 1.26+ installed (plus [Task](https://taskfile.dev) if you want to use the Taskfile targets).
3. **SSH**: Reachable SSH on any remote fleet hosts you intend to deploy to. Authentication is configured in `mesh.toml` (`[[credentials]]`) — either a private key file or a username/password. The deployer does **not** use your SSH agent.

---

## Step 1: Configuration (`mesh.toml`)

Create a `mesh.toml` in the directory where you will launch the gateway (searched at `./mesh.toml`, `/opt/cortex-mcp/etc/mesh.toml`, `<exe dir>/mesh.toml`, `~/.cortex-mcp/mesh.toml`, or pass `--config <path>`). This file defines the node identity, the fleet hosts, and the SSH credentials used to deploy them:

```toml
# Node identity (optional — defaults to $CORTEX_NODE_ID, then the hostname)
[node]
id        = "gateway-01"
mesh_port = 4443        # P2P mesh port (default)
ssh_port  = 22          # deploy SSH port (default)

# Remote fleet host definitions
[hosts.node1]
addresses = ["10.0.0.11"]

[hosts.node2]
addresses = ["10.0.0.12"]

# SSH credentials for deployment. Glob pattern — most specific match wins.
# Types: ssh_key (username + key_file), ssh_password (username + password),
# tls_cert (cert_file).
[[credentials]]
pattern  = "10.0.0.*"
type     = "ssh_key"
username = "root"
key_file = "~/.ssh/id_ed25519"

# Optional: node groups for @group targeting in call_tool
[groups]
storage = ["node1", "node2"]
```

> **Tip:** If you run a DDN EXAScaler cluster, `cortex-mcp import-exa <exascaler.toml>` generates the `[hosts]` and `[groups]` sections for you.

---

## Step 2: Build the Polymorphic Binary

```bash
task build            # or: go build -o cortex-mcp ./cmd/cortex-mcp
```

This single binary functions as the Gateway, the MCP server, and the Fleet Node daemon.

> Prefer `task build` — it injects the build timestamp used by the fleet auto-upgrade mechanism. A binary built with plain `go build` reports version `unknown`.

---

## Step 3: Deploy the Fleet and Start the MCP Server

```bash
./cortex-mcp mcp
```

This starts the gateway **and** the MCP Streamable HTTP server on `localhost:8080` (pass an explicit `ip:port` argument to change it).

### What happens behind the scenes?

1. The gateway loads (or auto-generates) the Ed25519 Mesh CA and issues node certificates (see [PKI.md](PKI.md)).
2. It SSHes to each host under `[hosts]`, uploads the binary via SFTP to `/tmp`, and starts it as an **ephemeral** node — config and certificates are passed over the SSH stdin pipe, not written to disk.
3. Nodes complete the mTLS handshake, upgrade to zstd + yamux multiplexed streams, and gossip their local tool capabilities into the mesh.
4. The gateway compares build versions and automatically upgrades outdated fleet nodes over the mesh.

Running `./cortex-mcp` with no arguments does the same minus the MCP HTTP server. With no hosts configured, either form runs as a local-only node exposing this machine's diagnostics.

### Optional: make the fleet persistent

Ephemeral nodes vanish on reboot. To install them as systemd services:

```bash
./cortex-mcp install            # all configured hosts (asks for confirmation)
./cortex-mcp install node1      # a single host
```

Manage the persistent fleet with `./cortex-mcp start|stop|uninstall [target]`.

---

## Step 4: Configure LLM Clients (MCP Integration)

The MCP server speaks **Streamable HTTP** at `http://localhost:8080/mcp` (there is no stdio transport). Point any MCP client at that URL.

### Cursor

Add to `~/.cursor/mcp.json` (or via **Settings → MCP → Add New MCP Server**):

```json
{
  "mcpServers": {
    "cortex-mesh": {
      "url": "http://localhost:8080/mcp"
    }
  }
}
```

### Claude Code

```bash
claude mcp add --transport http cortex-mesh http://localhost:8080/mcp
```

### Claude Desktop

Add a custom connector pointing at `http://localhost:8080/mcp` (**Settings → Connectors → Add custom connector**), or bridge via [`mcp-remote`](https://www.npmjs.com/package/mcp-remote) if your build only supports stdio servers:

```json
{
  "mcpServers": {
    "cortex-mesh": {
      "command": "npx",
      "args": ["mcp-remote", "http://localhost:8080/mcp"]
    }
  }
}
```

---

## Step 5: Interacting with the Mesh

Your LLM client will now have access to exactly 4 meta-tools. The LLM interacts with the mesh via these 4 verbs:

### 1. `get_mesh_overview`
Returns the cluster topology map, active nodes, and roles (SFA/MGS/MDS/OSS/Client), plus a pre-rendered Mermaid diagram.
- *LLM Prompt*: `"Show me the current state and structure of the cluster mesh."`

### 2. `get_tool_list`
Discovers all registered diagnostic tools available in the fleet.
- *LLM Prompt*: `"What diagnostics tools are available in the cluster?"`

### 3. `get_tool_help`
Retrieves the exact parameter schema for a specific tool.
- *LLM Prompt*: `"Get the schema and description for 'query_dmesg'."`

### 4. `call_tool`
Executes a tool on a specific node, a group, or fans out across all nodes.
- **Unicast**: Target a specific host (e.g. `node_name="node1"`).
- **Fan-out**: Execute across a nodeset pattern (e.g. `node_name="*"`, `"oss[01-72]!oss[10-15]"`, or `"@storage"`).
- **Auto-route**: Leave `node_name` empty to automatically run on the least-loaded node offering the tool.

See [TOOL_CATALOG.md](TOOL_CATALOG.md) for the full tool reference.
