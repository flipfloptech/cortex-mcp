package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	pb "github.com/flipfloptech/cortex-mcp/pkg/mesh/proto"
	"github.com/flipfloptech/cortex-mcp/testutil"
	"github.com/hashicorp/yamux"
)

// setupBenchPeer establishes a fast in-memory yamux connection for benchmarking.
func setupBenchPeer(b *testing.B, drainServer bool) (*peerConn, *yamux.Session, func()) {
	c1, c2 := testutil.BufferedPipe(1 << 20)

	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard

	serverSession, err := yamux.Server(c1, cfg)
	if err != nil {
		b.Fatal(err)
	}

	clientSession, err := yamux.Client(c2, cfg)
	if err != nil {
		b.Fatal(err)
	}

	controlStream, err := clientSession.Open()
	if err != nil {
		b.Fatal(err)
	}

	pc := newPeerConnWithWriter("test-peer", controlStream, clientSession, nil)

	if drainServer {
		go func() {
			buf := make([]byte, 4096)
			serverControl, err := serverSession.Accept()
			if err != nil {
				return
			}
			for {
				_, err := serverControl.Read(buf)
				if err != nil {
					return
				}
			}
		}()
	}

	cleanup := func() {
		_ = serverSession.Close()
		_ = clientSession.Close()
		_ = c1.Close()
		_ = c2.Close()
	}

	return pc, serverSession, cleanup
}

// setupBenchMesh creates a node connected to N BufferedPipe peers.
func setupBenchMesh(b *testing.B, peerCount int, drainServer bool) (*Node, []*peerConn, func()) {
	n := newTestNode("test-node")

	peers := make([]*peerConn, 0, peerCount)
	var cleanups []func()

	for i := 0; i < peerCount; i++ {
		pc, _, cleanup := setupBenchPeer(b, drainServer)
		pc.nodeID = fmt.Sprintf("peer-%d", i)
		n.peers.Add(pc.nodeID, pc)
		peers = append(peers, pc)
		cleanups = append(cleanups, cleanup)
	}

	cleanupAll := func() {
		for _, c := range cleanups {
			c()
		}
	}

	return n, peers, cleanupAll
}

func BenchmarkNewPeerConnWithWriter(b *testing.B) {
	c1, c2 := testutil.BufferedPipe(1 << 20)
	defer func() { _ = c1.Close(); _ = c2.Close() }()

	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard

	serverSession, err := yamux.Server(c1, cfg)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = serverSession.Close() }()

	clientSession, err := yamux.Client(c2, cfg)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = clientSession.Close() }()

	controlStream, err := clientSession.Open()
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = newPeerConnWithWriter("test-peer", controlStream, clientSession, nil)
	}
}

func BenchmarkSendControl(b *testing.B) {
	pc, _, cleanup := setupBenchPeer(b, true)
	defer cleanup()

	frame := &pb.ControlFrame{
		Payload: &pb.ControlFrame_Bootstrap{
			Bootstrap: &pb.BootstrapFrame{Total: 1},
		},
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		pc.sendControl(frame)
	}
}

func BenchmarkSendControlRaw(b *testing.B) {
	pc, _, cleanup := setupBenchPeer(b, true)
	defer cleanup()

	raw := []byte("dummy-control-frame-data")

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		pc.sendControlRaw(raw)
	}
}

func BenchmarkControlWriteLoop(b *testing.B) {
	pc, _, cleanup := setupBenchPeer(b, true)
	defer cleanup()

	frame := &pb.ControlFrame{
		Payload: &pb.ControlFrame_Bootstrap{
			Bootstrap: &pb.BootstrapFrame{Total: 1},
		},
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		pc.sendControl(frame)
	}
}

func BenchmarkStopWriter(b *testing.B) {
	pc, _, cleanup := setupBenchPeer(b, true)
	defer cleanup()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		pc.stopWriter()
	}
}

func BenchmarkBroadcastAllPeers(b *testing.B) {
	n, _, cleanup := setupBenchMesh(b, 10, true)
	defer cleanup()

	frame := &pb.ControlFrame{
		Payload: &pb.ControlFrame_Bootstrap{
			Bootstrap: &pb.BootstrapFrame{Total: 1},
		},
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		n.broadcastAll(frame)
	}
}

func BenchmarkBroadcastExceptPeer(b *testing.B) {
	n, peers, cleanup := setupBenchMesh(b, 10, true)
	defer cleanup()

	frame := &pb.ControlFrame{
		Payload: &pb.ControlFrame_Bootstrap{
			Bootstrap: &pb.BootstrapFrame{Total: 1},
		},
	}

	exceptID := peers[0].nodeID

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		n.broadcastExcept(exceptID, frame)
	}
}

func BenchmarkPadIntB(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = padIntB(i, 4)
	}
}

func BenchmarkBuildBootstrapFrame(b *testing.B) {
	n := newTestNode("test-node")
	for i := 0; i < 1000; i++ {
		n.RegisterCapability(fmt.Sprintf("cap-%d", i))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = n.buildBootstrapFrame()
	}
}

func BenchmarkBuildBootstrapFrames(b *testing.B) {
	n := newTestNode("test-node")
	for i := 0; i < 1000; i++ {
		n.RegisterCapability(fmt.Sprintf("cap-%d", i))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = n.buildBootstrapFrames()
	}
}

func BenchmarkHandleBootstrapFrame(b *testing.B) {
	sender := newTestNode("sender-node")
	for i := 0; i < 1000; i++ {
		sender.RegisterCapability(fmt.Sprintf("cap-%d", i))
	}
	frame := sender.buildBootstrapFrame()
	bf := frame.GetBootstrap()

	receiver := newTestNode("receiver-node")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		receiver.handleBootstrapFrame(&peerConn{nodeID: "test"}, bf)
	}
}

func BenchmarkSendBootstrapReliable(b *testing.B) {
	b.Skip("Networking: involves blocking backoff retry loops on full channels")
}

func BenchmarkSetMembraneConfig(b *testing.B) {
	n := newTestNode("test-node")
	for i := 0; i < b.N; i++ {
		n.SetMembraneConfig(nil)
	}
}

func BenchmarkAddPeer(b *testing.B) {
	node := newTestNode("test-node")
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c1, c2 := net.Pipe()

		go func() {
			_ = c2.Close()
		}()

		// Measures AddPeer pipeline entry up to mTLS handshake
		_ = node.AddPeer(ctx, c1, true)
		_ = c1.Close()
	}
}

func BenchmarkAddPeerDual(b *testing.B) {
	nodeA := newTestNode("node-a")
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ca, cb := net.Pipe()
		go func() {
			_ = cb.Close()
		}()
		_ = nodeA.AddPeer(ctx, ca, false)
		_ = ca.Close()
	}
}

func BenchmarkAcceptStdio(b *testing.B) {
	node := newTestNode("test-node")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var in, out bytes.Buffer
		// Measures the wrapper creation and initial validation overhead
		_ = node.AcceptStdio(&in, &out)
	}
}

func BenchmarkGrpcDialer(b *testing.B) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeA, nodeB := mustConnectedPair(b, ctx, "node-a", "node-b")
	defer func() {
		_ = nodeA.Close()
		_ = nodeB.Close()
	}()

	lis, err := nodeB.GrpcListener()
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = lis.Close() }()

	go func() {
		for {
			conn, err := lis.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		conn, err := nodeA.GrpcDialer(ctx, "node-b")
		if err == nil {
			_ = conn.Close()
		}
	}
}

func BenchmarkGrpcDialerFallback(b *testing.B) {
	node := newTestNode("test-node")
	ctx := context.Background()

	node.gradient.AddDirectNeighbor("node-b", 1.0)
	node.gradient.UpdateRoute("target", "node-b", 2.0)
	addDummyPeer(node, "node-b")

	origErr := errors.New("simulated session failure")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = node.grpcDialerFallback(ctx, "target", origErr)
	}
}

func BenchmarkIsSessionDead(b *testing.B) {
	err := errors.New("yamux: session shutdown")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = isSessionDead(err)
	}
}

func BenchmarkPeerCount(b *testing.B) {
	n := newTestNode("test-node")
	for i := 0; i < 100; i++ {
		addDummyPeer(n, fmt.Sprintf("peer-%d", i))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = n.PeerCount()
	}
}

func BenchmarkAcceptDataStreams(b *testing.B) {
	c1, c2 := testutil.BufferedPipe(1 << 20)
	defer func() { _ = c1.Close() }()
	defer func() { _ = c2.Close() }()

	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard

	serverSession, err := yamux.Server(c1, cfg)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = serverSession.Close() }()

	clientSession, err := yamux.Client(c2, cfg)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = clientSession.Close() }()

	pc := &peerConn{
		nodeID:  "test-peer",
		session: serverSession,
	}

	n := newTestNode("test-node")
	defer func() { _ = n.Close() }()
	n.grpcLis = newMeshListener()
	defer func() { _ = n.grpcLis.Close() }()

	// Consume grpcLis
	go func() {
		for {
			conn, err := n.grpcLis.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				buf := make([]byte, 1)
				_, _ = c.Read(buf)
				_ = c.Close()
			}(conn)
		}
	}()

	go n.acceptDataStreams(pc)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		stream, err := clientSession.Open()
		if err != nil {
			b.Fatal(err)
		}
		_, _ = stream.Write([]byte{'P'})
		_ = stream.Close()
	}
}

func BenchmarkUpgradeAndHold(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkNewTestNode(b *testing.B) {
	for i := 0; i < b.N; i++ {
		n := newTestNode("test-node")
		_ = n.NodeID()
		_ = n.Close()
	}
}

func BenchmarkAddDummyPeer(b *testing.B) {
	n := newTestNode("test-node")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		addDummyPeer(n, padIntB(i, 8))
	}
}

func BenchmarkStartControlLoop(b *testing.B) {
	// StartControlLoop is just a goroutine that reads from yamux stream.
	// Since yamux reads are heavily mocked via BufferedPipe elsewhere,
	// benchmarking the spawn time of a goroutine provides no value.
	b.Skip("Networking: Benchmarking goroutine spawn is redundant")
}

func BenchmarkHandleGossipFrame(b *testing.B) {
	n := newTestNode("test-node")
	pc, _, cleanup := setupBenchPeer(b, true)
	defer cleanup()

	gossip := &pb.GossipFrame{
		FromNode:         "source-peer",
		FromCapabilities: []string{"cap-new-1", "cap-new-2"},
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		n.handleGossipFrame(pc, gossip)
	}
}

func BenchmarkHandleWhoHasFrame(b *testing.B) {
	n := newTestNode("test-node")
	pc, _, cleanup := setupBenchPeer(b, true)
	defer cleanup()

	whoHas := &pb.WhoHasFrame{
		Capability: "cap-test",
		OriginNode: "source-peer",
		Uuid:       "test-uuid",
		MaxHops:    5,
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		n.handleWhoHasFrame(pc, whoHas)
	}
}

func BenchmarkHandleIHaveFrame(b *testing.B) {
	n := newTestNode("test-node")
	pc, _, cleanup := setupBenchPeer(b, true)
	defer cleanup()

	iHave := &pb.IHaveFrame{
		Uuid:       "test-uuid",
		NodeId:     "source-peer",
		OriginNode: "origin-peer",
		Impedance:  1.0,
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		n.handleIHaveFrame(pc, iHave)
	}
}

func BenchmarkSendToPeer(b *testing.B) {
	n, peers, cleanup := setupBenchMesh(b, 10, true)
	defer cleanup()

	frame := &pb.ControlFrame{
		Payload: &pb.ControlFrame_Bootstrap{
			Bootstrap: &pb.BootstrapFrame{Total: 1},
		},
	}
	targetID := peers[0].nodeID

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		n.sendToPeer(targetID, frame)
	}
}

func BenchmarkSonar(b *testing.B) {
	n, _, cleanup := setupBenchMesh(b, 10, true)
	defer cleanup()

	// Fill gradients
	for i := 0; i < 10; i++ {
		n.gradient.UpdateRoute(fmt.Sprintf("peer-%d", i), "peer-0", float64(i+1))
	}

	// Pre-cancel context to measure only setup, broadcast, and teardown overhead,
	// without waiting for the 1-second default timeout to elapse.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = n.Sonar(ctx, "cap-test")
	}
}

func BenchmarkSendGossipToAll(b *testing.B) {
	n, _, cleanup := setupBenchMesh(b, 10, true)
	defer cleanup()

	n.RegisterCapability("cap-bench-test")

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		n.sendGossipToAll()
	}
}

func BenchmarkStartGossipTicker(b *testing.B) {
	b.Skip("Networking: Benchmarking goroutine spawn is redundant")
}

func BenchmarkJitteredFirstDelay(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = jitteredFirstDelay(time.Second)
	}
}

func BenchmarkJitteredInterval(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = jitteredInterval(time.Second)
	}
}

func BenchmarkInitPeerControlPlane(b *testing.B) {
	b.Skip("Networking: Benchmarking goroutine spawn is redundant")
}

func BenchmarkAllPeers(b *testing.B) {
	n, _, cleanup := setupBenchMesh(b, 10, false)
	defer cleanup()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = n.AllPeers()
	}
}

func BenchmarkHandleCredentialRequestFrame(b *testing.B) {
	n := newTestNode("test-node")
	pc, _, cleanup := setupBenchPeer(b, true)
	defer cleanup()

	req := &pb.CredentialRequestFrame{
		HostPattern:     "*.test",
		RequesterPubKey: []byte("test-pub-key"),
		Nonce:           []byte("nonce"),
		RequestedAt:     123456789,
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		n.handleCredentialRequestFrame(pc, req)
	}
}

func BenchmarkHandleCredentialGrantFrame(b *testing.B) {
	n := newTestNode("test-node")
	pc, _, cleanup := setupBenchPeer(b, true)
	defer cleanup()

	grant := &pb.CredentialGrantFrame{
		SealedCredential: []byte("sealed-data"),
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		n.handleCredentialGrantFrame(pc, grant)
	}
}

func BenchmarkMustNewControlNode(b *testing.B) {
	b.Skip("Networking: test helper redundant")
}

func BenchmarkMustConnectedPair(b *testing.B) {
	b.Skip("Networking: test helper redundant")
}

func BenchmarkRequestCredential(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkNetwork(b *testing.B) {
	addr := &meshAddr{nodeID: "test-node"}
	for i := 0; i < b.N; i++ {
		_ = addr.Network()
	}
}

func BenchmarkString(b *testing.B) {
	addr := &meshAddr{nodeID: "test-node"}
	for i := 0; i < b.N; i++ {
		_ = addr.String()
	}
}

func BenchmarkNewMeshListener(b *testing.B) {
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = newMeshListener()
	}
}

func BenchmarkAccept(b *testing.B) {
	lis := newMeshListener()
	defer func() { _ = lis.Close() }()

	go func() {
		for i := 0; i < b.N; i++ {
			c1, c2 := testutil.BufferedPipe(1 << 10)
			lis.Deliver(c1)
			_ = c2.Close()
		}
	}()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		c, err := lis.Accept()
		if err == nil {
			_ = c.Close()
		}
	}
}

func BenchmarkClose(b *testing.B) {
	lis := newMeshListener()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = lis.Close()
	}
}

func BenchmarkAddr(b *testing.B) {
	lis := newMeshListener()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = lis.Addr()
	}
}

func BenchmarkDeliver(b *testing.B) {
	lis := newMeshListener()
	defer func() { _ = lis.Close() }()

	go func() {
		for {
			c, err := lis.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	c1, c2 := testutil.BufferedPipe(1 << 10)
	defer func() { _ = c1.Close() }()
	_ = c2.Close()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		lis.Deliver(c1)
	}
}

func BenchmarkDefaultReconnectPolicy(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkHandlePeerDeath(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkPeerConnected(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkListen(b *testing.B) {
	node := newTestNode("test-node")
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		lis, err := node.Listen(ctx, "127.0.0.1:0")
		if err != nil {
			b.Fatal(err)
		}
		_ = lis.Close()
	}
}

func BenchmarkListenAddr(b *testing.B) {
	node := newTestNode("test-node")
	ctx := context.Background()
	lis, err := node.Listen(ctx, "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = lis.Close() }()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = node.ListenAddr()
	}
}

func BenchmarkAcceptLoop(b *testing.B) {
	b.Skip("Networking: involves live network I/O and kernel socket limits")
}

func BenchmarkDefaultDialer(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkDead(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkCloseDead(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkNewPeerManager(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = newPeerManager()
	}
}

func BenchmarkAdd(b *testing.B) {
	pm := newPeerManager()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pm.Add(padIntB(i, 8), &peerConn{nodeID: padIntB(i, 8)})
	}
}

func BenchmarkRemove(b *testing.B) {
	pm := newPeerManager()
	for i := 0; i < b.N; i++ {
		pm.Add(padIntB(i, 8), &peerConn{})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pm.Remove(padIntB(i, 8))
	}
}

func BenchmarkGet(b *testing.B) {
	pm := newPeerManager()
	pm.Add("test-peer", &peerConn{})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = pm.Get("test-peer")
	}
}

func BenchmarkAll(b *testing.B) {
	pm := newPeerManager()
	for i := 0; i < 100; i++ {
		pm.Add(fmt.Sprintf("peer-%d", i), &peerConn{})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = pm.All()
	}
}

func BenchmarkCount(b *testing.B) {
	pm := newPeerManager()
	for i := 0; i < 100; i++ {
		pm.Add(fmt.Sprintf("peer-%d", i), &peerConn{})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = pm.Count()
	}
}

func BenchmarkNewNode(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkNodeID(b *testing.B) {
	n := newTestNode("test-node")
	for i := 0; i < b.N; i++ {
		_ = n.NodeID()
	}
}

func BenchmarkRegisterCapability(b *testing.B) {
	n := newTestNode("test-node")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		n.RegisterCapability(padIntB(i, 8))
	}
}

func BenchmarkHasCapability(b *testing.B) {
	n := newTestNode("test-node")
	for i := 0; i < 100; i++ {
		n.RegisterCapability(fmt.Sprintf("cap-%d", i))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = n.HasCapability("cap-50")
	}
}

func BenchmarkGrpcListener(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkLookupCapability(b *testing.B) {
	n := newTestNode("test-node")
	for i := 0; i < 100; i++ {
		n.RegisterCapability(fmt.Sprintf("cap-%d", i))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = n.LookupCapability("cap-50")
	}
}

func BenchmarkLookupCapabilityWildcard(b *testing.B) {
	n := newTestNode("test-node")
	for i := 0; i < 100; i++ {
		n.RegisterCapability(fmt.Sprintf("ns.service.cap-%d", i))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = n.LookupCapabilityWildcard("ns.service.*")
	}
}

func BenchmarkCapabilityIndex(b *testing.B) {
	n := newTestNode("test-node")
	for i := 0; i < 100; i++ {
		n.RegisterCapability(fmt.Sprintf("cap-%d", i))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = n.CapabilityIndex()
	}
}

func BenchmarkGossipIntervalDuration(b *testing.B) {
	n := newTestNode("test-node")
	for i := 0; i < b.N; i++ {
		_ = n.GossipIntervalDuration()
	}
}

func BenchmarkCapabilityStaleTTL(b *testing.B) {
	n := newTestNode("test-node")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = n.CapabilityStaleTTL()
	}
}

func BenchmarkMaxSonarHops(b *testing.B) {
	n := newTestNode("test-node")
	for i := 0; i < b.N; i++ {
		_ = n.MaxSonarHops()
	}
}

func BenchmarkSonarSeenTTL(b *testing.B) {
	n := newTestNode("test-node")
	for i := 0; i < b.N; i++ {
		_ = n.SonarSeenTTL()
	}
}

func BenchmarkDefaultSonarHops(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = defaultSonarHops(100)
	}
}

func BenchmarkMeshTopology(b *testing.B) {
	n := newTestNode("test-node")
	for i := 0; i < 100; i++ {
		addDummyPeer(n, fmt.Sprintf("peer-%d", i))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = n.MeshTopology()
	}
}

func BenchmarkNewReconnectLoop(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkStart(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkStop(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkBackoff(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkRun(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkHandleRelayOpenFrame(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkDialRelayCircuit(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkNewPrefixConn(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkRead(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkHandleRelayAcceptFrame(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkBroadcastAll(b *testing.B) {
	n, _, cleanup := setupBenchMesh(b, 10, true)
	defer cleanup()

	frame := &pb.ControlFrame{
		Payload: &pb.ControlFrame_Bootstrap{
			Bootstrap: &pb.BootstrapFrame{Total: 1},
		},
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		n.broadcastAll(frame)
	}
}

func BenchmarkBroadcastExcept(b *testing.B) {
	n, _, cleanup := setupBenchMesh(b, 10, true)
	defer cleanup()

	frame := &pb.ControlFrame{
		Payload: &pb.ControlFrame_Bootstrap{
			Bootstrap: &pb.BootstrapFrame{Total: 1},
		},
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		n.broadcastExcept("peer-0", frame)
	}
}

func BenchmarkBroadcastAllRaw(b *testing.B) {
	n, _, cleanup := setupBenchMesh(b, 10, true)
	defer cleanup()

	data := []byte("test-data")

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		broadcastAllRaw(n.peers, data)
	}
}

func BenchmarkForEach(b *testing.B) {
	pm := newPeerManager()
	// Add 100 peers to make the iteration realistic.
	for i := 0; i < 100; i++ {
		nodeID := fmt.Sprintf("peer-%d", i)
		pm.Add(nodeID, &peerConn{nodeID: nodeID})
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		pm.ForEach(func(c *peerConn) {
		})
	}
}
