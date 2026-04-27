package api

import (
	"fmt"
	"net"
	"sync"
)

// meshAddr implements net.Addr for mesh connections.
type meshAddr struct {
	nodeID string
}

func (a *meshAddr) Network() string { return "mesh" }
func (a *meshAddr) String() string  { return a.nodeID }

// meshListener implements net.Listener for gRPC server integration.
// Incoming yamux data streams are delivered via Deliver() and accepted
// via Accept(), bridging the mesh transport to standard Go gRPC servers.
type meshListener struct {
	incoming chan net.Conn
	closed   chan struct{}
	once     sync.Once
	addr     net.Addr
}

// newMeshListener creates a mesh listener that yields connections
// from incoming yamux data streams.
func newMeshListener() *meshListener {
	return &meshListener{
		incoming: make(chan net.Conn, 64),
		closed:   make(chan struct{}),
		addr:     &meshAddr{nodeID: "local"},
	}
}

// Accept waits for and returns the next incoming connection.
// Blocks until a connection is delivered or the listener is closed.
func (ml *meshListener) Accept() (net.Conn, error) {
	select {
	case conn := <-ml.incoming:
		return conn, nil
	case <-ml.closed:
		return nil, fmt.Errorf("mesh: listener closed")
	}
}

// Close stops the listener. Any blocked Accept calls return an error.
func (ml *meshListener) Close() error {
	ml.once.Do(func() {
		close(ml.closed)
	})
	return nil
}

// Addr returns the listener's address.
func (ml *meshListener) Addr() net.Addr {
	return ml.addr
}

// Deliver feeds an incoming connection to the listener.
// Called when a new yamux data stream arrives for this node.
func (ml *meshListener) Deliver(conn net.Conn) {
	select {
	case ml.incoming <- conn:
	case <-ml.closed:
		if err := conn.Close(); err != nil {
			// Listener is closed; discard error.
			return
		}
	}
}
