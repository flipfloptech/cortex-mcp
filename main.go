// Package main demonstrates a minimal cortex-mesh consumer binary.
//
// This is the reference integration pattern: a single binary that can
// serve as either a gateway (bootstrap node) or a fleet node (deployed),
// depending on how it was launched.
//
// The example demonstrates the FULL mesh lifecycle:
//  1. Load config (mesh.toml) — node identity, seed hosts, credentials
//  2. Initialize credential vault
//  3. Register tools locally
//  4. Deploy this binary to each seed host via SSH (SelfDeployer)
//  5. Invoke tools remotely via the tool wire protocol
//  6. Fan-out invocations across all deployed nodes
//  7. Clean teardown — close connections, deployed nodes exit
//
// The example uses the tool wire protocol directly over deploy streams.
// In production, connections would go through the membrane (mTLS) and
// yamux multiplexing layers for security and stream isolation. Those
// layers are thoroughly tested in their own packages.
//
// Configuration is loaded from mesh.toml (see -config flag).
// Credentials are loaded into the encrypted vault at startup.
//
// Usage:
//
//	# Build first (so the binary can self-deploy):
//	go build -o mesh-example ./example/
//
//	# Run the demo:
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
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/cortex-mesh/cortex-mesh/example/config"
	"github.com/cortex-mesh/cortex-mesh/tools"
	"github.com/cortex-mesh/cortex-mesh/transport"
	"github.com/cortex-mesh/cortex-mesh/vault"
	"golang.org/x/crypto/ssh"
)

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

	// --- Register tools ---
	registry := tools.NewRegistry(nil)

	registry.Register(tools.ToolDefinition{
		Name:        "hello",
		Description: "Say hello from this node",
		Category:    "demo",
		Parameters: []tools.ToolParam{
			{Name: "name", Type: "string", Description: "Who to greet", Required: false, Default: "world"},
		},
	}, func(_ context.Context, args json.RawMessage) (*tools.ToolResult, error) {
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
	})

	registry.Register(tools.ToolDefinition{
		Name:            "system_info",
		Description:     "Get basic system information",
		LongDescription: "Returns the hostname, OS, architecture, and number of CPUs for this node.",
		Category:        "system",
	}, func(_ context.Context, _ json.RawMessage) (*tools.ToolResult, error) {
		info := map[string]interface{}{
			"node_id":  nodeID,
			"hostname": nodeID,
			"os":       runtime.GOOS,
			"arch":     runtime.GOARCH,
			"cpus":     runtime.NumCPU(),
		}
		data, _ := json.Marshal(info)
		return &tools.ToolResult{Content: data}, nil
	})

	// --- Fleet node mode ---
	// When deployed via SelfDeployer, the binary serves tools over
	// stdin/stdout using the length-prefixed protobuf wire protocol.
	if transport.WasDeployed() {
		slog.Info("deployed fleet node — serving tools", "node_id", nodeID)

		// Wrap stdin/stdout as a net.Conn for the tool protocol.
		conn := transport.NewStdioConn(os.Stdin, os.Stdout)

		if err := tools.ServeToolConn(ctx, conn, registry); err != nil {
			slog.Error("serve tools", "error", err)
		}
		return
	}

	// --- Gateway / Bootstrap mode ---
	printHeader(nodeID, registry)

	// Initialize the credential vault.
	v, err := initVault(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}

	// --- Phase 1: Local tool invocation ---
	fmt.Fprintf(os.Stderr, "--- Phase 1: Local tool invocation ---\n")
	runLocalDemo(ctx, registry)

	// --- Phase 2: Deploy to seed hosts ---
	knownHosts := cfg.KnownHosts()
	if len(knownHosts) == 0 {
		fmt.Fprintf(os.Stderr, "\nNo seed hosts configured in mesh.toml. Skipping remote phases.\n")
		fmt.Fprintf(os.Stderr, "\nDone (local-only mode).\n")
		return
	}

	fmt.Fprintf(os.Stderr, "\n--- Phase 2: Deploy to seed hosts ---\n")
	deployedNodes := deployToHosts(ctx, knownHosts, v)

	if len(deployedNodes) == 0 {
		fmt.Fprintf(os.Stderr, "\nNo nodes deployed successfully. Exiting.\n")
		return
	}

	// --- Phase 3: Remote tool invocation (unicast) ---
	fmt.Fprintf(os.Stderr, "\n--- Phase 3: Remote tool invocation ---\n")
	for _, dn := range deployedNodes {
		result, err := tools.DialInvoke(ctx, dn.conn, "system_info", nil)
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

	// --- Phase 4: Fan-out hello ---
	fmt.Fprintf(os.Stderr, "\n--- Phase 4: Fan-out hello across all nodes ---\n")
	fanOutHello(ctx, deployedNodes)

	// --- Phase 5: Cleanup ---
	fmt.Fprintf(os.Stderr, "\n--- Phase 5: Cleanup ---\n")
	cancel()
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
func printHeader(nodeID string, registry *tools.Registry) {
	fmt.Fprintf(os.Stderr, "\n=== cortex-mesh E2E example ===\n")
	fmt.Fprintf(os.Stderr, "Gateway node: %s\n", nodeID)
	fmt.Fprintf(os.Stderr, "Registered tools:\n")
	for _, t := range registry.ListLocal() {
		fmt.Fprintf(os.Stderr, "  - %s (%s): %s\n", t.Name, t.Category, t.Description)
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

// deployToHosts deploys the mesh binary to each seed host.
func deployToHosts(ctx context.Context, knownHosts map[string][]string, v *vault.Vault) []deployedNode {
	var deployed []deployedNode

	deployer := &transport.SelfDeployer{}

	for nodeID, addrs := range knownHosts {
		if len(addrs) == 0 {
			fmt.Fprintf(os.Stderr, "  ✗ %s: no addresses configured\n", nodeID)
			continue
		}

		addr := addrs[0]

		// Ensure addr has a port.
		if !strings.Contains(addr, ":") {
			addr = addr + ":22"
		}

		// Look up credentials from the vault.
		cred, ok := v.Match(addr)
		if !ok {
			// Try matching without port.
			host := strings.Split(addr, ":")[0]
			cred, ok = v.Match(host)
		}
		if !ok {
			fmt.Fprintf(os.Stderr, "  ✗ %s (%s): no matching credentials in vault\n", nodeID, addr)
			continue
		}

		// Convert vault credential to deploy credential.
		deployCred, err := toDeployCredential(cred)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ %s (%s): %v\n", nodeID, addr, err)
			continue
		}

		fmt.Fprintf(os.Stderr, "  → %s (%s): deploying...", nodeID, addr)

		stream, err := deployer.Deploy(ctx, addr, deployCred, nil) // nil host key = TOFU
		if err != nil {
			fmt.Fprintf(os.Stderr, " ✗ %v\n", err)
			continue
		}

		fmt.Fprintf(os.Stderr, " ✓ deployed\n")
		deployed = append(deployed, deployedNode{
			nodeID: nodeID,
			conn:   transport.NewStdioConn(stream, stream),
		})
	}

	return deployed
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
	// Invoke "hello" locally.
	result, err := registry.InvokeLocal(ctx, "hello", json.RawMessage(`{"name":"cortex-mesh"}`))
	if err != nil {
		fmt.Fprintf(os.Stderr, "  hello failed: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "  hello: %s\n", result.Content)
	}

	// Invoke "system_info" locally.
	result, err = registry.InvokeLocal(ctx, "system_info", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  system_info failed: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "  system_info: %s\n", result.Content)
	}
}

// fanOutHello invokes the "hello" tool on all deployed nodes concurrently.
func fanOutHello(ctx context.Context, nodes []deployedNode) {
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

	// Try default locations in order.
	defaults := []string{
		"mesh.toml",
		"example/mesh.toml",
	}

	// Also try relative to the executable.
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
