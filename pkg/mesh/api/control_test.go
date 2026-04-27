package api

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/membrane"
	pb "github.com/flipfloptech/cortex-mcp/pkg/mesh/proto"
	"github.com/flipfloptech/cortex-mcp/pkg/mesh/routing"
	"github.com/flipfloptech/cortex-mcp/pkg/mesh/transport"
	"github.com/flipfloptech/cortex-mcp/testutil"
)

// --- Protobuf frame codec over stream ---

func TestFrameCodec_WriteRead_Roundtrip(t *testing.T) {
	t.Parallel()

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

	frame := &pb.ControlFrame{
		Payload: &pb.ControlFrame_WhoHas{
			WhoHas: &pb.WhoHasFrame{
				Uuid:       "test-uuid",
				Capability: "tool:test",
				OriginNode: "node-1",
			},
		},
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- pb.WriteFrame(a, frame)
	}()

	fr := pb.NewFrameReader(2 * 1024 * 1024)
	got, err := fr.ReadFrame(b)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}

	if got.GetWhoHas() == nil {
		t.Fatal("expected who_has payload")
	}
	if got.GetWhoHas().Uuid != "test-uuid" {
		t.Fatalf("uuid = %q, want %q", got.GetWhoHas().Uuid, "test-uuid")
	}
}

func TestFrameCodec_MultipleFrames(t *testing.T) {
	t.Parallel()

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

	frames := []*pb.ControlFrame{
		{Payload: &pb.ControlFrame_WhoHas{WhoHas: &pb.WhoHasFrame{Uuid: "u1", Capability: "cap1", OriginNode: "n1"}}},
		{Payload: &pb.ControlFrame_IHave{IHave: &pb.IHaveFrame{Uuid: "u1", NodeId: "n2", Impedance: 1.0}}},
		{Payload: &pb.ControlFrame_Gossip{Gossip: &pb.GossipFrame{FromNode: "n1", Routes: map[string]float64{"n2": 1.0}}}},
	}

	go func() {
		for _, f := range frames {
			if err := pb.WriteFrame(a, f); err != nil {
				return
			}
		}
	}()

	for i := range frames {
		fr := pb.NewFrameReader(2 * 1024 * 1024)
		got, err := fr.ReadFrame(b)
		if err != nil {
			t.Fatalf("frame %d: ReadFrame: %v", i, err)
		}
		if got == nil {
			t.Fatalf("frame %d: nil frame", i)
		}
	}
}

func TestFrameCodec_ReadClosed(t *testing.T) {
	t.Parallel()

	a, b := net.Pipe()
	if err := a.Close(); err != nil {
		t.Logf("close a: %v", err)
	}

	fr := pb.NewFrameReader(2 * 1024 * 1024)
	_, err := fr.ReadFrame(b)
	if err == nil {
		t.Fatal("expected error on closed pipe")
	}
	if err := b.Close(); err != nil {
		t.Logf("close b: %v", err)
	}
}

// --- Control loop integration ---

func TestControlLoop_GossipDispatch(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	nodeA, nodeB := mustConnectedPair(t, ctx, "node-a", "node-b")
	defer func() {
		if err := nodeA.Close(); err != nil {
			t.Logf("close nodeA: %v", err)
		}
	}()
	defer func() {
		if err := nodeB.Close(); err != nil {
			t.Logf("close nodeB: %v", err)
		}
	}()

	// Node B adds a direct neighbor route.
	nodeB.gradient.AddDirectNeighbor("node-c", 3.0)

	// Send gossip from B to A via the control plane (protobuf).
	gossipVector := nodeB.gradient.GenerateGossip(1.0, routing.GossipInput{})

	// Convert routing gossip to protobuf format.
	routes := make(map[string]float64, len(gossipVector.Routes))
	for _, r := range gossipVector.Routes {
		routes[r.TargetID] = r.Cost
	}

	frame := &pb.ControlFrame{
		Payload: &pb.ControlFrame_Gossip{
			Gossip: &pb.GossipFrame{
				FromNode: gossipVector.FromNodeID,
				Routes:   routes,
			},
		},
	}

	peerB, _ := nodeB.peers.Get("node-a")
	if err := pb.WriteFrame(peerB.control, frame); err != nil {
		t.Fatalf("write gossip: %v", err)
	}

	// Wait for A to process it.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for gossip processing")
		default:
			if _, ok := nodeA.gradient.BestRoute("node-c"); ok {
				return // success — gossip was processed
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestControlLoop_SonarBroadcast(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	nodeA, nodeB := mustConnectedPair(t, ctx, "node-a", "node-b")
	defer func() {
		if err := nodeA.Close(); err != nil {
			t.Logf("close nodeA: %v", err)
		}
	}()
	defer func() {
		if err := nodeB.Close(); err != nil {
			t.Logf("close nodeB: %v", err)
		}
	}()

	// Register a capability on B so it responds to WhoHas.
	nodeB.RegisterCapability("tool:lustre_health")

	// Node A broadcasts Sonar for the capability.
	sonarCtx, sonarCancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer sonarCancel()

	results, err := nodeA.Sonar(sonarCtx, "tool:lustre_health")
	if err != nil {
		t.Fatalf("Sonar: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("expected at least one Sonar response")
	}

	found := false
	for _, r := range results {
		if r.NodeID == "node-b" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected response from node-b, got: %+v", results)
	}
}

func TestControlLoop_SonarNoMatch(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	nodeA, nodeB := mustConnectedPair(t, ctx, "node-a", "node-b")
	defer func() {
		if err := nodeA.Close(); err != nil {
			t.Logf("close nodeA: %v", err)
		}
	}()
	defer func() {
		if err := nodeB.Close(); err != nil {
			t.Logf("close nodeB: %v", err)
		}
	}()

	// No capabilities registered on B.

	// Short timeout — nothing should respond.
	shortCtx, shortCancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer shortCancel()

	results, err := nodeA.Sonar(shortCtx, "tool:nonexistent")
	if err != nil {
		t.Fatalf("Sonar: %v", err)
	}

	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
}

// --- Test helpers ---

// mustNewControlNode creates a Node with membrane config set.
func mustNewControlNode(t testing.TB, ctx context.Context, nodeID string, cfg *membrane.Config) *Node {
	t.Helper()

	node, err := NewNode(ctx, NodeConfig{NodeID: nodeID})
	if err != nil {
		t.Fatalf("NewNode %s: %v", nodeID, err)
	}
	node.SetMembraneConfig(cfg)
	return node
}

// mustConnectedPair creates two nodes and connects them via net.Pipe.
// Both nodes have control loops running.
func mustConnectedPair(t testing.TB, ctx context.Context, idA, idB string) (*Node, *Node) {
	t.Helper()

	serverCfg, clientCfg := testutil.GenerateTestCertsWithIDs(t, idA, idB)

	nodeA := mustNewControlNode(t, ctx, idA, serverCfg)
	nodeB := mustNewControlNode(t, ctx, idB, clientCfg)

	rawA, rawB := net.Pipe()

	addDone := make(chan error, 2)
	go func() {
		connA := transport.NewStdioConn(rawA, rawA)
		addDone <- nodeA.AddPeer(ctx, connA, true)
	}()
	go func() {
		connB := transport.NewStdioConn(rawB, rawB)
		addDone <- nodeB.AddPeer(ctx, connB, false)
	}()

	for i := 0; i < 2; i++ {
		if err := <-addDone; err != nil {
			if cerr := nodeA.Close(); cerr != nil {
				t.Logf("close nodeA: %v", cerr)
			}
			if cerr := nodeB.Close(); cerr != nil {
				t.Logf("close nodeB: %v", cerr)
			}
			t.Fatalf("AddPeer: %v", err)
		}
	}

	return nodeA, nodeB
}
