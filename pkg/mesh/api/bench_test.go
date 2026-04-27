package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"
)

func BenchmarkNewPeerConnWithWriter(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkSendControl(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkSendControlRaw(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkControlWriteLoop(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkStopWriter(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkBroadcastAllPeers(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkBroadcastExceptPeer(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkBroadcastAllRaw(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkWaitForWriteQueueDrain(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
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
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkUpgradeAndHold(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkNewTestNode(b *testing.B) {
	for i := 0; i < b.N; i++ {
		n := newTestNode("test-node")
		_ = n.NodeID()
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
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkHandleGossipFrame(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkHandleWhoHasFrame(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkHandleIHaveFrame(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkSendToPeer(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkBroadcastExcept(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkBroadcastAll(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkSonar(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkSendGossipToAll(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkStartGossipTicker(b *testing.B) {
	b.Skip("Networking: starts unmanaged goroutines and timers")
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
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}
func BenchmarkAllPeers(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}
func BenchmarkHandleCredentialRequestFrame(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkHandleCredentialGrantFrame(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkMustNewControlNode(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkMustConnectedPair(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
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
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkAccept(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkClose(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkAddr(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}

func BenchmarkDeliver(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
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
	node := newTestNode("test-node")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Suppress the million slog warning prints during tight benchmarking.
	oldLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer slog.SetDefault(oldLog)

	lis, err := net.Listen("unix", "\x00cortex-bench-acceptloop")
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = lis.Close() }()

	go node.acceptLoop(ctx, lis)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		conn, err := net.Dial("unix", "\x00cortex-bench-acceptloop")
		if err != nil {
			b.Fatal(err)
		}
		_ = conn.Close()
	}
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
