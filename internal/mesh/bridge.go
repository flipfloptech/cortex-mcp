// Package main provides SSH bridge mode for firewall-transparent connectivity.
//
// When TCP:4443 is firewalled, the gateway cannot reach mesh nodes directly.
// The bridge solves this: the gateway SSHes to the host and execs the mesh
// binary with -bridge, which pipes stdin/stdout to the local mesh port.
//
// The bridge is a dumb pipe — no mesh logic, no TLS, no tools. It just
// does io.Copy in both directions. mTLS, yamux, gossip, and tool invocation
// all happen transparently over this pipe.
//
// Lifecycle:
//   - Bridge starts → dials local TCP → signals ready (4-byte magic)
//   - stdin/stdout relay data bidirectionally
//   - When stdin closes (SSH session drops), bridge exits immediately
//   - No stale processes: bridge lifetime = SSH session lifetime
package mesh

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/cortex-mesh/cortex-mesh/transport"
	"github.com/flipfloptech/cortex-mcp/internal/registry/tools/lifecycle"
)

const (
	// bridgeDialTimeout is the TCP dial timeout for the bridge connecting
	// to the local mesh node.
	bridgeDialTimeout = 5 * time.Second
)

// runBridge connects stdin/stdout to a local TCP address, acting as a
// transparent byte-stream relay. It signals readiness using the same
// 4-byte DeployReadyMagic as a freshly deployed node.
//
// The bridge exits when:
//   - stdin is closed (SSH session drops)
//   - the local TCP connection closes
//   - the context is cancelled
//
// Parameters:
//   - addr: local TCP address to bridge to (e.g. "localhost:4443")
//   - stdin: input stream (SSH session's stdin)
//   - stdout: output stream (SSH session's stdout)
func runBridge(ctx context.Context, addr string, stdin io.Reader, stdout io.Writer) error {
	// Dial the local mesh node.
	var d net.Dialer
	dialCtx, dialCancel := context.WithTimeout(ctx, bridgeDialTimeout)
	defer dialCancel()

	conn, err := d.DialContext(dialCtx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("bridge: dial %q: %w", addr, err)
	}
	defer func() { _ = conn.Close() }()

	// Signal readiness using the same magic as transport.SignalReady.
	magic := transport.DeployReadyMagic
	if _, err := stdout.Write(magic[:]); err != nil {
		return fmt.Errorf("bridge: signal ready: %w", err)
	}

	// Bidirectional copy: stdin↔conn.
	// When either direction ends, close both and return.
	errCh := make(chan error, 2)

	// stdin → conn (gateway sends to mesh node)
	go func() {
		_, err := io.Copy(conn, stdin)
		// When stdin closes (SSH drops), close the TCP conn write side
		// to signal the mesh node.
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
		errCh <- err
	}()

	// conn → stdout (mesh node sends to gateway)
	go func() {
		_, err := io.Copy(stdout, conn)
		errCh <- err
	}()

	// Wait for either direction to finish.
	// The first one to end means the session is over.
	<-errCh

	return nil
}

// --- Service detection ---

// serviceActiveCommand returns the shell command to check if the
// cortex-mesh systemd service is currently running.
func serviceActiveCommand() string {
	return fmt.Sprintf("systemctl is-active %s", lifecycle.ServiceName)
}

// parseServiceActive determines if the service is active from
// the output of "systemctl is-active".
func parseServiceActive(output string) bool {
	return strings.TrimSpace(output) == "active"
}
