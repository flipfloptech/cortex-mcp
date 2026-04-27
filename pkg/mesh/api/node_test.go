package api

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

// --- meshListener ---

func TestMeshListener_Accept(t *testing.T) {
	t.Parallel()

	ml := newMeshListener()
	defer func() {
		if err := ml.Close(); err != nil {
			t.Logf("close ml: %v", err)
		}
	}()

	// Feed a connection into the listener.
	a, b := net.Pipe()
	defer func() {
		if err := a.Close(); err != nil {
			t.Logf("close a: %v", err)
		}
	}()
	defer func() {
		if err := b.Close(); err != nil {
			t.Logf("close b: %v", err)
		}
	}()

	go func() {
		ml.Deliver(a)
	}()

	conn, err := ml.Accept()
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if conn != a {
		t.Fatal("accepted connection should be the delivered connection")
	}
}

func TestMeshListener_Accept_Multiple(t *testing.T) {
	t.Parallel()

	ml := newMeshListener()
	defer func() {
		if err := ml.Close(); err != nil {
			t.Logf("close ml: %v", err)
		}
	}()

	const count = 5
	conns := make([]net.Conn, count)
	for i := 0; i < count; i++ {
		a, b := net.Pipe()
		defer func() {
			if err := b.Close(); err != nil {
				// expected in test
				return
			}
		}()
		conns[i] = a
	}

	go func() {
		for _, c := range conns {
			ml.Deliver(c)
		}
	}()

	for i := 0; i < count; i++ {
		_, err := ml.Accept()
		if err != nil {
			t.Fatalf("Accept %d: %v", i, err)
		}
	}
}

func TestMeshListener_Close_UnblocksAccept(t *testing.T) {
	t.Parallel()

	ml := newMeshListener()

	done := make(chan error, 1)
	go func() {
		_, err := ml.Accept()
		done <- err
	}()

	// Give Accept a moment to block.
	time.Sleep(10 * time.Millisecond)
	if err := ml.Close(); err != nil {
		t.Logf("close ml: %v", err)
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Accept after Close should return error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Accept should unblock after Close")
	}
}

func TestMeshListener_Addr(t *testing.T) {
	t.Parallel()

	ml := newMeshListener()
	defer func() {
		if err := ml.Close(); err != nil {
			t.Logf("close ml: %v", err)
		}
	}()

	addr := ml.Addr()
	if addr == nil {
		t.Fatal("Addr should not be nil")
	}
	if addr.Network() != "mesh" {
		t.Fatalf("network = %q, want %q", addr.Network(), "mesh")
	}
}

func TestMeshListener_ConcurrentDeliverAccept(t *testing.T) {
	t.Parallel()

	ml := newMeshListener()
	defer func() {
		if err := ml.Close(); err != nil {
			t.Logf("close ml: %v", err)
		}
	}()

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n * 2)

	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			a, b := net.Pipe()
			defer func() {
				if err := b.Close(); err != nil {
					// expected in test
					return
				}
			}()
			ml.Deliver(a)
		}()
	}

	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if _, err := ml.Accept(); err != nil {
				// expected when closed
				return
			}
		}()
	}

	wg.Wait()
}

// --- Peer management ---

func TestPeerManager_AddPeer(t *testing.T) {
	t.Parallel()

	pm := newPeerManager()
	pm.Add("node-1", &peerConn{})

	if pm.Count() != 1 {
		t.Fatalf("expected 1 peer, got %d", pm.Count())
	}
}

func TestPeerManager_RemovePeer(t *testing.T) {
	t.Parallel()

	pm := newPeerManager()
	pm.Add("node-1", &peerConn{})
	pm.Remove("node-1")

	if pm.Count() != 0 {
		t.Fatalf("expected 0 peers, got %d", pm.Count())
	}
}

func TestPeerManager_GetPeer(t *testing.T) {
	t.Parallel()

	pm := newPeerManager()
	pc := &peerConn{}
	pm.Add("node-1", pc)

	got, ok := pm.Get("node-1")
	if !ok {
		t.Fatal("peer should exist")
	}
	if got != pc {
		t.Fatal("returned wrong peer")
	}
}

func TestPeerManager_GetPeer_NotFound(t *testing.T) {
	t.Parallel()

	pm := newPeerManager()
	_, ok := pm.Get("nonexistent")
	if ok {
		t.Fatal("should return false for unknown peer")
	}
}

func TestPeerManager_AllPeers(t *testing.T) {
	t.Parallel()

	pm := newPeerManager()
	pm.Add("node-1", &peerConn{})
	pm.Add("node-2", &peerConn{})
	pm.Add("node-3", &peerConn{})

	peers := pm.All()
	if len(peers) != 3 {
		t.Fatalf("expected 3 peers, got %d", len(peers))
	}
}

func TestPeerManager_DuplicateAdd(t *testing.T) {
	t.Parallel()

	pm := newPeerManager()
	pm.Add("node-1", &peerConn{})
	pm.Add("node-1", &peerConn{}) // overwrite

	if pm.Count() != 1 {
		t.Fatalf("expected 1 peer after duplicate add, got %d", pm.Count())
	}
}

func TestPeerManager_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	pm := newPeerManager()
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(3)
		go func() {
			defer wg.Done()
			pm.Add("node", &peerConn{})
		}()
		go func() {
			defer wg.Done()
			_ = pm.All()
		}()
		go func() {
			defer wg.Done()
			pm.Remove("node")
		}()
	}
	wg.Wait()
}

// --- Node construction ---

func TestNewNode_Config(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	n, err := NewNode(ctx, NodeConfig{
		NodeID: "test-node-1",
	})
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	defer func() {
		if err := n.Close(); err != nil {
			t.Logf("close n: %v", err)
		}
	}()

	if n.NodeID() != "test-node-1" {
		t.Fatalf("NodeID = %q, want %q", n.NodeID(), "test-node-1")
	}
}

func TestNewNode_RegisterCapability(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	n, err := NewNode(ctx, NodeConfig{NodeID: "test-node"})
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	defer func() {
		if err := n.Close(); err != nil {
			t.Logf("close n: %v", err)
		}
	}()

	n.RegisterCapability("role:mds")
	n.RegisterCapability("tool:lustre_health")

	if !n.HasCapability("role:mds") {
		t.Fatal("should have role:mds capability")
	}
	if !n.HasCapability("tool:lustre_health") {
		t.Fatal("should have tool:lustre_health capability")
	}
}

func TestNewNode_GrpcListener(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	n, err := NewNode(ctx, NodeConfig{NodeID: "test-node"})
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	defer func() {
		if err := n.Close(); err != nil {
			t.Logf("close n: %v", err)
		}
	}()

	lis, err := n.GrpcListener()
	if err != nil {
		t.Fatalf("GrpcListener: %v", err)
	}
	if lis == nil {
		t.Fatal("listener should not be nil")
	}
}

func TestNewNode_EmptyNodeID_Error(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := NewNode(ctx, NodeConfig{})
	if err == nil {
		t.Fatal("expected error for empty NodeID")
	}
}
