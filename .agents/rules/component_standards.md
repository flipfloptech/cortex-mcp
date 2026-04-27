---
trigger: always_on
description: Component design standards and API contracts for the cortex-mcp application.
---

# Component & API Standards

## Design Principles

- **Separation of Concerns**: Keep Model Context Protocol handling (`internal/mcp`), Mesh orchestration (`internal/mesh`), and Tool logic (`internal/registry`) entirely decoupled.
- **Library Utilization**: You are a consumer of `cortex-mcp`. Rely on the library for all transports, multiplexing, and routing. Do not reimplement `Neuron` or `Membrane` logic.
- **Fail Fast**: If configuration is invalid, fail on startup. If a required dependency is missing, fail clearly.

## Application Architecture

### The Gateway / Bootstrap Mode

1. Initiated by the user or an LLM client.
2. Loads configuration and creates the root mesh identity (Site CA).
3. Instantiates `cortex-mcp/gateway` and `cortex-mcp/tools.NewNeuronBridge`.
4. Deploys fleet nodes via SSH (`transport.SelfDeployer`).
5. Handles the MCP stdio protocol loop to serve LLM requests, translating them into `cortex-mcp` Meta-Tool invocations.

### The Fleet Node / Daemon Mode

1. Initiated by SSH deployer from the Gateway.
2. Performs the initial readiness handshake over stdin/stdout.
3. Upgrades the stream to mTLS and yamux.
4. If instructed to "install", copies itself to `/opt/cortex-mcp/bin/`, installs a systemd unit, and transitions to a persistent daemon.
5. In daemon mode, listens via HTTPS or SSH transports, registering its local capability subset into the mesh.

## Tool Registry Contract (`internal/registry`)

Tools are the verbs of the mesh.

### Defining Tools

- Use `tools.ToolDefinition`.
- Every tool must have a clear `Name`, a one-line `Description`, and a detailed `LongDescription` with accurate `Parameters` documentation.
- The parameter schemas must be comprehensive to ensure LLMs know exactly what to provide.

### Handlers

- Handlers (`tools.ToolHandler`) must execute locally on the node where they are registered.
- They must not assume gateway access.
- They must respect `context.Context` cancellation.
- Errors must be encapsulated cleanly into `tools.ToolResult`.

### Example Tool Layout

```go
package registry

import (
    "context"
    "encoding/json"
    "github.com/cortex-mcp/cortex-mcp/tools"
)

// RegisterSystemTools adds base system utilities to the registry.
func RegisterSystemTools(reg *tools.Registry) {
    reg.Register(tools.ToolDefinition{
        Name:        "system_info",
        Description: "Get basic system information",
        Category:    "system",
    }, handleSystemInfo)
}
```

## Configuration Contract (`internal/config`)

- Parse using standard `toml`.
- Provide defaults where applicable.
- Pass clear strongly typed structs down to the mesh and registry packages.
