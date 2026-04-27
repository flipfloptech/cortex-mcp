package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/api"
	"go.uber.org/zap"
)

// TopologyProvider abstracts access to a node's local mesh topology snapshot.
type TopologyProvider interface {
	MeshTopology() api.TopologySnapshot
}

// MeshOverview is the aggregate response returned to the LLM.
type MeshOverview struct {
	GatewayNodeID string         `json:"gateway_node_id"`
	Timestamp     string         `json:"timestamp"`
	TotalNodes    int            `json:"total_nodes"`
	DirectPeers   int            `json:"direct_peers"`
	RoleCounts    map[string]int `json:"role_counts"`
	VersionCounts map[string]int `json:"version_counts"`
	Nodes         []NodeOverview `json:"nodes"`
	Edges         []MeshEdge     `json:"edges"`
	MermaidGraph  string         `json:"mermaid_graph"`
}

// NodeOverview is a per-node summary in the cluster overview.
type NodeOverview struct {
	NodeID             string   `json:"node_id"`
	Hostname           string   `json:"hostname"`
	Roles              []string `json:"roles"`
	OS                 string   `json:"os"`
	Arch               string   `json:"arch"`
	CPUs               int      `json:"cpus"`
	Kernel             string   `json:"kernel"`
	Distro             string   `json:"distro"`
	Impedance          float64  `json:"impedance"`
	IsDirect           bool     `json:"is_direct"`
	NextHop            string   `json:"next_hop"`
	PeerCount          int      `json:"peer_count"`
	Tools              []string `json:"tools"`
	ProtocolVersion    uint16   `json:"protocol_version"`
	ApplicationVersion string   `json:"application_version"`
	Status             string   `json:"status"`
	Error              string   `json:"error,omitempty"`
}

// MeshEdge represents a direct connection between two nodes.
type MeshEdge struct {
	From      string  `json:"from"`
	To        string  `json:"to"`
	Impedance float64 `json:"impedance"`
}

// MeshOverviewHandler orchestrates the fan-out and aggregation.
type MeshOverviewHandler struct {
	dispatcher Dispatcher
	topology   TopologyProvider
}

// NewMeshOverviewHandler creates a new handler.
func NewMeshOverviewHandler(dispatcher Dispatcher, topology TopologyProvider) *MeshOverviewHandler {
	return &MeshOverviewHandler{
		dispatcher: dispatcher,
		topology:   topology,
	}
}

// Execute runs the full cluster overview aggregation.
func (h *MeshOverviewHandler) Execute(ctx context.Context) (*MeshOverview, error) {
	overview := &MeshOverview{
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
		RoleCounts:    make(map[string]int),
		VersionCounts: make(map[string]int),
	}

	// 1. Get gateway's own topology snapshot (local, zero network).
	gwTopo := h.topology.MeshTopology()
	overview.GatewayNodeID = gwTopo.NodeID
	overview.DirectPeers = gwTopo.DirectPeers

	// Build a lookup of gateway's view per node.
	gwView := make(map[string]api.NodeSummary)
	for _, ns := range gwTopo.NodeDetails {
		gwView[ns.NodeID] = ns
	}

	// 2. Fan-out system_info to all nodes.
	sysInfoByNode := h.fanOutTool(ctx, "get_system_info")

	// 3. Fan-out get_mesh_topology to all nodes.
	topoByNode := h.fanOutTool(ctx, "get_mesh_topology")

	// 4. Parse system_info results and build node overviews.
	nodeMap := make(map[string]*NodeOverview)
	for nodeID, content := range sysInfoByNode {
		no := parseSysInfoResult(nodeID, content)

		// Enrich with gateway's routing view.
		if gv, ok := gwView[nodeID]; ok {
			no.Impedance = gv.Impedance
			no.IsDirect = gv.IsDirect
			no.NextHop = gv.NextHop
			no.Tools = extractTools(gv.Capabilities)
		}

		nodeMap[nodeID] = no

		// Accumulate role counts.
		for _, role := range no.Roles {
			overview.RoleCounts[role]++
		}

		// Accumulate version counts.
		if no.ApplicationVersion != "" {
			overview.VersionCounts[no.ApplicationVersion]++
		}
	}

	// 5. Parse get_mesh_topology results from every node and build the edge set.
	edgeSet := make(map[edgeKey]MeshEdge) // key: deduped by node IDs

	// Include gateway's own direct edges.
	for _, ns := range gwTopo.NodeDetails {
		if ns.IsDirect {
			addEdge(edgeSet, gwTopo.NodeID, ns.NodeID, ns.Impedance)
		}
	}

	// Include each remote node's direct edges.
	for reporterID, content := range topoByNode {
		parseTopoEdges(edgeSet, reporterID, content)
	}

	// Also extract peer counts from topo results.
	for nodeID, content := range topoByNode {
		if no, ok := nodeMap[nodeID]; ok {
			no.PeerCount = parseDirectPeerCount(content)
		}
	}

	// 6. Assemble final result.
	overview.TotalNodes = len(nodeMap)
	overview.Nodes = make([]NodeOverview, 0, len(nodeMap))
	for _, no := range nodeMap {
		overview.Nodes = append(overview.Nodes, *no)
	}
	sort.Slice(overview.Nodes, func(i, j int) bool {
		return overview.Nodes[i].NodeID < overview.Nodes[j].NodeID
	})

	overview.Edges = make([]MeshEdge, 0, len(edgeSet))
	for _, edge := range edgeSet {
		overview.Edges = append(overview.Edges, edge)
	}
	sort.Slice(overview.Edges, func(i, j int) bool {
		if overview.Edges[i].From != overview.Edges[j].From {
			return overview.Edges[i].From < overview.Edges[j].From
		}
		return overview.Edges[i].To < overview.Edges[j].To
	})

	// 7. Render Mermaid graph.
	overview.MermaidGraph = renderMermaid(overview)

	return overview, nil
}

// fanOutTool dispatches a call_tool with node_name="*" and returns per-node raw content.
func (h *MeshOverviewHandler) fanOutTool(ctx context.Context, toolName string) map[string]json.RawMessage {
	args, _ := json.Marshal(map[string]interface{}{
		"tool_name": toolName,
		"node_name": "*",
	})

	result, err := h.dispatcher.Dispatch(ctx, "call_tool", args)
	if err != nil {
		zap.S().Warnw("get_mesh_overview fan-out failed", "tool", toolName, "error", err)
		return nil
	}

	// Parse the CallToolResponse envelope.
	var resp struct {
		Results []struct {
			NodeID  string          `json:"node_id"`
			Content json.RawMessage `json:"content"`
			IsError bool            `json:"is_error"`
			Error   string          `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(result.Content, &resp); err != nil {
		zap.S().Warnw("get_mesh_overview: failed to parse fan-out response", "tool", toolName, "error", err)
		return nil
	}

	byNode := make(map[string]json.RawMessage, len(resp.Results))
	for _, r := range resp.Results {
		if !r.IsError && len(r.Content) > 0 {
			byNode[r.NodeID] = r.Content
		}
	}
	return byNode
}

// parseSysInfoResult extracts a NodeOverview from a system_info ToolResult JSON.
func parseSysInfoResult(nodeID string, content json.RawMessage) *NodeOverview {
	no := &NodeOverview{
		NodeID: nodeID,
		Status: "ok",
		Roles:  []string{"generic"},
	}

	// system_info wraps its output in a registry.ToolResult envelope.
	var envelope struct {
		Data struct {
			Hostname           string   `json:"hostname"`
			OS                 string   `json:"os"`
			Arch               string   `json:"arch"`
			CPUs               int      `json:"cpus"`
			Kernel             string   `json:"kernel"`
			Distro             string   `json:"distro"`
			Roles              []string `json:"roles"`
			ProtocolVersion    uint16   `json:"protocol_version"`
			ApplicationVersion string   `json:"application_version"`
		} `json:"data"`
	}

	if err := json.Unmarshal(content, &envelope); err != nil {
		no.Status = "unreachable"
		no.Error = err.Error()
		return no
	}

	no.Hostname = envelope.Data.Hostname
	no.OS = envelope.Data.OS
	no.Arch = envelope.Data.Arch
	no.CPUs = envelope.Data.CPUs
	no.Kernel = envelope.Data.Kernel
	no.Distro = envelope.Data.Distro
	no.ProtocolVersion = envelope.Data.ProtocolVersion
	no.ApplicationVersion = envelope.Data.ApplicationVersion
	if len(envelope.Data.Roles) > 0 {
		no.Roles = envelope.Data.Roles
	}

	return no
}

// parseTopoEdges extracts direct peer edges from a get_mesh_topology result.
func parseTopoEdges(edgeSet map[edgeKey]MeshEdge, reporterID string, content json.RawMessage) {
	var topo struct {
		NodeDetails []struct {
			NodeID    string  `json:"node_id"`
			Impedance float64 `json:"impedance"`
			IsDirect  bool    `json:"is_direct"`
		} `json:"node_details"`
	}

	if err := json.Unmarshal(content, &topo); err != nil {
		return
	}

	for _, nd := range topo.NodeDetails {
		if nd.IsDirect {
			addEdge(edgeSet, reporterID, nd.NodeID, nd.Impedance)
		}
	}
}

// parseDirectPeerCount extracts the direct_peers count from a topology result.
func parseDirectPeerCount(content json.RawMessage) int {
	var topo struct {
		DirectPeers int `json:"direct_peers"`
	}
	_ = json.Unmarshal(content, &topo)
	return topo.DirectPeers
}

type edgeKey struct {
	from, to string
}

// addEdge adds a deduplicated edge to the set. Uses sorted node IDs as key.
func addEdge(edgeSet map[edgeKey]MeshEdge, a, b string, impedance float64) {
	from, to := a, b
	if from > to {
		from, to = to, from
	}
	key := edgeKey{from: from, to: to}
	if _, exists := edgeSet[key]; !exists {
		edgeSet[key] = MeshEdge{From: from, To: to, Impedance: impedance}
	}
}

// extractTools filters capabilities to tool names (strips "tool:" prefix).
func extractTools(capabilities []string) []string {
	var tools []string
	for _, cap := range capabilities {
		if strings.HasPrefix(cap, "tool:") {
			if tools == nil {
				tools = make([]string, 0, len(capabilities))
			}
			tools = append(tools, cap[5:])
		}
	}
	return tools
}

// renderMermaid generates a Mermaid graph from the cluster overview.
func renderMermaid(overview *MeshOverview) string {
	if len(overview.Nodes) == 0 && len(overview.Edges) == 0 {
		return "graph TD\n    empty[\"No nodes in mesh\"]"
	}

	var b strings.Builder
	b.WriteString("graph TD\n")

	// Class definitions for role-based styling.
	b.WriteString("    classDef gateway fill:#f39c12,color:white\n")
	b.WriteString("    classDef mgs fill:#e74c3c,color:white\n")
	b.WriteString("    classDef mds fill:#3498db,color:white\n")
	b.WriteString("    classDef oss fill:#2ecc71,color:white\n")
	b.WriteString("    classDef sfa fill:#9b59b6,color:white\n")
	b.WriteString("    classDef client fill:#95a5a6,color:white\n")
	b.WriteString("    classDef generic fill:#bdc3c7\n")
	b.WriteString("\n")

	// Emit gateway node.
	fmt.Fprintf(&b, "    %s[\"%s<br/>MCP Gateway\"]:::gateway\n",
		sanitizeMermaidID(overview.GatewayNodeID), overview.GatewayNodeID)

	// Emit all discovered nodes.
	for _, node := range overview.Nodes {
		roleLabel := strings.Join(node.Roles, "+")
		cssClass := primaryRole(node.Roles)
		fmt.Fprintf(&b, "    %s[\"%s<br/>%s\"]:::%s\n",
			sanitizeMermaidID(node.NodeID), node.NodeID, strings.ToUpper(roleLabel), cssClass)
	}

	b.WriteString("\n")

	// Emit edges.
	for _, edge := range overview.Edges {
		fmt.Fprintf(&b, "    %s ---|%.2f| %s\n",
			sanitizeMermaidID(edge.From), edge.Impedance, sanitizeMermaidID(edge.To))
	}

	return b.String()
}

// primaryRole returns the first non-generic role for CSS class selection.
func primaryRole(roles []string) string {
	priority := []string{"sfa", "mgs", "mds", "oss", "client"}
	for _, p := range priority {
		for _, r := range roles {
			if r == p {
				return p
			}
		}
	}
	return "generic"
}

// sanitizeMermaidID replaces characters that are invalid in Mermaid node IDs.
func sanitizeMermaidID(id string) string {
	replacer := strings.NewReplacer("-", "_", ".", "_", " ", "_")
	return replacer.Replace(id)
}
