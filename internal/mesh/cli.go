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
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"go.uber.org/zap"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cortex-mesh/cortex-mesh/api"
	"github.com/cortex-mesh/cortex-mesh/gateway"
	"github.com/cortex-mesh/cortex-mesh/membrane"
	"github.com/cortex-mesh/cortex-mesh/tools"
	"github.com/cortex-mesh/cortex-mesh/transport"
	"github.com/cortex-mesh/cortex-mesh/vault"
	"github.com/flipfloptech/cortex-mcp/internal/config"
	"github.com/flipfloptech/cortex-mcp/internal/harness"
	"github.com/flipfloptech/cortex-mcp/internal/logger"
	"github.com/flipfloptech/cortex-mcp/internal/mcp"
	"github.com/flipfloptech/cortex-mcp/internal/registry"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"
)

// toolEntry pairs a definition with its handler for re-registration.
type toolEntry struct {
	def     tools.ToolDefinition
	handler tools.ToolHandler
}

// GatewayOptions contains operation modes for the bootstrap node.
type GatewayOptions struct {
	Install         bool
	Stop            bool
	Target          string
	PureClient      bool
	ServeHTTP       string // address to serve HTTP on
	HarnessType     string // "check", "soak", "deploy"
	HarnessCount    int
	HarnessDuration time.Duration
}

func Execute() {
	var configPath string
	var skipDeploy bool

	rootCmd := &cobra.Command{
		Use:   "cortex-mcp",
		Short: "Cortex MCP Application",
		Run: func(cmd *cobra.Command, args []string) {
			ctx, cancel, nodeID, cfg, entries, plugins := initEnv(configPath)
			defer cancel()
			runGateway(ctx, cancel, nodeID, cfg, entries, plugins, skipDeploy, GatewayOptions{})
		},
	}

	rootCmd.PersistentFlags().StringVar(&configPath, "config", "", "path to mesh.toml config file")
	rootCmd.PersistentFlags().BoolVar(&skipDeploy, "skip-deploy", false, "skip SFTP upload when deploying nodes")

	bridgeCmd := &cobra.Command{
		Use:   "bridge <addr>",
		Short: "Raw TCP bridge for firewall traversal",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			ctx, cancel, _, _, _, _ := initEnv(configPath)
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
			ctx, cancel, nodeID, cfg, entries, plugins := initEnv(configPath)
			defer cancel()
			liveMode = true
			runFleetNode(ctx, nodeID, entries, plugins, cfg, false)
		},
	}

	daemonCmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run as a persistent daemon (systemd entry)",
		Run: func(cmd *cobra.Command, args []string) {
			ctx, cancel, nodeID, cfg, entries, plugins := initEnv(configPath)
			defer cancel()
			liveMode = true
			runFleetNode(ctx, nodeID, entries, plugins, cfg, true)
		},
	}

	uninstallCmd := &cobra.Command{
		Use:   "uninstall [target]",
		Short: "Remove nodes (ephemeral or persistent)",
		Args:  cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			ctx, cancel, _, cfg, _, _ := initEnv(configPath)
			defer cancel()
			target := ""
			if len(args) > 0 {
				target = args[0]
			}
			uninstallFleet(ctx, cfg, target)
		},
	}

	startCmd := &cobra.Command{
		Use:   "start [target]",
		Short: "Start persistent services via SSH",
		Args:  cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			ctx, cancel, _, cfg, _, _ := initEnv(configPath)
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
			ctx, cancel, nodeID, cfg, entries, plugins := initEnv(configPath)
			defer cancel()
			target := ""
			if len(args) > 0 {
				target = args[0]
			}
			opts := GatewayOptions{Stop: true, Target: target}
			runGateway(ctx, cancel, nodeID, cfg, entries, plugins, skipDeploy, opts)
		},
	}

	installCmd := &cobra.Command{
		Use:   "install [target]",
		Short: "Persist nodes as systemd services",
		Args:  cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			ctx, cancel, nodeID, cfg, entries, plugins := initEnv(configPath)
			defer cancel()
			target := ""
			if len(args) > 0 {
				target = args[0]
			}
			opts := GatewayOptions{Install: true, Target: target}
			runGateway(ctx, cancel, nodeID, cfg, entries, plugins, skipDeploy, opts)
		},
	}

	mcpCmd := &cobra.Command{
		Use:   "mcp [addr]",
		Short: "Run as an mTLS HTTP MCP Server",
		Args:  cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			addr := "localhost:8080"
			if len(args) > 0 {
				addr = args[0]
			}
			ctx, cancel, nodeID, cfg, entries, plugins := initEnv(configPath)
			defer cancel()
			opts := GatewayOptions{PureClient: true, ServeHTTP: addr}
			runGateway(ctx, cancel, nodeID, cfg, entries, plugins, skipDeploy, opts)
		},
	}

	harnessCmd := &cobra.Command{
		Use:   "harness [type]",
		Short: "Run test harness (check, soak, deploy)",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			hType := args[0]
			if hType != "check" && hType != "soak" && hType != "deploy" {
				fmt.Fprintf(os.Stderr, "invalid harness type: %s\n", hType)
				os.Exit(1)
			}
			ctx, cancel, nodeID, cfg, entries, plugins := initEnv(configPath)
			defer cancel()

			// parse optional duration/count
			count, _ := cmd.Flags().GetInt("count")
			durStr, _ := cmd.Flags().GetString("duration")
			dur, _ := time.ParseDuration(durStr)

			opts := GatewayOptions{
				PureClient:      true, // Just be a client, let other nodes do the work
				HarnessType:     hType,
				HarnessCount:    count,
				HarnessDuration: dur,
			}
			// For deploy soak, we don't skip deploy initially, but we might want to let the harness control it.
			// Actually, deploy soak will deploy them inside the loop. Let's start with skipDeploy = true.

			runGateway(ctx, cancel, nodeID, cfg, entries, plugins, true, opts)
		},
	}
	harnessCmd.Flags().Int("count", 0, "Number of iterations (0 = infinite)")
	harnessCmd.Flags().String("duration", "0", "Duration of soak test (e.g. 1h, 30m, 0 = infinite)")

	rootCmd.AddCommand(bridgeCmd, serveCmd, daemonCmd, uninstallCmd, startCmd, stopCmd, installCmd, mcpCmd, harnessCmd)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// initEnv bootstraps the common context, configuration, nodeID, tool definitions,
// and the plugin registry. The plugin registry evaluates each tool's IsSupported()
// against the local environment — unsupported tools are logged and excluded.
func initEnv(configPath string) (context.Context, context.CancelFunc, string, *config.MeshConfig, []toolEntry, *registry.PluginRegistry) {
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
		cfg = &config.MeshConfig{}
	}

	nodeID := cfg.Node.ID
	if nodeID == "" {
		nodeID = os.Getenv("CORTEX_NODE_ID")
	}
	if nodeID == "" {
		hostname, _ := os.Hostname()
		nodeID = hostname
	}

	entries := defineTools(nodeID)

	// Build the plugin registry from globally registered tools.
	// Each tool's IsSupported() is evaluated against the local environment.
	plugins := registry.NewPluginRegistry(nodeID)
	for name, reason := range plugins.Unsupported() {
		zap.S().Infow("plugin skipped", "tool", name, "reason", reason)
	}
	zap.S().Infow("plugin registry loaded", "supported", len(plugins.Supported()), "skipped", len(plugins.Unsupported()))

	return ctx, cancel, nodeID, cfg, entries, plugins
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
						zap.S().Debugw("hello: unmarshal args", "error", err)
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
		{
			def: tools.ToolDefinition{
				Name:            "node_install",
				Description:     "Install this node as a persistent systemd service",
				LongDescription: "Copies the binary to /opt/cortex-mesh/bin/, writes a systemd unit, and enables/starts the service. Converts an ephemeral node into persistent infrastructure.",
				Category:        "lifecycle",
			},
			handler: handleNodeInstall,
		},
		{
			def: tools.ToolDefinition{
				Name:            "node_uninstall",
				Description:     "Remove this node — handles both persistent (systemd) and ephemeral (/tmp) nodes",
				LongDescription: "Detects whether the node is persistent or ephemeral. Persistent: stops/disables service, removes unit + binary. Ephemeral: removes the /tmp binary and exits.",
				Category:        "lifecycle",
			},
			handler: handleNodeUninstall,
		},
		{
			def: tools.ToolDefinition{
				Name:            "node_restart",
				Description:     "Restart the local cortex-mesh systemd service",
				LongDescription: "Runs systemctl restart cortex-mesh. Use after binary upgrades or configuration changes.",
				Category:        "lifecycle",
			},
			handler: handleNodeRestart,
		},
		{
			def: tools.ToolDefinition{
				Name:            "node_stop",
				Description:     "Stop the cortex-mesh systemd service without uninstalling",
				LongDescription: "Gracefully stops the service. The node remains installed and can be restarted. Use for maintenance windows.",
				Category:        "lifecycle",
			},
			handler: handleNodeStop,
		},
		{
			def: tools.ToolDefinition{
				Name:            "node_upgrade",
				Description:     "Upgrade the node binary and restart the service",
				LongDescription: "Copies a new binary from the specified path over the installed binary, reloads systemd, and restarts the service.",
				Category:        "lifecycle",
				Parameters: []tools.ToolParam{
					{Name: "path", Type: "string", Description: "Path to the new binary (e.g., /tmp/cortex-mesh-new)", Required: true},
				},
			},
			handler: handleNodeUpgrade,
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

	// Add dynamic tools based on environment detection
	entries = append(entries, detectTools(nodeID)...)

	return entries
}

// registerTools populates a registry with the given tool entries.
func registerTools(registry *tools.Registry, entries []toolEntry) {
	for _, e := range entries {
		registry.Register(e.def, e.handler)
	}
}

// registerNodeTools registers tools that depend on a live *api.Node instance.
// These can't go in defineTools() because the node hasn't been created yet.
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
		Parameters: []tools.ToolParam{
			{Name: "target", Type: "string", Description: "Target host address (e.g., 10.0.1.5 or host:port)", Required: true},
		},
	}, buildNodeDeployHandler(node))
}

// runFleetNode handles the deployed fleet node lifecycle.
func runFleetNode(ctx context.Context, nodeID string, entries []toolEntry, plugins *registry.PluginRegistry, cfg *config.MeshConfig, isDaemon bool) {
	zap.S().Infow("deployed fleet node — bootstrapping", "node_id", nodeID, "daemon", isDaemon)

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

			// ACK to gateway
			_, _ = os.Stdout.Write([]byte{'O', 'K', 0x00, 0x06})

			// Detach and fork with "daemon" subcommand
			exe, _ := os.Executable()
			cmd := exec.Command(exe, "daemon")
			cmd.Env = os.Environ()
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			if err := cmd.Start(); err != nil {
				zap.S().Errorw("spawn detached daemon", "error", err)
				os.Exit(1)
			}
			// Exit cleanly, dropping the SSH stream!
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
	registerTools(meshReg, entries)
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
		tcpLis, err := node.Listen(ctx, "0.0.0.0:4443")
		if err != nil {
			zap.S().Errorw("daemon listen 4443", "error", err)
		} else {
			zap.S().Infow("daemon listening on TCP", "addr", tcpLis.Addr())
		}
	}

	zap.S().Infow("fleet node ready — serving tools", "node_id", nodeID, "tools", len(meshReg.ListLocal()))
	tools.ServeToolListener(ctx, lis, meshReg)
}

// runGateway handles the gateway (bootstrap) node lifecycle.
func runGateway(ctx context.Context, cancel context.CancelFunc, nodeID string, cfg *config.MeshConfig, entries []toolEntry, plugins *registry.PluginRegistry, skipDeploy bool, opts GatewayOptions) {
	install := opts.Install
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
		fmt.Fprintf(os.Stderr, "fatal: create node: %v\n", err)
		os.Exit(1)
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
		registerTools(meshReg, entries)
		plugins.BridgeToMesh(meshReg)
		registerNodeTools(meshReg, node)
	}

	fmt.Fprintf(os.Stderr, "  ✓ Node created: %s (peers=0, caps=%d)\n", nodeID, len(meshReg.ListLocal()))
	fmt.Fprintf(os.Stderr, "  ✓ Reconnect policy: enabled (1s→30s backoff, 5m timeout)\n")

	// Initialize the credential vault.
	v, err := initVault(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}

	// --- Phase 3: Local tool invocation ---
	fmt.Fprintf(os.Stderr, "\n--- Phase 3: Local tool invocation ---\n")
	if !opts.PureClient {
		runLocalDemo(ctx, meshReg)
	} else {
		fmt.Fprintf(os.Stderr, "  (Skipped in PureClient mode)\n")
	}

	// --- Phase 4: Gateway meta-tools ---
	fmt.Fprintf(os.Stderr, "\n--- Phase 4: Gateway meta-tools ---\n")
	bridge := tools.NewNeuronBridge(node)

	// Create group resolver to inject into Gateway for nodeset processing
	resolver := &staticGroupResolver{
		groups: map[string][]string{
			"storage": {"oss1"},
			"network": {"oss2"},
		},
	}

	gw := gateway.New(meshReg, bridge, gateway.WithGroupResolver(resolver))
	if !opts.PureClient {
		runGatewayMetaTools(ctx, gw)
	} else {
		fmt.Fprintf(os.Stderr, "  (Demo skipped in PureClient mode)\n")
	}

	// --- Phase 5: Deploy to seed hosts ---
	knownHosts := cfg.KnownHosts()
	if len(knownHosts) == 0 {
		fmt.Fprintf(os.Stderr, "\nNo seed hosts configured in mesh.toml. Skipping remote phases.\n")
		fmt.Fprintf(os.Stderr, "\nDone (local-only mode).\n")
		return
	}

	fmt.Fprintf(os.Stderr, "\n--- Phase 5: Deploy + mesh connect ---\n")
	deployedNodes := deployAndConnect(ctx, node, pki, knownHosts, v, skipDeploy, false, install)

	if len(deployedNodes) == 0 {
		fmt.Fprintf(os.Stderr, "\nWarning: No remote nodes successfully joined. Are credentials valid?\n")
	}

	if opts.Stop {
		fmt.Fprintf(os.Stderr, "\n--- Stopping Remote Nodes ---\n")

		agents, _ := bridge.DiscoverTool(ctx, "node_stop")
		var nodeIDs []string
		for _, a := range agents {
			if opts.Target == "" || a.NodeID == opts.Target {
				nodeIDs = append(nodeIDs, a.NodeID)
			}
		}

		res, err := tools.NewRemoteInvoker(bridge).FanOut(ctx, nodeIDs, "node_stop", nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ stop dispatch failed: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "✓ stop broadcast successful (%d nodes)\n", len(res))
		}
		cancel()
		time.Sleep(500 * time.Millisecond)
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
	snap := node.CapabilityIndex().Snapshot()
	if len(allTools) > 0 {
		fmt.Fprintf(os.Stderr, "  ✓ Wildcard 'tool:*' found %d node(s) offering tool capabilities across the mesh:\n", len(allTools))
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
	if !opts.PureClient {
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
	} else {
		fmt.Fprintf(os.Stderr, "  (Skipped in PureClient mode)\n")
	}

	// --- Phase 9: Fan-out hello ---
	fmt.Fprintf(os.Stderr, "\n--- Phase 9: Fan-out hello ---\n")
	if !opts.PureClient {
		runGatewayFanOut(ctx, gw, deployedNodes)
	} else {
		fmt.Fprintf(os.Stderr, "  (Skipped in PureClient mode)\n")
	}

	// --- Phase 10: Mesh topology print ---
	fmt.Fprintf(os.Stderr, "\n--- Phase 10: Mesh topology ---\n")
	if !opts.PureClient {
		runGatewayTopology(ctx, gw)
	} else {
		fmt.Fprintf(os.Stderr, "  (Skipped in PureClient mode)\n")
	}

	// --- Phase 10b: Serve HTTP if requested ---
	if opts.ServeHTTP != "" {
		fmt.Fprintf(os.Stderr, "\n--- Serving MCP HTTP on %s ---\n", opts.ServeHTTP)

		// Create an MCP Server adapter for the Gateway Dispatcher
		mcpSrv := mcp.NewServer(gw)

		// We use the same identity cert for mTLS
		caPool := x509.NewCertPool()
		caPool.AppendCertsFromPEM(gatewayCert.Certificate[0]) // wait, pki.ca is the root.

		// Wait, gatewayCert doesn't contain the CA directly, we should get the CA pool.
		// pki.pool is private but we can use pki.membraneConfig(gatewayCert).CACert
		memCfg := pki.membraneConfig(gatewayCert)

		go func() {
			if err := mcp.StartHTTPServer(opts.ServeHTTP, mcpSrv, gatewayCert, memCfg.CACert); err != nil {
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

	// --- Phase 10c: Test Harness ---
	if opts.HarnessType != "" {
		fmt.Fprintf(os.Stderr, "\n--- Running Harness: %s ---\n", opts.HarnessType)
		h := harness.NewHarness(gw)
		var remoteNodes []string
		for _, nt := range allTools {
			if nt.NodeID != nodeID {
				remoteNodes = append(remoteNodes, nt.NodeID)
			}
		}

		if len(remoteNodes) == 0 && opts.HarnessType != "deploy" {
			fmt.Fprintf(os.Stderr, "  ⚠ No remote nodes found to run harness against\n")
		} else {
			// Get all tool names from wildcard index
			toolSet := make(map[string]bool)
			for _, nt := range allTools {
				for _, cap := range snap[nt.NodeID] {
					if strings.HasPrefix(cap, "tool:") {
						toolSet[strings.TrimPrefix(cap, "tool:")] = true
					}
				}
			}
			var toolsList []string
			for t := range toolSet {
				toolsList = append(toolsList, t)
			}

			switch opts.HarnessType {
			case "check":
				rep := h.RunToolCheck(ctx, toolsList, remoteNodes)
				fmt.Fprintf(os.Stderr, "  ✓ Check complete: %d total, %d success, %d failed\n", rep.Total, rep.Successful, rep.Failed)
			case "soak":
				rep := h.RunToolSoak(ctx, toolsList, remoteNodes, opts.HarnessCount, opts.HarnessDuration)
				fmt.Fprintf(os.Stderr, "  ✓ Soak complete: %d total, %d success, %d failed\n", rep.Total, rep.Successful, rep.Failed)
			case "deploy":
				da := &deploySoakAdapter{
					node:       node,
					pki:        pki,
					knownHosts: knownHosts,
					v:          v,
					cfg:        cfg,
				}
				rep := h.RunDeploySoak(ctx, da, opts.HarnessCount, opts.HarnessDuration)
				fmt.Fprintf(os.Stderr, "  ✓ Deploy soak complete: %d total, %d success, %d failed\n", rep.Total, rep.Successful, rep.Failed)
			}
		}
	}

	// --- Phase 11: Cleanup ---
	fmt.Fprintf(os.Stderr, "\n--- Phase 11: Cleanup ---\n")
	cancel()
	if err := node.Close(); err != nil {
		zap.S().Debugw("close node", "error", err)
	}
	for _, dn := range deployedNodes {
		if err := dn.conn.Close(); err != nil {
			zap.S().Debugw("close deploy conn", "node", dn.nodeID, "error", err)
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

// deploySoakAdapter implements harness.FleetDeployer for testing.
type deploySoakAdapter struct {
	node       *api.Node
	pki        *ephemeralPKI
	knownHosts map[string][]string
	v          *vault.Vault
	cfg        *config.MeshConfig
	nodes      []deployedNode
}

func (a *deploySoakAdapter) Deploy(ctx context.Context) error {
	a.nodes = deployAndConnect(ctx, a.node, a.pki, a.knownHosts, a.v, false, false, false)
	return nil
}

func (a *deploySoakAdapter) Uninstall(ctx context.Context) error {
	uninstallFleet(ctx, a.cfg, "")
	for _, dn := range a.nodes {
		if dn.conn != nil {
			_ = dn.conn.Close()
		}
	}
	a.nodes = nil
	return nil
}

// printHeader displays startup information.
func printHeader(nodeID string, entries []toolEntry) {
	fmt.Fprintf(os.Stderr, "\n=== Cortex MCP Application ===\n")
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
func deployAndConnect(ctx context.Context, node *api.Node, pki *ephemeralPKI, knownHosts map[string][]string, v *vault.Vault, skipDeploy, daemon, install bool) []deployedNode {
	var deployed []deployedNode
	deployer := &transport.SelfDeployer{
		SkipUpload: skipDeploy,
	}

	if daemon {
		deployer.ExecArgs = []string{"-daemon"}
	}

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

		// --- Connectivity decision tree ---
		// 1. TCP:4443 reachable → direct connect (upgrade if needed)
		// 2. TCP:4443 firewalled, service active → SSH bridge
		// 3. Neither → fresh deploy
		host, _, splitErr := net.SplitHostPort(addr)
		if splitErr != nil {
			host = strings.Split(addr, ":")[0]
		}

		meshAddr := host + ":4443"
		probeConn := probeExistingNode(ctx, meshAddr)

		if probeConn != nil {
			// --- Path 1: TCP reachable, direct connect ---
			_ = probeConn.Close()

			if needsUpgrade(skipDeploy) {
				fmt.Fprintf(os.Stderr, "  → %s (%s): upgrading...", remoteNodeID, addr)
				remotePath := installRemotePath("")
				if err := upgradeRemoteNode(ctx, addr, deployCred, remotePath); err != nil {
					fmt.Fprintf(os.Stderr, " ✗ %v\n", err)
					continue
				}
				fmt.Fprintf(os.Stderr, " binary pushed, restarting...")
				time.Sleep(upgradeWaitAfterRestart)
			} else {
				fmt.Fprintf(os.Stderr, "  → %s (%s): reconnecting...", remoteNodeID, addr)
			}

			var d net.Dialer
			conn, err := d.DialContext(ctx, "tcp", meshAddr)
			if err != nil {
				fmt.Fprintf(os.Stderr, " ✗ mesh connect (TCP): %v\n", err)
				continue
			}

			if err := node.AddPeer(ctx, conn, false); err != nil {
				fmt.Fprintf(os.Stderr, " ✗ mTLS membrane: %v\n", err)
				_ = conn.Close()
				continue
			}

			fmt.Fprintf(os.Stderr, " ✓ connected (mTLS via TCP)\n")
			deployed = append(deployed, deployedNode{
				nodeID: remoteNodeID,
				conn:   conn,
			})
			continue
		}

		// TCP:4443 unreachable — check if service is installed but firewalled.
		if checkServiceActive(ctx, addr, deployCred) {
			// --- Path 2: Service active, port firewalled → SSH bridge ---
			if needsUpgrade(skipDeploy) {
				fmt.Fprintf(os.Stderr, "  → %s (%s): upgrading (SSH)...", remoteNodeID, addr)
				remotePath := installRemotePath("")
				if err := upgradeRemoteNode(ctx, addr, deployCred, remotePath); err != nil {
					fmt.Fprintf(os.Stderr, " ✗ %v\n", err)
					continue
				}
				fmt.Fprintf(os.Stderr, " binary pushed, restarting...")
				time.Sleep(upgradeWaitAfterRestart)
			} else {
				fmt.Fprintf(os.Stderr, "  → %s (%s): bridging (SSH)...", remoteNodeID, addr)
			}

			remotePath := installRemotePath("")
			stream, err := sshExecBridge(ctx, addr, deployCred, remotePath)
			if err != nil {
				fmt.Fprintf(os.Stderr, " ✗ bridge: %v\n", err)
				continue
			}

			// Wait for bridge readiness (same 4-byte magic as deploy).
			readyBuf := make([]byte, 4)
			if _, err := io.ReadFull(stream, readyBuf); err != nil {
				fmt.Fprintf(os.Stderr, " ✗ bridge ready: %v\n", err)
				_ = stream.Close()
				continue
			}

			conn := transport.NewStdioConn(stream, stream)
			if err := node.AddPeer(ctx, conn, false); err != nil {
				fmt.Fprintf(os.Stderr, " ✗ mTLS membrane: %v\n", err)
				_ = conn.Close()
				continue
			}

			fmt.Fprintf(os.Stderr, " ✓ connected (mTLS via SSH bridge)\n")
			deployed = append(deployed, deployedNode{
				nodeID: remoteNodeID,
				conn:   conn,
			})
			continue
		}

		// --- Path 3: No existing node → fresh deploy ---
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
				zap.S().Debugw("close stream", "error", cerr)
			}
			continue
		}

		modeByte := byte(0x00)
		if install {
			modeByte = 0x01
		}
		if _, err := stream.Write([]byte{modeByte}); err != nil {
			fmt.Fprintf(os.Stderr, " ✗ send mode protocol: %v\n", err)
			dumpRemoteStderr(stream)
			_ = stream.Close()
			continue
		}

		if err := writeCertBundle(stream, bundle); err != nil {
			fmt.Fprintf(os.Stderr, " ✗ send certs: %v\n", err)
			dumpRemoteStderr(stream)
			if cerr := stream.Close(); cerr != nil {
				zap.S().Debugw("close stream", "error", cerr)
			}
			continue
		}

		if install {
			ackBuf := make([]byte, 4)
			if _, err := io.ReadFull(stream, ackBuf); err != nil || string(ackBuf) != string([]byte{'O', 'K', 0x00, 0x06}) {
				fmt.Fprintf(os.Stderr, " ✗ install failed/invalid ack: %v\n", err)
				dumpRemoteStderr(stream)
				_ = stream.Close()
				continue
			}
			_ = stream.Close()

			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				host = strings.Split(addr, ":")[0]
			}

			// Dial the newly spawned daemon on its TCP port
			var d net.Dialer
			conn, err := d.DialContext(ctx, "tcp", host+":4443")
			if err != nil {
				fmt.Fprintf(os.Stderr, " ✗ mesh connect (TCP): %v\n", err)
				continue
			}

			if err := node.AddPeer(ctx, conn, false); err != nil {
				fmt.Fprintf(os.Stderr, " ✗ mTLS membrane: %v\n", err)
				_ = conn.Close()
				continue
			}

			fmt.Fprintf(os.Stderr, " ✓ installed + connected (mTLS via TCP)\n")
			deployed = append(deployed, deployedNode{
				nodeID: remoteNodeID,
				conn:   conn,
			})
		} else {
			// Wrap the stream as a net.Conn and establish mesh connection
			// via membrane handshake (mTLS + yamux).
			conn := transport.NewStdioConn(stream, stream)
			if err := node.AddPeer(ctx, conn, false); err != nil {
				fmt.Fprintf(os.Stderr, " ✗ mesh connect: %v\n", err)
				dumpRemoteStderr(stream)
				if cerr := conn.Close(); cerr != nil {
					zap.S().Debugw("close conn", "error", cerr)
				}
				continue
			}

			fmt.Fprintf(os.Stderr, " ✓ deployed + connected (mTLS)\n")
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

// runGatewayTopology invokes the mesh_topology tool via the gateway and pretty-prints the output.
func runGatewayTopology(ctx context.Context, gw *gateway.Gateway) {
	// Call the built-in mesh_topology tool locally
	result, err := gw.Dispatch(ctx, "call_tool", json.RawMessage(`{"tool_name":"mesh_topology","args":{}}`))
	if err != nil {
		fmt.Fprintf(os.Stderr, "  ✗ mesh_topology error: %v\n", err)
		return
	}

	var prettyJSON bytes.Buffer
	if err := json.Indent(&prettyJSON, result.Content, "  ", "  "); err != nil {
		fmt.Fprintf(os.Stderr, "  ✗ failed to format topology JSON: %v\n", err)
		return
	}

	fmt.Fprintf(os.Stderr, "%s\n", prettyJSON.String())
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

	knownHosts := cfg.KnownHosts()
	remotePath := installRemotePath("")

	for remoteNodeID, addrs := range knownHosts {
		// Skip nodes that don't match the target filter.
		if target != "" && remoteNodeID != target {
			continue
		}
		if len(addrs) == 0 {
			continue
		}

		addr := addrs[0]
		if !strings.Contains(addr, ":") {
			addr = addr + ":22"
		}

		cred, ok := v.Match(addr)
		if !ok {
			host := strings.Split(addr, ":")[0]
			cred, ok = v.Match(host)
		}
		if !ok {
			fmt.Fprintf(os.Stderr, "  ✗ %s (%s): no credentials\n", remoteNodeID, addr)
			continue
		}

		deployCred, err := toDeployCredential(cred)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ %s (%s): %v\n", remoteNodeID, addr, err)
			continue
		}

		fmt.Fprintf(os.Stderr, "  → %s (%s): uninstalling...", remoteNodeID, addr)

		// SSH exec the binary with the uninstall subcommand.
		client, err := dialSSH(ctx, addr, deployCred)
		if err != nil {
			fmt.Fprintf(os.Stderr, " ✗ SSH: %v\n", err)
			continue
		}

		uninstallCmd := fmt.Sprintf("%s uninstall", remotePath)
		if err := execSSHCommand(client, uninstallCmd); err != nil {
			_ = client.Close()
			fmt.Fprintf(os.Stderr, " ✗ %v\n", err)
			continue
		}

		_ = client.Close()
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

	knownHosts := cfg.KnownHosts()

	for remoteNodeID, addrs := range knownHosts {
		// Skip nodes that don't match the target filter.
		if target != "" && remoteNodeID != target {
			continue
		}
		if len(addrs) == 0 {
			continue
		}

		addr := addrs[0]
		if !strings.Contains(addr, ":") {
			addr = addr + ":22"
		}

		cred, ok := v.Match(addr)
		if !ok {
			host := strings.Split(addr, ":")[0]
			cred, ok = v.Match(host)
		}
		if !ok {
			fmt.Fprintf(os.Stderr, "  ✗ %s (%s): no credentials\n", remoteNodeID, addr)
			continue
		}

		deployCred, err := toDeployCredential(cred)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ %s (%s): %v\n", remoteNodeID, addr, err)
			continue
		}

		fmt.Fprintf(os.Stderr, "  → %s (%s): starting service...", remoteNodeID, addr)

		client, err := dialSSH(ctx, addr, deployCred)
		if err != nil {
			fmt.Fprintf(os.Stderr, " ✗ SSH: %v\n", err)
			continue
		}

		startCmd := fmt.Sprintf("sudo systemctl start %s", serviceName)
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
