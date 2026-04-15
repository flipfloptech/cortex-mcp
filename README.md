# cortex-mesh Example

This is the reference integration for a cortex-mesh consumer binary. It demonstrates the **full mesh lifecycle** in a single binary that can serve as either a **gateway** (bootstrap node) or a **fleet node** (deployed node), depending on how it was launched.

## What It Demonstrates

The example runs the complete Deploy → Invoke → Cleanup cycle:

| Phase | What Happens |
|-------|-------------|
| **Config** | Load `mesh.toml` — node identity, seed hosts, credentials |
| **Local** | Register tools (`hello`, `system_info`), invoke them locally |
| **Deploy** | SelfDeployer copies this binary to each seed host via SSH |
| **Invoke** | Call `system_info` on each deployed node via the tool wire protocol |
| **Fan-out** | Concurrent `hello` across all deployed nodes |
| **Cleanup** | Close connections, deployed nodes exit automatically |

## Prerequisites

- **Go 1.22+** (`go version`)
- **SSH access** to your seed hosts (key-based or password auth)

## Building

> [!IMPORTANT]
> You **must** build the binary before running the demo. The SelfDeployer copies the running binary to remote hosts via SFTP. If you use `go run`, it deploys a temporary file, which will not work as expected.

```bash
# From the repository root:
go build -o mesh-example ./example/
```

## Configuration

The example reads its configuration from `mesh.toml`. By default, it searches for `mesh.toml` in the current directory and `example/mesh.toml`.

```bash
# Use the bundled config (run from repo root):
./mesh-example

# Use a custom config:
./mesh-example -config /path/to/mesh.toml
```

### mesh.toml format

```toml
# Node identity (optional — defaults to hostname)
[node]
id = "gateway-01"

# Seed hosts for mesh discovery
[hosts.oss-01]
addresses = ["10.0.2.30"]

[hosts.oss-02]
addresses = ["10.0.2.198"]

# SSH credentials for deployment (glob patterns, most specific wins)
[[credentials]]
pattern  = "10.0.2.*"
type     = "ssh_password"
username = "root"
password = "changeme"

# SSH key authentication
# [[credentials]]
# pattern  = "10.0.1.*"
# type     = "ssh_key"
# username = "deploy"
# key_file = "~/.ssh/id_ed25519"
```

**Seed hosts** are starting points for deployment. The binary is deployed to each host and tools are invoked remotely.

**Credentials** are loaded into the encrypted vault at startup. Pattern matching uses glob syntax — the most specific pattern wins (exact > glob > wildcard). SSH key files are read from disk; `~` expands to the home directory.

## Running

### Gateway mode (default)

```bash
./mesh-example -config example/mesh.toml
```

**Expected output (with seed hosts configured):**

```
=== cortex-mesh E2E example ===
Gateway node: myhost
Registered tools:
  - hello (demo): Say hello from this node
  - system_info (system): Get basic system information

--- Phase 1: Local tool invocation ---
  hello: {"text":"Hello, cortex-mesh! From node myhost"}
  system_info: {"arch":"amd64","cpus":8,"hostname":"myhost","os":"linux","node_id":"myhost"}

--- Phase 2: Deploy to seed hosts ---
  → oss1 (10.0.2.30:22): deploying... ✓ deployed
  → oss2 (10.0.2.198:22): deploying... ✓ deployed

--- Phase 3: Remote tool invocation ---
  ✓ oss1: {"arch":"amd64","cpus":64,"hostname":"oss1","node_id":"oss1","os":"linux"}
  ✓ oss2: {"arch":"amd64","cpus":64,"hostname":"oss2","node_id":"oss2","os":"linux"}

--- Phase 4: Fan-out hello across all nodes ---
  ✓ oss1: {"text":"Hello, mesh-gateway! From node oss1"}
  ✓ oss2: {"text":"Hello, mesh-gateway! From node oss2"}

--- Phase 5: Cleanup ---
  ✓ oss1: disconnected
  ✓ oss2: disconnected

Done.
```

**Expected output (no seed hosts / local-only):**

```
--- Phase 1: Local tool invocation ---
  hello: {"text":"Hello, cortex-mesh! From node myhost"}
  system_info: {"arch":"amd64","cpus":8,...}

No seed hosts configured in mesh.toml. Skipping remote phases.

Done (local-only mode).
```

### Fleet node mode

When the SelfDeployer copies and launches this binary on a remote host, it sets `CORTEX_MESH_SPAWNED=1`. The binary then serves tools over stdin/stdout:

```bash
CORTEX_MESH_SPAWNED=1 ./mesh-example
```

In fleet mode, the binary:
1. Registers tools locally (`hello`, `system_info`)
2. Wraps stdin/stdout as a `net.Conn`
3. Serves tools via the length-prefixed protobuf wire protocol
4. Exits when the gateway closes the connection

## Architecture

```
main.go
├── Load mesh.toml config
├── Initialize vault with credentials
├── Register tools (hello, system_info)
├── if CORTEX_MESH_SPAWNED=1:
│   └── ServeToolConn(stdin/stdout) → serve tools, exit on EOF
└── else (gateway mode):
    ├── Phase 1: Local invocation demo
    ├── Phase 2: SelfDeployer → deploy to seed hosts
    ├── Phase 3: DialInvoke → remote tool calls
    ├── Phase 4: Fan-out → concurrent calls
    └── Phase 5: Cleanup → close connections
```

The same binary runs everywhere — gateway and fleet nodes are identical code with different entry paths.

### Tool Wire Protocol

Remote tool invocation uses length-prefixed protobuf framing over raw byte streams:

```
[4 bytes: big-endian payload length][protobuf: ToolRequest or ToolResponse]
```

This is the same framing pattern used by the control plane codec. In production, these streams would run over the membrane (mTLS) and yamux multiplexing layers for security and stream isolation. The example uses raw deploy streams for simplicity.

## Testing

```bash
# Run config package tests:
go test -race -v ./example/config/

# Build check:
go build -o /dev/null ./example/
```
