import re

def main():
    with open('internal/mesh/cli.go', 'r') as f:
        content = f.read()

    # Import lifecycle
    content = content.replace('"github.com/cortex-mesh/cortex-mesh/tools"\n\t"github.com/cortex-mesh/cortex-mesh/transport"', '"github.com/cortex-mesh/cortex-mesh/tools"\n\t"github.com/cortex-mesh/cortex-mesh/transport"\n\t"github.com/flipfloptech/cortex-mcp/internal/registry/tools/lifecycle"')

    # Replace liveMode = true
    content = content.replace('liveMode = true', 'lifecycle.SetLiveMode(true)')

    # Re-insert buildNodeDeployHandler
    # It was in lifecycle.go. Let's append it to cli.go.
    handler_code = """

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
"""
    content += handler_code
    
    with open('internal/mesh/cli.go', 'w') as f:
        f.write(content)

if __name__ == '__main__':
    main()
