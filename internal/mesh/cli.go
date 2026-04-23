// Package main provides the Cortex MCP server application.
//
// This application is a single binary that can
// serve as either a gateway (bootstrap node) or a fleet node (deployed),
// depending on how it was launched.
//
// The application demonstrates ALL mesh lifecycle features:
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
//	CGO_ENABLED=0 go build -o cortex-mcp .
//
//	# Connect to existing mesh or deploy ephemerally:
//	./cortex-mcp --config mesh.toml
//
//	# Install persistent services on fleet nodes:
//	./cortex-mcp install [node_id]
//
//	# Uninstall from fleet nodes:
//	./cortex-mcp uninstall [node_id]
//
//	# Stop fleet nodes without uninstalling:
//	./cortex-mcp stop [node_id]
//
//	# Bridge stdin/stdout to a local TCP address:
//	./cortex-mcp bridge localhost:4443
//
//	# Run as a persistent daemon (systemd entry point):
//	./cortex-mcp daemon
package mesh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/cortex-mesh/cortex-mesh/api"
	"github.com/cortex-mesh/cortex-mesh/gateway"
	"github.com/cortex-mesh/cortex-mesh/membrane"
	"github.com/cortex-mesh/cortex-mesh/tools"
	"github.com/cortex-mesh/cortex-mesh/transport"
	"github.com/cortex-mesh/cortex-mesh/vault"
	"github.com/flipfloptech/cortex-mcp/internal/config"
	"github.com/flipfloptech/cortex-mcp/internal/logger"
	"github.com/flipfloptech/cortex-mcp/internal/mcp"
	"github.com/flipfloptech/cortex-mcp/internal/registry"
	"github.com/flipfloptech/cortex-mcp/internal/registry/tools/lifecycle"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"
)

// toolEntry pairs a definition with its handler for re-registration.
// GatewayOptions contains operation modes for the bootstrap node.
type GatewayOptions struct {
	Install    bool
	Stop       bool
	Target     string
	PureClient bool
	ServeHTTP  string // address to serve HTTP on
}

func Execute() {
	var configPath string
	var skipDeploy bool

	rootCmd := &cobra.Command{
		Use:   "cortex-mcp",
		Short: "Cortex MCP Application",
		Run: func(cmd *cobra.Command, args []string) {
			ctx, cancel, nodeID, cfg, plugins := initEnv(configPath)
			defer cancel()
			runGateway(ctx, cancel, nodeID, cfg, plugins, skipDeploy, GatewayOptions{})
		},
	}

	rootCmd.PersistentFlags().StringVar(&configPath, "config", "", "path to mesh.toml config file")
	rootCmd.PersistentFlags().BoolVar(&skipDeploy, "skip-deploy", false, "skip SFTP upload when deploying nodes")

	bridgeCmd := &cobra.Command{
		Use:   "bridge <addr>",
		Short: "Raw TCP bridge for firewall traversal",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			ctx, cancel, _, _, _ := initEnv(configPath)
			defer cancel()
			if err := runBridge(ctx, args[0], os.Stdin, os.Stdout); err != nil {
				fmt.Fprintf(os.Stderr, "bridge: %v\n", err)
				os.Exit(1)
			}
		},
	}

	serveCmd := &cobra.Command{
		Use:   "serve",
		Short: "Run as an ephemeral fleet node",
		Run: func(cmd *cobra.Command, args []string) {
			ctx, cancel, nodeID, cfg, plugins := initEnv(configPath)
			defer cancel()
			lifecycle.SetLiveMode(true)
			runFleetNode(ctx, nodeID, plugins, cfg, false)
		},
	}

	daemonCmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run as a persistent daemon (systemd entry)",
		Run: func(cmd *cobra.Command, args []string) {
			ctx, cancel, nodeID, cfg, plugins := initEnv(configPath)
			defer cancel()
			lifecycle.SetLiveMode(true)
			runFleetNode(ctx, nodeID, plugins, cfg, true)
		},
	}

	uninstallCmd := &cobra.Command{
		Use:   "uninstall [target]",
		Short: "Remove nodes (ephemeral or persistent)",
		Args:  cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			ctx, cancel, _, cfg, _ := initEnv(configPath)
			defer cancel()
			target := ""
			if len(args) > 0 {
				target = args[0]
			}
			uninstallFleet(ctx, cfg, target)
		},
	}

	localOpCmd := &cobra.Command{
		Use:    "local-op <action>",
		Short:  "Execute a lifecycle operation locally (hidden)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			action := args[0]
			var ops []lifecycle.LifecycleOp
			switch action {
			case "install":
				ops = lifecycle.SelfInstallOps()
			case "uninstall":
				ops = lifecycle.SelfUninstallOps()
			default:
				fmt.Fprintf(os.Stderr, "unknown local-op action: %s\n", action)
				os.Exit(1)
			}

			// We only want this executed intentionally
			// The caller (like SelfDeployer) can just run it, but we can verify it's root
			if os.Geteuid() != 0 {
				fmt.Fprintf(os.Stderr, "local-op %s must be run as root\n", action)
				os.Exit(1)
			}

			if err := lifecycle.ExecuteOps(ops); err != nil {
				fmt.Fprintf(os.Stderr, "local-op %s failed: %v\n", action, err)
				os.Exit(1)
			}
			fmt.Fprintf(os.Stderr, "local-op %s completed successfully\n", action)
		},
	}

	reinstallCmd := &cobra.Command{
		Use:   "reinstall [target]",
		Short: "Wipe and forcefully reinstall fleet nodes",
		Args:  cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			ctx, cancel, nodeID, cfg, plugins := initEnv(configPath)
			defer cancel()
			target := ""
			if len(args) > 0 {
				target = args[0]
			}
			uninstallFleet(ctx, cfg, target)
			opts := GatewayOptions{Install: true, Target: target}
			runGateway(ctx, cancel, nodeID, cfg, plugins, skipDeploy, opts)
		},
	}

	startCmd := &cobra.Command{
		Use:   "start [target]",
		Short: "Start persistent services via SSH",
		Args:  cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			ctx, cancel, _, cfg, _ := initEnv(configPath)
			defer cancel()
			target := ""
			if len(args) > 0 {
				target = args[0]
			}
			startFleet(ctx, cfg, target)
		},
	}

	stopCmd := &cobra.Command{
		Use:   "stop [target]",
		Short: "Stop fleet nodes without uninstalling",
		Args:  cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			ctx, cancel, nodeID, cfg, plugins := initEnv(configPath)
			defer cancel()
			target := ""
			if len(args) > 0 {
				target = args[0]
			}
			opts := GatewayOptions{Stop: true, Target: target}
			runGateway(ctx, cancel, nodeID, cfg, plugins, skipDeploy, opts)
		},
	}

	installCmd := &cobra.Command{
		Use:   "install [target]",
		Short: "Persist nodes as systemd services",
		Args:  cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			ctx, cancel, nodeID, cfg, plugins := initEnv(configPath)
			defer cancel()
			target := ""
			if len(args) > 0 {
				target = args[0]
			}
			opts := GatewayOptions{Install: true, Target: target}
			runGateway(ctx, cancel, nodeID, cfg, plugins, skipDeploy, opts)
		},
	}

	mcpCmd := &cobra.Command{
		Use:   "mcp [ip:port]",
		Short: "Run as an mTLS Streamable HTTP MCP Server",
		Args:  cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			addr := "localhost:8080"
			if len(args) > 0 {
				addr = args[0]
			}
			ctx, cancel, nodeID, cfg, plugins := initEnv(configPath)
			defer cancel()
			opts := GatewayOptions{
				PureClient: true,
				ServeHTTP:  addr,
			}
			runGateway(ctx, cancel, nodeID, cfg, plugins, skipDeploy, opts)
		},
	}

	rootCmd.AddCommand(bridgeCmd, serveCmd, daemonCmd, uninstallCmd, reinstallCmd, startCmd, stopCmd, installCmd, mcpCmd, buildImportExaCmd(), localOpCmd)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// initEnv bootstraps the common context, configuration, nodeID, tool definitions,
// and the plugin registry. The plugin registry evaluates each tool's IsSupported()
// against the local environment — unsupported tools are logged and excluded.
func initEnv(configPath string) (context.Context, context.CancelFunc, string, *config.MeshConfig, *registry.PluginRegistry) {
	ctx, cancel := context.WithCancel(context.Background())
	cfg, err := loadConfig(configPath)

	// Setup best-in-class logging before doing anything else
	logLvl := "info"
	logEnc := "console"
	if err == nil {
		// In the future, read logLvl and logEnc from cfg
		logLvl = "debug"
	}
	if _, logErr := logger.InitLogger(logger.Config{Level: logLvl, Encoding: logEnc}); logErr != nil {
		fmt.Fprintf(os.Stderr, "failed to init logger: %v\n", logErr)
	}

	if err != nil {
		zap.S().Warnw("config not loaded, using defaults", "error", err)
		cfg = config.Default()
	}

	nodeID := cfg.Node.ID
	if nodeID == "" {
		nodeID = os.Getenv("CORTEX_NODE_ID")
	}
	if nodeID == "" {
		hostname, _ := os.Hostname()
		nodeID = hostname
	}

	// Build the plugin registry from globally registered tools.
	// Each tool's IsSupported() is evaluated against the local environment.
	plugins := registry.NewPluginRegistry(nodeID)
	for name, reason := range plugins.Unsupported() {
		zap.S().Infow("plugin skipped", "tool", name, "reason", reason)
	}
	zap.S().Infow("plugin registry loaded", "supported", len(plugins.Supported()), "skipped", len(plugins.Unsupported()))

	return ctx, cancel, nodeID, cfg, plugins
}

// staticGroupResolver implements a hardcoded static grouping.
type staticGroupResolver struct {
	groups map[string][]string
}

func (e *staticGroupResolver) Resolve(source, group string) ([]string, error) {
	if nodes, ok := e.groups[group]; ok {
		return nodes, nil
	}
	return nil, fmt.Errorf("unknown group: %s", group)
}

func (e *staticGroupResolver) List(source string) ([]string, error) {
	var keys []string
	for k := range e.groups {
		keys = append(keys, k)
	}
	return keys, nil
}

func registerNodeTools(registry *tools.Registry, node *api.Node) {
	registry.Register(tools.ToolDefinition{
		Name:            "mesh_topology",
		Description:     "Reports the current mesh topology: peer count, known nodes, resolver entries, and per-node details",
		LongDescription: "Returns a point-in-time snapshot of the mesh topology as seen by this node. Includes directly connected peers, all nodes learned via gossip (with impedance and capabilities), and the resolver cache size. Useful for verifying deployment completeness and debugging connectivity.",
		Category:        "mesh",
	}, func(_ context.Context, _ json.RawMessage) (*tools.ToolResult, error) {
		snap := node.MeshTopology()
		data, err := json.Marshal(snap)
		if err != nil {
			return nil, fmt.Errorf("marshal topology: %w", err)
		}
		return &tools.ToolResult{Content: data}, nil
	})

	registry.Register(tools.ToolDefinition{
		Name:            "node_deploy",
		Description:     "Deploy this binary to another host via the mesh",
		LongDescription: "Deploys the mesh binary to the specified target host using SelfDeployer. Any node in the fabric can act as a jumphost, enabling deployment to hosts unreachable from the gateway.",
		Category:        "lifecycle",
		Hidden:          true,
		Parameters: []tools.ToolParam{
			{Name: "target", Type: "string", Description: "Target host address (e.g., 10.0.1.5 or host:port)", Required: true},
		},
	}, buildNodeDeployHandler(node))
}

// runFleetNode handles the deployed fleet node lifecycle.
func runFleetNode(ctx context.Context, nodeID string, plugins *registry.PluginRegistry, cfg *config.MeshConfig, isDaemon bool) {
	zap.S().Infow("deployed fleet node — bootstrapping", "node_id", nodeID, "daemon", isDaemon)

	lockName := "@cortex-mcp-lock"
	lockCloser, err := lifecycle.AcquireLock(ctx, lockName)
	if err != nil {
		zap.S().Warnw("lock acquisition failed, attempting to kill legacy process", "error", err)
		if killErr := lifecycle.KillLockedProcess(ctx, lockName); killErr != nil {
			zap.S().Errorw("failed to kill legacy process", "error", killErr)
			os.Exit(1)
		}
		// Give the kernel a moment to release the abstract socket
		time.Sleep(500 * time.Millisecond)

		lockCloser, err = lifecycle.AcquireLock(ctx, lockName)
		if err != nil {
			zap.S().Errorw("failed to acquire lock after kill", "error", err)
			os.Exit(1)
		}
	}
	defer func() {
		_ = lockCloser.Close()
	}()

	if isDaemon {
		// Ignore SIGHUP so we survive SSH terminal detachment.
		signal.Ignore(syscall.SIGHUP)
	}

	var membraneCfg *membrane.Config

	if isDaemon {
		// Read identity strictly from disk cache
		loadedNodeID, cfgMembrane, err := LoadIdentity()
		if err != nil {
			zap.S().Errorw("load identity from disk", "error", err)
			os.Exit(1)
		}
		nodeID = loadedNodeID
		membraneCfg = cfgMembrane
	} else {
		// Signal readiness to the deployer. Deploy() blocks until this
		// magic arrives, so the stream is guaranteed ready for cert exchange.
		if err := transport.SignalReady(os.Stdout); err != nil {
			zap.S().Errorw("signal ready", "error", err)
			os.Exit(1)
		}

		// Read deployment protocol mode byte.
		var modeBuf [1]byte
		if _, err := io.ReadFull(os.Stdin, modeBuf[:]); err != nil {
			zap.S().Errorw("read deploy mode", "error", err)
			os.Exit(1)
		}
		isInstall := modeBuf[0] == 0x01

		// Read cert bundle from stdin (sent by gateway after receiving ready signal).
		bundle, cfgMembrane, err := readCertBundle(os.Stdin)
		if err != nil {
			zap.S().Errorw("read cert bundle", "error", err)
			os.Exit(1)
		}
		membraneCfg = cfgMembrane

		if isInstall {
			if err := SaveIdentity(nodeID, bundle); err != nil {
				zap.S().Errorw("save identity bundle", "error", err)
				os.Exit(1)
			}

			if err := lifecycle.ExecuteOps(lifecycle.SelfInstallOps()); err != nil {
				zap.S().Errorw("install systemd service", "error", err)
				os.Exit(1)
			}

			// ACK to gateway
			_, _ = os.Stdout.Write([]byte{'O', 'K', 0x00, 0x06})

			// Exit cleanly, dropping the SSH stream! The systemd service will start the daemon.
			os.Exit(0)
		}
	}

	reconnectPolicy := api.ReconnectPolicy{}
	if isDaemon {
		reconnectPolicy = api.ReconnectPolicy{
			Enabled:      true,
			InitialDelay: 1 * time.Second,
			MaxDelay:     30 * time.Second,
			Timeout:      5 * time.Minute,
		}
	}

	privKey, ok := membraneCfg.Certificate.PrivateKey.(ed25519.PrivateKey)
	if !ok {
		zap.S().Errorw("node private key is not ed25519")
		os.Exit(1)
	}

	v, err := vault.New(privKey)
	if err != nil {
		zap.S().Errorw("failed to create vault", "error", err)
		os.Exit(1)
	}

	// Create the mesh node with membrane config.
	node, err := api.NewNode(ctx, api.NodeConfig{
		NodeID:     nodeID,
		Vault:      v, // Empty vault enables fleet nodes to request credentials for node_deploy
		KnownHosts: cfg.KnownHosts(),
		Reconnect:  reconnectPolicy,
		Events: api.NodeEvents{
			OnPeerJoined:  func(peerID string) { zap.S().Infow("fleet: peer joined", "peer", peerID) },
			OnPeerLost:    func(peerID string) { zap.S().Infow("fleet: peer lost", "peer", peerID) },
			OnIsolated:    func() { zap.S().Warnw("fleet: isolated — zero peers") },
			OnReconnected: func(peerID string) { zap.S().Infow("fleet: reconnected!", "peer", peerID) },
			OnOrphaned: func() {
				zap.S().Errorw("fleet: orphaned — reconnect exhausted, shutting down")
				_ = transport.SelfCleanup()
				os.Exit(0)
			},
		},
	})
	if err != nil {
		zap.S().Errorw("create fleet node", "error", err)
		os.Exit(1)
	}
	node.SetMembraneConfig(membraneCfg)

	// Register tools with capability advertising.
	meshReg := tools.NewRegistry(node)
	plugins.BridgeToMesh(meshReg)
	registerNodeTools(meshReg, node)

	if !isDaemon {
		// For standard temporary stdioconns, accept the deployer's connection (mTLS handshake + yamux).
		if err := node.AcceptStdio(os.Stdin, os.Stdout); err != nil {
			zap.S().Errorw("accept stdio", "error", err)
			os.Exit(1)
		}
	}

	// Start gossip ticker — propagate our capabilities to peers.
	node.StartGossipTicker(ctx, node.GossipIntervalDuration())

	// Serve tools on the mesh listener.
	lis, err := node.GrpcListener()
	if err != nil {
		zap.S().Errorw("grpc listener", "error", err)
		os.Exit(1)
	}

	if isDaemon {
		listenAddr := fmt.Sprintf("0.0.0.0:%d", cfg.Node.MeshPort)
		tcpLis, err := node.Listen(ctx, listenAddr)
		if err != nil {
			zap.S().Errorw("daemon listen failed", "addr", listenAddr, "error", err)
		} else {
			zap.S().Infow("daemon listening on TCP", "addr", tcpLis.Addr())
		}
	}

	zap.S().Infow("fleet node ready — serving tools", "node_id", nodeID, "tools", len(meshReg.ListLocal()))
	tools.ServeToolListener(ctx, lis, meshReg)
}

// runGateway handles the gateway (bootstrap) node lifecycle.
func runGateway(ctx context.Context, cancel context.CancelFunc, nodeID string, cfg *config.MeshConfig, plugins *registry.PluginRegistry, skipDeploy bool, opts GatewayOptions) {
	install := opts.Install

	zap.S().Infow("starting cortex-mcp gateway", "node_id", nodeID)

	pki, isNew, err := loadOrGeneratePKI()
	if err != nil {
		zap.S().Fatalw("load or generate PKI failed", "error", err)
	}
	gatewayCert, err := pki.generateNodeCert(nodeID, false)
	if err != nil {
		zap.S().Fatalw("generate node cert failed", "error", err)
	}

	zap.S().Infow("mesh PKI initialized", "node_id", nodeID, "new_ca", isNew)

	// --- Phase 2: Create mesh node ---
	node, err := api.NewNode(ctx, api.NodeConfig{
		NodeID:         nodeID,
		KnownHosts:     cfg.KnownHosts(),
		GossipInterval: 3 * time.Second, // configurable: 3s for HPC, 10s for WAN
		Events: api.NodeEvents{
			OnPeerJoined: func(peerID string) {
				zap.S().Infow("peer joined", "peer_id", peerID)
			},
			OnPeerLost: func(peerID string) {
				zap.S().Infow("peer lost", "peer_id", peerID)
			},
			OnIsolated: func() {
				zap.S().Warnw("mesh isolated", "peers", 0)
			},
			OnReconnected: func(peerID string) {
				zap.S().Infow("mesh reconnected", "peer_id", peerID)
			},
			OnOrphaned: func() {
				zap.S().Errorw("mesh orphaned", "action", "cleanup")
				if err := transport.SelfCleanup(); err != nil {
					zap.S().Debugw("self-cleanup", "error", err)
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
		zap.S().Fatalw("create mesh node failed", "error", err)
	}
	defer func() {
		if err := node.Close(); err != nil {
			zap.S().Debugw("close node", "error", err)
		}
	}()
	node.SetMembraneConfig(pki.membraneConfig(gatewayCert))

	// Register tools with capability advertising (unless PureClient mode).
	meshReg := tools.NewRegistry(node)
	if !opts.PureClient {
		plugins.BridgeToMesh(meshReg)
		registerNodeTools(meshReg, node)
	}

	zap.S().Infow("node created", "node_id", nodeID, "caps", len(meshReg.ListLocal()), "reconnect_policy", "1s->30s")

	// Initialize the credential vault.
	v, err := initVault(cfg)
	if err != nil {
		zap.S().Fatalw("init vault failed", "error", err)
	}

	bridge := tools.NewNeuronBridge(node)

	// Create group resolver to inject into Gateway for nodeset processing
	resolver := &staticGroupResolver{
		groups: cfg.Groups,
	}

	gw := gateway.New(meshReg, bridge, gateway.WithGroupResolver(resolver))

	// --- Phase 5: Deploy to seed hosts ---
	knownHosts := cfg.KnownHosts()
	if len(knownHosts) == 0 {
		zap.S().Infow("no seed hosts configured - running in local-only mode")
		return
	}

	zap.S().Infow("deploying to mesh seed hosts")
	deployedNodes := deployAndConnect(ctx, node, pki, cfg, v, skipDeploy, false, install)

	if len(deployedNodes) == 0 {
		zap.S().Warnw("no remote nodes successfully joined - check credentials")
	}

	if opts.Stop {
		zap.S().Infow("stopping remote nodes")

		agents, _ := bridge.DiscoverTool(ctx, "node_stop")
		var nodeIDs []string
		for _, a := range agents {
			if opts.Target == "" || a.NodeID == opts.Target {
				nodeIDs = append(nodeIDs, a.NodeID)
			}
		}

		res, err := tools.NewRemoteInvoker(bridge).FanOut(ctx, nodeIDs, "node_stop", nil)
		if err != nil {
			zap.S().Errorw("stop dispatch failed", "error", err)
		} else {
			zap.S().Infow("stop broadcast successful", "nodes", len(res))
		}
		cancel()
		time.Sleep(500 * time.Millisecond)
		return
	}

	// --- Phase 6: Start gossip ---
	gossipInterval := node.GossipIntervalDuration()
	node.StartGossipTicker(ctx, gossipInterval)
	zap.S().Infow("gossip ticker started", "interval", gossipInterval)

	zap.S().Infow("waiting for gossip convergence")
	time.Sleep(gossipInterval + 1*time.Second)
	zap.S().Infow("gossip converged", "peers", node.PeerCount())

	// --- Phase 10b: Serve HTTP if requested ---
	if opts.ServeHTTP != "" {
		zap.S().Infow("serving MCP Streamable HTTP", "addr", opts.ServeHTTP)

		// Create an MCP Server adapter for the Gateway Dispatcher
		mcpSrv := mcp.NewServer(gw, node, plugins)

		go func() {
			if err := mcp.StartHTTPServer(opts.ServeHTTP, mcpSrv); err != nil {
				zap.S().Errorw("mcp http server failed", "error", err)
				cancel()
			}
		}()

		// Block until shutdown signal
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt, syscall.SIGTERM)
		select {
		case <-c:
			zap.S().Infow("shutting down mcp server")
		case <-ctx.Done():
		}
	}

	// --- Phase 11: Cleanup ---
	zap.S().Infow("cleanup starting")
	cancel()
	if err := node.Close(); err != nil {
		zap.S().Debugw("close node", "error", err)
	}
	for _, dn := range deployedNodes {
		if err := dn.conn.Close(); err != nil {
			zap.S().Debugw("close deploy conn", "node", dn.nodeID, "error", err)
		}
		zap.S().Debugw("disconnected node", "node_id", dn.nodeID)
	}
	zap.S().Infow("shutdown complete")
}

// deployedNode tracks a deployed remote node.
type deployedNode struct {
	nodeID string
	conn   net.Conn
}

// printHeader displays startup information.
func printHeader(nodeID string) {
	fmt.Fprintf(os.Stderr, "\n=== Cortex MCP Application ===\n")
	fmt.Fprintf(os.Stderr, "Gateway node: %s\n", nodeID)
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
		zap.S().Warnw("some credentials failed to load", "error", err)
	}

	patterns := v.AllPatterns()
	if len(patterns) > 0 {
		zap.S().Infow("vault loaded", "credential_patterns", patterns)
	}

	return v, nil
}

// deployAndConnect deploys the binary to each seed host and establishes
// mesh connections via the membrane (mTLS) handshake.
func deployAndConnect(ctx context.Context, node *api.Node, pki *ephemeralPKI, cfg *config.MeshConfig, v *vault.Vault, skipDeploy, daemon, install bool) []deployedNode {
	var deployed []deployedNode
	deployer := &transport.SelfDeployer{
		RemotePath: fmt.Sprintf("/tmp/cortex-mcp-deploy-%d", time.Now().UnixNano()),
		SkipUpload: skipDeploy,
	}

	if daemon {
		deployer.ExecArgs = []string{"-daemon"}
	}

	for remoteNodeID, hostCfg := range cfg.Hosts {
		if len(hostCfg.Addresses) == 0 {
			zap.S().Warnw("no addresses configured for node", "node_id", remoteNodeID)
			continue
		}

		baseAddr := hostCfg.Addresses[0]
		// Determine base host (strip any existing port for calculation)
		hostStr, portStr, splitErr := net.SplitHostPort(baseAddr)
		if splitErr != nil {
			hostStr = baseAddr
			portStr = ""
		}

		sshPort := hostCfg.GetSSHPort(cfg.Node.SSHPort)
		meshPort := hostCfg.GetMeshPort(cfg.Node.MeshPort)

		var sshAddr, meshAddr string
		if portStr != "" {
			sshAddr = baseAddr
			meshAddr = fmt.Sprintf("%s:%d", hostStr, meshPort)
		} else {
			sshAddr = fmt.Sprintf("%s:%d", hostStr, sshPort)
			meshAddr = fmt.Sprintf("%s:%d", hostStr, meshPort)
		}

		// Look up credentials from the vault.
		cred, ok := v.Match(sshAddr)
		if !ok {
			cred, ok = v.Match(hostStr)
		}
		if !ok {
			zap.S().Warnw("no matching credentials in vault", "node_id", remoteNodeID, "addr", sshAddr)
			continue
		}

		deployCred, err := toDeployCredential(cred)
		if err != nil {
			zap.S().Errorw("invalid credentials", "node_id", remoteNodeID, "addr", sshAddr, "error", err)
			continue
		}

		// --- Connectivity decision tree ---
		// 1. TCP reachable → direct connect (upgrade if needed)
		// 2. TCP firewalled, service active → SSH bridge
		// 3. Neither → fresh deploy
		probeConn := probeExistingNode(ctx, meshAddr)

		if probeConn != nil {
			// --- Path 1: TCP reachable, direct connect ---
			_ = probeConn.Close()

			if needsUpgrade(skipDeploy) {
				zap.S().Infow("upgrading remote node via direct TCP", "node_id", remoteNodeID, "addr", sshAddr)
				remotePath := lifecycle.InstallRemotePath("")
				if err := upgradeRemoteNode(ctx, sshAddr, deployCred, remotePath); err != nil {
					zap.S().Errorw("upgrade failed", "node_id", remoteNodeID, "error", err)
					continue
				}
				time.Sleep(upgradeWaitAfterRestart)
			} else {
				zap.S().Infow("reconnecting to remote node", "node_id", remoteNodeID, "addr", meshAddr)
			}

			var d net.Dialer
			conn, err := d.DialContext(ctx, "tcp", meshAddr)
			if err != nil {
				zap.S().Errorw("mesh connect TCP failed", "node_id", remoteNodeID, "error", err)
				continue
			}

			if err := node.AddPeer(ctx, conn, false); err != nil {
				zap.S().Errorw("mTLS membrane handshake failed via TCP", "node_id", remoteNodeID, "error", err)
				_ = conn.Close()
				continue
			}

			zap.S().Infow("connected to mesh peer via TCP", "node_id", remoteNodeID)
			deployed = append(deployed, deployedNode{
				nodeID: remoteNodeID,
				conn:   conn,
			})
			continue
		}

		// TCP unreachable — check if service is installed but firewalled.
		if checkServiceActive(ctx, sshAddr, deployCred) {
			// --- Path 2: Service active, port firewalled → SSH bridge ---
			if needsUpgrade(skipDeploy) {
				zap.S().Infow("upgrading remote node via SSH bridge", "node_id", remoteNodeID, "addr", sshAddr)
				remotePath := lifecycle.InstallRemotePath("")
				if err := upgradeRemoteNode(ctx, sshAddr, deployCred, remotePath); err != nil {
					zap.S().Errorw("upgrade via SSH bridge failed", "node_id", remoteNodeID, "error", err)
					continue
				}
				time.Sleep(upgradeWaitAfterRestart)
			} else {
				zap.S().Infow("bridging to remote node via SSH", "node_id", remoteNodeID, "addr", sshAddr)
			}

			remotePath := lifecycle.InstallRemotePath("")
			stream, err := sshExecBridge(ctx, sshAddr, deployCred, remotePath, meshPort)
			if err != nil {
				zap.S().Errorw("SSH bridge failed", "node_id", remoteNodeID, "error", err)
				continue
			}

			// Wait for bridge readiness (same 4-byte magic as deploy).
			readyBuf := make([]byte, 4)
			if _, err := io.ReadFull(stream, readyBuf); err != nil {
				zap.S().Errorw("bridge readiness failed", "node_id", remoteNodeID, "error", err)
				_ = stream.Close()
				continue
			}

			conn := transport.NewStdioConn(stream, stream)
			if err := node.AddPeer(ctx, conn, false); err != nil {
				zap.S().Errorw("mTLS membrane handshake failed over bridge", "node_id", remoteNodeID, "error", err)
				_ = conn.Close()
				continue
			}

			zap.S().Infow("connected to mesh peer via SSH bridge", "node_id", remoteNodeID)
			deployed = append(deployed, deployedNode{
				nodeID: remoteNodeID,
				conn:   conn,
			})
			continue
		}

		// --- Path 3: No existing node → fresh deploy ---
		zap.S().Infow("deploying to fresh remote node", "node_id", remoteNodeID, "addr", sshAddr)

		stream, err := deployer.Deploy(ctx, sshAddr, deployCred, nil)
		if err != nil {
			zap.S().Errorw("deployment failed", "node_id", remoteNodeID, "error", err)
			continue
		}

		// Deploy() already waited for the readiness handshake.
		// The stream is ready for cert exchange.

		// Generate cert bundle for the fleet node and send it
		// over the raw stream BEFORE the membrane handshake.
		bundle, err := pki.generateNodeBundle(remoteNodeID)
		if err != nil {
			zap.S().Errorw("generate node cert bundle failed", "node_id", remoteNodeID, "error", err)
			dumpRemoteStderr(stream)
			if cerr := stream.Close(); cerr != nil {
				zap.S().Debugw("close stream", "error", cerr)
			}
			continue
		}

		modeByte := byte(0x00)
		if install {
			modeByte = 0x01
		}
		if _, err := stream.Write([]byte{modeByte}); err != nil {
			zap.S().Errorw("send protocol mode failed", "node_id", remoteNodeID, "error", err)
			dumpRemoteStderr(stream)
			_ = stream.Close()
			continue
		}

		if err := writeCertBundle(stream, bundle); err != nil {
			zap.S().Errorw("send cert bundle failed", "node_id", remoteNodeID, "error", err)
			dumpRemoteStderr(stream)
			if cerr := stream.Close(); cerr != nil {
				zap.S().Debugw("close stream", "error", cerr)
			}
			continue
		}

		if install {
			ackBuf := make([]byte, 4)
			if _, err := io.ReadFull(stream, ackBuf); err != nil || string(ackBuf) != string([]byte{'O', 'K', 0x00, 0x06}) {
				zap.S().Errorw("install ack failed", "node_id", remoteNodeID, "error", err)
				dumpRemoteStderr(stream)
				_ = stream.Close()
				continue
			}
			_ = stream.Close()

			// Dial the newly spawned daemon on its TCP port (with retries for boot-up time)
			var conn net.Conn
			var dialErr error
			for i := 0; i < 5; i++ {
				var d net.Dialer
				conn, dialErr = d.DialContext(ctx, "tcp", meshAddr)
				if dialErr == nil {
					break
				}
				time.Sleep(500 * time.Millisecond)
			}

			if dialErr != nil {
				zap.S().Errorw("mesh connect TCP after install failed", "node_id", remoteNodeID, "error", dialErr)
				continue
			}

			if err := node.AddPeer(ctx, conn, false); err != nil {
				zap.S().Errorw("mTLS membrane handshake after install failed", "node_id", remoteNodeID, "error", err)
				_ = conn.Close()
				continue
			}

			zap.S().Infow("installed and connected to mesh peer via TCP", "node_id", remoteNodeID)
			deployed = append(deployed, deployedNode{
				nodeID: remoteNodeID,
				conn:   conn,
			})
		} else {
			// Wrap the stream as a net.Conn and establish mesh connection
			// via membrane handshake (mTLS + yamux).
			conn := transport.NewStdioConn(stream, stream)
			if err := node.AddPeer(ctx, conn, false); err != nil {
				zap.S().Errorw("mTLS handshake failed over SSH", "node_id", remoteNodeID, "error", err)
				dumpRemoteStderr(stream)
				if cerr := conn.Close(); cerr != nil {
					zap.S().Debugw("close conn", "error", cerr)
				}
				continue
			}

			zap.S().Infow("deployed and connected to mesh peer via SSH", "node_id", remoteNodeID)
			deployed = append(deployed, deployedNode{
				nodeID: remoteNodeID,
				conn:   conn,
			})
		}
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

// runGatewayMetaTools exercises the gateway's meta-tool dispatch.

// runGatewayFanOut demonstrates fan-out dispatch across deployed nodes.

// runGatewayTopology invokes the mesh_topology tool via the gateway and pretty-prints the output.

// fanOutDirect invokes the "get_system_info" tool on all deployed nodes concurrently.

// loadConfig finds and loads mesh.toml from the given path or default locations.
func loadConfig(path string) (*config.MeshConfig, error) {
	if path != "" {
		return config.Load(path)
	}

	defaults := []string{
		"mesh.toml",
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

// uninstallFleet SSHes to each node and removes the cortex-mesh
// systemd service, unit file, and binary. If target is non-empty,
// only the specified node is uninstalled.
func uninstallFleet(ctx context.Context, cfg *config.MeshConfig, target string) {
	fmt.Fprintf(os.Stderr, "--- Uninstalling cortex-mesh from fleet ---\n")

	// Build vault for SSH credentials.
	_, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  ✗ generate key: %v\n", err)
		return
	}

	v, err := vault.New(privKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  ✗ vault: %v\n", err)
		return
	}

	if err := cfg.LoadCredentials(v); err != nil {
		zap.S().Warnw("some credentials failed to load", "error", err)
	}

	for remoteNodeID, hostCfg := range cfg.Hosts {
		// Skip nodes that don't match the target filter.
		if target != "" && remoteNodeID != target {
			continue
		}
		if len(hostCfg.Addresses) == 0 {
			continue
		}

		baseAddr := hostCfg.Addresses[0]
		hostStr, portStr, splitErr := net.SplitHostPort(baseAddr)
		if splitErr != nil {
			hostStr = baseAddr
			portStr = ""
		}

		sshPort := hostCfg.GetSSHPort(cfg.Node.SSHPort)
		var sshAddr string
		if portStr != "" {
			sshAddr = baseAddr
		} else {
			sshAddr = fmt.Sprintf("%s:%d", hostStr, sshPort)
		}

		cred, ok := v.Match(sshAddr)
		if !ok {
			cred, ok = v.Match(hostStr)
		}
		if !ok {
			fmt.Fprintf(os.Stderr, "  ✗ %s (%s): no credentials\n", remoteNodeID, sshAddr)
			continue
		}

		deployCred, err := toDeployCredential(cred)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ %s (%s): %v\n", remoteNodeID, sshAddr, err)
			continue
		}

		fmt.Fprintf(os.Stderr, "  → %s (%s): uninstalling...", remoteNodeID, sshAddr)

		// Use SelfDeployer to deploy the binary to a unique /tmp path and run local-op uninstall
		deployer := &transport.SelfDeployer{
			RemotePath: fmt.Sprintf("/tmp/cortex-mcp-uninstall-%d", time.Now().UnixNano()),
			ExecArgs:   []string{"local-op", "uninstall"},
		}
		stream, err := deployer.Deploy(ctx, sshAddr, deployCred, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, " ✗ Deploy: %v\n", err)
			continue
		}

		// stream contains stdout/stderr, we should read it until EOF to let it finish
		_, _ = io.Copy(os.Stderr, stream)
		_ = stream.Close()
		fmt.Fprintf(os.Stderr, " ✓ removed\n")
	}

	fmt.Fprintf(os.Stderr, "--- Uninstall complete ---\n")
}

// startFleet connects to known persistent nodes via SSH and runs systemctl start.
// This is used to reactivate nodes that were previously stopped and are off the mesh.
func startFleet(ctx context.Context, cfg *config.MeshConfig, target string) {
	fmt.Fprintf(os.Stderr, "\n--- Starting Persistent Nodes ---\n")

	v, err := initVault(cfg)
	if err != nil {
		zap.S().Errorw("failed to init vault", "error", err)
		return
	}
	if err := cfg.LoadCredentials(v); err != nil {
		zap.S().Warnw("some credentials failed to load", "error", err)
	}

	for remoteNodeID, hostCfg := range cfg.Hosts {
		// Skip nodes that don't match the target filter.
		if target != "" && remoteNodeID != target {
			continue
		}
		if len(hostCfg.Addresses) == 0 {
			continue
		}

		baseAddr := hostCfg.Addresses[0]
		hostStr, portStr, splitErr := net.SplitHostPort(baseAddr)
		if splitErr != nil {
			hostStr = baseAddr
			portStr = ""
		}

		sshPort := hostCfg.GetSSHPort(cfg.Node.SSHPort)
		var sshAddr string
		if portStr != "" {
			sshAddr = baseAddr
		} else {
			sshAddr = fmt.Sprintf("%s:%d", hostStr, sshPort)
		}

		cred, ok := v.Match(sshAddr)
		if !ok {
			cred, ok = v.Match(hostStr)
		}
		if !ok {
			fmt.Fprintf(os.Stderr, "  ✗ %s (%s): no credentials\n", remoteNodeID, sshAddr)
			continue
		}

		deployCred, err := toDeployCredential(cred)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ %s (%s): %v\n", remoteNodeID, sshAddr, err)
			continue
		}

		fmt.Fprintf(os.Stderr, "  → %s (%s): starting service...", remoteNodeID, sshAddr)

		client, err := dialSSH(ctx, sshAddr, deployCred)
		if err != nil {
			fmt.Fprintf(os.Stderr, " ✗ SSH: %v\n", err)
			continue
		}

		startCmd := fmt.Sprintf("sudo systemctl start %s", lifecycle.ServiceName)
		if err := execSSHCommand(client, startCmd); err != nil {
			_ = client.Close()
			fmt.Fprintf(os.Stderr, " ✗ %v\n", err)
			continue
		}

		_ = client.Close()
		fmt.Fprintf(os.Stderr, " ✓ started\n")
	}

	fmt.Fprintf(os.Stderr, "--- Start complete ---\n")
}

// buildNodeDeployHandler returns a tool handler that accepts a target host
// and deploys this binary to it via the mesh using SelfDeployer.
// This allows any mesh node to act as a "jumphost" for deploying deeper
// nodes that the gateway cannot directly SSH to.
func buildNodeDeployHandler(node *api.Node) tools.ToolHandler {
	return func(ctx context.Context, args json.RawMessage) (*tools.ToolResult, error) {
		var params struct {
			Target string `json:"target"`
		}
		if len(args) > 0 {
			if err := json.Unmarshal(args, &params); err != nil {
				return tools.NewErrorResult(fmt.Sprintf("parse args: %v", err)), nil
			}
		}

		if params.Target == "" {
			return tools.NewErrorResult("target is required: provide the host to deploy to"), nil
		}

		if node == nil {
			return tools.NewErrorResult("node_deploy requires an active mesh node"), nil
		}

		// 1. Request credentials for the target from the mesh
		cred, err := node.RequestCredential(ctx, params.Target)
		if err != nil {
			return tools.NewErrorResult(fmt.Sprintf("failed to get credentials: %v", err)), nil
		}

		deployCred, err := toDeployCredential(*cred)
		if err != nil {
			return tools.NewErrorResult(fmt.Sprintf("failed to parse credential: %v", err)), nil
		}

		// 2. Deploy using SelfDeployer
		deployer := &transport.SelfDeployer{
			RemotePath: fmt.Sprintf("/tmp/cortex-mcp-deploy-%d", time.Now().UnixNano()),
			ExecArgs:   []string{"serve"},
			SkipUpload: false,
		}

		stream, err := deployer.Deploy(ctx, params.Target, deployCred, nil)
		if err != nil {
			return tools.NewErrorResult(fmt.Sprintf("deployment failed: %v", err)), nil
		}

		conn := transport.NewStdioConn(stream, stream)

		// 3. Add the resulting stream as a new mesh peer
		if err := node.AddPeer(ctx, conn, false); err != nil {
			_ = conn.Close()
			return tools.NewErrorResult(fmt.Sprintf("failed to add peer: %v", err)), nil
		}

		resp := map[string]interface{}{
			"target": params.Target,
			"status": "deployed",
		}
		data, _ := json.Marshal(resp)

		return &tools.ToolResult{Content: data}, nil
	}
}
