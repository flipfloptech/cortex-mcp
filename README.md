# Cortex MCP Application

A complete, feature-rich binary demonstrating the full cortex-mesh lifecycle. This application serves as the production integration pattern — a single Go program that operates as either a **gateway** (bootstrap node) or a **fleet node** (deployed), depending on how it was launched.

## Operational Modes

### Test Mode (default, temporary)

```bash
./cortex-mcp --config mesh.toml
```

Deploys to **only** the seed hosts in `mesh.toml`. Nodes are temporary processes
that die when the gateway disconnects. Great for validating connectivity,
credentials, and tool execution.

### Persistent Mode (install + systemd)

```bash
./cortex-mcp install --config mesh.toml
```

Deploys to seed hosts **and** installs as a systemd service. Nodes persist
across reboots and are managed via standard commands:

```bash
./cortex-mcp start       # start the mesh node
./cortex-mcp stop        # graceful shutdown
./cortex-mcp uninstall   # uninstall the mesh node
```

### Fleet Node Mode (automatic)

```bash
# Set automatically by SelfDeployer — never run manually:
CORTEX_MESH_SPAWNED=1 ./cortex-mcp daemon
```

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
| 10 | Mesh topology snapshot | `api` |
| 11 | Clean teardown with node event callbacks | `api` |

## Quick Start

```bash
# Build first — static binary, no libc dependency:
# You can use the task runner: `task build`
# Or manually:
CGO_ENABLED=0 go build -o cortex-mcp ./cmd/cortex-mcp/...

# Test mode — deploy to seed hosts, run demo, exit:
./cortex-mcp -config mesh.toml

# Persistent mode — install systemd services on remote hosts:
./cortex-mcp -config mesh.toml -install

# Local-only mode (no remote hosts):
./cortex-mcp
```

## Architecture

```
┌──────────────────────────────────────────────────────────────────┐
│  cortex-mcp binary                                             │
│                                                                  │
│  Gateway mode:                    Fleet node mode:               │
│  ┌──────────────────┐             ┌──────────────────┐           │
│  │ Phase 1: PKI     │             │ SignalReady()     │ → DeployReadyMagic
│  │ Phase 2: Node    │             │ readCertBundle()  │           │
│  │ Phase 3: Local   │             │ SaveIdentity()    │           │
│  │ Phase 4: Gateway │             │ fork -daemon      │           │
│  │ Phase 5: Deploy  │──SSH/SFTP─→ │ api.NewNode()     │           │
│  │ Phase 6: Gossip  │ cert boot   │ Listen(4443)      │ ← TCP mTLS│
│  │ Phase 7: Sonar   │             │ ServeToolListener │           │
│  │ Phase 8: Invoke  │             │ <block forever>   │           │
│  │ Phase 9: FanOut  │             └──────────────────┘           │
│  │ Phase 10: Topo   │                                            │
│  │ Phase 11: Close  │                                            │
│  └──────────────────┘                                            │
└──────────────────────────────────────────────────────────────────┘
```

## Deployment Model

The deployer works **strictly from the config file**:

1. Reads seed hosts from `mesh.toml` `[hosts.*]` sections
2. Resolves each target: config IPs first, DNS fallback for hostnames
3. Deploys the binary via SSH/SFTP to each target
4. In persistent mode (`-install`): installs a systemd service unit
5. Connects to each deployed node via mTLS

**No `/etc/hosts` scraping. No autonomous spreading. No background discovery loops.**

The deployer does exactly what the config says — no more, no less.

## Configuration (`mesh.toml`)

```toml
[node]
id = "gateway-01"

# Deployment targets — these are the ONLY hosts that will be deployed to.
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
CGO_ENABLED=0 go build -o cortex-mcp .

# Verify:
ldd ./cortex-mcp  # → "not a dynamic executable"
```

`SelfDeployer.Deploy()` validates this automatically — if the binary has a `PT_INTERP` program header (dynamic linker), it rejects the deploy with an actionable error before any SSH connection.

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
   │                                      │  fork -daemon process
   │◄─── Process exits 0 ─────────────────│  SSH channel closed
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

## Systemd Service (Persistent Mode)

When `-install` is used, the deployer writes a systemd unit to each remote host:

```ini
[Unit]
Description=Cortex Mesh Node
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/opt/cortex-mesh/bin/cortex-mesh -daemon
Environment=CORTEX_MESH_SPAWNED=1
Restart=on-failure
RestartSec=5
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
```

The binary is installed to `/opt/cortex-mesh/bin/cortex-mesh` (permanent) instead of `/tmp/cortex-mesh-node` (ephemeral).

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
