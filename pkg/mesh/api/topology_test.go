package api

import (
	"testing"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/nucleus"
	"github.com/flipfloptech/cortex-mcp/pkg/mesh/routing"
)

// --- MeshTopology() ---

func TestNode_MeshTopology_EmptyMesh(t *testing.T) {
	t.Parallel()

	n := &Node{
		manifest: nucleus.NewManifest("node-1"),
		gradient: routing.NewGradientTable("node-1"),
		resolver: nucleus.NewResolver(),
		capIndex: routing.NewCapabilityIndex(),
		peers:    newPeerManager(),
	}

	snap := n.MeshTopology()
	if snap.NodeID != "node-1" {
		t.Fatalf("NodeID = %q, want %q", snap.NodeID, "node-1")
	}
	if snap.DirectPeers != 0 {
		t.Fatalf("DirectPeers = %d, want 0", snap.DirectPeers)
	}
	if snap.KnownNodes != 0 {
		t.Fatalf("KnownNodes = %d, want 0", snap.KnownNodes)
	}
	if snap.ResolverEntries != 0 {
		t.Fatalf("ResolverEntries = %d, want 0", snap.ResolverEntries)
	}
}

func TestNode_MeshTopology_WithRoutes(t *testing.T) {
	t.Parallel()

	n := &Node{
		manifest: nucleus.NewManifest("gateway"),
		gradient: routing.NewGradientTable("gateway"),
		resolver: nucleus.NewResolver(),
		capIndex: routing.NewCapabilityIndex(),
		peers:    newPeerManager(),
	}

	// Simulate 3 known routes (via gossip).
	n.gradient.AddDirectNeighbor("mds-01", 1.5)
	n.gradient.AddDirectNeighbor("oss-01", 2.0)
	n.gradient.UpdateRoute("compute-01", "mds-01", 3.5) // transitive route

	// Simulate 2 direct peers.
	n.peers.Add("mds-01", &peerConn{nodeID: "mds-01"})
	n.peers.Add("oss-01", &peerConn{nodeID: "oss-01"})

	// Simulate 4 resolver entries.
	n.resolver.AddEntry("mds-01", []string{"10.0.1.1"}, 0)
	n.resolver.AddEntry("oss-01", []string{"10.0.2.1"}, 0)
	n.resolver.AddEntry("compute-01", []string{"10.0.3.1"}, 0)
	n.resolver.AddEntry("admin", []string{"10.0.0.1"}, 0)

	// Simulate capabilities.
	n.capIndex.Update("mds-01", 1.5, []string{"tool:lustre_health", "tool:uptime"})
	n.capIndex.Update("oss-01", 2.0, []string{"tool:uptime"})

	snap := n.MeshTopology()

	if snap.NodeID != "gateway" {
		t.Fatalf("NodeID = %q, want %q", snap.NodeID, "gateway")
	}
	if snap.DirectPeers != 2 {
		t.Fatalf("DirectPeers = %d, want 2", snap.DirectPeers)
	}
	if snap.KnownNodes != 3 { // mds-01, oss-01, compute-01
		t.Fatalf("KnownNodes = %d, want 3", snap.KnownNodes)
	}
	if snap.ResolverEntries != 4 {
		t.Fatalf("ResolverEntries = %d, want 4", snap.ResolverEntries)
	}

	// Check node details.
	if len(snap.NodeDetails) != 3 {
		t.Fatalf("NodeDetails len = %d, want 3", len(snap.NodeDetails))
	}

	// Find mds-01 — should be a direct peer.
	var mds *NodeSummary
	for i := range snap.NodeDetails {
		if snap.NodeDetails[i].NodeID == "mds-01" {
			mds = &snap.NodeDetails[i]
		}
	}
	if mds == nil {
		t.Fatal("expected mds-01 in NodeDetails")
	}
	if !mds.IsDirect {
		t.Fatal("mds-01 should be a direct peer")
	}
	if mds.Impedance != 1.5 {
		t.Fatalf("mds-01 impedance = %f, want 1.5", mds.Impedance)
	}

	// Find compute-01 — should NOT be a direct peer.
	var compute *NodeSummary
	for i := range snap.NodeDetails {
		if snap.NodeDetails[i].NodeID == "compute-01" {
			compute = &snap.NodeDetails[i]
		}
	}
	if compute == nil {
		t.Fatal("expected compute-01 in NodeDetails")
	}
	if compute.IsDirect {
		t.Fatal("compute-01 should NOT be a direct peer")
	}
}

// --- Bootstrap proxy URL roundtrip ---

func TestBootstrapFrame_ProxyURL_Roundtrip(t *testing.T) {
	t.Parallel()

	// Build: node with a proxy URL on a host.
	resolver := nucleus.NewResolver()
	resolver.AddEntryFull("behind-fw", []string{"10.0.1.1"}, 0, "https", "http://proxy.corp:8080")

	n := &Node{resolver: resolver}
	frame := n.buildBootstrapFrame()
	bf := frame.GetBootstrap()

	// The proto should carry the proxy URL.
	entry := bf.Hosts["behind-fw"]
	if entry == nil {
		t.Fatal("expected behind-fw in bootstrap")
	}
	if entry.ProxyUrl != "http://proxy.corp:8080" {
		t.Fatalf("ProxyUrl = %q, want %q", entry.ProxyUrl, "http://proxy.corp:8080")
	}

	// Handle: receiving node should merge the proxy URL.
	receiver := &Node{
		resolver: nucleus.NewResolver(),
	}
	receiver.handleBootstrapFrame(&peerConn{nodeID: "test"}, bf)

	entries := receiver.resolver.AllEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].ProxyURL != "http://proxy.corp:8080" {
		t.Fatalf("receiver ProxyURL = %q, want %q", entries[0].ProxyURL, "http://proxy.corp:8080")
	}
}

func TestBootstrapFrames_ProxyURL_Chunked(t *testing.T) {
	t.Parallel()

	resolver := nucleus.NewResolver()
	resolver.AddEntryFull("proxy-host", []string{"10.0.1.1"}, 0, "https", "http://proxy:3128")
	resolver.AddEntry("direct-host", []string{"10.0.2.1"}, 0)

	n := &Node{resolver: resolver}
	frames := n.buildBootstrapFrames()

	if len(frames) == 0 {
		t.Fatal("expected at least 1 bootstrap frame")
	}

	// Collect all hosts across chunks.
	allHosts := make(map[string]*nucleus.ResolverEntry)
	for _, frame := range frames {
		bf := frame.GetBootstrap()
		for hostname, he := range bf.Hosts {
			allHosts[hostname] = &nucleus.ResolverEntry{
				Hostname:  hostname,
				Addresses: he.Addresses,
				Transport: he.Transport,
				ProxyURL:  he.GetProxyUrl(),
			}
		}
	}

	if proxyHost, ok := allHosts["proxy-host"]; !ok || proxyHost.ProxyURL != "http://proxy:3128" {
		t.Fatalf("proxy-host entry missing or wrong proxy: %+v", allHosts["proxy-host"])
	}
	if directHost, ok := allHosts["direct-host"]; !ok || directHost.ProxyURL != "" {
		t.Fatalf("direct-host should have no proxy: %+v", allHosts["direct-host"])
	}
}
