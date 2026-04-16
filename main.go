// Package main demonstrates a complete cortex-mesh consumer binary.
//
// This is the reference integration pattern: a single binary that can
// serve as either a gateway (bootstrap node) or a fleet node (deployed),
// depending on how it was launched.
//
// The example demonstrates ALL mesh lifecycle features:
//
//  1. Load config (mesh.toml) — node identity, seed hosts, credentials
//  2. Generate ephemeral PKI (site CA + node certificates)
//  3. Initialize credential vault
//  4. Create api.Node with full config (events, reconnect, known hosts)
//  5. Register tools locally with capability advertising
//  6. Deploy this binary to each seed host via SSH (SelfDeployer)
//  7. Wait for readiness handshake (4-byte DeployReadyMagic)
//  8. Bootstrap deployed nodes with cert material (pre-membrane)
//  9. Establish mesh connections via AddPeer (mTLS membrane handshake)
//  10. Start gossip ticker for impedance-cost-vector exchange
//  11. Sonar discovery — broadcast to find tools across the mesh
//  12. Remote invocation via NeuronBridge (GrpcDialer + DialInvoke)
//  13. Gateway meta-tool dispatch (list_tools, tool_help, call_tool)
//  14. Fan-out invocation across all deployed nodes
//  15. Clean teardown — close node, deployed nodes exit
//
// Configuration is loaded from mesh.toml (see -config flag).
// Credentials are loaded into the encrypted vault at startup.
//
// Usage:
//
//	# Build first (static binary — required for cross-host deployment):
//	CGO_ENABLED=0 go build -o mesh-example ./example/
//
//	# Run the full E2E demo:
//	./mesh-example -config example/mesh.toml
//
//	# As a deployed fleet node (set automatically by SelfDeployer):
//	CORTEX_MESH_SPAWNED=1 ./mesh-example
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/cortex-mesh/cortex-mesh/api"
	"github.com/cortex-mesh/cortex-mesh/example/config"
	"github.com/cortex-mesh/cortex-mesh/gateway"
	"github.com/cortex-mesh/cortex-mesh/tools"
	"github.com/cortex-mesh/cortex-mesh/transport"
	"github.com/cortex-mesh/cortex-mesh/vault"
	"golang.org/x/crypto/ssh"
)

// toolEntry pairs a definition with its handler for re-registration.
type toolEntry struct {
	def     tools.ToolDefinition
	handler tools.ToolHandler
}

func main() {
	// Parse CLI flags.
	configPath := flag.String("config", "", "path to mesh.toml config file")
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// --- Load configuration ---
	cfg, err := loadConfig(*configPath)
	if err != nil {
		slog.Warn("config not loaded, using defaults", "error", err)
		cfg = &config.MeshConfig{}
	}

	// Determine node identity: config > env > hostname.
	nodeID := cfg.Node.ID
	if nodeID == "" {
		nodeID = os.Getenv("CORTEX_NODE_ID")
	}
	if nodeID == "" {
		hostname, _ := os.Hostname()
		nodeID = hostname
	}

	// --- Define tools ---
	// Tool definitions are shared between gateway and fleet nodes.
	// Each mode creates its own Registry with the appropriate capability tracker.
	entries := defineTools(nodeID)

	// --- Fleet node mode ---
	// When deployed via SelfDeployer, the binary:
	//  1. Reads cert material from stdin (pre-membrane bootstrap)
	//  2. Creates a mesh Node with membrane config
	//  3. Accepts the deployer's connection via AcceptStdio (mTLS handshake)
	//  4. Serves tools on the GrpcListener
	//  5. Blocks until the connection closes
	if transport.WasDeployed() {
		runFleetNode(ctx, nodeID, entries)
		return
	}

	// --- Gateway / Bootstrap mode ---
	runGateway(ctx, cancel, nodeID, cfg, entries)
}

// exampleGroupResolver implements a hardcoded static grouping.
type exampleGroupResolver struct {
	groups map[string][]string
}

func (e *exampleGroupResolver) Resolve(source, group string) ([]string, error) {
	if nodes, ok := e.groups[group]; ok {
		return nodes, nil
	}
	return nil, fmt.Errorf("unknown group: %s", group)
}

func (e *exampleGroupResolver) List(source string) ([]string, error) {
	var keys []string
	for k := range e.groups {
		keys = append(keys, k)
	}
	return keys, nil
}

// defineTools returns the tool catalog shared by all modes.
func defineTools(nodeID string) []toolEntry {
	entries := []toolEntry{
		{
			def: tools.ToolDefinition{
				Name:            "hello",
				Description:     "Say hello from this node",
				LongDescription: "Returns a greeting message from the node. Useful for verifying connectivity and tool invocation.",
				Category:        "demo",
				Parameters: []tools.ToolParam{
					{Name: "name", Type: "string", Description: "Who to greet", Required: false, Default: "world"},
				},
			},
			handler: func(_ context.Context, args json.RawMessage) (*tools.ToolResult, error) {
				var params struct {
					Name string `json:"name"`
				}
				params.Name = "world"
				if len(args) > 0 {
					if err := json.Unmarshal(args, &params); err != nil {
						slog.Debug("hello: unmarshal args", "error", err)
					}
				}
				return tools.NewTextResult(fmt.Sprintf("Hello, %s! From node %s", params.Name, nodeID)), nil
			},
		},
		{
			def: tools.ToolDefinition{
				Name:            "system_info",
				Description:     "Get basic system information",
				LongDescription: "Returns the hostname, OS, architecture, and number of CPUs for this node.",
				Category:        "system",
			},
			handler: func(_ context.Context, _ json.RawMessage) (*tools.ToolResult, error) {
				hostname, _ := os.Hostname()
				info := map[string]interface{}{
					"node_id":  nodeID,
					"hostname": hostname,
					"os":       runtime.GOOS,
					"arch":     runtime.GOARCH,
					"cpus":     runtime.NumCPU(),
				}
				data, _ := json.Marshal(info)
				return &tools.ToolResult{Content: data}, nil
			},
		},
	}

	// Add node-specific capability for group demonstration
	switch nodeID {
	case "oss1":
		entries = append(entries, toolEntry{
			def: tools.ToolDefinition{
				Name:        "storage_check",
				Description: "Storage specific check",
				Category:    "storage",
			},
			handler: func(_ context.Context, _ json.RawMessage) (*tools.ToolResult, error) {
				return tools.NewTextResult("Storage OK from " + nodeID), nil
			},
		})
	case "oss2":
		entries = append(entries, toolEntry{
			def: tools.ToolDefinition{
				Name:        "network_check",
				Description: "Network specific check",
				Category:    "network",
			},
			handler: func(_ context.Context, _ json.RawMessage) (*tools.ToolResult, error) {
				return tools.NewTextResult("Network OK from " + nodeID), nil
			},
		})
	}

	return entries
}

// registerTools populates a registry with the given tool entries.
func registerTools(registry *tools.Registry, entries []toolEntry) {
	for _, e := range entries {
		registry.Register(e.def, e.handler)
	}
}

// runFleetNode handles the deployed fleet node lifecycle.
func runFleetNode(ctx context.Context, nodeID string, entries []toolEntry) {
	slog.Info("deployed fleet node — bootstrapping", "node_id", nodeID)

	// Signal readiness to the deployer. Deploy() blocks until this
	// magic arrives, so the stream is guaranteed ready for cert exchange.
	if err := transport.SignalReady(os.Stdout); err != nil {
		slog.Error("signal ready", "error", err)
		os.Exit(1)
	}

	// Read cert bundle from stdin (sent by gateway after receiving ready signal).
	membraneCfg, err := readCertBundle(os.Stdin)
	if err != nil {
		slog.Error("read cert bundle", "error", err)
		os.Exit(1)
	}

	// Create the mesh node with membrane config.
	node, err := api.NewNode(ctx, api.NodeConfig{
		NodeID: nodeID,
		Events: api.NodeEvents{
			OnPeerJoined: func(peerID string) {
				slog.Info("fleet: peer joined", "peer", peerID)
			},
			OnPeerLost: func(peerID string) {
				slog.Info("fleet: peer lost", "peer", peerID)
			},
			OnIsolated: func() {
				slog.Warn("fleet: isolated — zero peers, reconnect loop starting")
			},
			OnReconnected: func(peerID string) {
				slog.Info("fleet: reconnected!", "peer", peerID)
			},
			OnOrphaned: func() {
				slog.Error("fleet: orphaned — reconnect exhausted, shutting down")
				os.Exit(0)
			},
		},
	})
	if err != nil {
		slog.Error("create fleet node", "error", err)
		os.Exit(1)
	}
	node.SetMembraneConfig(membraneCfg)

	// Register tools with capability advertising.
	registry := tools.NewRegistry(node)
	registerTools(registry, entries)

	// Accept the deployer's connection (mTLS handshake + yamux).
	if err := node.AcceptStdio(os.Stdin, os.Stdout); err != nil {
		slog.Error("accept stdio", "error", err)
		os.Exit(1)
	}

	// Start gossip ticker — propagate our capabilities to peers.
	// Without this, the gateway's CapabilityIndex stays empty and
	// all capability lookups fall back to Sonar broadcast.
	node.StartGossipTicker(ctx, node.GossipIntervalDuration())

	// Serve tools on the mesh listener.
	lis, err := node.GrpcListener()
	if err != nil {
		slog.Error("grpc listener", "error", err)
		os.Exit(1)
	}
	slog.Info("fleet node ready — serving tools", "node_id", nodeID, "tools", len(registry.ListLocal()))
	tools.ServeToolListener(ctx, lis, registry)
}

// runGateway handles the gateway (bootstrap) node lifecycle.
func runGateway(ctx context.Context, cancel context.CancelFunc, nodeID string, cfg *config.MeshConfig, entries []toolEntry) {
	printHeader(nodeID, entries)

	// --- Phase 1: PKI ---
	fmt.Fprintf(os.Stderr, "--- Phase 1: Ephemeral PKI ---\n")
	pki, err := newEphemeralPKI()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
	gatewayCert, err := pki.generateNodeCert(nodeID, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "  ✓ Site CA generated (ephemeral, 24h validity)\n")
	fmt.Fprintf(os.Stderr, "  ✓ Gateway cert: CN=%s\n", nodeID)

	// --- Phase 2: Create mesh node ---
	fmt.Fprintf(os.Stderr, "\n--- Phase 2: Create mesh node ---\n")
	node, err := api.NewNode(ctx, api.NodeConfig{
		NodeID:         nodeID,
		KnownHosts:     cfg.KnownHosts(),
		GossipInterval: 3 * time.Second, // configurable: 3s for HPC, 10s for WAN
		Events: api.NodeEvents{
			OnPeerJoined: func(peerID string) {
				fmt.Fprintf(os.Stderr, "  [event] peer joined: %s\n", peerID)
			},
			OnPeerLost: func(peerID string) {
				fmt.Fprintf(os.Stderr, "  [event] peer lost: %s\n", peerID)
			},
			OnIsolated: func() {
				fmt.Fprintf(os.Stderr, "  [event] isolated — zero peers\n")
			},
			OnReconnected: func(peerID string) {
				fmt.Fprintf(os.Stderr, "  [event] reconnected via %s\n", peerID)
			},
			OnOrphaned: func() {
				fmt.Fprintf(os.Stderr, "  [event] orphaned — leaving no trace\n")
				if err := transport.SelfCleanup(); err != nil {
					slog.Debug("self-cleanup", "error", err)
				}
			},
		},
		Reconnect: api.ReconnectPolicy{
			Enabled:      true,
			InitialDelay: 1 * time.Second,
			MaxDelay:     30 * time.Second,
			Timeout:      5 * time.Minute,
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal: create node: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if err := node.Close(); err != nil {
			slog.Debug("close node", "error", err)
		}
	}()
	node.SetMembraneConfig(pki.membraneConfig(gatewayCert))

	// Register tools with capability advertising.
	registry := tools.NewRegistry(node)
	registerTools(registry, entries)

	fmt.Fprintf(os.Stderr, "  ✓ Node created: %s (peers=0, caps=%d)\n", nodeID, len(registry.ListLocal()))
	fmt.Fprintf(os.Stderr, "  ✓ Reconnect policy: enabled (1s→30s backoff, 5m timeout)\n")

	// Initialize the credential vault.
	v, err := initVault(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}

	// --- Phase 3: Local tool invocation ---
	fmt.Fprintf(os.Stderr, "\n--- Phase 3: Local tool invocation ---\n")
	runLocalDemo(ctx, registry)

	// --- Phase 4: Gateway meta-tools ---
	fmt.Fprintf(os.Stderr, "\n--- Phase 4: Gateway meta-tools ---\n")
	bridge := tools.NewNeuronBridge(node)

	// Create group resolver to inject into Gateway for nodeset processing
	resolver := &exampleGroupResolver{
		groups: map[string][]string{
			"storage": {"oss1"},
			"network": {"oss2"},
		},
	}

	gw := gateway.New(registry, bridge, gateway.WithGroupResolver(resolver))
	runGatewayMetaTools(ctx, gw)

	// --- Phase 5: Deploy to seed hosts ---
	knownHosts := cfg.KnownHosts()
	if len(knownHosts) == 0 {
		fmt.Fprintf(os.Stderr, "\nNo seed hosts configured in mesh.toml. Skipping remote phases.\n")
		fmt.Fprintf(os.Stderr, "\nDone (local-only mode).\n")
		return
	}

	fmt.Fprintf(os.Stderr, "\n--- Phase 5: Deploy + mesh connect ---\n")
	deployedNodes := deployAndConnect(ctx, node, pki, knownHosts, v)

	if len(deployedNodes) == 0 {
		fmt.Fprintf(os.Stderr, "\nNo nodes deployed successfully. Exiting.\n")
		return
	}

	// --- Phase 6: Start gossip ---
	fmt.Fprintf(os.Stderr, "\n--- Phase 6: Gossip ---\n")
	gossipInterval := node.GossipIntervalDuration()
	node.StartGossipTicker(ctx, gossipInterval)
	fmt.Fprintf(os.Stderr, "  ✓ Gossip ticker started (%s interval)\n", gossipInterval)
	fmt.Fprintf(os.Stderr, "  Waiting for gossip convergence...\n")
	time.Sleep(gossipInterval + 1*time.Second)
	fmt.Fprintf(os.Stderr, "  ✓ Peers: %d\n", node.PeerCount())

	// --- Phase 7: Capability discovery ---
	fmt.Fprintf(os.Stderr, "\n--- Phase 7: Capability discovery ---\n")

	// Tier 1: Local capability index (zero traffic — populated by gossip).
	indexEntries := node.LookupCapability("tool:system_info")
	if len(indexEntries) > 0 {
		fmt.Fprintf(os.Stderr, "  ✓ Capability index: %d node(s) with tool:system_info (zero traffic)\n", len(indexEntries))
		for _, e := range indexEntries {
			fmt.Fprintf(os.Stderr, "    - %s (impedance=%.1f)\n", e.NodeID, e.Impedance)
		}
	} else {
		fmt.Fprintf(os.Stderr, "  ⚠ Capability index empty — falling back to Sonar broadcast\n")
	}

	helloEntries := node.LookupCapability("tool:hello")
	if len(helloEntries) > 0 {
		fmt.Fprintf(os.Stderr, "  ✓ Capability index: %d node(s) with tool:hello (zero traffic)\n", len(helloEntries))
	}

	// Tier 2: Sonar broadcast (fallback — demonstrates backward compat).
	sonarCtx, sonarCancel := context.WithTimeout(ctx, 3*time.Second)
	defer sonarCancel()
	agents, err := node.Sonar(sonarCtx, "tool:system_info")
	if err != nil {
		fmt.Fprintf(os.Stderr, "  ✗ Sonar error: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "  ✓ Sonar discovered %d node(s) with tool:system_info\n", len(agents))
		for _, a := range agents {
			fmt.Fprintf(os.Stderr, "    - %s (impedance=%.1f)\n", a.NodeID, a.Impedance)
		}
	}

	// Wildcard lookup: all nodes offering ANY tools in the mesh.
	// Note: LookupCapabilityWildcard deduplicates by node, so this returns
	// the number of *nodes* offering tools, not the total count of tools.
	allTools := node.LookupCapabilityWildcard("tool:")
	if len(allTools) > 0 {
		fmt.Fprintf(os.Stderr, "  ✓ Wildcard 'tool:*' found %d node(s) offering tool capabilities across the mesh:\n", len(allTools))
		snap := node.CapabilityIndex().Snapshot()
		for _, nt := range allTools {
			var toolNames []string
			for _, cap := range snap[nt.NodeID] {
				if strings.HasPrefix(cap, "tool:") {
					toolNames = append(toolNames, strings.TrimPrefix(cap, "tool:"))
				}
			}
			fmt.Fprintf(os.Stderr, "    - %s offers tools: %s\n", nt.NodeID, strings.Join(toolNames, ", "))
		}
	}

	// --- Phase 8: Remote invocation via NeuronBridge ---
	fmt.Fprintf(os.Stderr, "\n--- Phase 8: Remote invocation via NeuronBridge ---\n")
	for _, dn := range deployedNodes {
		result, err := bridge.InvokeRemote(ctx, dn.nodeID, "system_info", nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ %s: invoke failed: %v\n", dn.nodeID, err)
			continue
		}
		if result.IsError {
			fmt.Fprintf(os.Stderr, "  ✗ %s: tool error: %s\n", dn.nodeID, result.Content)
			continue
		}
		fmt.Fprintf(os.Stderr, "  ✓ %s: %s\n", dn.nodeID, result.Content)
	}

	// --- Phase 9: Fan-out hello ---
	fmt.Fprintf(os.Stderr, "\n--- Phase 9: Fan-out hello ---\n")
	runGatewayFanOut(ctx, gw, deployedNodes)

	// --- Phase 10: Cleanup ---
	fmt.Fprintf(os.Stderr, "\n--- Phase 10: Cleanup ---\n")
	cancel()
	if err := node.Close(); err != nil {
		slog.Debug("close node", "error", err)
	}
	for _, dn := range deployedNodes {
		if err := dn.conn.Close(); err != nil {
			slog.Debug("close deploy conn", "node", dn.nodeID, "error", err)
		}
		fmt.Fprintf(os.Stderr, "  ✓ %s: disconnected\n", dn.nodeID)
	}
	fmt.Fprintf(os.Stderr, "\nDone.\n")
}

// deployedNode tracks a deployed remote node.
type deployedNode struct {
	nodeID string
	conn   net.Conn
}

// printHeader displays startup information.
func printHeader(nodeID string, entries []toolEntry) {
	fmt.Fprintf(os.Stderr, "\n=== cortex-mesh E2E example ===\n")
	fmt.Fprintf(os.Stderr, "Gateway node: %s\n", nodeID)
	fmt.Fprintf(os.Stderr, "Registered tools:\n")
	for _, e := range entries {
		fmt.Fprintf(os.Stderr, "  - %s (%s): %s\n", e.def.Name, e.def.Category, e.def.Description)
	}
	fmt.Fprintf(os.Stderr, "\n")
}

// initVault creates and populates the credential vault.
func initVault(cfg *config.MeshConfig) (*vault.Vault, error) {
	_, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}

	v, err := vault.New(privKey)
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}

	if err := cfg.LoadCredentials(v); err != nil {
		slog.Warn("some credentials failed to load", "error", err)
	}

	patterns := v.AllPatterns()
	if len(patterns) > 0 {
		slog.Info("vault loaded", "credential_patterns", patterns)
	}

	return v, nil
}

// deployAndConnect deploys the binary to each seed host and establishes
// mesh connections via the membrane (mTLS) handshake.
func deployAndConnect(ctx context.Context, node *api.Node, pki *ephemeralPKI, knownHosts map[string][]string, v *vault.Vault) []deployedNode {
	var deployed []deployedNode
	deployer := &transport.SelfDeployer{}

	for remoteNodeID, addrs := range knownHosts {
		if len(addrs) == 0 {
			fmt.Fprintf(os.Stderr, "  ✗ %s: no addresses configured\n", remoteNodeID)
			continue
		}

		addr := addrs[0]
		if !strings.Contains(addr, ":") {
			addr = addr + ":22"
		}

		// Look up credentials from the vault.
		cred, ok := v.Match(addr)
		if !ok {
			host := strings.Split(addr, ":")[0]
			cred, ok = v.Match(host)
		}
		if !ok {
			fmt.Fprintf(os.Stderr, "  ✗ %s (%s): no matching credentials in vault\n", remoteNodeID, addr)
			continue
		}

		deployCred, err := toDeployCredential(cred)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ %s (%s): %v\n", remoteNodeID, addr, err)
			continue
		}

		fmt.Fprintf(os.Stderr, "  → %s (%s): deploying...", remoteNodeID, addr)

		stream, err := deployer.Deploy(ctx, addr, deployCred, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, " ✗ %v\n", err)
			continue
		}

		// Deploy() already waited for the readiness handshake.
		// The stream is ready for cert exchange.

		// Generate cert bundle for the fleet node and send it
		// over the raw stream BEFORE the membrane handshake.
		bundle, err := pki.generateNodeBundle(remoteNodeID)
		if err != nil {
			fmt.Fprintf(os.Stderr, " ✗ generate cert: %v\n", err)
			dumpRemoteStderr(stream)
			if cerr := stream.Close(); cerr != nil {
				slog.Debug("close stream", "error", cerr)
			}
			continue
		}

		if err := writeCertBundle(stream, bundle); err != nil {
			fmt.Fprintf(os.Stderr, " ✗ send certs: %v\n", err)
			dumpRemoteStderr(stream)
			if cerr := stream.Close(); cerr != nil {
				slog.Debug("close stream", "error", cerr)
			}
			continue
		}

		// Wrap the stream as a net.Conn and establish mesh connection
		// via membrane handshake (mTLS + yamux).
		conn := transport.NewStdioConn(stream, stream)
		if err := node.AddPeer(ctx, conn, false); err != nil {
			fmt.Fprintf(os.Stderr, " ✗ mesh connect: %v\n", err)
			dumpRemoteStderr(stream)
			if cerr := conn.Close(); cerr != nil {
				slog.Debug("close conn", "error", cerr)
			}
			continue
		}

		fmt.Fprintf(os.Stderr, " ✓ deployed + connected (mTLS)\n")
		deployed = append(deployed, deployedNode{
			nodeID: remoteNodeID,
			conn:   conn,
		})
	}

	return deployed
}

// dumpRemoteStderr extracts and displays the fleet node's stderr output
// from the deploy stream. This is critical for diagnosing handshake failures —
// if the remote binary crashes, its error output explains why.
func dumpRemoteStderr(stream io.ReadWriteCloser) {
	type stderrCapture interface {
		Stderr() string
	}
	if sc, ok := stream.(stderrCapture); ok {
		if stderr := sc.Stderr(); stderr != "" {
			fmt.Fprintf(os.Stderr, "\n    remote stderr:\n")
			for _, line := range strings.Split(strings.TrimSpace(stderr), "\n") {
				fmt.Fprintf(os.Stderr, "      %s\n", line)
			}
		}
	}
}

// toDeployCredential converts a vault.Credential to a transport.DeployCredential.
func toDeployCredential(cred vault.Credential) (transport.DeployCredential, error) {
	dc := transport.DeployCredential{
		SSHUser: cred.Username,
	}

	switch cred.Type {
	case vault.CredSSHKey:
		signer, err := ssh.ParsePrivateKey(cred.PrivateKey)
		if err != nil {
			return dc, fmt.Errorf("parse SSH key: %w", err)
		}
		dc.SSHKeyData = signer

	case vault.CredSSHPassword:
		dc.SSHPass = cred.Password

	default:
		return dc, fmt.Errorf("unsupported credential type %d for SSH deployment", cred.Type)
	}

	return dc, nil
}

// runLocalDemo invokes tools locally on the gateway node.
func runLocalDemo(ctx context.Context, registry *tools.Registry) {
	result, err := registry.InvokeLocal(ctx, "hello", json.RawMessage(`{"name":"cortex-mesh"}`))
	if err != nil {
		fmt.Fprintf(os.Stderr, "  hello failed: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "  hello: %s\n", result.Content)
	}

	result, err = registry.InvokeLocal(ctx, "system_info", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  system_info failed: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "  system_info: %s\n", result.Content)
	}
}

// runGatewayMetaTools exercises the gateway's meta-tool dispatch.
func runGatewayMetaTools(ctx context.Context, gw *gateway.Gateway) {
	// list_tools — discover available tools.
	result, err := gw.Dispatch(ctx, "list_tools", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  list_tools failed: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "  list_tools: %s\n", result.Content)
	}

	// tool_help — detailed help for system_info.
	result, err = gw.Dispatch(ctx, "tool_help", json.RawMessage(`{"tool_name":"system_info"}`))
	if err != nil {
		fmt.Fprintf(os.Stderr, "  tool_help failed: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "  tool_help: %s\n", result.Content)
	}

	// call_tool — invoke hello locally via the gateway.
	result, err = gw.Dispatch(ctx, "call_tool", json.RawMessage(`{"tool_name":"hello","args":{"name":"mesh-gateway"}}`))
	if err != nil {
		fmt.Fprintf(os.Stderr, "  call_tool failed: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "  call_tool: %s\n", result.Content)
	}
}

// runGatewayFanOut demonstrates fan-out dispatch across deployed nodes.
func runGatewayFanOut(ctx context.Context, gw *gateway.Gateway, nodes []deployedNode) {
	// Fan-out via call_tool with glob pattern.
	result, err := gw.Dispatch(ctx, "call_tool", json.RawMessage(`{"tool_name":"hello","args":{"name":"mesh-gateway"},"node_name":"*"}`))
	if err != nil {
		fmt.Fprintf(os.Stderr, "  ✗ fan-out via gateway error: %v\n", err)
		// Fallback to direct fan-out via DialInvoke over deploy streams.
		fmt.Fprintf(os.Stderr, "  Falling back to direct fan-out...\n")
		fanOutDirect(ctx, nodes)
		return
	}
	fmt.Fprintf(os.Stderr, "  ✓ fan-out (*) result: %s\n", result.Content)

	// Demonstration of fan-out via call_tool using a NodeSet @group
	resultGroup, errGroup := gw.Dispatch(ctx, "call_tool", json.RawMessage(`{"tool_name":"storage_check","args":{},"node_name":"@storage"}`))
	if errGroup != nil {
		fmt.Fprintf(os.Stderr, "  ✗ fan-out via group @storage error: %v\n", errGroup)
	} else {
		fmt.Fprintf(os.Stderr, "  ✓ fan-out (@storage) result: %s\n", resultGroup.Content)
	}
}

// fanOutDirect invokes the "hello" tool on all deployed nodes concurrently.
func fanOutDirect(ctx context.Context, nodes []deployedNode) {
	type nodeResult struct {
		nodeID  string
		content string
		err     error
	}

	results := make(chan nodeResult, len(nodes))

	var wg sync.WaitGroup
	for _, dn := range nodes {
		wg.Add(1)
		go func(dn deployedNode) {
			defer wg.Done()
			result, err := tools.DialInvoke(ctx, dn.conn, "hello", json.RawMessage(`{"name":"mesh-gateway"}`))
			if err != nil {
				results <- nodeResult{nodeID: dn.nodeID, err: err}
				return
			}
			results <- nodeResult{nodeID: dn.nodeID, content: string(result.Content)}
		}(dn)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	for r := range results {
		if r.err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ %s: %v\n", r.nodeID, r.err)
		} else {
			fmt.Fprintf(os.Stderr, "  ✓ %s: %s\n", r.nodeID, r.content)
		}
	}
}

// loadConfig finds and loads mesh.toml from the given path or default locations.
func loadConfig(path string) (*config.MeshConfig, error) {
	if path != "" {
		return config.Load(path)
	}

	defaults := []string{
		"mesh.toml",
		"example/mesh.toml",
	}
	if exe, err := os.Executable(); err == nil {
		defaults = append(defaults, filepath.Join(filepath.Dir(exe), "mesh.toml"))
	}

	for _, p := range defaults {
		if _, err := os.Stat(p); err == nil {
			return config.Load(p)
		}
	}

	return nil, fmt.Errorf("mesh.toml not found (tried: %v)", defaults)
}
