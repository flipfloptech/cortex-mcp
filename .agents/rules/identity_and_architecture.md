---
trigger: always_on
description: Core identity, philosophy, and architectural mental model for the cortex-mcp project.
---

# Identity & Architecture

## Identity

- **Role**: You are building `cortex-mcp`, the primary MCP (Model Context Protocol) server application built on top of the `cortex-mesh` decentralized P2P networking library.
- **Vision**: To provide a seamless integration between LLMs (via MCP) and a distributed fleet of agents (via Cortex Mesh). `cortex-mcp` exposes the unified stream-mesh capabilities to external LLM clients, allowing them to dynamically discover and invoke tools across an entire infrastructure without any centralized routing.
- **Philosophy**: Turn complex, distributed infrastructure into a flat, callable tool namespace for LLMs. Security and reliability are paramount.
- **Goals**: This is the flagship application consuming `cortex-mesh`. It acts as both the MCP Gateway (bootstrap node) and the Fleet Node (deployed agent). It should be a robust, production-ready daemon and CLI tool.

## Mental Model: The Application Boundary

1. **The Core Dependency**: You rely on `github.com/cortex-mesh/cortex-mesh`. You do not build the mesh routing or transport; you configure and utilize it.
2. **The MCP Interface**: You run an MCP server over stdio (when launched by an LLM IDE) or over SSE/HTTP (future). You bridge MCP tool calls into `cortex-mesh` Sonar and Laser protocols.
3. **The Tool Registry**: You manage the registration of tools (`internal/registry`). You define the actual handlers that run on the fleet nodes (e.g., system ops, deployments).
4. **The CLI & Lifecycle**: You manage the application lifecycle (`internal/mesh`), parsing configuration (`internal/config`), and executing commands (installing as daemon, starting, stopping, bridging).

## Architectural Principles

### Dual-Role Binary

- The `cortex-mcp` binary is polymorphic. Depending on how it's launched, it is either:
  1. **Gateway**: Launched by an LLM (e.g., Claude Desktop, Cursor). Runs the MCP server, deploys fleet nodes, and acts as the entry point.
  2. **Fleet Node**: Deployed via SSH by the Gateway to remote hosts. Runs persistently as a systemd service, serving registered tools back to the mesh.

### Standard Go Project Layout

- `cmd/cortex-mcp/`: Minimal wrapper for the main execution logic.
- `internal/config/`: Configuration file parsing (mesh.toml).
- `internal/mesh/`: The orchestration layer linking `cortex-mesh` primitives to the application lifecycle.
- `internal/mcp/`: Handling of Model Context Protocol specifics.
- `internal/registry/`: Tool definitions and handlers executed by the nodes.

### Configuration & State

- Uses TOML for configuration (`mesh.toml`).
- Configuration is strictly for the *application* logic and node initialization (e.g. initial known hosts, certificates).
- Once the mesh is formed, state is decentralized.

### Tool Philosophy

- Tools are registered in `internal/registry/`.
- Tools should be granular, focused, and secure.
- Handlers execute strictly on the node where they are registered, triggered via the mesh.

### The Meta-Tools Pattern

- `cortex-mcp` exposes exactly 3 meta-tools to the LLM over MCP:
  1. `get_tool_list`: Uses Sonar to find what's available in the mesh.
  2. `get_tool_help`: Fetches schemas for specific tools.
  3. `call_tool`: Invokes a tool, optionally fanning out or auto-routing.
- This prevents overwhelming the LLM context window with hundreds of granular distributed tools.
