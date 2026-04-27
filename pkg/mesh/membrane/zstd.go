package membrane

import (
	"fmt"
	"net"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// ZstdConn wraps a net.Conn with zstd compression.
// It implements net.Conn, compressing all outgoing writes and
// decompressing all incoming reads.
type ZstdConn struct {
	net.Conn
	r *zstd.Decoder

	writeMu sync.Mutex
	w       *zstd.Encoder
	closed  bool
}

// NewZstdConn creates a new compressed connection.
func NewZstdConn(c net.Conn) (*ZstdConn, error) {
	r, err := zstd.NewReader(c)
	if err != nil {
		return nil, fmt.Errorf("membrane: zstd reader: %w", err)
	}

	// SpeedFastest minimizes latency.
	// WithEncoderConcurrency(1) prevents spawning excessive goroutines
	// per connection, which is critical for networks with many peers.
	w, err := zstd.NewWriter(c,
		zstd.WithEncoderLevel(zstd.SpeedFastest),
		zstd.WithEncoderConcurrency(1),
	)
	if err != nil {
		r.Close()
		return nil, fmt.Errorf("membrane: zstd writer: %w", err)
	}

	return &ZstdConn{
		Conn: c,
		r:    r,
		w:    w,
	}, nil
}

// Read decompresses data from the underlying connection.
func (c *ZstdConn) Read(p []byte) (n int, err error) {
	return c.r.Read(p)
}

// Write compresses data and immediately flushes it to the underlying connection.
// Flushing is mandatory for multiplexed protocols (like yamux) where small
// control frames must not be buffered indefinitely.
func (c *ZstdConn) Write(p []byte) (n int, err error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	if c.closed {
		return 0, net.ErrClosed
	}

	n, err = c.w.Write(p)
	if err != nil {
		return n, err
	}

	// Immediate flush ensures control frames are pushed to the wire immediately.
	// Without this, yamux streams can deadlock waiting for window updates.
	if flushErr := c.w.Flush(); flushErr != nil {
		return n, flushErr
	}

	return n, nil
}

// Close closes the underlying connection immediately, unblocking any pending Read/Write calls.
// It deliberately avoids blocking on writing the zstd epilogue, which can cause
// deadlocks on synchronous network pipes (e.g. net.Pipe in tests) or hung networks.
func (c *ZstdConn) Close() error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	if c.closed {
		return net.ErrClosed
	}
	c.closed = true

	// Close the underlying connection FIRST. This unblocks any concurrent Read()
	// and ensures that the connection tears down immediately.
	err := c.Conn.Close()

	// Attempt to close the encoder (this will likely fail since the connection is closed,
	// but it safely releases encoder resources).
	_ = c.w.Close()

	return err
}
