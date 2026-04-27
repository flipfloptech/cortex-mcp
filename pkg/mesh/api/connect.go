package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/yamux"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/membrane"
	pb "github.com/flipfloptech/cortex-mcp/pkg/mesh/proto"
	"github.com/flipfloptech/cortex-mcp/pkg/mesh/routing"
	"github.com/flipfloptech/cortex-mcp/pkg/mesh/transport"
)

// ErrPeerDead is a sentinel error indicating that a peer's yamux session
// is no longer functional. Callers can check errors.Is(err, ErrPeerDead)
// to distinguish retryable mesh routing failures from permanent errors.
//
// When GrpcDialer encounters this condition, it automatically attempts
// a fast-path gradient lookup for an alternative route before returning.
var ErrPeerDead = errors.New("peer session dead")

// ErrMultiHopRequired is a sentinel error indicating that the target node
// is reachable (present in the gradient routing table) but requires a
// multi-hop relay that is not yet implemented.
//
// This is semantically distinct from ErrPeerDead — ErrPeerDead means
// "retry later, peer churn in progress" while ErrMultiHopRequired means
// "topology is fine, protocol feature not built yet". Callers must not
// retry blindly on this error.
var ErrMultiHopRequired = errors.New("multi-hop relay required but not implemented")

const (
	MagicRelay = byte(0xFF) // Invalid for HTTP/2 preface, does not conflict with 0x00 tools protocol MSB
	UUIDLength = 36         // Length of stringified UUID
)

// SetMembraneConfig sets the mTLS configuration for this node.
// Must be called before AddPeer, AcceptStdio, or any transport methods.
func (n *Node) SetMembraneConfig(cfg *membrane.Config) {
	n.membraneCfg = cfg
}

// AddPeer takes a raw net.Conn, runs it through the membrane pipeline
// (mTLS handshake → yamux multiplexing), and registers the peer.
//
// isServer determines the handshake role:
//   - true: this node acts as the TLS server (accepting side)
//   - false: this node acts as the TLS client (initiating side)
//
// After AddPeer returns, the peer is fully connected with:
//   - Control stream (Stream 0) for gossip/sonar
//   - Data streams available for gRPC traffic
func (n *Node) AddPeer(ctx context.Context, conn net.Conn, isServer bool) error {
	if n.membraneCfg == nil {
		return fmt.Errorf("api: membrane config not set (call SetMembraneConfig first)")
	}

	// Phase 1: mTLS handshake — extract peer's NodeID from the TLS cert.
	var tlsConn net.Conn
	var err error
	if isServer {
		tlsConn, err = membrane.HandshakeServer(ctx, conn, n.membraneCfg)
	} else {
		tlsConn, err = membrane.HandshakeClient(ctx, conn, n.membraneCfg)
	}
	if err != nil {
		return fmt.Errorf("api: add peer: %w", err)
	}

	// Extract peer's NodeID from TLS certificate BEFORE yamux wraps it.
	peerID := membrane.PeerNodeID(tlsConn)
	if peerID == "" {
		peerID = conn.RemoteAddr().String()
	}

	// Phase 2: yamux multiplexing over the TLS connection.
	mc, err := membrane.Multiplex(tlsConn, isServer)
	if err != nil {
		if cerr := tlsConn.Close(); cerr != nil {
			slog.Warn("api: close tls conn after multiplex failure", "error", cerr)
		}
		return fmt.Errorf("api: add peer: multiplex: %w", err)
	}

	peerPubKey := membrane.PeerPubKey(tlsConn)

	pc := newPeerConnWithWriter(peerID, mc.Control, mc.Session, peerPubKey)

	n.peers.Add(peerID, pc)

	// Initialize the control plane for this peer.
	n.initPeerControlPlane(pc)

	// Start accepting data streams in the background for the gRPC listener.
	go n.acceptDataStreams(pc)

	// Notify consumer.
	n.peerConnected(peerID)

	return nil
}

// AddPeerDual takes two raw net.Conns — one for the control plane and one
// for the data plane — and establishes a peer with true control plane
// isolation.
//
// Unlike AddPeer (which multiplexes both planes over a single connection),
// AddPeerDual gives the control plane its own TCP socket. This eliminates
// head-of-line blocking: data stream saturation cannot affect control
// plane latency.
//
// Both connections go through independent mTLS handshakes. The peer's
// NodeID is extracted from both certificates and must match (rejects
// mismatched identities).
//
// After AddPeerDual returns:
//   - Control traffic flows over controlConn (raw mTLS, no yamux)
//   - Data streams (gRPC, Stitch) flow over dataConn (mTLS → yamux)
func (n *Node) AddPeerDual(ctx context.Context, controlConn, dataConn net.Conn, isServer bool) error {
	if n.membraneCfg == nil {
		return fmt.Errorf("api: membrane config not set (call SetMembraneConfig first)")
	}

	// Phase 1: mTLS handshake on control connection.
	var controlTLS, dataTLS net.Conn
	var err error

	if isServer {
		controlTLS, err = membrane.HandshakeServer(ctx, controlConn, n.membraneCfg)
	} else {
		controlTLS, err = membrane.HandshakeClient(ctx, controlConn, n.membraneCfg)
	}
	if err != nil {
		return fmt.Errorf("api: add peer dual: control handshake: %w", err)
	}

	// Phase 2: mTLS handshake on data connection.
	if isServer {
		dataTLS, err = membrane.HandshakeServer(ctx, dataConn, n.membraneCfg)
	} else {
		dataTLS, err = membrane.HandshakeClient(ctx, dataConn, n.membraneCfg)
	}
	if err != nil {
		_ = controlTLS.Close()
		return fmt.Errorf("api: add peer dual: data handshake: %w", err)
	}

	// Phase 3: Validate same PeerID on both connections.
	controlPeerID := membrane.PeerNodeID(controlTLS)
	dataPeerID := membrane.PeerNodeID(dataTLS)

	if controlPeerID == "" {
		controlPeerID = controlConn.RemoteAddr().String()
	}
	if dataPeerID == "" {
		dataPeerID = dataConn.RemoteAddr().String()
	}

	if controlPeerID != dataPeerID {
		_ = controlTLS.Close()
		_ = dataTLS.Close()
		return fmt.Errorf("api: add peer dual: identity mismatch: control=%q data=%q", controlPeerID, dataPeerID)
	}

	peerID := controlPeerID

	// Phase 4: MultiplexDual — control is raw, data gets yamux.
	mc, err := membrane.MultiplexDual(controlTLS, dataTLS, isServer)
	if err != nil {
		_ = controlTLS.Close()
		_ = dataTLS.Close()
		return fmt.Errorf("api: add peer dual: multiplex: %w", err)
	}

	peerPubKey := membrane.PeerPubKey(controlTLS)

	pc := newPeerConnWithWriter(peerID, mc.Control, mc.Session, peerPubKey)
	pc.controlConn = controlTLS // Save for clean shutdown in handlePeerDeath.

	n.peers.Add(peerID, pc)

	// Initialize the control plane for this peer.
	n.initPeerControlPlane(pc)

	// Start accepting data streams in the background for the gRPC listener.
	go n.acceptDataStreams(pc)

	// Notify consumer.
	n.peerConnected(peerID)

	return nil
}

// AcceptStdio accepts a mesh connection from stdin/stdout.
// This is called on deployed nodes where the SSH session becomes
// the first mesh connection.
//
// # HOL Blocking (M-2)
//
// AcceptStdio uses single-connection mode: control and data streams
// share the same yamux session over one byte stream. Under heavy data
// load, control frames (gossip, sonar) compete with data frames for
// TCP bandwidth. Mitigations in place:
//   - Per-write controlWriteDeadline (5s): prevents control writes from
//     blocking indefinitely when the TCP buffer is saturated.
//   - yamux MeshConfig: MaxStreamWindowSize=256KB, tight keepalives.
//   - Per-peer FrameReader budget (H-3): prevents shared budget exhaustion.
//
// For true HOL elimination, use AddPeerDual with separate connections
// for control and data planes (e.g., two SSH channels or TCP sockets).
func (n *Node) AcceptStdio(in io.Reader, out io.Writer) error {
	conn := transport.NewStdioConn(in, out)
	return n.AddPeer(n.ctx, conn, true)
}

// GrpcDialer opens a new yamux data stream to the target node.
// The returned net.Conn can be used with gRPC's WithContextDialer
// for making RPC calls to the target.
//
// If the direct peer's yamux session is dead (session shutdown,
// keepalive timeout, connection reset), GrpcDialer performs a
// fast-path gradient lookup for an alternative route. The error
// is wrapped with ErrPeerDead so callers can identify retryable
// mesh routing failures via errors.Is(err, ErrPeerDead).
//
// Currently supports direct peers only. Multi-hop routing (via gradient
// table and relay handshake) will be added in the protobuf phase.
func (n *Node) GrpcDialer(ctx context.Context, targetNodeID string) (net.Conn, error) {
	// Check context before work.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("api: grpc dialer to %q: %w", targetNodeID, err)
	}

	// Try direct peer first.
	if pc, ok := n.peers.Get(targetNodeID); ok {
		stream, err := pc.session.Open()
		if err != nil {
			if !isSessionDead(err) {
				// Non-session-death error — don't retry.
				return nil, fmt.Errorf("api: open stream to %q: %w", targetNodeID, err)
			}

			// Session is dead. Fast-path gradient retry.
			slog.Warn("api: peer session dead, attempting gradient reroute",
				"target", targetNodeID, "error", err)

			return n.grpcDialerFallback(ctx, targetNodeID, err)
		}
		return stream, nil
	}

	// Check gradient routing table for multi-hop relay.
	route, ok := n.gradient.BestRoute(targetNodeID)
	if !ok {
		return nil, fmt.Errorf("api: no route to node %q", targetNodeID)
	}

	nextHopPC, ok := n.peers.Get(route.NextHop)
	if !ok {
		return nil, fmt.Errorf("api: next hop %q for %q not connected", route.NextHop, targetNodeID)
	}

	return n.dialRelayCircuit(ctx, nextHopPC, targetNodeID)
}

// grpcDialerFallback attempts to reroute a failed direct-peer dial
// through the gradient table. If an alternative route exists through
// a different next-hop, the stream is opened through that peer.
//
// All errors from this path are wrapped with ErrPeerDead so callers
// can distinguish retryable mesh errors from permanent failures.
func (n *Node) grpcDialerFallback(ctx context.Context, targetNodeID string, origErr error) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("api: reroute %q: %w: %w", targetNodeID, ErrPeerDead, err)
	}

	route, ok := n.gradient.BestRoute(targetNodeID)
	if !ok {
		return nil, fmt.Errorf("api: %w: no route to %q after session death: %w", ErrPeerDead, targetNodeID, origErr)
	}

	// Try to open a stream through the next-hop peer.
	nextHopPC, ok := n.peers.Get(route.NextHop)
	if !ok {
		return nil, fmt.Errorf("api: %w: next-hop %q for %q not connected: %w", ErrPeerDead, route.NextHop, targetNodeID, origErr)
	}

	// The next-hop is a direct peer — we can open a stream.
	if route.NextHop != targetNodeID {
		return n.dialRelayCircuit(ctx, nextHopPC, targetNodeID)
	}

	// The gradient route points to a direct peer (e.g., the same node
	// is connected through a different neighbor, or the route hasn't
	// been purged yet). Try the stream.
	stream, err := nextHopPC.session.Open()
	if err != nil {
		return nil, fmt.Errorf("api: %w: reroute stream to %q via %q: %w", ErrPeerDead, targetNodeID, route.NextHop, err)
	}

	slog.Info("api: rerouted stream after peer death",
		"target", targetNodeID, "via", route.NextHop, "cost", route.TotalCost)
	return stream, nil
}

// dialRelayCircuit performs the multi-hop wave collapse handshake and stream initialization.
func (n *Node) dialRelayCircuit(ctx context.Context, nextHopPC *peerConn, targetNodeID string) (net.Conn, error) {
	// 1. Generate Circuit ID
	circuitID := uuid.New().String()

	// Setup waiter channel
	waitCh := make(chan *pb.RelayAcceptFrame, 1)
	n.relayMutex.Lock()
	n.relayWaiters[circuitID] = waitCh
	n.relayMutex.Unlock()

	// Cleanup waiter on exit
	defer func() {
		n.relayMutex.Lock()
		delete(n.relayWaiters, circuitID)
		n.relayMutex.Unlock()
	}()

	// 2. Send RelayOpenFrame
	openFrame := &pb.ControlFrame{
		Payload: &pb.ControlFrame_RelayOpen{
			RelayOpen: &pb.RelayOpenFrame{
				TargetNodeId: targetNodeID,
				CircuitId:    circuitID,
			},
		},
	}
	if !nextHopPC.sendControl(openFrame) {
		return nil, fmt.Errorf("api: failed to send RelayOpenFrame to %q", nextHopPC.nodeID)
	}

	// 3. Wait for RelayAcceptFrame or context timeout
	select {
	case accept := <-waitCh:
		if !accept.Success {
			return nil, fmt.Errorf("api: relay to %q rejected by %q: %s", targetNodeID, nextHopPC.nodeID, accept.Error)
		}
	case <-ctx.Done():
		return nil, fmt.Errorf("api: relay handshake timeout: %w", ctx.Err())
	}

	// 4. Open Yamux stream
	stream, err := nextHopPC.session.Open()
	if err != nil {
		return nil, fmt.Errorf("api: open relay stream: %w", err)
	}

	// Write the Magic Header + circuit_id
	payload := make([]byte, 1+UUIDLength)
	payload[0] = MagicRelay
	copy(payload[1:], circuitID)

	if _, err := stream.Write(payload); err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("api: failed to write relay magic header: %w", err)
	}

	return stream, nil
}

// isSessionDead returns true if the error indicates a yamux session
// is no longer functional and the peer should be considered dead.
func isSessionDead(err error) bool {
	// Check for specific yamux sentinel errors.
	if errors.Is(err, yamux.ErrSessionShutdown) ||
		errors.Is(err, yamux.ErrKeepAliveTimeout) ||
		errors.Is(err, yamux.ErrConnectionReset) ||
		errors.Is(err, yamux.ErrRemoteGoAway) ||
		errors.Is(err, yamux.ErrConnectionWriteTimeout) {
		return true
	}
	return false
}

// PeerCount returns the number of connected peers.
func (n *Node) PeerCount() int {
	return n.peers.Count()
}

// acceptDataStreams accepts incoming yamux data streams from a peer
// and delivers them to the gRPC listener or stitches them to relay circuits.
func (n *Node) acceptDataStreams(pc *peerConn) {
	for {
		stream, err := pc.session.Accept()
		if err != nil {
			// Session closed — peer died or disconnected.
			pc.deathOnce.Do(func() { go n.handlePeerDeath(pc) })
			return
		}

		go func(s net.Conn) {
			pc := &prefixConn{Conn: s}
			pc.prefix = pc.magic[:]

			// Protect against slowloris attacks on stream creation
			_ = s.SetReadDeadline(time.Now().Add(5 * time.Second))
			if _, err := io.ReadFull(s, pc.prefix); err != nil {
				_ = s.Close()
				return
			}
			_ = s.SetReadDeadline(time.Time{}) // reset deadline

			if pc.prefix[0] == MagicRelay {
				// Read the 36-byte circuit_id
				circuitIDBytes := make([]byte, UUIDLength)
				_ = s.SetReadDeadline(time.Now().Add(5 * time.Second))
				if _, err := io.ReadFull(s, circuitIDBytes); err != nil {
					_ = s.Close()
					return
				}
				_ = s.SetReadDeadline(time.Time{})
				circuitID := string(circuitIDBytes)

				// Look up the pending circuit
				n.relayMutex.RLock()
				targetConn, exists := n.pendingCircuits[circuitID]
				n.relayMutex.RUnlock()

				if !exists {
					slog.Warn("api: received relay stream for unknown circuit", "circuit_id", circuitID)
					_ = s.Close()
					return
				}

				// Consume the circuit map entry
				n.relayMutex.Lock()
				delete(n.pendingCircuits, circuitID)
				n.relayMutex.Unlock()

				// Route to Stitch
				routing.Stitch(s, targetConn)
			} else {
				// It's a gRPC stream (HTTP/2 preface starts with 'P' = 0x50).
				// Prepend the magic byte back and deliver to listener.
				if n.grpcLis != nil {
					n.grpcLis.Deliver(pc)
				} else {
					if err := s.Close(); err != nil {
						slog.Debug("api: close undelivered stream", "error", err)
					}
				}
			}
		}(stream)
	}
}

// upgradeAndHold is a test helper that runs the membrane upgrade
// and keeps the connection alive by blocking until context cancellation.
func upgradeAndHold(ctx context.Context, conn net.Conn, cfg *membrane.Config) error {
	mc, err := membrane.Upgrade(ctx, conn, cfg, false)
	if err != nil {
		return err
	}

	// Block until context is done, keeping the yamux session alive.
	go func() {
		<-ctx.Done()
		if err := mc.Close(); err != nil {
			// expected on shutdown
			return
		}
	}()

	return nil
}

// prefixConn wraps a net.Conn and prepends a byte slice to its Read method.
type prefixConn struct {
	magic  [1]byte
	prefix []byte
	net.Conn
}

func (p *prefixConn) Read(b []byte) (int, error) {
	if len(p.prefix) > 0 {
		n := copy(b, p.prefix)
		p.prefix = p.prefix[n:]
		return n, nil
	}
	return p.Conn.Read(b)
}
