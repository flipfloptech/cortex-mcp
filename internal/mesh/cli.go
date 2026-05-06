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
//  13. Gateway meta-tool dispatch (get_tool_list, get_tool_help, call_tool)
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
	"bufio"
	"bytes"
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
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/BurntSushi/toml"

	"github.com/flipfloptech/cortex-mcp/internal/config"
	"github.com/flipfloptech/cortex-mcp/internal/logger"
	"github.com/flipfloptech/cortex-mcp/internal/mcp"
	"github.com/flipfloptech/cortex-mcp/internal/registry"
	"github.com/flipfloptech/cortex-mcp/internal/registry/tools/lifecycle"
	"github.com/flipfloptech/cortex-mcp/internal/version"
	"github.com/flipfloptech/cortex-mcp/pkg/mesh/api"
	"github.com/flipfloptech/cortex-mcp/pkg/mesh/gateway"
	"github.com/flipfloptech/cortex-mcp/pkg/mesh/membrane"
	"github.com/flipfloptech/cortex-mcp/pkg/mesh/nucleus"
	"github.com/flipfloptech/cortex-mcp/pkg/mesh/tools"
	"github.com/flipfloptech/cortex-mcp/pkg/mesh/transport"
	"github.com/flipfloptech/cortex-mcp/pkg/mesh/vault"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"
)

// toolEntry pairs a definition with its handler for re-registration.
// GatewayOptions contains operation modes for the bootstrap node.
type GatewayOptions struct {
	Install        bool
	Stop           bool
	Target         string
	PureClient     bool
	ServeHTTP      string // address to serve HTTP on
	Force          bool
	RegenerateKeys bool
}

var (
	forceFlag          bool
	regenerateKeysFlag bool
)

func promptConfirmation(action string, r io.Reader, w io.Writer) error {
	_, _ = fmt.Fprintf(w, "WARNING: This will %s the fleet nodes.\nType '%s' to confirm: ", action, action)
	scanner := bufio.NewScanner(r)
	if !scanner.Scan() {
		return fmt.Errorf("aborted")
	}
	input := strings.TrimSpace(scanner.Text())
	if input != action {
		return fmt.Errorf("aborted: input '%s' did not match '%s'", input, action)
	}
	return nil
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

	daemonCmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run as a fleet node (persistent if systemd/terminal, ephemeral otherwise)",
		Run: func(cmd *cobra.Command, args []string) {
			ctx, cancel, nodeID, cfg, plugins := initEnv(configPath)
			defer cancel()
			lifecycle.SetLiveMode(true)

			// Auto-detect node mode.
			// Systemd sets INVOCATION_ID. Terminal provides an interactive TTY.
			// SSH deployer provides piped stdin/stdout and no INVOCATION_ID.
			isSystemd := os.Getenv("INVOCATION_ID") != ""
			stat, _ := os.Stdin.Stat()
			isTerminal := (stat.Mode() & os.ModeCharDevice) != 0

			isPersistent := isSystemd || isTerminal
			runFleetNode(ctx, nodeID, plugins, cfg, isPersistent)
		},
	}

	bridgeCmd := &cobra.Command{
		Use:   "bridge",
		Short: "Run as a lightweight foreground bridge node (no diagnostic tools)",
		Run: func(cmd *cobra.Command, args []string) {
			ctx, cancel, nodeID, cfg, plugins := initEnv(configPath)
			defer cancel()
			lifecycle.SetLiveMode(true)
			runBridgeNode(ctx, nodeID, plugins, cfg)
		},
	}

	uninstallCmd := &cobra.Command{
		Use:   "uninstall [target]",
		Short: "Remove nodes (ephemeral or persistent)",
		Args:  cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			if err := promptConfirmation("uninstall", os.Stdin, os.Stderr); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			ctx, cancel, _, cfg, _ := initEnv(configPath)
			defer cancel()
			target := ""
			if len(args) > 0 {
				target = args[0]
			}
			uninstallFleet(ctx, cfg, target)
		},
	}
	uninstallCmd.Flags().BoolVar(&forceFlag, "force", false, "Bypass confirmation prompt")

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
				ops = lifecycle.DetachedUninstallOps()
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

	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Print the application version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(version.ApplicationVersion)
		},
	}

	// reinstallCmd removed: use install --force instead

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
			if err := promptConfirmation("install", os.Stdin, os.Stderr); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			ctx, cancel, nodeID, cfg, plugins := initEnv(configPath)
			defer cancel()
			target := ""
			if len(args) > 0 {
				target = args[0]
			}
			opts := GatewayOptions{
				Install:        true,
				Target:         target,
				Force:          forceFlag,
				RegenerateKeys: regenerateKeysFlag,
			}
			runGateway(ctx, cancel, nodeID, cfg, plugins, skipDeploy, opts)
		},
	}
	installCmd.Flags().BoolVar(&forceFlag, "force", false, "Bypass confirmation prompt and already-installed checks")
	installCmd.Flags().BoolVar(&regenerateKeysFlag, "regenerate-keys", false, "Force a clean wipe and generate new mTLS keys for the node")

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
			defer fmt.Fprintln(os.Stderr) // Print newline to cleanly drop the shell prompt on exit
			opts := GatewayOptions{
				PureClient: true,
				ServeHTTP:  addr,
			}
			runGateway(ctx, cancel, nodeID, cfg, plugins, skipDeploy, opts)
		},
	}

	rootCmd.AddCommand(daemonCmd, bridgeCmd, uninstallCmd, startCmd, stopCmd, installCmd, mcpCmd, buildImportExaCmd(), localOpCmd, versionCmd)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// initEnv bootstraps the common context, configuration, nodeID, tool definitions,
// and the plugin registry. The plugin registry evaluates each tool's IsSupported()
// against the local environment — unsupported tools are logged and excluded.
func initEnv(configPath string) (context.Context, context.CancelFunc, string, *config.MeshConfig, *registry.PluginRegistry) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
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
		Name:            "get_mesh_topology",
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

	var isInstall bool
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
		isInstall = modeBuf[0] == 0x01

		// Read stripped config bytes from stdin.
		configBytes, err := readField(os.Stdin)
		if err != nil {
			zap.S().Errorw("read config payload", "error", err)
			os.Exit(1)
		}

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
			if err := SaveConfig(configBytes); err != nil {
				zap.S().Errorw("save config failed", "error", err)
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

	reconnectPolicy := api.ReconnectPolicy{
		Enabled:      true,
		InitialDelay: 1 * time.Second,
		MaxDelay:     30 * time.Second,
		Timeout:      0,
		MaxAttempts:  0,
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

	var nodePtr *api.Node
	dialer := createResilientDialer(cfg, v, &nodePtr)

	// Create the mesh node with membrane config.
	node, err := api.NewNode(ctx, api.NodeConfig{
		NodeID:             nodeID,
		ProtocolVersion:    1,
		ApplicationVersion: version.ApplicationVersion,
		Vault:              v, // Empty vault enables fleet nodes to request credentials for node_deploy
		Dialer:             dialer,
		KnownHosts:         cfg.KnownHosts(),
		Reconnect:          reconnectPolicy,
		Events: api.NodeEvents{
			OnPeerJoined:  func(peerID string) { zap.S().Infow("fleet: peer joined", "peer", peerID) },
			OnPeerLost:    func(peerID string) { zap.S().Infow("fleet: peer lost", "peer", peerID) },
			OnIsolated:    func() { zap.S().Warnw("fleet: isolated — zero peers") },
			OnReconnected: func(peerID string) { zap.S().Debugw("fleet: reconnected!", "peer", peerID) },
			OnOrphaned: func() {
				zap.S().Warnw("fleet: orphaned — reconnect exhausted")
			},
		},
	})
	if err != nil {
		zap.S().Errorw("create fleet node", "error", err)
		os.Exit(1)
	}
	nodePtr = node
	node.SetMembraneConfig(membraneCfg)

	// Register configured proxies as capabilities.
	for _, p := range cfg.Proxies {
		node.RegisterCapability(fmt.Sprintf("proxy:%s=%s", p.Pattern, p.URL))
	}

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
			go func() {
				<-ctx.Done()
				_ = tcpLis.Close()
			}()
		}
	}

	if isDaemon {
		go func() {
			// Give the mesh time to initialize before proactively probing seeds
			time.Sleep(3 * time.Second)
			zap.S().Infow("fleet node fanning out proactive peer connections")
			runConnectionReconciler(ctx, node, nil, cfg, v, true)
		}()
	}

	go func() {
		<-ctx.Done()
		_ = node.Close()
	}()

	zap.S().Infow("fleet node ready — serving tools", "node_id", nodeID, "tools", len(meshReg.ListLocal()))
	ServeMultiplexedListener(ctx, lis, meshReg)
}

// runBridgeNode handles a lightweight foreground bridge node (no diagnostic tools).
func runBridgeNode(ctx context.Context, nodeID string, plugins *registry.PluginRegistry, cfg *config.MeshConfig) {
	zap.S().Infow("started cortex-mcp bridge node", "node_id", nodeID)

	pki, _, err := loadOrGeneratePKI()
	if err != nil {
		zap.S().Fatalw("load or generate PKI failed", "error", err)
	}
	bridgeCert, err := pki.generateNodeCert(nodeID, false)
	if err != nil {
		zap.S().Fatalw("generate node cert failed", "error", err)
	}

	_, ok := bridgeCert.PrivateKey.(ed25519.PrivateKey)
	if !ok {
		zap.S().Fatalw("bridge private key is not ed25519")
	}

	v, err := initVault(cfg)
	if err != nil {
		zap.S().Fatalw("init vault failed", "error", err)
	}

	reconnectPolicy := api.ReconnectPolicy{
		Enabled:      true,
		InitialDelay: 1 * time.Second,
		MaxDelay:     30 * time.Second,
		Timeout:      0,
		MaxAttempts:  0,
	}

	var nodePtr *api.Node
	dialer := createResilientDialer(cfg, v, &nodePtr)

	node, err := api.NewNode(ctx, api.NodeConfig{
		NodeID:             nodeID,
		ProtocolVersion:    1,
		ApplicationVersion: version.ApplicationVersion,
		Vault:              v,
		Dialer:             dialer,
		KnownHosts:         cfg.KnownHosts(),
		Reconnect:          reconnectPolicy,
		Events: api.NodeEvents{
			OnPeerJoined:  func(peerID string) { zap.S().Infow("bridge: peer joined", "peer", peerID) },
			OnPeerLost:    func(peerID string) { zap.S().Infow("bridge: peer lost", "peer", peerID) },
			OnIsolated:    func() { zap.S().Warnw("bridge: isolated — zero peers") },
			OnReconnected: func(peerID string) { zap.S().Debugw("bridge: reconnected!", "peer", peerID) },
			OnOrphaned: func() {
				zap.S().Warnw("bridge: orphaned — reconnect exhausted")
			},
		},
	})
	if err != nil {
		zap.S().Fatalw("create bridge node failed", "error", err)
	}
	nodePtr = node
	defer func() {
		if err := node.Close(); err != nil {
			zap.S().Debugw("close bridge node", "error", err)
		}
	}()

	node.SetMembraneConfig(pki.membraneConfig(bridgeCert))

	for _, p := range cfg.Proxies {
		node.RegisterCapability(fmt.Sprintf("proxy:%s=%s", p.Pattern, p.URL))
	}

	meshReg := tools.NewRegistry(node)
	registerNodeTools(meshReg, node)

	node.StartGossipTicker(ctx, node.GossipIntervalDuration())

	lis, err := node.GrpcListener()
	if err != nil {
		zap.S().Fatalw("grpc listener failed", "error", err)
	}

	listenAddr := fmt.Sprintf("0.0.0.0:%d", cfg.Node.MeshPort)
	tcpLis, err := node.Listen(ctx, listenAddr)
	if err != nil {
		zap.S().Errorw("bridge listen failed", "addr", listenAddr, "error", err)
	} else {
		zap.S().Infow("bridge listening on TCP", "addr", tcpLis.Addr())
		go func() {
			<-ctx.Done()
			_ = tcpLis.Close()
		}()
	}

	go func() {
		time.Sleep(1 * time.Second)
		_ = deployAndConnect(ctx, node, pki, cfg, v, true, false, false, false, nil, false, false)
	}()

	go func() {
		<-ctx.Done()
		_ = node.Close()
	}()

	zap.S().Infow("bridge node ready — serving lifecycle tools", "node_id", nodeID, "tools", len(meshReg.ListLocal()))
	ServeMultiplexedListener(ctx, lis, meshReg)
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

	_, ok := gatewayCert.PrivateKey.(ed25519.PrivateKey)
	if !ok {
		zap.S().Fatalw("gateway private key is not ed25519")
	}

	// Initialize the credential vault.
	v, err := initVault(cfg)
	if err != nil {
		zap.S().Fatalw("init vault failed", "error", err)
	}

	zap.S().Infow("mesh PKI initialized", "node_id", nodeID, "new_ca", isNew)

	var nodePtr *api.Node
	dialer := createResilientDialer(cfg, v, &nodePtr)

	// --- Phase 2: Create mesh node ---
	node, err := api.NewNode(ctx, api.NodeConfig{
		NodeID:             nodeID,
		ProtocolVersion:    1,
		ApplicationVersion: version.ApplicationVersion,
		Vault:              v,
		Dialer:             dialer,
		KnownHosts:         cfg.KnownHosts(),
		GossipInterval:     3 * time.Second, // configurable: 3s for HPC, 10s for WAN
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
				zap.S().Debugw("mesh reconnected", "peer_id", peerID)
			},
			OnOrphaned: func() {
				zap.S().Warnw("mesh orphaned", "action", "keep-alive")
			},
		},
		Reconnect: api.ReconnectPolicy{
			Enabled:      true,
			InitialDelay: 1 * time.Second,
			MaxDelay:     30 * time.Second,
			Timeout:      0,
			MaxAttempts:  0,
		},
	})
	if err != nil {
		zap.S().Fatalw("create mesh node failed", "error", err)
	}
	nodePtr = node
	defer func() {
		if err := node.Close(); err != nil {
			zap.S().Debugw("close node", "error", err)
		}
	}()
	node.SetMembraneConfig(pki.membraneConfig(gatewayCert))

	// Register configured proxies as capabilities.
	for _, p := range cfg.Proxies {
		node.RegisterCapability(fmt.Sprintf("proxy:%s=%s", p.Pattern, p.URL))
	}

	// Register tools with capability advertising (unless PureClient mode).
	meshReg := tools.NewRegistry(node)
	if !opts.PureClient {
		plugins.BridgeToMesh(meshReg)
		registerNodeTools(meshReg, node)
	}

	zap.S().Infow("node created", "node_id", nodeID, "caps", len(meshReg.ListLocal()), "reconnect_policy", "1s->30s")

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

	var deployedNodes []deployedNode
	if install || opts.Stop {
		zap.S().Infow("deploying to mesh seed hosts (foreground mode)")
		deployedNodes = deployAndConnect(ctx, node, pki, cfg, v, skipDeploy, false, install, false, nil, opts.Force, opts.RegenerateKeys)
		if len(deployedNodes) == 0 {
			zap.S().Errorw("no remote nodes successfully joined - check credentials")
			return
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
			time.Sleep(500 * time.Millisecond)
			return
		}

	}

	// --- Phase 6: Start gossip ---
	gossipInterval := node.GossipIntervalDuration()
	node.StartGossipTicker(ctx, gossipInterval)
	zap.S().Infow("gossip ticker started", "interval", gossipInterval)

	zap.S().Infow("waiting for gossip convergence")
	time.Sleep(gossipInterval + 1*time.Second)
	zap.S().Infow("gossip converged", "peers", node.PeerCount())

	if install {
		zap.S().Infow("installation and health-check complete")
		return
	}

	// Launch background connection reconciler to maintain fleet connectivity
	zap.S().Infow("launching background connection reconciler")
	go runConnectionReconciler(ctx, node, pki, cfg, v, skipDeploy)

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

	// Cleanup is handled by defer blocks.
}

// nodeBackoff tracks exponential backoff for indirect route dialing.
type nodeBackoff struct {
	nextAttempt time.Time
	interval    time.Duration
}

// runConnectionReconciler periodically ensures connectivity to configured hosts via exponential backoff.
func runConnectionReconciler(ctx context.Context, node *api.Node, pki *ephemeralPKI, cfg *config.MeshConfig, v *vault.Vault, skipDeploy bool) {
	zap.S().Infow("reconciler started: ensuring mesh connectivity")

	indirectBackoffs := make(map[string]*nodeBackoff)

	// Trigger an initial, immediate reconciliation round.
	deployAndConnect(ctx, node, pki, cfg, v, skipDeploy, false, false, true, indirectBackoffs, false, false)

	// Backoff configuration
	baseInterval := 5 * time.Second
	maxInterval := 60 * time.Second
	currentInterval := baseInterval

	timer := time.NewTimer(currentInterval)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			zap.S().Infow("reconciler shutting down")
			return
		case <-timer.C:
			zap.S().Debugw("reconciler round starting")
			nodesConnected := deployAndConnect(ctx, node, pki, cfg, v, skipDeploy, false, false, true, indirectBackoffs, false, false)

			// Exponential backoff logic: reset if we made a connection, back off if we didn't.
			if len(nodesConnected) > 0 {
				zap.S().Infow("reconciler successfully connected to nodes", "count", len(nodesConnected))
				currentInterval = baseInterval
			} else {
				currentInterval *= 2
				if currentInterval > maxInterval {
					currentInterval = maxInterval
				}
			}

			zap.S().Debugw("reconciler round complete", "next_interval", currentInterval)
			timer.Reset(currentInterval)
		}
	}
}

// deployedNode tracks a deployed remote node.
type deployedNode struct {
	nodeID string
	conn   net.Conn
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
func deployAndConnect(ctx context.Context, node *api.Node, pki *ephemeralPKI, cfg *config.MeshConfig, v *vault.Vault, skipDeploy, daemon, install, reconcileMode bool, indirectBackoffs map[string]*nodeBackoff, force, regenerateKeys bool) []deployedNode {
	// Grab current topology snapshot to prevent dialing known hosts
	topology := node.MeshTopology()
	isDirect := make(map[string]bool)
	isIndirect := make(map[string]bool)
	for _, n := range topology.NodeDetails {
		if n.IsDirect {
			isDirect[n.NodeID] = true
		} else {
			isIndirect[n.NodeID] = true
		}
	}

	var deployed []deployedNode
	deployer := &transport.SelfDeployer{
		RemotePath: fmt.Sprintf("/tmp/cortex-mcp-deploy-%d", time.Now().UnixNano()),
		SkipUpload: skipDeploy,
	}

	if daemon {
		deployer.ExecArgs = []string{"-daemon"}
	}

	for remoteNodeID, hostCfg := range cfg.Hosts {
		if reconcileMode {
			if isDirect[remoteNodeID] {
				zap.S().Debugw("node directly active in topology, skipping dial", "node_id", remoteNodeID)
				continue
			}
			if isIndirect[remoteNodeID] && indirectBackoffs != nil {
				backoff, ok := indirectBackoffs[remoteNodeID]
				if !ok {
					backoff = &nodeBackoff{
						interval: 1 * time.Minute,
					}
					indirectBackoffs[remoteNodeID] = backoff
				}
				if time.Now().Before(backoff.nextAttempt) {
					zap.S().Debugw("node indirectly active, backoff active, skipping dial", "node_id", remoteNodeID)
					continue
				}
				// Dialing this round; increase backoff for next time if it fails
				backoff.nextAttempt = time.Now().Add(backoff.interval)
				backoff.interval *= 2
				if backoff.interval > 1*time.Hour {
					backoff.interval = 1 * time.Hour
				}
			}
		}

		if len(hostCfg.Addresses) == 0 {
			zap.S().Warnw("no addresses configured for node", "node_id", remoteNodeID)
			continue
		}

		var sshAddr, meshAddr, hostStr string
		var deployCred transport.DeployCredential
		var conn net.Conn
		var err error

		// Set default values based on the first address (used if all dial attempts fail)
		baseAddr0 := hostCfg.Addresses[0]
		hostStr, portStr0, splitErr0 := net.SplitHostPort(baseAddr0)
		if splitErr0 != nil {
			hostStr = baseAddr0
			portStr0 = ""
		}
		sshPort0 := hostCfg.GetSSHPort(cfg.Node.SSHPort)
		meshPort0 := hostCfg.GetMeshPort(cfg.Node.MeshPort)
		if portStr0 != "" {
			sshAddr = baseAddr0
			meshAddr = fmt.Sprintf("%s:%d", hostStr, meshPort0)
		} else {
			sshAddr = fmt.Sprintf("%s:%d", hostStr, sshPort0)
			meshAddr = fmt.Sprintf("%s:%d", hostStr, meshPort0)
		}

		cred, ok := v.Match(sshAddr)
		if !ok {
			cred, ok = v.Match(hostStr)
		}
		if ok {
			deployCred, _ = toDeployCredential(cred)
		}

		dialer := createResilientDialer(cfg, v, &node)
		var dialErrs []string

		for _, baseAddr := range hostCfg.Addresses {
			var currentSSHAddr, currentMeshAddr, currentHostStr string
			var currentDeployCred transport.DeployCredential

			currentHostStr, currentPortStr, splitErr := net.SplitHostPort(baseAddr)
			if splitErr != nil {
				currentHostStr = baseAddr
				currentPortStr = ""
			}

			sshPort := hostCfg.GetSSHPort(cfg.Node.SSHPort)
			meshPort := hostCfg.GetMeshPort(cfg.Node.MeshPort)

			if currentPortStr != "" {
				currentSSHAddr = baseAddr
				currentMeshAddr = fmt.Sprintf("%s:%d", currentHostStr, meshPort)
			} else {
				currentSSHAddr = fmt.Sprintf("%s:%d", currentHostStr, sshPort)
				currentMeshAddr = fmt.Sprintf("%s:%d", currentHostStr, meshPort)
			}

			currentCred, ok := v.Match(currentSSHAddr)
			if !ok {
				currentCred, ok = v.Match(currentHostStr)
			}
			if !ok {
				if install {
					zap.S().Warnw("no matching credentials in vault for install", "node_id", remoteNodeID, "addr", currentSSHAddr)
				} else {
					zap.S().Debugw("no matching credentials in vault, skipping ssh tunnel for dialing", "node_id", remoteNodeID, "addr", currentSSHAddr)
				}
			} else {
				var credErr error
				currentDeployCred, credErr = toDeployCredential(currentCred)
				if credErr != nil {
					zap.S().Errorw("invalid credentials", "node_id", remoteNodeID, "addr", currentSSHAddr, "error", credErr)
				}
			}

			c, dialErr := dialer(ctx, nucleus.DialTarget{Hostname: remoteNodeID, Address: currentMeshAddr})
			if dialErr == nil {
				conn = c
				err = nil
				sshAddr = currentSSHAddr
				meshAddr = currentMeshAddr
				deployCred = currentDeployCred
				break
			}
			dialErrs = append(dialErrs, fmt.Sprintf("%s=%v", baseAddr, dialErr))
		}

		if conn == nil {
			err = fmt.Errorf("all addresses failed: %s", strings.Join(dialErrs, "; "))
		}

		if err == nil {
			if install && !force {
				zap.S().Errorw("node already installed and running, use --force to overwrite", "node_id", remoteNodeID)
				_ = conn.Close()
				continue
			}

			if install && force && regenerateKeys {
				zap.S().Infow("node already installed, but --regenerate-keys requested. forcing fresh deploy", "node_id", remoteNodeID)
				_ = conn.Close()
				err = fmt.Errorf("forced key regeneration") // trigger fallthrough to Path 3
			}
		}

		if err == nil {
			if install && force && !regenerateKeys {
				_ = conn.Close()
				zap.S().Infow("forcing reinstall of remote node (preserving keys)", "node_id", remoteNodeID, "addr", sshAddr)
				remotePath := lifecycle.InstallRemotePath("")
				if err := upgradeRemoteNode(ctx, sshAddr, deployCred, remotePath, cfg); err != nil {
					zap.S().Errorw("upgrade failed", "node_id", remoteNodeID, "error", err)
					continue
				}
				time.Sleep(upgradeWaitAfterRestart)

				// Redial after upgrade
				conn, err = dialer(ctx, nucleus.DialTarget{Hostname: remoteNodeID, Address: meshAddr})
				if err != nil {
					zap.S().Errorw("reconnect after upgrade failed", "node_id", remoteNodeID, "error", err)
					continue
				}
			}

			if err := node.AddPeer(ctx, conn, false); err != nil {
				zap.S().Errorw("mTLS membrane handshake failed", "node_id", remoteNodeID, "error", err)
				_ = conn.Close()
				continue
			}

			zap.S().Debugw("connected to mesh peer", "node_id", remoteNodeID)
			if indirectBackoffs != nil {
				delete(indirectBackoffs, remoteNodeID) // reset backoff on success
			}
			deployed = append(deployed, deployedNode{
				nodeID: remoteNodeID,
				conn:   conn,
			})

			if !skipDeploy {
				go checkAndUpgradePeer(ctx, node, remoteNodeID)
			}
			continue
		}

		// --- Path 3: No existing node → fresh deploy ---
		if pki == nil || !install {
			zap.S().Debugw("all connection paths failed", "node_id", remoteNodeID, "error", err)
			continue
		}

		zap.S().Debugw("all connection paths failed, attempting deployment", "node_id", remoteNodeID, "error", err)

		if !force {
			// Quick check: does the node have the binary already but the service is just stopped?
			remoteVersion, verErr := getRemoteApplicationVersion(ctx, sshAddr, deployCred, lifecycle.InstallRemotePath(""))
			if verErr == nil && remoteVersion != "" {
				zap.S().Warnw("node already installed but stopped, use start command or --force to overwrite", "node_id", remoteNodeID)
				continue
			}
		}

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

		// Strip credentials and serialize config
		strippedCfg := cfg.Stripped()
		var configBuf bytes.Buffer
		if err := toml.NewEncoder(&configBuf).Encode(strippedCfg); err != nil {
			zap.S().Errorw("serialize stripped config failed", "node_id", remoteNodeID, "error", err)
			_ = stream.Close()
			continue
		}

		if err := writeField(stream, configBuf.Bytes()); err != nil {
			zap.S().Errorw("send config failed", "node_id", remoteNodeID, "error", err)
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

			// Dial the newly spawned daemon using the resilient dialer (with retries for boot-up time)
			var conn net.Conn
			var dialErr error
			for i := 0; i < 30; i++ {
				conn, dialErr = dialer(ctx, nucleus.DialTarget{Hostname: remoteNodeID, Address: meshAddr})
				if dialErr == nil {
					break
				}
				time.Sleep(500 * time.Millisecond)
			}

			if dialErr != nil {
				zap.S().Errorw("mesh connect after install failed", "node_id", remoteNodeID, "error", dialErr)
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

// runGatewayTopology invokes the get_mesh_topology tool via the gateway and pretty-prints the output.

// fanOutDirect invokes the "get_system_info" tool on all deployed nodes concurrently.

// loadConfig finds and loads mesh.toml from the given path or default locations.
func loadConfig(path string) (*config.MeshConfig, error) {
	if path != "" {
		return config.Load(path)
	}

	defaults := []string{
		"mesh.toml",
		"/opt/cortex-mcp/etc/mesh.toml",
		"/opt/cortex-mcp/bin/mesh.toml",
	}
	if exe, err := os.Executable(); err == nil {
		defaults = append(defaults, filepath.Join(filepath.Dir(exe), "mesh.toml"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		defaults = append(defaults, filepath.Join(home, ".cortex-mcp", "mesh.toml"))
	}

	var unique []string
	seen := make(map[string]bool)
	for _, p := range defaults {
		if !seen[p] {
			seen[p] = true
			unique = append(unique, p)
		}
	}

	for _, p := range unique {
		if _, err := os.Stat(p); err == nil {
			return config.Load(p)
		}
	}

	return nil, fmt.Errorf("mesh.toml not found (tried: %v)", unique)
}

// uninstallFleet uses a two-phase process to safely dismantle the mesh:
//  1. Mesh Uninstall (Furthest-First): Joins the mesh, queries the topology, and signals
//     the highest-impedance nodes to uninstall themselves over the mesh.
//  2. SSH Fallback: Uses the transport.SelfDeployer to SSH and uninstall any nodes
//     that were unreachable or failed the mesh uninstallation.
func uninstallFleet(ctx context.Context, cfg *config.MeshConfig, target string) {
	pki, _, err := loadOrGeneratePKI()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  ✗ failed to load PKI: %v\n", err)
		return
	}

	bridgeCert, err := pki.generateNodeCert("uninstall-cli", false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  ✗ failed to generate cert: %v\n", err)
		return
	}

	v, err := initVault(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  ✗ failed to init vault: %v\n", err)
		return
	}

	if err := cfg.LoadCredentials(v); err != nil {
		zap.S().Warnw("some credentials failed to load", "error", err)
	}

	reconnectPolicy := api.ReconnectPolicy{
		Enabled:      true,
		InitialDelay: 100 * time.Millisecond,
		MaxDelay:     1 * time.Second,
	}
	var nodePtr *api.Node
	dialer := createResilientDialer(cfg, v, &nodePtr)

	node, err := api.NewNode(ctx, api.NodeConfig{
		NodeID:             "uninstall-cli",
		ProtocolVersion:    1,
		ApplicationVersion: version.ApplicationVersion,
		Vault:              v,
		Dialer:             dialer,
		KnownHosts:         cfg.KnownHosts(),
		Reconnect:          reconnectPolicy,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "  ✗ failed to create ephemeral mesh node: %v\n", err)
		return
	}
	nodePtr = node
	node.SetMembraneConfig(pki.membraneConfig(bridgeCert))
	node.StartGossipTicker(ctx, node.GossipIntervalDuration())

	// Connect to known hosts so we join the mesh and discover topology
	_ = deployAndConnect(ctx, node, pki, cfg, v, true, false, false, false, nil, false, false)
	time.Sleep(2 * time.Second)

	snapshot := node.MeshTopology()
	entries := snapshot.NodeDetails

	// Sort by Impedance DESCENDING (furthest first)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Impedance > entries[j].Impedance
	})

	meshUninstalled := make(map[string]bool)
	bridge := tools.NewNeuronBridge(node)
	invoker := tools.NewRemoteInvoker(bridge)

	for _, entry := range entries {
		if entry.NodeID == "uninstall-cli" {
			continue
		}
		if target != "" && entry.NodeID != target {
			continue
		}
		if _, ok := cfg.Hosts[entry.NodeID]; !ok {
			continue // Only uninstall nodes that belong to the fleet
		}

		fmt.Fprintf(os.Stderr, "  → %s: uninstalling via mesh...", entry.NodeID)

		// Invoke the node_uninstall tool over the mesh
		_, invokeErr := invoker.Invoke(ctx, entry.NodeID, "node_uninstall", nil)
		if invokeErr != nil {
			fmt.Fprintf(os.Stderr, " ✗ Invoke: %v\n", invokeErr)
			continue
		}

		fmt.Fprintf(os.Stderr, " ✓ removed\n")
		meshUninstalled[entry.NodeID] = true

		// Brief pause to allow the node to cleanly process the shutdown
		time.Sleep(200 * time.Millisecond)
	}

	_ = node.Close()

	// Phase 2: SSH Fallback
	for remoteNodeID, hostCfg := range cfg.Hosts {
		if target != "" && remoteNodeID != target {
			continue
		}
		if meshUninstalled[remoteNodeID] {
			continue // Successfully uninstalled via mesh
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

		fmt.Fprintf(os.Stderr, "  → %s (%s): uninstalling via SSH...", remoteNodeID, sshAddr)

		deployer := &transport.SelfDeployer{
			RemotePath: fmt.Sprintf("/tmp/cortex-mcp-uninstall-%d", time.Now().UnixNano()),

			ExecArgs:    []string{"local-op", "uninstall"},
			NoWaitReady: true,
		}
		stream, err := deployer.Deploy(ctx, sshAddr, deployCred, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, " ✗ Deploy: %v\n", err)
			continue
		}

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

// createResilientDialer creates a custom Dialer for cortex-mcp that implements
// the TCP -> Proxy -> SSH Tunnel fallback chain.
func createResilientDialer(cfg *config.MeshConfig, v *vault.Vault, nodePtr **api.Node) func(ctx context.Context, target nucleus.DialTarget) (net.Conn, error) {
	return func(ctx context.Context, target nucleus.DialTarget) (net.Conn, error) {
		hostStr, _, splitErr := net.SplitHostPort(target.Address)
		if splitErr != nil {
			hostStr = target.Address
		}

		var errs []string

		// 1. Direct TCP
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", target.Address)
		if err == nil {
			zap.S().Debugw("dialer: direct TCP success", "target", target.Hostname, "addr", target.Address)
			return conn, nil
		}
		errs = append(errs, fmt.Sprintf("tcp=%v", err))

		// 2. HTTP CONNECT Proxy (via matched config patterns)
		for _, p := range cfg.Proxies {
			matched, _ := filepath.Match(p.Pattern, hostStr)
			if matched {
				conn, err := transport.DialProxy(ctx, p.URL, target.Address)
				if err == nil {
					zap.S().Debugw("dialer: proxy success", "target", target.Hostname, "proxy", p.URL)
					return conn, nil
				}
				errs = append(errs, fmt.Sprintf("proxy(%s)=%v", p.URL, err))
			}
		}

		// 3. Gossiped Proxies (Look up capabilities "proxy:*")
		if nodePtr != nil && *nodePtr != nil {
			node := *nodePtr
			snap := node.CapabilityIndex().Snapshot()
			for _, caps := range snap {
				for _, c := range caps {
					if strings.HasPrefix(c, "proxy:") {
						parts := strings.SplitN(strings.TrimPrefix(c, "proxy:"), "=", 2)
						if len(parts) == 2 {
							pattern, url := parts[0], parts[1]
							matched, _ := filepath.Match(pattern, hostStr)
							if matched {
								conn, err := transport.DialProxy(ctx, url, target.Address)
								if err == nil {
									zap.S().Debugw("dialer: gossiped proxy success", "target", target.Hostname, "proxy", url)
									return conn, nil
								}
								errs = append(errs, fmt.Sprintf("gossip_proxy(%s)=%v", url, err))
							}
						}
					}
				}
			}
		}

		// 4. SSH Port Forwarding
		cred, ok := v.Match(hostStr)
		if ok {
			deployCred, err := toDeployCredential(cred)
			if err == nil {
				// Use the SSH port from host config if defined, else fallback to global SSH port
				sshPort := cfg.Node.SSHPort
				if hCfg, found := cfg.Hosts[target.Hostname]; found && hCfg.SSHPort > 0 {
					sshPort = hCfg.SSHPort
				}
				sshAddr := fmt.Sprintf("%s:%d", hostStr, sshPort)

				sshConf := &ssh.ClientConfig{
					User:            deployCred.SSHUser,
					Auth:            []ssh.AuthMethod{},
					HostKeyCallback: ssh.InsecureIgnoreHostKey(),
					Timeout:         10 * time.Second,
				}
				if deployCred.SSHKeyData != nil {
					sshConf.Auth = append(sshConf.Auth, ssh.PublicKeys(deployCred.SSHKeyData))
				} else if deployCred.SSHPass != "" {
					sshConf.Auth = append(sshConf.Auth, ssh.Password(deployCred.SSHPass))
				}

				conn, err := transport.DialSSHTunnel(ctx, sshAddr, target.Address, sshConf)
				if err == nil {
					zap.S().Debugw("dialer: SSH tunnel success", "target", target.Hostname, "ssh_addr", sshAddr)
					return conn, nil
				}
				errs = append(errs, fmt.Sprintf("ssh=%v", err))
			} else {
				errs = append(errs, fmt.Sprintf("ssh_cred_err=%v", err))
			}
		} else {
			errs = append(errs, "ssh=no_creds_in_vault")
		}

		return nil, fmt.Errorf("all dialing methods failed for %s: %s", target.Hostname, strings.Join(errs, ", "))
	}
}

func checkAndUpgradePeer(ctx context.Context, node *api.Node, remoteNodeID string) {
	// Give the yamux session a moment to settle
	time.Sleep(500 * time.Millisecond)

	// 1. Dial the remote node over the mesh
	stream, err := node.GrpcDialer(ctx, remoteNodeID)
	if err != nil {
		zap.S().Debugw("auto upgrade: failed to dial peer", "node_id", remoteNodeID, "error", err)
		return
	}
	defer func() { _ = stream.Close() }()

	// 2. Invoke get_system_info
	res, err := tools.DialInvoke(ctx, stream, "get_system_info", nil)
	if err != nil {
		zap.S().Debugw("auto upgrade: failed to invoke system_info", "node_id", remoteNodeID, "error", err)
		return
	}

	if res.IsError {
		zap.S().Debugw("auto upgrade: system_info returned error", "node_id", remoteNodeID, "error", string(res.Content))
		return
	}

	// 3. Parse version
	var envelope struct {
		Data struct {
			ApplicationVersion string `json:"application_version"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Content, &envelope); err != nil {
		zap.S().Errorw("auto upgrade: failed to unmarshal system_info", "node_id", remoteNodeID, "error", err)
		return
	}

	localVer, errL := strconv.ParseInt(version.ApplicationVersion, 10, 64)
	remoteVer, errR := strconv.ParseInt(envelope.Data.ApplicationVersion, 10, 64)

	if errL != nil || errR != nil {
		zap.S().Errorw("auto upgrade: failed to parse version", "node_id", remoteNodeID, "local_version", version.ApplicationVersion, "remote_version", envelope.Data.ApplicationVersion, "err_local", errL, "err_remote", errR)
		return
	}

	if localVer <= remoteVer {
		// Peer is up to date or newer
		zap.S().Debugw("auto upgrade: skipping, peer is up to date", "node_id", remoteNodeID, "local_version", localVer, "remote_version", remoteVer)
		return
	}

	zap.S().Infow("auto upgrade initiated", "node_id", remoteNodeID, "local_version", localVer, "remote_version", remoteVer)

	// 4. Stream binary
	binStream, err := node.GrpcDialer(ctx, remoteNodeID)
	if err != nil {
		zap.S().Errorw("auto upgrade: failed to dial for binary upload", "node_id", remoteNodeID, "error", err)
		return
	}

	exePath, err := os.Executable()
	if err != nil {
		zap.S().Errorw("auto upgrade: failed to get executable path", "error", err)
		_ = binStream.Close()
		return
	}

	if err := DialUploadBinary(ctx, binStream, exePath); err != nil {
		zap.S().Errorw("auto upgrade: binary upload failed", "node_id", remoteNodeID, "error", err)
		return
	}

	// 5. Invoke node_upgrade
	upgradeStream, err := node.GrpcDialer(ctx, remoteNodeID)
	if err != nil {
		zap.S().Errorw("auto upgrade: failed to dial for upgrade invocation", "node_id", remoteNodeID, "error", err)
		return
	}
	defer func() { _ = upgradeStream.Close() }()

	args := []byte(fmt.Sprintf(`{"path":"%s"}`, DefaultUpdatePath))
	upgRes, err := tools.DialInvoke(ctx, upgradeStream, "node_upgrade", args)
	if err != nil || upgRes.IsError {
		zap.S().Errorw("auto upgrade: node_upgrade invocation failed", "node_id", remoteNodeID, "error", err, "res", string(upgRes.Content))
	} else {
		zap.S().Infow("auto upgrade: remote node scheduled for restart", "node_id", remoteNodeID)
	}
}
