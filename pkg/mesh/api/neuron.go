// Package api provides the public Neuron interface — the top-level
// orchestrator for the mesh. It composes the transport, membrane,
// nucleus, and routing layers into a unified mesh node.
//
// The Neuron interface is deliberately minimal: transport + routing only.
// Tool registration and MCP gateway are separate composable layers.
package api

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"log/slog"
	"math"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/membrane"
	"github.com/flipfloptech/cortex-mcp/pkg/mesh/nucleus"
	pb "github.com/flipfloptech/cortex-mcp/pkg/mesh/proto"
	"github.com/flipfloptech/cortex-mcp/pkg/mesh/routing"
	"github.com/flipfloptech/cortex-mcp/pkg/mesh/vault"
)

// CredentialGrantResult pairs a received grant frame with its sender's public key.
type CredentialGrantResult struct {
	Frame     *pb.CredentialGrantFrame
	SenderPub ed25519.PublicKey
}

// NodeConfig defines the configuration for creating a mesh node.
type NodeConfig struct {
	// NodeID is this node's unique identifier in the mesh.
	// Typically derived from the TLS certificate CommonName.
	NodeID string

	// ProtocolVersion is the wire-level protocol version of this node.
	ProtocolVersion uint16

	// ApplicationVersion tracks the application version (e.g. Git short commit).
	ApplicationVersion string

	// KnownHosts seeds the resolver with hostname-to-address mappings.
	KnownHosts map[string][]string

	// Events configures lifecycle callbacks (all optional).
	Events NodeEvents

	// Reconnect configures automatic reconnection behavior.
	// Zero value uses sensible defaults (enabled, 1s initial, 30s max, 5m timeout).
	Reconnect ReconnectPolicy

	// GossipInterval configures how often gossip vectors are emitted to peers.
	// Zero value defaults to 3 seconds.
	// TTS analysis shows 3s is sustainable for HPC networks at 100k nodes.
	// For WAN or constrained environments, use larger values (e.g., 10s).
	GossipInterval time.Duration

	// Vault provides the encrypted credential store for this node.
	// Required to support CredentialRequest/Grant mesh protocols.
	// If nil, this node will not handle credential delegations.
	Vault *vault.Vault

	// CapabilityStaleTTL bounds how long a gossiped capability entry
	// stays in the local CapabilityIndex without being refreshed by a
	// subsequent gossip tick. This is the TTL floor that makes additive
	// capability ingest safe at scale — entries age out by freshness
	// rather than accumulating forever.
	//
	// Zero defaults to 3 × GossipInterval, matching the gradient route
	// TTL convention. A negative value disables purging entirely (useful
	// for tests or for operators who explicitly manage index lifetime
	// by other means).
	CapabilityStaleTTL time.Duration

	// MaxCapabilityLength bounds an individual capability string in
	// bytes at ingest time. Strings longer than this are dropped. Zero
	// uses routing.DefaultMaxCapabilityLength. This is a per-peer byte
	// budget, not a fleet-size cap.
	MaxCapabilityLength int

	// MaxCapabilitiesPerNode bounds how many capabilities are accepted
	// from a single peer per Update call. Zero uses
	// routing.DefaultMaxCapabilitiesPerNode. Like MaxCapabilityLength
	// this is a per-peer budget, not a fleet-size cap.
	MaxCapabilitiesPerNode int

	// MaxSonarHops overrides the hop ceiling stamped on outbound
	// WhoHas frames. Zero uses the default formula:
	// ceil(log2(max(peerCount, 32))) + 4, which grows as O(log N)
	// with fleet size. The hop budget is the correctness backbone
	// for flood termination (P1-2).
	MaxSonarHops uint32

	// SonarSeenTTL bounds how long a WhoHas UUID is retained in the
	// dedup set. Zero uses routing.defaultSeenTTL. The set's size is
	// bounded by observed WhoHas rate × TTL, NOT by fleet size.
	SonarSeenTTL time.Duration

	// Dialer is a custom dialer for mesh node reconnections.
	// If nil, the node defaults to a simple TCP direct dialer.
	// Consumers can use this to provide complex fallback mechanisms
	// (e.g., HTTP Proxies, SSH Tunnels, or deployment fallbacks).
	Dialer func(ctx context.Context, target nucleus.DialTarget) (net.Conn, error)
}

// peerConn represents a connected peer in the mesh.
// It holds the multiplexed connection and the control stream.
type peerConn struct {
	nodeID    string
	control   net.Conn       // Stream 0 control channel (or raw conn in dual mode)
	session   *yamux.Session // yamux session for data streams
	deathOnce sync.Once      // ensures handlePeerDeath fires exactly once

	// Extracted Ed25519 public key of the connected peer, used for NaCl box unsealing.
	peerPubKey ed25519.PublicKey

	// controlConn is the underlying control TCP connection in dual mode.
	// In single-connection mode this is nil (control is a yamux stream).
	// Used by handlePeerDeath for clean shutdown of the dedicated conn.
	controlConn net.Conn

	// frameReader provides per-peer memory budget for control frame reads (H-3).
	// Each peer gets an isolated 2MB budget. A malicious or chatty peer cannot
	// exhaust the global budget and starve other peers' control frames.
	frameReader *pb.FrameReader

	// Async control writer — prevents slow peers from blocking the control plane.
	writeCh       chan controlMsg // buffered outbound message queue
	writeMu       sync.RWMutex    // protects writeCh close vs send
	writerWg      *sync.WaitGroup // tracks writer goroutine for clean shutdown
	writerOnce    sync.Once       // prevents double-close of writeCh
	writerStopped atomic.Bool     // prevents send on closed channel

	// deadCh is closed when the peer is declared dead (via closeDead).
	// Blocking senders (e.g. sendBootstrapReliable) select on this to
	// exit promptly instead of retrying a dead peer indefinitely.
	deadCh   chan struct{}
	deadOnce sync.Once // ensures deadCh is closed exactly once
}

// dead returns a channel that is closed when the peer dies.
// Use in select statements to abort blocked operations on peer death.
func (pc *peerConn) dead() <-chan struct{} {
	return pc.deadCh
}

// closeDead signals that this peer has died. Idempotent via sync.Once.
// Called from handlePeerDeath to unblock any goroutines selecting on dead().
// Safe to call on peerConn instances where deadCh was not initialized
// (e.g., bare struct literals in tests).
func (pc *peerConn) closeDead() {
	if pc.deadCh == nil {
		return
	}
	pc.deadOnce.Do(func() {
		close(pc.deadCh)
	})
}

// peerManager manages the set of connected peers.
// Goroutine-safe — all methods can be called concurrently.
type peerManager struct {
	mu    sync.RWMutex
	peers map[string]*peerConn
}

func newPeerManager() *peerManager {
	return &peerManager{
		peers: make(map[string]*peerConn),
	}
}

func (pm *peerManager) Add(nodeID string, pc *peerConn) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.peers[nodeID] = pc
}

func (pm *peerManager) Remove(nodeID string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	delete(pm.peers, nodeID)
}

func (pm *peerManager) Get(nodeID string) (*peerConn, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	pc, ok := pm.peers[nodeID]
	return pc, ok
}

func (pm *peerManager) All() []*peerConn {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	result := make([]*peerConn, 0, len(pm.peers))
	for _, pc := range pm.peers {
		result = append(result, pc)
	}
	return result
}

// ForEach iterates over all peers while holding the read lock.
// The provided function is called for each peer.
// Do not perform blocking operations or call back into the peerManager
// from within the closure to avoid deadlocks.
func (pm *peerManager) ForEach(fn func(*peerConn)) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	for _, pc := range pm.peers {
		fn(pc)
	}
}

func (pm *peerManager) Count() int {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return len(pm.peers)
}

// Node is the concrete implementation of the mesh Neuron.
// It orchestrates the transport, membrane, nucleus, and routing layers.
type Node struct {
	ctx    context.Context
	cancel context.CancelFunc

	// Membrane configuration for mTLS.
	membraneCfg *membrane.Config

	// Core subsystems.
	manifest *nucleus.Manifest
	sonar    *routing.Sonar
	gradient *routing.GradientTable
	resolver *nucleus.Resolver
	capIndex *routing.CapabilityIndex

	// Peer management.
	peers *peerManager

	// gRPC integration.
	grpcLis *meshListener

	// Versions.
	protocolVersion    uint16
	applicationVersion string

	// Lifecycle events & reconnection.
	events         NodeEvents
	reconnect      ReconnectPolicy
	reconnLoop     *reconnectLoop
	gossipInterval time.Duration

	// Credential Delegation
	vault           *vault.Vault
	credResolversMu sync.Mutex
	credResolvers   map[string]chan CredentialGrantResult

	// Multi-Hop Relay
	relayMutex      sync.RWMutex
	pendingCircuits map[string]net.Conn                  // circuit_id -> Target Connection
	relayWaiters    map[string]chan *pb.RelayAcceptFrame // circuit_id -> Waiter channel for Originator's dialer

	// capabilityStaleTTL is the PurgeStale threshold applied on each
	// gossip tick. A negative value disables purging.
	capabilityStaleTTL time.Duration

	// maxSonarHops is the hop ceiling stamped on outbound WhoHas
	// frames. See MaxSonarHops() for the dynamic default.
	maxSonarHops uint32

	// Listener address (if Listen was called).
	listenAddr string

	// Custom dialer, defaults to nil (TCP).
	dialer func(ctx context.Context, target nucleus.DialTarget) (net.Conn, error)
}

// NewNode creates and initializes a mesh node with the given configuration.
// The node starts idle — no connections are established until transport
// methods (AcceptStdio, DialHTTPS, etc.) are called.
func NewNode(ctx context.Context, cfg NodeConfig) (*Node, error) {
	if cfg.NodeID == "" {
		return nil, fmt.Errorf("api: NodeID is required")
	}

	nodeCtx, cancel := context.WithCancel(ctx)

	manifest := nucleus.NewManifest(cfg.NodeID)
	resolver := nucleus.NewResolver()

	// Seed known hosts.
	if len(cfg.KnownHosts) > 0 {
		resolver.SeedKnownHosts(cfg.KnownHosts)
	}

	gossipInterval := cfg.GossipInterval
	if gossipInterval == 0 {
		gossipInterval = 3 * time.Second
	}

	// CapabilityStaleTTL: zero → 3× gossip; negative → disabled.
	capabilityStaleTTL := cfg.CapabilityStaleTTL
	if capabilityStaleTTL == 0 {
		capabilityStaleTTL = 3 * gossipInterval
	}

	capIndex := routing.NewCapabilityIndex()
	capIndex.SetLimits(routing.IngestLimits{
		MaxCapabilityLength:    cfg.MaxCapabilityLength,
		MaxCapabilitiesPerNode: cfg.MaxCapabilitiesPerNode,
	})

	// Build Sonar options.
	var sonarOpts []routing.SonarOption
	if cfg.SonarSeenTTL > 0 {
		sonarOpts = append(sonarOpts, routing.WithSeenTTL(cfg.SonarSeenTTL))
	}

	n := &Node{
		ctx:                nodeCtx,
		cancel:             cancel,
		manifest:           manifest,
		sonar:              routing.NewSonar(manifest, sonarOpts...),
		gradient:           routing.NewGradientTable(cfg.NodeID),
		resolver:           resolver,
		capIndex:           capIndex,
		peers:              newPeerManager(),
		grpcLis:            newMeshListener(),
		events:             cfg.Events,
		reconnect:          cfg.Reconnect,
		gossipInterval:     gossipInterval,
		vault:              cfg.Vault,
		credResolvers:      make(map[string]chan CredentialGrantResult),
		pendingCircuits:    make(map[string]net.Conn),
		relayWaiters:       make(map[string]chan *pb.RelayAcceptFrame),
		capabilityStaleTTL: capabilityStaleTTL,
		maxSonarHops:       cfg.MaxSonarHops,
		dialer:             cfg.Dialer,
		protocolVersion:    cfg.ProtocolVersion,
		applicationVersion: cfg.ApplicationVersion,
	}

	// Apply default reconnect policy if zero-valued.
	if n.reconnect == (ReconnectPolicy{}) {
		n.reconnect = DefaultReconnectPolicy()
	}

	// Create the reconnect loop.
	dialerFunc := n.defaultDialer
	if n.dialer != nil {
		dialerFunc = func(ctx context.Context, target nucleus.DialTarget) (string, error) {
			if target.Hostname == n.manifest.NodeID() {
				return "", fmt.Errorf("api: refusing to dial self (%s)", target.Hostname)
			}
			conn, err := n.dialer(ctx, target)
			if err != nil {
				return "", err
			}
			if err := n.AddPeer(ctx, conn, false); err != nil {
				if cerr := conn.Close(); cerr != nil {
					slog.Debug("api: close failed dial conn", "error", cerr)
				}
				return "", fmt.Errorf("api: connect %s: %w", target.Hostname, err)
			}
			return target.Hostname, nil
		}
	}
	n.reconnLoop = newReconnectLoop(n, n.reconnect, dialerFunc)

	return n, nil
}

// NodeID returns this node's unique identifier.
func (n *Node) NodeID() string {
	return n.manifest.NodeID()
}

// RegisterCapability advertises a capability to the mesh.
// Capabilities are used by Sonar for distributed discovery.
func (n *Node) RegisterCapability(capability string) {
	n.manifest.RegisterCapability(capability)
}

// HasCapability reports whether this node has the given capability.
func (n *Node) HasCapability(capability string) bool {
	return n.manifest.HasCapability(capability)
}

// GrpcListener returns a net.Listener that yields incoming mesh
// connections for use with a gRPC server. The listener receives
// yamux data streams from connected peers.
//
// This method always returns the same listener instance.
func (n *Node) GrpcListener() (net.Listener, error) {
	return n.grpcLis, nil
}

// Close shuts down the node, closing all peer connections and
// stopping all background goroutines.
func (n *Node) Close() error {
	// Stop reconnect loop before cancelling context.
	if n.reconnLoop != nil {
		n.reconnLoop.Stop()
	}

	n.cancel()

	if n.grpcLis != nil {
		if err := n.grpcLis.Close(); err != nil {
			return fmt.Errorf("api: close grpc listener: %w", err)
		}
	}

	return nil
}

// LookupCapability returns nodes offering a capability, using the local
// capability index populated by gossip. Zero network traffic — this is
// a pure local map read. Results are sorted by impedance (lowest first).
//
// Returns an empty slice (not nil) if no nodes match.
func (n *Node) LookupCapability(capability string) []routing.NodeCapEntry {
	return n.capIndex.Lookup(capability)
}

// LookupCapabilityWildcard returns nodes with capabilities matching a
// prefix (e.g., "tool:" matches all tool capabilities). Zero traffic.
func (n *Node) LookupCapabilityWildcard(prefix string) []routing.NodeCapEntry {
	return n.capIndex.LookupWildcard(prefix)
}

// CapabilityIndex returns the node's capability index for direct access.
func (n *Node) CapabilityIndex() *routing.CapabilityIndex {
	return n.capIndex
}

// GossipIntervalDuration returns the configured gossip interval.
func (n *Node) GossipIntervalDuration() time.Duration {
	return n.gossipInterval
}

// CapabilityStaleTTL returns the effective TTL applied to gossiped
// capability entries by PurgeStale on each gossip tick. A negative
// value means purging is disabled.
func (n *Node) CapabilityStaleTTL() time.Duration {
	return n.capabilityStaleTTL
}

// MaxSonarHops returns the hop ceiling stamped on outbound WhoHas
// frames. When the operator provides a non-zero NodeConfig.MaxSonarHops,
// that value is returned directly. Otherwise the default formula is:
//
//	ceil(log2(max(peerCount, 32))) + 4
//
// This grows as O(log N) with fleet size so flood termination scales
// logarithmically — never as a fixed count.
func (n *Node) MaxSonarHops() uint32 {
	if n.maxSonarHops != 0 {
		return n.maxSonarHops
	}
	return defaultSonarHops(n.peers.Count())
}

// SonarSeenTTL returns the TTL configured on the WhoHas dedup set.
func (n *Node) SonarSeenTTL() time.Duration {
	return n.sonar.SeenTTL()
}

// defaultSonarHops computes the hop ceiling from the current peer count.
// Formula: ceil(log2(max(peerCount, 32))) + 4.
// The floor of 32 ensures a minimum hop budget even for tiny meshes;
// the +4 headroom covers topology asymmetry and churn.
func defaultSonarHops(peerCount int) uint32 {
	base := peerCount
	if base < 32 {
		base = 32
	}
	return uint32(math.Ceil(math.Log2(float64(base)))) + 4
}

// TopologySnapshot is a point-in-time summary of the mesh as seen by this node.
// It is derived from local state only — no network traffic required.
type TopologySnapshot struct {
	// NodeID is this node's identity.
	NodeID string `json:"node_id"`

	// DirectPeers is the number of currently connected peers.
	DirectPeers int `json:"direct_peers"`

	// KnownNodes is the total number of distinct nodes in the gradient
	// routing table (directly connected + transitively learned via gossip).
	KnownNodes int `json:"known_nodes"`

	// ResolverEntries is the number of hosts in the resolver cache.
	ResolverEntries int `json:"resolver_entries"`

	// NodeDetails contains per-node detail for known nodes.
	NodeDetails []NodeSummary `json:"node_details,omitempty"`
}

// NodeSummary describes a single known node in the mesh topology.
type NodeSummary struct {
	// NodeID is the node's unique identifier.
	NodeID string `json:"node_id"`

	// Impedance is the total path cost to reach this node.
	Impedance float64 `json:"impedance"`

	// NextHop is the NodeID of the next hop toward this node.
	// For direct peers, NextHop equals NodeID.
	NextHop string `json:"next_hop"`

	// Capabilities lists the node's registered capabilities (from gossip).
	Capabilities []string `json:"capabilities,omitempty"`

	// IsDirect is true if this node is a directly connected peer.
	IsDirect bool `json:"is_direct"`
}

// MeshTopology returns a point-in-time snapshot of the mesh topology
// as seen by this node. All data is sourced locally — no network traffic.
//
// This powers built-in mesh intelligence tools (e.g., mesh_topology)
// and gives consumers hard numbers on deployment completeness.
func (n *Node) MeshTopology() TopologySnapshot {
	routes := n.gradient.AllRoutes()
	capSnap := n.capIndex.Snapshot()
	directPeers := n.peers.All()

	directSet := make(map[string]struct{}, len(directPeers))
	for _, pc := range directPeers {
		directSet[pc.nodeID] = struct{}{}
	}

	details := make([]NodeSummary, 0, len(routes))
	for nodeID, route := range routes {
		_, isDirect := directSet[nodeID]
		details = append(details, NodeSummary{
			NodeID:       nodeID,
			Impedance:    route.TotalCost,
			NextHop:      route.NextHop,
			Capabilities: capSnap[nodeID],
			IsDirect:     isDirect,
		})
	}

	return TopologySnapshot{
		NodeID:          n.manifest.NodeID(),
		DirectPeers:     len(directPeers),
		KnownNodes:      len(routes),
		ResolverEntries: len(n.resolver.AllEntries()),
		NodeDetails:     details,
	}
}
