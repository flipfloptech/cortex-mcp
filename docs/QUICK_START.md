# Quick Start Guide

This guide will walk you through setting up `cortex-mcp` from scratch, deploying fleet nodes to remote hosts, and connecting an LLM client (like Cursor or Claude Desktop) to interact with the decentralized mesh.

---

## Prerequisites

1. **Operating System**: Linux (systemd required for persistent daemons).
2. **Go**: Version 1.26+ installed.
3. **SSH**: Passwordless SSH key access to any remote fleet nodes you intend to deploy to.

---

## Step 1: Configuration (`mesh.toml`)

Create a `mesh.toml` configuration file in the directory where you will launch the gateway. This file defines the cluster identity and the initial hosts in the fleet:

```toml
# Cluster Configuration
[cluster]
name = "hpc-cluster"

# Gateway Node configuration
[gateway]
addr = "10.0.0.1"

# Remote Fleet Hosts definitions
[hosts.node1]
addresses = ["10.0.0.11"]

[hosts.node2]
addresses = ["10.0.0.12"]
```

---

## Step 2: Build the Polymorphic Binary

Build the `cortex-mcp` binary:

```bash
go build -o bin/cortex-mcp ./cmd/cortex-mcp
```

This single binary functions as both the Gateway (Bootstrap node) and the Fleet Node (Daemon).

---

## Step 3: Bootstrap and Deploy the Fleet

Run the gateway in bootstrap mode, instructing it to self-deploy to the remote nodes defined in `mesh.toml`:

```bash
./bin/cortex-mcp bootstrap --install
```

### What happens behind the scenes?
1. The gateway generates the Root CA and node credentials.
2. It SSHes to each remote host defined under `[hosts]`.
3. It copies the `cortex-mcp` binary to `/opt/cortex-mcp/bin/`, generates systemd units, registers them, and starts the persistent daemon services.
4. Remote nodes complete a handshake, upgrade connection streams to mTLS + Yamux, and register their local tool capabilities back to the mesh gateway.

---

## Step 4: Configure LLM Clients (MCP Integration)

Once the fleet converges, you can configure your IDE or LLM application to speak with `cortex-mcp` via Stdio.

### Claude Desktop Configuration
Add the following block to your `claude_desktop_config.json` (usually at `~/.config/Claude/claude_desktop_config.json`):

```json
{
  "mcpServers": {
    "cortex-mesh": {
      "command": "/path/to/cortex-mcp/bin/cortex-mcp",
      "args": ["stdio"]
    }
  }
}
```

### Cursor Configuration
1. Open Cursor settings (`Ctrl + ,` or `Cmd + ,`).
2. Go to **Features** -> **MCP**.
3. Click **+ Add New MCP Server**:
   - **Name**: `cortex-mesh`
   - **Type**: `command`
   - **Command**: `/path/to/cortex-mcp/bin/cortex-mcp stdio`

---

## Step 5: Interacting with the Mesh

Your LLM client will now have access to exactly 4 meta-tools. The LLM interacts with the mesh via these 4 verbs:

### 1. `get_mesh_overview`
Returns the cluster topology map, active nodes, and roles (SFA/MGS/MDS/OSS/Client).
- *LLM Prompt*: `"Show me the current state and structure of the cluster mesh."`

### 2. `get_tool_list`
Discovers all registered diagnostics/tuning tools available in the fleet.
- *LLM Prompt*: `"What diagnostics tools are available in the cluster?"`

### 3. `get_tool_help`
Retrieves the exact parameter schema for a specific tool.
- *LLM Prompt*: `"Get the schema and description for 'query_dmesg'."`

### 4. `call_tool`
Executes a tool on a specific node, a group, or fans out across all nodes.
- **Unicast**: Target a specific host (e.g. `node_name="node1"`).
- **Fan-out**: Execute across a pattern (e.g. `node_name="*"` or `node_name="oss*"`).
- **Auto-route**: Leave `node_name` empty to automatically run on the best matching node.
