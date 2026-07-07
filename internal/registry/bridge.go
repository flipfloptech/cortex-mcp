package registry

import (
	"context"
	"encoding/json"
	"time"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/tools"
)

// BridgeToMesh registers all supported tools from the PluginRegistry
// into a cortex-mcp tools.Registry. This bridges our plugin system
// into the mesh's tool discovery and invocation infrastructure.
//
// Each tool is wrapped in a handler that:
//   - Converts the plugin's ToolResult to the mesh's tools.ToolResult
//   - Populates the nodeID in the result envelope
//   - Tracks execution time
func (pr *PluginRegistry) BridgeToMesh(meshReg *tools.Registry) {
	for _, t := range pr.Supported() {
		tool := t // capture for closure

		// Convert our ToolParam to cortex-mcp's tools.ToolParam.
		var meshParams []tools.ToolParam
		for _, p := range tool.Parameters() {
			meshParams = append(meshParams, tools.ToolParam{
				Name:        p.Name,
				Type:        p.Type,
				Description: p.Description,
				Required:    p.Required,
				Default:     p.Default,
			})
		}

		meshReg.Register(tools.ToolDefinition{
			Name:            tool.Name(),
			Description:     tool.Description(),
			LongDescription: tool.Help(),
			Category:        tool.Category().String(),
			Parameters:      meshParams,
			Hidden:          tool.Hidden(),
		}, func(ctx context.Context, args json.RawMessage) (*tools.ToolResult, error) {
			start := time.Now()

			result, err := tool.Execute(ctx, args)
			if err != nil {
				return tools.NewErrorResult(err.Error()), nil
			}

			// Forcefully populate nodeID with the true mesh identity.
			result.NodeID = pr.nodeID
			result.Metadata.ExecutionTimeMs = time.Since(start).Milliseconds()

			// Marshal the full ToolResult envelope as the mesh result content.
			content, _ := json.Marshal(result)
			return &tools.ToolResult{Content: content}, nil
		})
	}
}
