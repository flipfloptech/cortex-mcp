# cortex-mesh Example

This is the reference integration for a cortex-mesh consumer binary. It demonstrates the full mesh lifecycle in a single binary that can serve as either a **gateway** (MCP-facing) or a **fleet node** (mesh-facing), depending on how it was launched.

## Prerequisites

- **Go 1.22+** (`go version`)
- **protoc** (optional — only needed if modifying `.proto` files)

## Building

```bash
# From the repository root:
go build -o mesh-example ./example/
```

## Configuration

The example reads its configuration from `mesh.toml`. By default, it searches for `mesh.toml` in the current directory and `example/mesh.toml`.

```bash
# Use the bundled config (run from repo root):
go run ./example/

# Use a custom config:
go run ./example/ -config /path/to/mesh.toml
```

### mesh.toml format

```toml
# Node identity (optional — defaults to hostname)
[node]
id = "gateway-01"

# Seed hosts for mesh discovery
[hosts.mds-01]
addresses = ["10.0.1.5"]

[hosts.oss-01]
addresses = ["10.0.1.10", "10.0.1.11"]

# SSH credentials for deployment
[[credentials]]
pattern  = "10.0.1.*"        # host glob pattern
type     = "ssh_key"          # ssh_key | ssh_password | tls_cert
username = "deploy"
key_file = "~/.ssh/id_ed25519"

[[credentials]]
pattern  = "*.internal.corp"
type     = "ssh_password"
username = "admin"
password = "changeme"
```

**Seed hosts** are starting points for mesh peer discovery. The mesh grows organically through gossip once initial connections are established.

**Credentials** are loaded into the encrypted vault at startup. Pattern matching uses glob syntax — the most specific pattern wins (exact > glob > wildcard). SSH key files are read from disk; `~` expands to the home directory.

## Running

### Gateway mode (default)

```bash
# From repo root:
go run ./example/

# Or with the built binary:
./mesh-example
```

The gateway node exposes the mesh's tool catalog to an LLM via MCP. In this demo, it invokes tools locally and prints the results.

**Expected output:**

```
INFO vault loaded credential_patterns=[10.0.1.*]
[hostname] gateway node ready
Registered tools:
  - hello (demo): Say hello from this node
  - system_info (system): Get basic system information

--- Local tool invocation demo ---
hello result: {"text":"Hello, cortex-mesh! From node hostname"}
system_info result: {"arch":"amd64","cpus":8,"hostname":"hostname","os":"linux"}

--- Gateway meta-tool demo ---
list_tools result: {"tools":[...]}
tool_help result: {"name":"system_info",...}
```

### Fleet node mode

When the mesh's SelfDeployer copies and launches this binary on a remote host, it sets `CORTEX_MESH_SPAWNED=1`. The binary then serves as a mesh peer instead of a gateway:

```bash
CORTEX_MESH_SPAWNED=1 ./mesh-example
```

In fleet mode, the binary:
1. Accepts the deployer's connection over stdin/stdout
2. Registers the same tools as the gateway
3. Blocks forever, serving tools through the mesh

## Architecture

```
main.go
├── Load mesh.toml config
├── Initialize vault with credentials
├── Create mesh node with seed hosts
├── Register tools (hello, system_info)
├── if CORTEX_MESH_SPAWNED=1:
│   └── AcceptStdio → serve as fleet peer
└── else:
    └── Gateway mode → MCP dispatch
```

The same binary runs everywhere — gateway and fleet nodes are identical code with different entry paths.

## Testing

```bash
# Run config package tests:
go test -race -v ./example/config/

# Build check:
go build -o /dev/null ./example/
```
