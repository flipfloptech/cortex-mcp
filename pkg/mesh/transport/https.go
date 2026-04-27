package transport

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
)

// DialHTTPS establishes a TLS connection to the given address and returns
// the resulting net.Conn. This is the default outbound transport on
// routed/VPN'd networks (~99% of internal fleet communication).
//
// The caller provides a pre-configured *tls.Config with the appropriate
// root CAs and client certificates (for mTLS). In the mesh, these come
// from the vault — the Neuron implementation builds the tls.Config
// from vault contents, so callers of DialHTTPS never handle raw keys.
//
// The returned net.Conn is a *tls.Conn — it supports deadlines, addresses,
// and all standard net.Conn operations. Feed it into the membrane layer
// for yamux multiplexing.
func DialHTTPS(ctx context.Context, addr string, tlsConf *tls.Config) (net.Conn, error) {
	// Use a standard TCP dialer with context support.
	var dialer net.Dialer
	rawConn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("transport: https: dial %q: %w", addr, err)
	}

	// Upgrade to TLS. Clone the config to set ServerName without
	// mutating the caller's config.
	conf := tlsConf.Clone()
	if conf.ServerName == "" {
		// Extract hostname from addr (strip port).
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			// addr might not have a port — use as-is.
			host = addr
		}
		conf.ServerName = host
	}

	tlsConn := tls.Client(rawConn, conf)

	// Perform the TLS handshake with context support.
	// This surfaces certificate validation errors immediately rather
	// than deferring them to the first Read/Write.
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		if cerr := rawConn.Close(); cerr != nil {
			return nil, fmt.Errorf("transport: https: TLS handshake with %q: %w (close: %v)", addr, err, cerr)
		}
		return nil, fmt.Errorf("transport: https: TLS handshake with %q: %w", addr, err)
	}

	return tlsConn, nil
}

// ListenHTTPS creates a TLS listener on the given address. It returns
// a net.Listener that yields *tls.Conn on Accept().
//
// The listener is bound to the provided context: when the context is
// canceled, the listener is closed and any blocked Accept() calls
// return an error.
//
// The caller provides a *tls.Config with the server certificate and
// client CA pool (for mTLS enforcement). In production, these come
// from the vault.
func ListenHTTPS(ctx context.Context, addr string, tlsConf *tls.Config) (net.Listener, error) {
	// Bind TCP first, then wrap with TLS.
	lc := net.ListenConfig{}
	rawLn, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("transport: https: listen on %q: %w", addr, err)
	}

	tlsLn := tls.NewListener(rawLn, tlsConf)

	// Close the listener when context is canceled.
	go func() {
		<-ctx.Done()
		if err := tlsLn.Close(); err != nil {
			slog.Warn("transport: https: listener close on context cancel", "error", err)
		}
	}()

	return tlsLn, nil
}
