package mesh

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/cortex-mesh/cortex-mesh/tools"
	"go.uber.org/zap"
)

const MagicBinaryTransfer byte = 0x01

var DefaultUpdatePath = "/tmp/cortex-mcp.update"

// prefixConn wraps a net.Conn and prepends a buffered byte slice to its read stream.
type prefixConn struct {
	net.Conn
	prefix []byte
}

func (c *prefixConn) Read(b []byte) (int, error) {
	if len(c.prefix) > 0 {
		n := copy(b, c.prefix)
		c.prefix = c.prefix[n:]
		return n, nil
	}
	return c.Conn.Read(b)
}

// ServeMultiplexedListener accepts connections from a mesh GrpcListener
// and demultiplexes them based on the first byte. If the first byte is MagicBinaryTransfer,
// it streams the connection to the local disk. Otherwise, it hands the connection
// back to the tools protocol handler.
func ServeMultiplexedListener(ctx context.Context, lis net.Listener, registry *tools.Registry) {
	for {
		conn, err := lis.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				zap.S().Errorw("ServeMultiplexedListener accept error", "error", err)
				return
			}
		}

		go func(c net.Conn) {
			magic := make([]byte, 1)
			_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
			if _, err := io.ReadFull(c, magic); err != nil {
				_ = c.Close()
				return
			}
			_ = c.SetReadDeadline(time.Time{}) // reset deadline

			if magic[0] == MagicBinaryTransfer {
				handleBinaryTransfer(c)
			} else {
				// Re-prefix the byte and send to the tool protocol
				pc := &prefixConn{
					Conn:   c,
					prefix: []byte{magic[0]},
				}
				_ = tools.ServeToolConn(ctx, pc, registry)
			}
		}(conn)
	}
}

func handleBinaryTransfer(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	// Use os.O_TRUNC to overwrite any existing file, creating it if it doesn't exist
	f, err := os.OpenFile(DefaultUpdatePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		zap.S().Errorw("failed to open update file", "error", err)
		_, _ = conn.Write([]byte{0x01}) // Error code
		return
	}

	if _, err := io.Copy(f, conn); err != nil {
		_ = f.Close()
		zap.S().Errorw("failed to receive update binary", "error", err)
		return
	}
	_ = f.Close()

	// Send success
	_, _ = conn.Write([]byte{0x00})
}

// DialUploadBinary dials the given connection with the file transfer protocol
// and streams the specified local file to the remote node.
func DialUploadBinary(ctx context.Context, conn net.Conn, path string) error {
	defer func() { _ = conn.Close() }()

	// 1. Write Magic Byte
	if _, err := conn.Write([]byte{MagicBinaryTransfer}); err != nil {
		return fmt.Errorf("write magic byte: %w", err)
	}

	// 2. Open file
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open source file: %w", err)
	}
	defer func() { _ = f.Close() }()

	// 3. Stream to remote
	if _, err := io.Copy(conn, f); err != nil {
		return fmt.Errorf("stream binary: %w", err)
	}

	// Important: we must close our write half so the remote io.Copy unblocks
	// and knows the stream is finished. If it's a TCP connection, CloseWrite is used.
	// If it's a Yamux stream, Close() signals EOF.
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}

	// 4. Wait for success signal
	buf := make([]byte, 1)
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		// If io.Copy returned successfully but we get EOF here, the other side might have just closed the connection
		if err == io.EOF {
			return nil
		}
		return fmt.Errorf("read status: %w", err)
	}
	_ = conn.SetReadDeadline(time.Time{})

	if buf[0] != 0x00 {
		return fmt.Errorf("remote node reported transfer error")
	}

	return nil
}
