package transport

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

// sshAddr implements net.Addr for SSH connections, providing the
// underlying TCP address with an "ssh" network identifier.
type sshAddr struct {
	addr string
}

func (a sshAddr) Network() string { return "ssh" }
func (a sshAddr) String() string  { return a.addr }

// SSHConn wraps an SSH session's stdin/stdout pipes as a net.Conn.
// This is the transport that activates when connecting to an existing
// mesh peer via SSH, or when a deployment session transitions from
// SFTP/exec into the mesh byte stream.
//
// Thread safety: Read and Write operate on separate pipes and are
// safe for concurrent use from different goroutines. Close is idempotent.
type SSHConn struct {
	stdin  io.WriteCloser
	stdout io.Reader

	session *ssh.Session
	client  *ssh.Client

	localAddr  string
	remoteAddr string

	closed atomic.Bool
	once   sync.Once
	// closeErr captures the error from the first (and only) close operation.
	closeErr error
}

// Read reads data from the SSH session's stdout pipe.
// Returns an error if the connection has been closed.
func (c *SSHConn) Read(b []byte) (int, error) {
	if c.closed.Load() {
		return 0, ErrClosed
	}
	return c.stdout.Read(b)
}

// Write writes data to the SSH session's stdin pipe.
// Returns an error if the connection has been closed.
func (c *SSHConn) Write(b []byte) (int, error) {
	if c.closed.Load() {
		return 0, ErrClosed
	}
	return c.stdin.Write(b)
}

// Close closes the SSH session, stdin pipe, and the underlying SSH client
// connection. Close is idempotent — subsequent calls return the same error
// as the first call without side effects.
func (c *SSHConn) Close() error {
	c.once.Do(func() {
		c.closed.Store(true)

		var firstErr error

		// Close stdin pipe first to signal the remote side.
		if err := c.stdin.Close(); err != nil && firstErr == nil {
			firstErr = err
		}

		// Close the session (signals EOF on the channel).
		if err := c.session.Close(); err != nil && firstErr == nil {
			// Session close often returns "EOF" — that's expected.
			if err != io.EOF {
				firstErr = err
			}
		}

		// Close the underlying SSH connection.
		if err := c.client.Close(); err != nil && firstErr == nil {
			firstErr = err
		}

		c.closeErr = firstErr
	})
	return c.closeErr
}

// LocalAddr returns the local network address with network "ssh".
func (c *SSHConn) LocalAddr() net.Addr {
	return sshAddr{addr: c.localAddr}
}

// RemoteAddr returns the remote network address with network "ssh".
func (c *SSHConn) RemoteAddr() net.Addr {
	return sshAddr{addr: c.remoteAddr}
}

// SetDeadline returns ErrDeadlinesNotSupported. SSH channels do not
// support OS-level deadlines. Timeout behavior is handled upstream
// by context cancellation at the membrane/yamux layer.
func (c *SSHConn) SetDeadline(_ time.Time) error {
	return ErrDeadlinesNotSupported
}

// SetReadDeadline returns ErrDeadlinesNotSupported.
func (c *SSHConn) SetReadDeadline(_ time.Time) error {
	return ErrDeadlinesNotSupported
}

// SetWriteDeadline returns ErrDeadlinesNotSupported.
func (c *SSHConn) SetWriteDeadline(_ time.Time) error {
	return ErrDeadlinesNotSupported
}

// DialSSH establishes an SSH connection to the given address, opens a session,
// and returns the session's stdin/stdout as a net.Conn. This is the outbound
// SSH transport for both fleet communication and the deployment path.
//
// The returned SSHConn wraps the session's pipes — data written to the conn
// appears on the remote session's stdin, and data from the remote's stdout
// appears on reads. This is how deployed mesh nodes communicate: the SSH
// session that launches the binary becomes the first mesh connection.
//
// The caller provides a pre-configured *ssh.ClientConfig with authentication
// methods and host key validation. In the mesh, these come from the vault.
func DialSSH(ctx context.Context, addr string, config *ssh.ClientConfig) (net.Conn, error) {
	// Dial TCP with context support.
	var dialer net.Dialer
	tcpConn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("transport: ssh: dial %q: %w", addr, err)
	}

	// Perform SSH handshake over the TCP connection.
	sshConn, chans, reqs, err := ssh.NewClientConn(tcpConn, addr, config)
	if err != nil {
		if cerr := tcpConn.Close(); cerr != nil {
			return nil, fmt.Errorf("transport: ssh: handshake with %q: %w (close: %v)", addr, err, cerr)
		}
		return nil, fmt.Errorf("transport: ssh: handshake with %q: %w", addr, err)
	}

	client := ssh.NewClient(sshConn, chans, reqs)

	// Cleanup helper: closes resources in reverse order, checking each error.
	cleanup := func(stdin io.WriteCloser, session *ssh.Session, client *ssh.Client) error {
		var firstErr error
		if stdin != nil {
			if cerr := stdin.Close(); cerr != nil && firstErr == nil {
				firstErr = cerr
			}
		}
		if session != nil {
			if cerr := session.Close(); cerr != nil && firstErr == nil {
				firstErr = cerr
			}
		}
		if client != nil {
			if cerr := client.Close(); cerr != nil && firstErr == nil {
				firstErr = cerr
			}
		}
		return firstErr
	}

	// Open a session — this gives us the stdin/stdout pipes.
	session, err := client.NewSession()
	if err != nil {
		if cerr := cleanup(nil, nil, client); cerr != nil {
			return nil, fmt.Errorf("transport: ssh: open session on %q: %w (cleanup: %v)", addr, err, cerr)
		}
		return nil, fmt.Errorf("transport: ssh: open session on %q: %w", addr, err)
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		if cerr := cleanup(nil, session, client); cerr != nil {
			return nil, fmt.Errorf("transport: ssh: stdin pipe on %q: %w (cleanup: %v)", addr, err, cerr)
		}
		return nil, fmt.Errorf("transport: ssh: stdin pipe on %q: %w", addr, err)
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		if cerr := cleanup(stdin, session, client); cerr != nil {
			return nil, fmt.Errorf("transport: ssh: stdout pipe on %q: %w (cleanup: %v)", addr, err, cerr)
		}
		return nil, fmt.Errorf("transport: ssh: stdout pipe on %q: %w", addr, err)
	}

	// Request a shell to activate the session's I/O.
	// On mesh nodes, the remote binary will be serving on stdin/stdout,
	// so the "shell" is really just the mesh protocol.
	if err := session.Shell(); err != nil {
		if cerr := cleanup(stdin, session, client); cerr != nil {
			return nil, fmt.Errorf("transport: ssh: start shell on %q: %w (cleanup: %v)", addr, err, cerr)
		}
		return nil, fmt.Errorf("transport: ssh: start shell on %q: %w", addr, err)
	}

	return &SSHConn{
		stdin:      stdin,
		stdout:     stdout,
		session:    session,
		client:     client,
		localAddr:  tcpConn.LocalAddr().String(),
		remoteAddr: addr,
	}, nil
}

// sshTunnelConn wraps a net.Conn returned by ssh.Client.Dial, ensuring
// that closing the connection also closes the underlying SSH client and TCP socket.
type sshTunnelConn struct {
	net.Conn
	client   *ssh.Client
	closed   atomic.Bool
	once     sync.Once
	closeErr error
}

func (c *sshTunnelConn) Close() error {
	c.once.Do(func() {
		c.closed.Store(true)

		var firstErr error

		// Close the forwarded channel.
		if err := c.Conn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}

		// Close the underlying SSH connection.
		if err := c.client.Close(); err != nil && firstErr == nil {
			firstErr = err
		}

		c.closeErr = firstErr
	})
	return c.closeErr
}

// DialSSHTunnel establishes an SSH connection to sshAddr, and then uses
// SSH Port Forwarding (Direct-TCP/IP) to dial targetHost from the remote machine.
// This is used as a fallback to reach firewalled mesh nodes without executing
// remote processes.
func DialSSHTunnel(ctx context.Context, sshAddr string, targetHost string, config *ssh.ClientConfig) (net.Conn, error) {
	// Dial TCP with context support.
	var dialer net.Dialer
	tcpConn, err := dialer.DialContext(ctx, "tcp", sshAddr)
	if err != nil {
		return nil, fmt.Errorf("transport: ssh: dial %q: %w", sshAddr, err)
	}

	// Perform SSH handshake over the TCP connection.
	sshConn, chans, reqs, err := ssh.NewClientConn(tcpConn, sshAddr, config)
	if err != nil {
		if cerr := tcpConn.Close(); cerr != nil {
			return nil, fmt.Errorf("transport: ssh: handshake with %q: %w (close: %v)", sshAddr, err, cerr)
		}
		return nil, fmt.Errorf("transport: ssh: handshake with %q: %w", sshAddr, err)
	}

	client := ssh.NewClient(sshConn, chans, reqs)

	// Use SSH port forwarding to dial the target host from the remote machine.
	// Note: We don't use DialContext on the client as x/crypto/ssh Client doesn't support it directly,
	// but the underlying TCP connection handles the initial timeout.
	fwdConn, err := client.Dial("tcp", targetHost)
	if err != nil {
		if cerr := client.Close(); cerr != nil {
			return nil, fmt.Errorf("transport: ssh: dial tunnel to %q via %q: %w (close: %v)", targetHost, sshAddr, err, cerr)
		}
		return nil, fmt.Errorf("transport: ssh: dial tunnel to %q via %q: %w", targetHost, sshAddr, err)
	}

	return &sshTunnelConn{
		Conn:   fwdConn,
		client: client,
	}, nil
}
