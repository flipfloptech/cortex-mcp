package api

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"net"
	"time"

	"crypto/ed25519"

	pb "github.com/flipfloptech/cortex-mcp/pkg/mesh/proto"
	"github.com/flipfloptech/cortex-mcp/pkg/mesh/routing"
	"github.com/flipfloptech/cortex-mcp/pkg/mesh/tools"
	"github.com/flipfloptech/cortex-mcp/pkg/mesh/vault"
)

// peerFrameBudget is the per-peer memory budget for in-flight control frame
// reads. 2MB allows ~8 concurrent 256KB frames per peer, which is more than
// sufficient since control loops read frames sequentially. This replaces the
// global 16MB budget that could be exhausted by dense peer topologies (H-3).
const peerFrameBudget int64 = 2 * 1024 * 1024

// --- Control loop ---

// startControlLoop begins the per-peer control plane processing loop.
// It reads protobuf-encoded ControlFrames from the peer's control
// stream (Stream 0) and dispatches them to the appropriate handler.
//
// This runs as a background goroutine — one per peer.
func (n *Node) startControlLoop(pc *peerConn) {
	go func() {
		for {
			frame, err := pc.frameReader.ReadFrame(pc.control)
			if err != nil {
				// Control stream closed — peer disconnected.
				pc.deathOnce.Do(func() { go n.handlePeerDeath(pc) })
				return
			}

			switch {
			case frame.GetGossip() != nil:
				n.handleGossipFrame(pc, frame.GetGossip())
			case frame.GetWhoHas() != nil:
				n.handleWhoHasFrame(pc, frame.GetWhoHas())
			case frame.GetIHave() != nil:
				n.handleIHaveFrame(pc, frame.GetIHave())
			case frame.GetBootstrap() != nil:
				n.handleBootstrapFrame(pc, frame.GetBootstrap())
			case frame.GetCredRequest() != nil:
				n.handleCredentialRequestFrame(pc, frame.GetCredRequest())
			case frame.GetCredGrant() != nil:
				n.handleCredentialGrantFrame(pc, frame.GetCredGrant())
			case frame.GetRelayOpen() != nil:
				n.handleRelayOpenFrame(pc, frame.GetRelayOpen())
			case frame.GetRelayAccept() != nil:
				n.handleRelayAcceptFrame(pc, frame.GetRelayAccept())
			default:
				slog.Warn("control: unknown frame type", "peer", pc.nodeID)
			}
		}
	}()
}

// handleGossipFrame processes an incoming gradient gossip vector.
// In addition to route updates, it extracts capability data for the
// CapabilityIndex — enabling transitive capability propagation.
func (n *Node) handleGossipFrame(_ *peerConn, gossip *pb.GossipFrame) {
	// Convert protobuf gossip to routing.GossipVector (routes).
	routes := make([]routing.GossipRoute, 0, len(gossip.Routes))
	for targetID, cost := range gossip.Routes {
		routes = append(routes, routing.GossipRoute{
			TargetID: targetID,
			Cost:     cost,
		})
	}

	vec := routing.GossipVector{
		FromNodeID:    gossip.FromNode,
		FromImpedance: gossip.FromImpedance,
		Routes:        routes,
	}

	n.gradient.ProcessGossip(vec)

	// --- Capability index updates ---

	// Update sender's own capabilities.
	if len(gossip.FromCapabilities) > 0 {
		n.capIndex.Update(gossip.FromNode, gossip.FromImpedance, gossip.FromCapabilities)
	}

	// Update transitively learned capabilities from other nodes.
	for nodeID, caps := range gossip.GetNodeCapabilities() {
		if nodeID == n.manifest.NodeID() {
			continue // skip our own capabilities
		}
		if caps != nil && len(caps.Capabilities) > 0 {
			// Use the route cost as impedance for the remote node if available.
			var impedance float64
			if routeCost, ok := gossip.Routes[nodeID]; ok {
				impedance = routeCost + gossip.FromImpedance
			} else {
				// Node not in routes — use a high default impedance.
				impedance = 100.0
			}
			n.capIndex.Update(nodeID, impedance, caps.Capabilities)
		}
	}
}

// handleWhoHasFrame processes an incoming Sonar WhoHas broadcast.
// If this node has the capability, it sends an IHave response back.
// Either way, the frame is forwarded to all other peers (if not already seen
// and the hop budget is not exhausted).
func (n *Node) handleWhoHasFrame(pc *peerConn, whoHas *pb.WhoHasFrame) {
	// Don't process our own frames that bounced back.
	if whoHas.OriginNode == n.manifest.NodeID() {
		return
	}

	// Convert to routing.WhoHasFrame for dedup + capability check.
	routingWhoHas := &routing.WhoHasFrame{
		UUID:       whoHas.Uuid,
		Capability: whoHas.Capability,
		OriginID:   whoHas.OriginNode,
		MaxHops:    whoHas.MaxHops, // P1-2: carry hop budget
	}

	// Process: check dedup + local capabilities in one call.
	resp, firstSeen := n.sonar.ProcessWhoHas(routingWhoHas)

	if !firstSeen {
		return // duplicate — already processed
	}

	// Respond with IHave if we have the capability.
	if resp != nil {
		iHaveFrame := &pb.ControlFrame{
			Payload: &pb.ControlFrame_IHave{
				IHave: &pb.IHaveFrame{
					Uuid:       resp.UUID,
					NodeId:     resp.NodeID,
					Impedance:  resp.Impedance,
					OriginNode: whoHas.OriginNode, // H-1: carry origin for gradient routing
				},
			},
		}
		// Send IHave back to the peer who sent the WhoHas.
		pc.sendControl(iHaveFrame)
	}

	// P1-2: Forward to neighbors if hop budget allows.
	// ProcessWhoHas already handled UUID dedup (firstSeen gate above).
	// We only need the hop-budget and self-origin checks here.
	if routingWhoHas.OriginID == n.manifest.NodeID() {
		return // don't forward our own frames (shouldn't happen — caught above)
	}
	if routingWhoHas.MaxHops == 0 {
		return // hop budget exhausted
	}

	// Decrement hop budget before relay — the next receiver sees one
	// fewer hop remaining.
	fwdFrame := &pb.ControlFrame{
		Payload: &pb.ControlFrame_WhoHas{
			WhoHas: &pb.WhoHasFrame{
				Uuid:       whoHas.Uuid,
				Capability: whoHas.Capability,
				OriginNode: whoHas.OriginNode,
				MaxHops:    whoHas.MaxHops - 1,
			},
		},
	}
	n.broadcastExcept(pc.nodeID, fwdFrame)
}

// handleIHaveFrame processes an incoming Sonar IHave response.
// Routes it to the appropriate ResponseCollector if this node is the origin.
// If not, routes the IHave toward the origin via the gradient table (H-1).
// Previous behavior was to broadcast IHave to all peers, causing O(N²) amplification.
func (n *Node) handleIHaveFrame(_ *peerConn, iHave *pb.IHaveFrame) {
	// Convert to routing.IHaveFrame for the sonar collector.
	routingIHave := routing.IHaveFrame{
		UUID:      iHave.Uuid,
		NodeID:    iHave.NodeId,
		Impedance: iHave.Impedance,
	}

	// Try to deliver to a local collector (we're the origin).
	if n.sonar.DeliverResponse(routingIHave) {
		return
	}

	// H-1: Route toward origin via gradient table instead of broadcasting.
	// This prevents O(N²) IHave flooding in large meshes.
	if iHave.OriginNode == "" {
		return // no origin — legacy frame, drop silently
	}

	// Look up the best route toward the origin.
	route, ok := n.gradient.BestRoute(iHave.OriginNode)
	if !ok {
		return // no route to origin — drop (will be retried on next Sonar)
	}

	// Forward to the next-hop peer toward the origin.
	fwdFrame := &pb.ControlFrame{
		Payload: &pb.ControlFrame_IHave{
			IHave: iHave,
		},
	}
	n.sendToPeer(route.NextHop, fwdFrame)
}

// sendToPeer sends a control frame to a specific peer by nodeID.
// Returns false if the peer is not connected (silently dropped).
func (n *Node) sendToPeer(nodeID string, frame *pb.ControlFrame) bool {
	pc, ok := n.peers.Get(nodeID)
	if !ok {
		return false
	}
	return pc.sendControl(frame)
}

// broadcastExcept sends a protobuf control frame to all peers except the excluded one.
func (n *Node) broadcastExcept(excludeNodeID string, frame *pb.ControlFrame) {
	sent, dropped := broadcastExceptPeer(n.peers, excludeNodeID, frame)
	if dropped > 0 {
		slog.Debug("api: broadcastExcept drops (telemetry)", "dropped", dropped, "sent", sent)
	}
}

// broadcastAll sends a protobuf control frame to all connected peers.
func (n *Node) broadcastAll(frame *pb.ControlFrame) {
	sent, dropped := broadcastAllPeers(n.peers, frame)
	if dropped > 0 {
		slog.Debug("api: broadcastAll drops (telemetry)", "dropped", dropped, "sent", sent)
	}
}

// --- Sonar: broadcast capability discovery ---

// Sonar triggers a mesh-wide broadcast for a capability.
// It creates a WhoHas frame, broadcasts it to all peers, and
// collects IHave responses until the context is canceled or times out.
//
// Returns AgentInfo for each responding node, sorted by impedance
// (lowest first).
func (n *Node) Sonar(ctx context.Context, capability string) ([]tools.AgentInfo, error) {
	// Create the WhoHas frame with hop budget (P1-2).
	whoHas := n.sonar.CreateWhoHas(capability, n.MaxSonarHops())

	// Start collecting responses.
	collector := n.sonar.StartCollection(whoHas.UUID)
	defer n.sonar.StopCollection(whoHas.UUID) // prevent collector map leak (L-2)

	// Broadcast to all peers via protobuf.
	frame := &pb.ControlFrame{
		Payload: &pb.ControlFrame_WhoHas{
			WhoHas: &pb.WhoHasFrame{
				Uuid:       whoHas.UUID,
				Capability: whoHas.Capability,
				OriginNode: whoHas.OriginID,
				MaxHops:    whoHas.MaxHops,
			},
		},
	}
	n.broadcastAll(frame)

	// Collect responses until context cancellation/timeout.
	iHaves := collector.Collect(ctx)

	// Convert to AgentInfo.
	results := make([]tools.AgentInfo, 0, len(iHaves))
	for _, ih := range iHaves {
		results = append(results, tools.AgentInfo{
			NodeID:    ih.NodeID,
			Impedance: ih.Impedance,
		})
	}

	return results, nil
}

// sendGossipToAll emits a gossip vector (routes + capabilities) to all
// connected peers. Called by the gossip ticker at GossipInterval.
//
// The ControlFrame is marshaled to bytes once, then the pre-marshaled
// bytes are sent to all peers via broadcastAllRaw. This eliminates
// redundant protobuf marshaling — the previous code marshaled the same
// frame N times (once per peer in each writer goroutine).
func (n *Node) sendGossipToAll() {
	// Age out stale capability entries before snapshotting the index.
	// This bounds the index by freshness rather than by entry count —
	// the TTL floor that makes additive capability ingest safe at
	// scale (P1-3). A negative TTL disables purging.
	if n.capabilityStaleTTL > 0 {
		n.capIndex.PurgeStale(n.capabilityStaleTTL)
	}

	// Age out stale route entries from the gradient routing table.
	// Routes expire if not updated within 3× the gossip interval.
	n.gradient.PurgeStale(3 * n.gossipInterval)

	// Build GossipInput with capability data.
	input := routing.GossipInput{
		LocalCapabilities: n.manifest.Capabilities(),
		KnownCapabilities: n.capIndex.Snapshot(),
	}

	gossip := n.gradient.GenerateGossip(n.manifest.Impedance(), input)

	// Convert routing.GossipVector to protobuf.
	routes := make(map[string]float64, len(gossip.Routes))
	for _, r := range gossip.Routes {
		routes[r.TargetID] = r.Cost
	}

	// Convert node capabilities to protobuf format.
	var nodeCaps map[string]*pb.NodeCapabilities
	if len(gossip.NodeCapabilities) > 0 {
		nodeCaps = make(map[string]*pb.NodeCapabilities, len(gossip.NodeCapabilities))
		for nodeID, caps := range gossip.NodeCapabilities {
			nodeCaps[nodeID] = &pb.NodeCapabilities{
				Capabilities: caps,
			}
		}
	}

	frame := &pb.ControlFrame{
		Payload: &pb.ControlFrame_Gossip{
			Gossip: &pb.GossipFrame{
				FromNode:         gossip.FromNodeID,
				Routes:           routes,
				FromImpedance:    gossip.FromImpedance,
				FromCapabilities: gossip.FromCapabilities,
				NodeCapabilities: nodeCaps,
			},
		},
	}

	// Marshal once.
	data, err := pb.MarshalFrame(frame)
	if err != nil {
		slog.Error("api: marshal gossip frame", "error", err)
		return
	}

	// Write pre-marshaled bytes to all peers — zero re-marshal.
	sent, dropped := broadcastAllRaw(n.peers, data)
	if dropped > 0 {
		slog.Debug("api: gossip broadcast drops (telemetry)", "dropped", dropped, "sent", sent)
	}
}

// StartGossipTicker begins periodic gossip emission in a background goroutine.
// Gossip vectors are sent to all connected peers at the given interval.
// The ticker runs until the context is canceled.
//
// Jitter (P2-2): the first tick is delayed by a random offset in [0, interval)
// to break fleet-wide phase-lock from simultaneous boot. Subsequent ticks
// are jittered by ±10% of the interval.
func (n *Node) StartGossipTicker(ctx context.Context, interval time.Duration) {
	go func() {
		// First tick: random delay in [0, interval) to desynchronize
		// nodes that boot at the same wall-clock time.
		firstDelay := jitteredFirstDelay(interval)
		select {
		case <-ctx.Done():
			return
		case <-n.ctx.Done():
			return
		case <-time.After(firstDelay):
			n.sendGossipToAll()
		}

		// Subsequent ticks: interval ± 10% jitter.
		for {
			nextTick := jitteredInterval(interval)
			select {
			case <-ctx.Done():
				return
			case <-n.ctx.Done():
				return
			case <-time.After(nextTick):
				n.sendGossipToAll()
			}
		}
	}()
}

// gossipJitterFraction is the ± fraction applied to subsequent gossip ticks.
// 0.10 means ±10% of the base interval.
const gossipJitterFraction = 0.10

// jitteredFirstDelay returns a random duration in [0, interval) for the
// first gossip tick. This breaks fleet-wide phase-lock when all nodes
// boot simultaneously.
func jitteredFirstDelay(interval time.Duration) time.Duration {
	if interval <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(interval)))
}

// jitteredInterval returns a duration in [interval*(1-f), interval*(1+f)]
// where f = gossipJitterFraction (default 10%). This prevents nodes from
// remaining phase-locked after the initial desynchronization.
func jitteredInterval(interval time.Duration) time.Duration {
	if interval <= 0 {
		return 0
	}
	base := float64(interval)
	jitter := base * gossipJitterFraction
	// rand.Float64() is in [0, 1) → scale to [-1, 1) → multiply by jitter.
	offset := (rand.Float64()*2 - 1) * jitter
	return time.Duration(base + offset)
}

// --- Peer integration ---

// initPeerControlPlane handles post-connection setup for a new peer:
// registers the peer as a direct neighbor in the gradient table
// and starts the control loop goroutine.
func (n *Node) initPeerControlPlane(pc *peerConn) {
	// Register the peer as a direct neighbor.
	n.gradient.AddDirectNeighbor(pc.nodeID, 1.0) // default impedance until gossip arrives

	// Send bootstrap frame with our resolver cache in a goroutine.
	// sendBootstrapReliable retries with backoff on queue-full, so it
	// must not block initPeerControlPlane. It exits on peer death or
	// context cancellation.
	go n.sendBootstrapReliable(pc)

	// Start reading control frames from this peer.
	n.startControlLoop(pc)
}

// AllPeers is a convenience accessor for the peer list from control stream.
func (n *Node) AllPeers() []net.Conn {
	peers := n.peers.All()
	conns := make([]net.Conn, len(peers))
	for i, pc := range peers {
		conns[i] = pc.control
	}
	return conns
}

// handleCredentialRequestFrame processes an incoming point-to-point credential request.
func (n *Node) handleCredentialRequestFrame(pc *peerConn, req *pb.CredentialRequestFrame) {
	if n.vault == nil {
		return // Node not configured with credentials
	}

	vaultReq := vault.CredentialRequest{
		HostPattern:     req.HostPattern,
		RequesterPubKey: ed25519.PublicKey(req.RequesterPubKey),
		Nonce:           req.Nonce,
		RequestedAt:     req.RequestedAt,
	}

	grant, err := n.vault.HandleRequest(vaultReq)
	if err != nil {
		slog.Debug("api: vault rejected request", "peer", pc.nodeID, "error", err)
		return
	}
	if grant == nil {
		// Valid request, but no matching credentials.
		return
	}

	// Send grant back to requester on the same peer channel
	grantFrame := &pb.ControlFrame{
		Payload: &pb.ControlFrame_CredGrant{
			CredGrant: &pb.CredentialGrantFrame{
				SealedCredential: grant.SealedCredential,
			},
		},
	}
	pc.sendControl(grantFrame)
}

// handleCredentialGrantFrame routes a received credential grant to local waiters.
func (n *Node) handleCredentialGrantFrame(pc *peerConn, grant *pb.CredentialGrantFrame) {
	n.credResolversMu.Lock()
	defer n.credResolversMu.Unlock()

	res := CredentialGrantResult{
		Frame:     grant,
		SenderPub: pc.peerPubKey,
	}

	// Fan out to all pending resolvers. The actual requester will successfully
	// unseal it and validate the nonce, other pending resolvers will fail unseal
	// and continue waiting.
	for _, ch := range n.credResolvers {
		select {
		case ch <- res:
		default:
		}
	}
}

// handleRelayOpenFrame is called on the intermediary/target node when receiving an open request.
func (n *Node) handleRelayOpenFrame(pc *peerConn, req *pb.RelayOpenFrame) {
	// 1. Dial target_node_id
	// Note: using context.Background() as GrpcDialer handles its own internal timeouts
	// and the wave collapse should not be bound to the control frame's read context.
	targetConn, err := n.GrpcDialer(context.Background(), req.TargetNodeId)
	if err != nil {
		// Send RelayAcceptFrame back to Originator with error
		acceptFrame := &pb.ControlFrame{
			Payload: &pb.ControlFrame_RelayAccept{
				RelayAccept: &pb.RelayAcceptFrame{
					CircuitId: req.CircuitId,
					Success:   false,
					Error:     err.Error(),
				},
			},
		}
		pc.sendControl(acceptFrame)
		return
	}

	// 2. Store targetConn in pendingCircuits
	n.relayMutex.Lock()
	n.pendingCircuits[req.CircuitId] = targetConn
	n.relayMutex.Unlock()

	// 3. Send RelayAcceptFrame back to Originator with success
	acceptFrame := &pb.ControlFrame{
		Payload: &pb.ControlFrame_RelayAccept{
			RelayAccept: &pb.RelayAcceptFrame{
				CircuitId: req.CircuitId,
				Success:   true,
			},
		},
	}
	pc.sendControl(acceptFrame)
}

// handleRelayAcceptFrame is called on the Originator node when the circuit is established.
func (n *Node) handleRelayAcceptFrame(pc *peerConn, req *pb.RelayAcceptFrame) {
	n.relayMutex.RLock()
	waiter, exists := n.relayWaiters[req.CircuitId]
	n.relayMutex.RUnlock()

	// 1. Notify the waiting Originator GrpcDialer
	if exists {
		// Non-blocking send in case the dialer already timed out
		select {
		case waiter <- req:
		default:
		}
	}
}
