# Cortex MCP Server

A production-ready Model Context Protocol (MCP) server that provides AI agents with secure, distributed access to infrastructure telemetry, diagnostics, and filesystem management via the Cortex Mesh P2P network.

This application operates as either a **gateway** (bootstrap node connecting to the LLM) or a **fleet node** (deployed agent), depending on how it is launched. It turns complex, distributed infrastructure into a flat, callable tool namespace for LLMs.

## Installation

The `cortex-mcp` binary is distributed as a fully static, standalone executable.

### Building from Source

All project dependencies are pure Go. Building with `CGO_ENABLED=0` produces a fully static binary that can be safely deployed across diverse Linux environments.

```bash
# Clone the repository
git clone https://github.com/flipfloptech/cortex-mcp.git
cd cortex-mcp

# Build the static binary using the Task runner
task build

# Or build manually
CGO_ENABLED=0 go build -o cortex-mcp ./cmd/cortex-mcp/...
```

## Quick Start

The fastest way to spin up the Cortex Mesh is by importing an existing cluster configuration (like Exascaler TOML) and deploying the fleet.

```bash
# 1. Convert your existing configuration into a mesh topology
./cortex-mcp import-exa /path/to/memexa01.toml ./mesh.toml

# 2. Install the mesh agents onto the target hosts (requires root/sudo)
./cortex-mcp install --config ./mesh.toml

# 3. Start the MCP Gateway server using Streamable HTTP for LLMs to connect to
./cortex-mcp mcp --config ./mesh.toml
```

## Configuring LLM Clients

Cortex MCP is an HTTP server that streams capabilities directly to any standard Model Context Protocol client using Streamable HTTP.

First, ensure the gateway server is running:
```bash
./cortex-mcp mcp 127.0.0.1:8080 --config ./mesh.toml
```

### Claude Desktop Integration

Add the following to your `claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "cortex-mesh": {
      "type": "streamablehttp",
      "url": "http://127.0.0.1:8080/mcp"
    }
  }
}
```

### Cursor / OpenCode Integration

In Cursor, navigate to **Settings -> Features -> MCP Servers** and add a new server:
- **Type**: `streamablehttp`
- **Name**: `cortex-mesh`
- **URL**: `http://127.0.0.1:8080/mcp`

## CLI Reference

`cortex-mcp` features a robust set of subcommands for managing the distributed mesh.

- `import-exa <in.toml> [out.toml]`: Convert Exascaler topology definitions into native `mesh.toml` configuration.
- `install`: Deploy the static binary and install the systemd daemon on all remote hosts defined in the configuration.
- `start`: Start the `cortex-mesh` systemd service on all remote hosts.
- `stop`: Stop the `cortex-mesh` systemd service on all remote hosts.
- `uninstall`: Remove the systemd service and binary from all remote hosts.
- `mcp`: Launch the gateway in MCP server mode over Streamable HTTP (used by LLMs).
- `harness`: Run the automated soak-testing harness against the deployment.

## Tool Catalog

For a comprehensive list of all available tools, their mathematical models, data sources, and philosophical design (such as graceful degradation), please see the [Cortex MCP Tool Catalog](TOOL_CATALOG.md).

## Configuration (`mesh.toml`)

```toml
[node]
  id = "gateway-01"

# Automatically target subsets of hosts using group mapping
[groups]
  mdt = ["oss-01", "oss-02"]
  ost = ["oss-03", "oss-04"]

[hosts]
  [hosts.oss-01]
    addresses = ["10.0.1.10", "10.0.1.11"]

  [hosts.oss-02]
    addresses = ["10.0.1.12"]

# Define SSH access credentials for deployment operations
[[credentials]]
  type = "ssh_key"
  pattern = "10.0.1.*"
  username = "admin"
  key_file = "~/.ssh/id_ed25519"
```

## Architecture

```
┌──────────────────────────────────────────────────────────────────┐
│  cortex-mcp binary                                               │
│                                                                  │
│  Gateway mode:                    Fleet node mode:               │
│  ┌──────────────────┐             ┌──────────────────┐           │
│  │ Phase 1: PKI     │             │ SignalReady()    │ → ReadyMagic
│  │ Phase 2: Node    │             │ readCertBundle() │           │
│  │ Phase 3: Local   │             │ SaveIdentity()   │           │
│  │ Phase 4: Gateway │             │ fork -daemon     │           │
│  │ Phase 5: Deploy  │──SSH/SFTP─→ │ api.NewNode()    │           │
│  │ Phase 6: Gossip  │ cert boot   │ Listen(4443)     │ ← TCP mTLS│
│  │ Phase 7: Sonar   │             │ ServeTools       │           │
│  │ Phase 8: Invoke  │             │ <block forever>  │           │
│  └──────────────────┘             └──────────────────┘           │
└──────────────────────────────────────────────────────────────────┘
```

### Deployment Model

The deployment commands (`install`, `uninstall`, etc.) work **strictly from the config file**:
1. Reads seed hosts from `mesh.toml` `[hosts.*]` sections.
2. Resolves each target using the mapped IP addresses.
3. Connects via SSH and executes operations concurrently across the fleet.

**No `/etc/hosts` scraping. No autonomous spreading. The deployer does exactly what the config says — no more, no less.**
