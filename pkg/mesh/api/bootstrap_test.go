package api

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/nucleus"
	pb "github.com/flipfloptech/cortex-mcp/pkg/mesh/proto"
	"github.com/flipfloptech/cortex-mcp/pkg/mesh/routing"
)

// --- Bootstrap frame construction ---

func TestBuildBootstrapFrame(t *testing.T) {
	t.Parallel()

	resolver := nucleus.NewResolver()
	resolver.AddEntryWithTransport("mds-01", []string{"10.0.1.1", "10.0.1.2"}, 0, "ssh")
	resolver.AddEntryWithTransport("oss-01", []string{"10.0.2.1"}, 0, "https")

	n := &Node{
		resolver:           resolver,
		protocolVersion:    1,
		applicationVersion: "abcd123",
	}

	frame := n.buildBootstrapFrame()
	bf := frame.GetBootstrap()
	if bf == nil {
		t.Fatal("expected BootstrapFrame payload")
	}

	if bf.ProtocolVersion != 1 {
		t.Fatalf("expected protocol version 1, got %d", bf.ProtocolVersion)
	}
	if string(bf.ApplicationVersion) != "abcd123" {
		t.Fatalf("expected app version abcd123, got %s", string(bf.ApplicationVersion))
	}

	if len(bf.Hosts) != 2 {
		t.Fatalf("expected 2 hosts, got %d", len(bf.Hosts))
	}

	mds := bf.Hosts["mds-01"]
	if mds == nil {
		t.Fatal("mds-01 should be in bootstrap")
	}
	if len(mds.Addresses) != 2 {
		t.Fatalf("mds-01 should have 2 addresses, got %d", len(mds.Addresses))
	}
	if mds.Transport != "ssh" {
		t.Fatalf("mds-01 transport = %q, want %q", mds.Transport, "ssh")
	}

	oss := bf.Hosts["oss-01"]
	if oss == nil {
		t.Fatal("oss-01 should be in bootstrap")
	}
	if oss.Transport != "https" {
		t.Fatalf("oss-01 transport = %q, want %q", oss.Transport, "https")
	}
}

// --- Bootstrap frame handling ---

func TestHandleBootstrapFrame_MergesIntoResolver(t *testing.T) {
	t.Parallel()

	resolver := nucleus.NewResolver()
	resolver.AddEntry("existing", []string{"10.0.0.1"}, 0) // pre-existing

	n := &Node{
		resolver:        resolver,
		protocolVersion: 1, // local node is v1
	}

	bf := &pb.BootstrapFrame{
		ProtocolVersion: 1, // incoming frame is v1
		Hosts: map[string]*pb.HostEntry{
			"mds-01": {Addresses: []string{"10.0.1.1"}, Transport: "ssh"},
			"oss-01": {Addresses: []string{"10.0.2.1", "10.0.2.2"}, Transport: "https"},
		},
	}

	n.handleBootstrapFrame(&peerConn{nodeID: "test"}, bf)

	// Verify new hosts are resolvable.
	addrs := n.resolver.Resolve("mds-01")
	if len(addrs) != 1 || addrs[0] != "10.0.1.1" {
		t.Fatalf("mds-01 expected [10.0.1.1], got %v", addrs)
	}

	addrs = n.resolver.Resolve("oss-01")
	if len(addrs) != 2 {
		t.Fatalf("oss-01 expected 2 addresses, got %v", addrs)
	}

	// Pre-existing should survive.
	addrs = n.resolver.Resolve("existing")
	if len(addrs) != 1 {
		t.Fatalf("existing host should survive merge, got %v", addrs)
	}

	// Verify transport is preserved.
	entries := n.resolver.AllEntries()
	for _, e := range entries {
		if e.Hostname == "mds-01" && e.Transport != "ssh" {
			t.Fatalf("mds-01 transport should be 'ssh', got %q", e.Transport)
		}
	}
}

// --- Bootstrap frame protocol mismatch ---

func TestHandleBootstrapFrame_ProtocolMismatch(t *testing.T) {
	t.Parallel()

	resolver := nucleus.NewResolver()
	n := &Node{
		resolver:        resolver,
		protocolVersion: 2, // local node is v2
	}

	bf := &pb.BootstrapFrame{
		ProtocolVersion: 1, // peer is v1
		Hosts: map[string]*pb.HostEntry{
			"mds-01": {Addresses: []string{"10.0.1.1"}, Transport: "ssh"},
		},
	}

	pc := &peerConn{
		nodeID: "mismatch-peer",
		deadCh: make(chan struct{}),
	}

	n.handleBootstrapFrame(pc, bf)

	// Verify the connection was closed.
	select {
	case <-pc.dead():
		// Success — the mismatch correctly severed the connection.
	default:
		t.Fatal("peer connection should have been closed dead due to protocol mismatch")
	}

	// Verify the resolver was NOT updated.
	addrs := n.resolver.Resolve("mds-01")
	if len(addrs) != 0 {
		t.Fatalf("resolver should not be updated on protocol mismatch, got %v", addrs)
	}
}

// --- initPeerControlPlane sends bootstrap ---

func TestInitPeerControlPlane_SendsBootstrap(t *testing.T) {
	t.Parallel()

	resolver := nucleus.NewResolver()
	resolver.AddEntryWithTransport("seed-01", []string{"10.0.1.1"}, 0, "ssh")

	c1, c2 := net.Pipe()
	defer func() { _ = c1.Close() }()
	defer func() { _ = c2.Close() }()

	n := &Node{
		manifest: nucleus.NewManifest("test-node"),
		sonar:    routing.NewSonar(nucleus.NewManifest("test-node")),
		gradient: routing.NewGradientTable("test-node"),
		resolver: resolver,
		peers:    newPeerManager(),
	}

	pc := newPeerConnWithWriter("peer-1", c1, nil, nil)
	defer pc.stopWriter()

	// Read from the other end in a goroutine.
	done := make(chan *pb.ControlFrame)
	go func() {
		fr := pb.NewFrameReader(2 * 1024 * 1024)
		frame, err := fr.ReadFrame(c2)
		if err != nil {
			t.Errorf("ReadFrame: %v", err)
			done <- nil
			return
		}
		done <- frame
	}()

	n.initPeerControlPlane(pc)

	select {
	case frame := <-done:
		if frame == nil {
			t.Fatal("failed to read bootstrap frame")
		}
		bf := frame.GetBootstrap()
		if bf == nil {
			t.Fatal("first frame should be BootstrapFrame")
		}
		if _, ok := bf.Hosts["seed-01"]; !ok {
			t.Fatal("bootstrap should contain seed-01")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for bootstrap frame")
	}
}

// --- Bootstrap frame roundtrip via Pipe ---

func TestBootstrapFrame_Roundtrip(t *testing.T) {
	t.Parallel()

	r1, w1 := io.Pipe()
	defer func() { _ = r1.Close() }()
	defer func() { _ = w1.Close() }()

	// Build a frame.
	outFrame := &pb.ControlFrame{
		Payload: &pb.ControlFrame_Bootstrap{
			Bootstrap: &pb.BootstrapFrame{
				ProtocolVersion:    42,
				ApplicationVersion: []byte("deadbeef"),
				Hosts: map[string]*pb.HostEntry{
					"node-1": {Addresses: []string{"10.0.1.1"}, Transport: "ssh"},
				},
			},
		},
	}

	go func() {
		_ = pb.WriteFrame(w1, outFrame)
	}()

	fr := pb.NewFrameReader(2 * 1024 * 1024)
	inFrame, err := fr.ReadFrame(r1)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}

	bf := inFrame.GetBootstrap()
	if bf == nil {
		t.Fatal("should have BootstrapFrame")
	}
	if len(bf.Hosts) != 1 {
		t.Fatalf("expected 1 host, got %d", len(bf.Hosts))
	}
	if bf.ProtocolVersion != 42 {
		t.Fatalf("expected protocol 42, got %d", bf.ProtocolVersion)
	}
	if string(bf.ApplicationVersion) != "deadbeef" {
		t.Fatalf("expected application deadbeef, got %s", string(bf.ApplicationVersion))
	}
}
