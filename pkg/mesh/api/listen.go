package api

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/nucleus"
)

// Listen starts a TCP listener on the given address and accepts inbound
// mesh connections. Each accepted connection is run through AddPeer
// (mTLS handshake → yamux multiplexing) as the TLS server.
//
// The listener's address is automatically registered in the node's
// resolver so that other nodes can discover it via gossip/bootstrap.
// This enables autonomous reconnection — isolated nodes dial back to
// known listener addresses without consumer intervention.
//
// The accept loop runs until the context is cancelled or the listener
// is closed. Use addr ":0" to bind to a random available port.
func (n *Node) Listen(ctx context.Context, addr string) (net.Listener, error) {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("api: listen %s: %w", addr, err)
	}

	// Store the actual bound address (important when using ":0").
	n.listenAddr = lis.Addr().String()

	// Register our listen address in the resolver only if it's routable.
	// We MUST NOT register an unspecified IP (0.0.0.0 or [::]) because it's
	// unroutable for other peers. The correct routable IP is discovered
	// dynamically during the mTLS handshake when a peer connects to us.
	host, _, err := net.SplitHostPort(n.listenAddr)
	if err == nil {
		ip := net.ParseIP(host)
		if ip != nil && ip.IsUnspecified() {
			slog.Debug("api: skipping resolver registration for unspecified listen address", "addr", n.listenAddr)
		} else {
			n.resolver.AddEntry(n.manifest.NodeID(), []string{n.listenAddr}, 0)
		}
	} else {
		n.resolver.AddEntry(n.manifest.NodeID(), []string{n.listenAddr}, 0)
	}

	slog.Info("api: listener started", "addr", n.listenAddr, "nodeID", n.manifest.NodeID())

	go n.acceptLoop(ctx, lis)

	return lis, nil
}

// ListenAddr returns the address this node is listening on, or empty
// if Listen has not been called.
func (n *Node) ListenAddr() string {
	return n.listenAddr
}

// acceptLoop accepts incoming TCP connections and processes them as
// mesh peers. Each connection goes through the full AddPeer pipeline
// (mTLS → yamux → control plane). Runs until context cancellation
// or listener close.
func (n *Node) acceptLoop(ctx context.Context, lis net.Listener) {
	for {
		conn, err := lis.Accept()
		if err != nil {
			// Check if we're shutting down.
			select {
			case <-ctx.Done():
				return
			case <-n.ctx.Done():
				return
			default:
				// Listener closed or permanent error — exit the loop.
				slog.Warn("api: accept error", "error", err)
				return
			}
		}

		go func() {
			if err := n.AddPeer(ctx, conn, true); err != nil {
				slog.Warn("api: inbound peer handshake failed",
					"remote", conn.RemoteAddr(), "error", err)
				if cerr := conn.Close(); cerr != nil {
					slog.Debug("api: close rejected conn", "error", cerr)
				}
			}
		}()
	}
}

// defaultDialer is the built-in reconnect dialer. It dials a target
// via TCP and runs the connection through AddPeer (mTLS → yamux).
//
// This keeps all mesh communication logic inside the library — the
// consumer never needs to know about transport details for reconnection.
func (n *Node) defaultDialer(ctx context.Context, target nucleus.DialTarget) (string, error) {
	// Don't dial ourselves.
	if target.Hostname == n.manifest.NodeID() {
		return "", fmt.Errorf("api: refusing to dial self (%s)", target.Hostname)
	}

	// TCP dial to the target address.
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", target.Address)
	if err != nil {
		return "", fmt.Errorf("api: dial %s (%s): %w", target.Hostname, target.Address, err)
	}

	// Run through the membrane pipeline as client (we're initiating).
	if err := n.AddPeer(ctx, conn, false); err != nil {
		if cerr := conn.Close(); cerr != nil {
			slog.Debug("api: close failed dial conn", "error", cerr)
		}
		return "", fmt.Errorf("api: connect %s: %w", target.Hostname, err)
	}

	return target.Hostname, nil
}
