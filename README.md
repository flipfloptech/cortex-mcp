# cortex-mesh E2E Example

A complete, feature-rich example binary demonstrating the full cortex-mesh lifecycle. This binary serves as the reference integration pattern — a single Go program that operates as either a **gateway** (bootstrap node) or a **fleet node** (deployed), depending on how it was launched.

## What It Demonstrates

| Phase | Feature | Layer |
|-------|---------|-------|
| 1 | Ephemeral PKI generation (site CA + node certs) | `membrane` |
| 2 | Node creation with lifecycle events + reconnect policy | `api` |
| 3 | Local tool registration + invocation | `tools` |
| 4 | Gateway meta-tools (list_tools, tool_help, call_tool) | `gateway` |
| 5 | SSH deploy + readiness handshake + cert bootstrap + mTLS mesh connect | `transport` + `membrane` |
| 6 | Gossip ticker startup (3s impedance exchange) | `routing` |
| 7 | Sonar broadcast discovery (`tool:*`) | `routing` |
| 8 | Remote invocation via NeuronBridge (GrpcDialer + DialInvoke) | `tools` |
| 9 | Fan-out invocation across fleet | `gateway` + `tools` |
| 10 | Clean teardown with node event callbacks | `api` |

## Quick Start

```bash
# Build first — static binary, no libc dependency:
CGO_ENABLED=0 go build -o mesh-example ./example/

# Or use task (CGO_ENABLED=0 is set automatically):
task example

# Run with a config file (enables remote deployment):
./mesh-example -config example/mesh.toml

# Run without config (local-only mode — demonstrates phases 1–4):
./mesh-example
```

## Architecture

```
┌──────────────────────────────────────────────────────────────────┐
│  mesh-example binary                                             │
│                                                                  │
│  Gateway mode:                    Fleet node mode:               │
│  ┌──────────────────┐             ┌──────────────────┐           │
│  │ Phase 1: PKI     │             │ SignalReady()     │ → magic  │
│  │ Phase 2: Node    │             │ readCertBundle()  │           │
│  │ Phase 3: Local   │             │ SaveIdentity()    │           │
│  │ Phase 4: Gateway │             │ fork -daemon      │ → "Installed!"
│  │ Phase 5: Deploy  │──SSH/SFTP─→ │ api.NewNode()     │           │
│  │ Phase 6: Gossip  │ cert boot   │ Listen(4443)      │ ← TCP mTLS│
│  │ Phase 7: Sonar   │             │ ServeToolListener │           │
│  │ Phase 8: Invoke  │             │ <block forever>   │           │
│  │ Phase 9: FanOut  │             └──────────────────┘           │
│  │ Phase 10: Close  │                                            │
│  └──────────────────┘                                            │
└──────────────────────────────────────────────────────────────────┘
```

## Configuration (`mesh.toml`)

```toml
[node]
id = "gateway-01"

[hosts.oss-01]
addresses = ["10.0.1.10"]

[hosts.oss-02]
addresses = ["10.0.1.11"]

[[credentials]]
type = "ssh_password"
pattern = "10.0.1.*"
username = "admin"
password = "changeme"

[[credentials]]
type = "ssh_key"
pattern = "10.0.2.*"
username = "admin"
key_file = "~/.ssh/id_ed25519"
```

## Static Builds

The mesh binary **must** be statically linked. `SelfDeployer` copies `os.Executable()` to remote hosts that may have different (usually older) libc versions. A dynamically-linked binary will crash with `GLIBC_X.XX not found`.

All project dependencies are pure Go — no cgo is required. Building with `CGO_ENABLED=0` produces a fully static binary:

```bash
# The cgo dependencies are only from stdlib (net, os/user) which have
# pure Go fallbacks. No project dependencies require cgo.
CGO_ENABLED=0 go build -o mesh-example ./example/

# Verify:
ldd ./mesh-example  # → "not a dynamic executable"
```

`SelfDeployer.Deploy()` validates this automatically — if the binary has a `PT_INTERP` program header (dynamic linker), it rejects the deploy with an actionable error before any SSH connection.

## Expected Output (local-only mode)

```
=== cortex-mesh E2E example ===
Gateway node: my-laptop
Registered tools:
  - hello (demo): Say hello from this node
  - system_info (system): Get basic system information

--- Phase 1: Ephemeral PKI ---
  ✓ Site CA generated (ephemeral, 24h validity)
  ✓ Gateway cert: CN=my-laptop

--- Phase 2: Create mesh node ---
  ✓ Node created: my-laptop (peers=0, caps=2)
  ✓ Reconnect policy: enabled (1s→30s backoff, 5m timeout)

--- Phase 3: Local tool invocation ---
  hello: {"text":"Hello, cortex-mesh! From node my-laptop"}
  system_info: {"arch":"amd64","cpus":16,"hostname":"my-laptop","node_id":"my-laptop","os":"linux"}

--- Phase 4: Gateway meta-tools ---
  list_tools: [{"name":"hello",...},{"name":"system_info",...}]
  tool_help: {"name":"system_info","description":"Get basic system information",...}
  call_tool: {"text":"Hello, mesh-gateway! From node my-laptop"}

No seed hosts configured in mesh.toml. Skipping remote phases.

Done (local-only mode).
```

## Expected Output (with remote hosts)

When seed hosts are configured, the output continues with phases 5–10:

```
--- Phase 5: Deploy + mesh connect ---
  → oss-01 (10.0.1.10:22): deploying... ✓ installed + connected (mTLS via TCP)
  [event] peer joined: oss-01

--- Phase 6: Gossip ---
  ✓ Gossip ticker started (3s interval)
  Waiting for gossip convergence...
  ✓ Peers: 1

--- Phase 7: Sonar discovery ---
  ✓ Discovered 1 node(s) with tool:system_info
    - oss-01 (impedance=5.0)

--- Phase 8: Remote invocation via NeuronBridge ---
  ✓ oss-01: {"node_id":"oss-01","hostname":"oss-01","os":"linux","arch":"amd64","cpus":32}

--- Phase 9: Fan-out hello ---
  ✓ fan-out result: [{"text":"Hello, mesh-gateway! From node oss-01"}]

--- Phase 10: Cleanup ---
  [event] peer lost: oss-01
  ✓ oss-01: disconnected

Done.
```

## Deploy + Cert Bootstrap Protocol

After SSH deployment, the gateway waits for the fleet node's readiness signal before sending certificate material. This ensures the remote binary is alive and ready before any data exchange:

```
Gateway (Deploy)                     Fleet Node
   │                                      │
   │── SSH + SFTP binary ────────────────→│  CORTEX_MESH_SPAWNED=1
   │                                      │  transport.SignalReady()
   │◄── [4B magic: "CM" 0x01 0x01] ──────│  (DeployReadyMagic)
   │                                      │
   │    Deploy() returns — stream ready    │
   │                                      │
   │──── [4B len][cert PEM] ─────────────→│
   │──── [4B len][key PEM] ──────────────→│  readCertBundle()
   │──── [4B len][CA PEM] ───────────────→│  SaveIdentity()
   │                                      │
   │◄─── "Installed!\n" ──────────────────│  fork -daemon process
   │     SSH channel closed                │
   │                                      │
   │                                      │  -daemon node boots
   │                                      │  Listen(ctx, "0.0.0.0:4443")
   │                                      │
   │ Gateway dials TCP host:4443          │
   │◄════ membrane handshake (mTLS) ═════►│  AddPeer()
   │◄════ yamux multiplexing ════════════►│
   │                                      │
   │  Stream 0: gossip, sonar            │
   │  Stream N: tool invocation           │
```

## Tool Wire Protocol

Remote tool invocation uses length-prefixed protobuf framing over mesh streams:

```
[4 bytes: big-endian length][protobuf: ToolRequest/ToolResponse]
```

This is the same framing used by the control plane codec, keeping the protocol uniform.

## Library Features Demonstrated

- **api.NewNode** — full NodeConfig with events, reconnect, known hosts
- **api.NodeEvents** — OnPeerJoined, OnPeerLost, OnIsolated, OnReconnected, OnOrphaned
- **api.ReconnectPolicy** — exponential backoff with timeout
- **membrane.Config** — mTLS with ephemeral Ed25519 certs
- **transport.SelfDeployer** — binary self-deployment via SSH/SFTP
- **transport.SignalReady** — 4-byte readiness handshake from fleet nodes
- **transport.WasDeployed** — detection of fleet vs gateway mode
- **transport.NewStdioConn** — wrapping streams as net.Conn
- **transport.SelfCleanup** — leave-no-trace binary deletion
- **tools.Registry** — tool registration with capability advertising
- **tools.ServeToolConn/Listener** — server-side wire protocol
- **tools.DialInvoke** — client-side wire protocol
- **tools.NeuronBridge** — mesh-aware remote invocation adapter
- **gateway.Gateway** — MCP meta-tool dispatch (list_tools, tool_help, call_tool)
- **vault.Vault** — encrypted credential storage
- **vault.Credential** — SSH key and password authentication
- **routing.Sonar** — broadcast capability discovery
- **routing.GradientTable** — impedance-based gossip routing
