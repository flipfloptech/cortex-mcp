// Package main demonstrates a minimal cortex-mesh consumer binary.
//
// This is the reference integration pattern: a single binary that can
// serve as either a gateway (MCP-facing) or a fleet node (mesh-facing),
// depending on how it was launched.
//
// Usage:
//
//	# As the bootstrap/gateway node:
//	./mesh-example
//
//	# As a deployed fleet node (set automatically by SelfDeployer):
//	CORTEX_MESH_SPAWNED=1 ./mesh-example
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"

	"github.com/cortex-mesh/cortex-mesh/api"
	"github.com/cortex-mesh/cortex-mesh/gateway"
	"github.com/cortex-mesh/cortex-mesh/tools"
	"github.com/cortex-mesh/cortex-mesh/transport"
)

func main() {
	ctx := context.Background()

	// Determine node identity. In production, this comes from the
	// deployment system. For the example, fall back to hostname.
	nodeID := os.Getenv("CORTEX_NODE_ID")
	if nodeID == "" {
		hostname, _ := os.Hostname()
		nodeID = hostname
	}

	// Create the mesh node.
	node, err := api.NewNode(ctx, api.NodeConfig{
		NodeID: nodeID,
		KnownHosts: map[string][]string{
			// Pre-seed known hosts for the mesh.
			// In production, these come from inventory systems.
			"mds-01": {"10.0.1.5"},
			"oss-01": {"10.0.1.10"},
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
	defer node.Close()

	// Create the tool registry — this handles local tool registration
	// and capability advertisement across the mesh.
	registry := tools.NewRegistry(node)

	// --- Register local tools ---
	// Every node offers the same tools. The mesh handles routing
	// invocations to the right node based on impedance and capability.

	registry.Register(tools.ToolDefinition{
		Name:        "hello",
		Description: "Say hello from this node",
		Category:    "demo",
		Parameters: []tools.ToolParam{
			{Name: "name", Type: "string", Description: "Who to greet", Required: false, Default: "world"},
		},
	}, func(ctx context.Context, args json.RawMessage) (*tools.ToolResult, error) {
		var params struct {
			Name string `json:"name"`
		}
		params.Name = "world"
		json.Unmarshal(args, &params)
		return tools.NewTextResult(fmt.Sprintf("Hello, %s! From node %s", params.Name, node.NodeID())), nil
	})

	registry.Register(tools.ToolDefinition{
		Name:            "system_info",
		Description:     "Get basic system information",
		LongDescription: "Returns the hostname, OS, architecture, and number of CPUs for this node.",
		Category:        "system",
		Parameters:      []tools.ToolParam{},
	}, func(ctx context.Context, args json.RawMessage) (*tools.ToolResult, error) {
		info := map[string]interface{}{
			"hostname": nodeID,
			"os":       runtime.GOOS,
			"arch":     runtime.GOARCH,
			"cpus":     runtime.NumCPU(),
		}
		data, _ := json.Marshal(info)
		return &tools.ToolResult{Content: data}, nil
	})

	// --- Deployment check ---
	// If this binary was deployed to a remote host by the mesh's
	// SelfDeployer, it should serve as a fleet node: accept the
	// deployer's connection over stdin/stdout and block forever,
	// serving tools through the mesh.
	if transport.WasDeployed() {
		fmt.Fprintf(os.Stderr, "[%s] deployed fleet node — accepting mesh connection\n", nodeID)
		if err := node.AcceptStdio(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "fatal: accept stdio: %v\n", err)
			os.Exit(1)
		}
		select {} // block forever — serve as mesh peer + tool host
	}

	// --- Bootstrap/Gateway mode ---
	// This is the entry point node. It exposes the mesh's tool catalog
	// to the LLM via MCP (JSON-RPC over stdio).
	gw := gateway.New(registry, nil) // nil mesh bridge for local-only demo

	fmt.Fprintf(os.Stderr, "[%s] gateway node ready\n", nodeID)
	fmt.Fprintf(os.Stderr, "Registered tools:\n")
	for _, t := range registry.ListLocal() {
		fmt.Fprintf(os.Stderr, "  - %s (%s): %s\n", t.Name, t.Category, t.Description)
	}
	fmt.Fprintf(os.Stderr, "\n")

	// In a real deployment, this would serve MCP over stdio:
	//   gw.Serve(os.Stdin, os.Stdout)
	//
	// For the example, we just demonstrate invoking tools locally.
	fmt.Fprintf(os.Stderr, "--- Local tool invocation demo ---\n")

	// Invoke the hello tool.
	result, err := registry.InvokeLocal(ctx, "hello", json.RawMessage(`{"name":"cortex-mesh"}`))
	if err != nil {
		fmt.Fprintf(os.Stderr, "hello failed: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "hello result: %s\n", result.Content)
	}

	// Invoke system_info.
	result, err = registry.InvokeLocal(ctx, "system_info", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "system_info failed: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "system_info result: %s\n", result.Content)
	}

	// Demonstrate gateway meta-tools.
	fmt.Fprintf(os.Stderr, "\n--- Gateway meta-tool demo ---\n")
	listResult, err := gw.Dispatch(ctx, "list_tools", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list_tools failed: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "list_tools result: %s\n", listResult.Content)
	}

	helpResult, err := gw.Dispatch(ctx, "tool_help", json.RawMessage(`{"tool_name":"system_info"}`))
	if err != nil {
		fmt.Fprintf(os.Stderr, "tool_help failed: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "tool_help result: %s\n", helpResult.Content)
	}
}
