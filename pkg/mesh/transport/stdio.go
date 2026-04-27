// Package transport wraps raw byte streams (stdin/stdout, HTTPS, SSH, HTTP
// CONNECT proxies) into abstract net.Conn interfaces. Upstream layers
// (membrane, routing) never know or care which transport is underneath.
package transport

import (
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// ErrDeadlinesNotSupported is returned by SetDeadline, SetReadDeadline,
// and SetWriteDeadline on transports that do not support OS-level deadlines
// (e.g., stdin/stdout pipes). Timeout behavior is handled upstream by
// context cancellation at the membrane/yamux layer.
var ErrDeadlinesNotSupported = errors.New("transport: deadlines not supported on stdio connections")

// ErrClosed is returned when Read or Write is called on a closed StdioConn.
var ErrClosed = errors.New("transport: connection closed")

// stdioAddr implements net.Addr for stdio connections.
// There is no meaningful network address for a pipe — the address is
// purely informational for logging and diagnostics.
type stdioAddr struct{}

func (stdioAddr) Network() string { return "stdio" }
func (stdioAddr) String() string  { return "stdin/stdout" }

// StdioConn wraps an io.Reader and io.Writer into a net.Conn.
// This is the transport that activates when a mesh binary is deployed via SSH —
// the deploying SSH session's stdin/stdout byte streams become the first
// mesh connection.
//
// Thread safety: Read and Write are safe for concurrent use from different
// goroutines. Close is idempotent via sync.Once.
type StdioConn struct {
	reader io.Reader
	writer io.Writer

	closed atomic.Bool
	once   sync.Once
	// closeErr captures the error from the first (and only) close operation.
	closeErr error
}

// NewStdioConn creates a new net.Conn backed by the given reader and writer.
// The reader typically wraps os.Stdin and the writer wraps os.Stdout for
// SSH-deployed mesh nodes.
func NewStdioConn(in io.Reader, out io.Writer) net.Conn {
	return &StdioConn{
		reader: in,
		writer: out,
	}
}

// Read reads data from the underlying reader (stdin side).
// Returns ErrClosed if the connection has been closed.
func (c *StdioConn) Read(b []byte) (int, error) {
	if c.closed.Load() {
		return 0, ErrClosed
	}
	return c.reader.Read(b)
}

// Write writes data to the underlying writer (stdout side).
// Returns ErrClosed if the connection has been closed.
func (c *StdioConn) Write(b []byte) (int, error) {
	if c.closed.Load() {
		return 0, ErrClosed
	}
	return c.writer.Write(b)
}

// Close closes both the underlying reader and writer if they implement
// io.Closer. Close is idempotent — subsequent calls return the same error
// as the first call without side effects.
func (c *StdioConn) Close() error {
	c.once.Do(func() {
		c.closed.Store(true)

		var firstErr error

		if closer, ok := c.reader.(io.Closer); ok {
			if err := closer.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}

		if closer, ok := c.writer.(io.Closer); ok {
			if err := closer.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}

		c.closeErr = firstErr
	})
	return c.closeErr
}

// LocalAddr returns the local network address. For stdio connections,
// this is a synthetic address with network "stdio".
func (c *StdioConn) LocalAddr() net.Addr {
	return stdioAddr{}
}

// RemoteAddr returns the remote network address. For stdio connections,
// this is identical to LocalAddr — there is no meaningful remote address
// for a pipe.
func (c *StdioConn) RemoteAddr() net.Addr {
	return stdioAddr{}
}

// SetDeadline returns ErrDeadlinesNotSupported. Stdin/stdout pipes do not
// support OS-level deadlines. Timeout behavior is handled upstream by
// context cancellation at the membrane/yamux layer.
func (c *StdioConn) SetDeadline(_ time.Time) error {
	return ErrDeadlinesNotSupported
}

// SetReadDeadline returns ErrDeadlinesNotSupported.
func (c *StdioConn) SetReadDeadline(_ time.Time) error {
	return ErrDeadlinesNotSupported
}

// SetWriteDeadline returns ErrDeadlinesNotSupported.
func (c *StdioConn) SetWriteDeadline(_ time.Time) error {
	return ErrDeadlinesNotSupported
}
