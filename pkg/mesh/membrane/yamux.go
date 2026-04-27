package membrane

import (
	"context"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/hashicorp/yamux"
)

// MultiplexedConn wraps a yamux.Session with a control channel.
//
// Two construction modes:
//
//   - Single-connection (legacy): Created by Multiplex(). Control is a yamux
//     Stream 0 sharing the same TCP connection as data streams. Subject to
//     HOL blocking under data saturation.
//
//   - Dual-connection (preferred): Created by MultiplexDual(). Control is a
//     raw net.Conn on its own TCP socket. Data gets yamux on a separate
//     TCP socket. True control plane isolation — zero HOL blocking.
//
// Callers use mc.Control for control traffic and mc.Session for data.
// They do not need to know which mode was used.
type MultiplexedConn struct {
	// Session is the yamux session for opening/accepting data streams.
	Session *yamux.Session

	// Control is the control plane channel.
	// In single-connection mode: a yamux Stream 0.
	// In dual-connection mode: a raw mTLS net.Conn on its own TCP socket.
	Control net.Conn

	// isDual indicates whether this MultiplexedConn was created in
	// dual-connection mode. Affects Close() behavior.
	isDual bool

	// controlConn is the underlying control TCP connection in dual mode.
	// In single-connection mode this is nil (control is a yamux stream
	// and closing the Session handles cleanup).
	controlConn net.Conn
}

// Close closes the control channel and the yamux session.
//
// In dual mode, controlConn is closed separately since it's an independent
// TCP connection. In single mode, the control stream is a yamux stream
// that is cleaned up by closing the session.
func (mc *MultiplexedConn) Close() error {
	var firstErr error

	if mc.Control != nil {
		if err := mc.Control.Close(); err != nil {
			firstErr = err
		}
	}

	if mc.Session != nil {
		if err := mc.Session.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	// In dual mode, also close the underlying control TCP connection.
	// The control net.Conn (mc.Control) may be the same as controlConn
	// or a TLS wrapper — closing both ensures full cleanup.
	if mc.isDual && mc.controlConn != nil {
		if err := mc.controlConn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	return firstErr
}

// MeshConfig returns a yamux configuration tuned for the cortex-mesh
// control plane. This replaces yamux.DefaultConfig() with settings that
// mitigate head-of-line (HOL) blocking between Stream 0 (control) and
// data streams (Stream 1+).
//
// # HOL Blocking Mitigation
//
// Yamux does NOT support stream prioritization. Stream 0 (control plane:
// gossip, Sonar, credential exchange) competes equally with data streams
// (Stitch forwarding, gRPC tool calls) for the TCP write buffer. When
// data streams saturate the TCP window, control plane latency spikes,
// causing stale gradient routes and false routing purges.
//
// Yamux enforces MaxStreamWindowSize >= 256KB, so we cannot reduce
// per-stream buffering below that floor. Instead, we tighten timeouts
// and keepalives to detect and recover from congestion faster:
//
//   - KeepAliveInterval: 10s (default: 30s). Detects dead peers before
//     route entries expire (PurgeStale at 3× gossip = 9s).
//   - ConnectionWriteTimeout: 5s (default: 10s). Fails congested writes
//     faster, preventing goroutine pile-ups waiting on full TCP buffers.
//   - MaxStreamWindowSize: 256KB (yamux minimum). Limits per-stream
//     buffering to the smallest allowed value, creating more natural
//     interleaving between control and data frames.
//
// # Architectural Note
//
// For true control plane isolation, the long-term solution is separate
// TCP connections (one for control, one for data) or a transport that
// supports stream prioritization. The config tuning here limits damage
// but cannot eliminate HOL blocking entirely within a single yamux session.
func MeshConfig() *yamux.Config {
	return &yamux.Config{
		AcceptBacklog: 256,

		// Tight keepalive: detect dead peers before gradient routes expire.
		// Gossip interval is 3s, PurgeStale fires at 3× = 9s. Keepalive
		// at 10s ensures dead connections are caught within one purge cycle.
		EnableKeepAlive:   true,
		KeepAliveInterval: 10 * time.Second,

		// Aggressive write timeout: fail fast on congested TCP paths.
		// Default 10s is too generous — a blocked write for 5s already
		// indicates a severely degraded connection.
		ConnectionWriteTimeout: 5 * time.Second,

		// Pin to yamux minimum. This is the smallest window yamux allows
		// (256KB). Each data stream can buffer at most this much before
		// back-pressure kicks in, limiting how much TCP bandwidth a single
		// stream can monopolize.
		MaxStreamWindowSize: 256 * 1024,

		// Standard timeouts for stream lifecycle.
		StreamCloseTimeout: 5 * time.Minute,
		StreamOpenTimeout:  75 * time.Second,

		// Suppress yamux logs — we use slog for operational output.
		LogOutput: io.Discard,
	}
}

// Multiplex wraps a secured net.Conn in yamux, establishing Stream 0
// for the control plane. The isServer parameter determines which side
// opens vs accepts Stream 0:
//   - Server (isServer=true): accepts Stream 0 (waits for client to open)
//   - Client (isServer=false): opens Stream 0 (initiates the control channel)
//
// After Multiplex completes, both sides have a matching control stream
// and a yamux session for additional data streams.
func Multiplex(conn net.Conn, isServer bool) (*MultiplexedConn, error) {
	if conn == nil {
		return nil, fmt.Errorf("membrane: multiplex: nil connection")
	}

	// Use mesh-tuned config for HOL blocking mitigation.
	conf := MeshConfig()

	zstdConn, err := NewZstdConn(conn)
	if err != nil {
		return nil, fmt.Errorf("membrane: multiplex: zstd init: %w", err)
	}

	var session *yamux.Session

	if isServer {
		session, err = yamux.Server(zstdConn, conf)
	} else {
		session, err = yamux.Client(zstdConn, conf)
	}
	if err != nil {
		_ = zstdConn.Close()
		return nil, fmt.Errorf("membrane: yamux session: %w", err)
	}

	// Establish Stream 0 (control plane).
	var control net.Conn
	if isServer {
		// Server waits for the client to open Stream 0.
		control, err = session.Accept()
		if err != nil {
			_ = session.Close()
			return nil, fmt.Errorf("membrane: accept control stream: %w", err)
		}
	} else {
		// Client opens Stream 0.
		control, err = session.Open()
		if err != nil {
			_ = session.Close()
			return nil, fmt.Errorf("membrane: open control stream: %w", err)
		}
	}

	return &MultiplexedConn{
		Session: session,
		Control: control,
	}, nil
}

// Upgrade is the full membrane pipeline: mTLS handshake → yamux multiplexing.
// It takes a raw transport net.Conn and returns a fully secured, multiplexed
// connection with the control stream established.
//
// This is the primary entry point for the membrane layer — transport code
// calls Upgrade immediately after establishing a connection.
func Upgrade(ctx context.Context, conn net.Conn, cfg *Config, isServer bool) (*MultiplexedConn, error) {
	// Phase 1: mTLS handshake.
	var tlsConn net.Conn
	var err error

	if isServer {
		tlsConn, err = HandshakeServer(ctx, conn, cfg)
	} else {
		tlsConn, err = HandshakeClient(ctx, conn, cfg)
	}
	if err != nil {
		return nil, fmt.Errorf("membrane: upgrade: %w", err)
	}

	// Phase 2: yamux multiplexing.
	mc, err := Multiplex(tlsConn, isServer)
	if err != nil {
		_ = tlsConn.Close()
		return nil, fmt.Errorf("membrane: upgrade: %w", err)
	}

	return mc, nil
}

// MultiplexDual creates a MultiplexedConn from two separate connections:
// one dedicated to the control plane and one for the data plane.
//
// Unlike Multiplex (which wraps a single connection in yamux and reserves
// Stream 0 for control), MultiplexDual gives the control plane its own
// TCP socket. This eliminates head-of-line blocking entirely — the control
// plane has an independent TCP buffer and congestion window.
//
// The controlConn is used directly (raw framed reads/writes).
// The dataConn is wrapped in a yamux session for multiplexed data streams.
func MultiplexDual(controlConn, dataConn net.Conn, isServer bool) (*MultiplexedConn, error) {
	if controlConn == nil {
		return nil, fmt.Errorf("membrane: multiplex dual: nil control connection")
	}
	if dataConn == nil {
		return nil, fmt.Errorf("membrane: multiplex dual: nil data connection")
	}

	zstdControlConn, err := NewZstdConn(controlConn)
	if err != nil {
		return nil, fmt.Errorf("membrane: multiplex dual: control zstd init: %w", err)
	}

	zstdDataConn, err := NewZstdConn(dataConn)
	if err != nil {
		_ = zstdControlConn.Close()
		return nil, fmt.Errorf("membrane: multiplex dual: data zstd init: %w", err)
	}

	// yamux on the data connection only — no Stream 0 reservation.
	conf := MeshConfig()

	var session *yamux.Session

	if isServer {
		session, err = yamux.Server(zstdDataConn, conf)
	} else {
		session, err = yamux.Client(zstdDataConn, conf)
	}
	if err != nil {
		_ = zstdControlConn.Close()
		_ = zstdDataConn.Close()
		return nil, fmt.Errorf("membrane: yamux data session: %w", err)
	}

	return &MultiplexedConn{
		Session:     session,
		Control:     zstdControlConn,
		isDual:      true,
		controlConn: controlConn,
	}, nil
}

// UpgradeDual is the full membrane pipeline for dual-connection mode:
// mTLS handshake on both connections → MultiplexDual.
//
// Both connections must present certificates signed by the same Site CA.
// The caller is responsible for ensuring both connections are from the
// same peer (either by pairing them before this call or by validating
// PeerNodeID equality after handshake).
func UpgradeDual(ctx context.Context, controlConn, dataConn net.Conn, cfg *Config, isServer bool) (*MultiplexedConn, error) {
	// Phase 1: mTLS handshake on both connections.
	var controlTLS, dataTLS net.Conn
	var err error

	if isServer {
		controlTLS, err = HandshakeServer(ctx, controlConn, cfg)
	} else {
		controlTLS, err = HandshakeClient(ctx, controlConn, cfg)
	}
	if err != nil {
		return nil, fmt.Errorf("membrane: upgrade dual: control handshake: %w", err)
	}

	if isServer {
		dataTLS, err = HandshakeServer(ctx, dataConn, cfg)
	} else {
		dataTLS, err = HandshakeClient(ctx, dataConn, cfg)
	}
	if err != nil {
		_ = controlTLS.Close()
		return nil, fmt.Errorf("membrane: upgrade dual: data handshake: %w", err)
	}

	// Phase 2: MultiplexDual — control is raw, data gets yamux.
	mc, err := MultiplexDual(controlTLS, dataTLS, isServer)
	if err != nil {
		_ = controlTLS.Close()
		_ = dataTLS.Close()
		return nil, fmt.Errorf("membrane: upgrade dual: %w", err)
	}

	return mc, nil
}
