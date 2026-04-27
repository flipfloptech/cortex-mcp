package api

import (
	"testing"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/nucleus"
	pb "github.com/flipfloptech/cortex-mcp/pkg/mesh/proto"
)

// --- C-1: Bootstrap pagination tests ---

func TestBuildBootstrapFrames_SmallResolver(t *testing.T) {
	t.Parallel()

	resolver := nucleus.NewResolver()
	for i := 0; i < 10; i++ {
		resolver.AddEntryWithTransport(
			"node-"+string(rune('A'+i)),
			[]string{"10.0.1." + string(rune('0'+i))},
			0, "ssh",
		)
	}

	n := &Node{resolver: resolver}
	frames := n.buildBootstrapFrames()

	if len(frames) != 1 {
		t.Fatalf("small resolver should produce 1 frame, got %d", len(frames))
	}

	bf := frames[0].GetBootstrap()
	if bf == nil {
		t.Fatal("expected BootstrapFrame payload")
	}
	if len(bf.Hosts) != 10 {
		t.Fatalf("expected 10 hosts in single chunk, got %d", len(bf.Hosts))
	}
	if bf.Sequence != 0 {
		t.Fatalf("sequence = %d, want 0", bf.Sequence)
	}
	if bf.Total != 1 {
		t.Fatalf("total = %d, want 1", bf.Total)
	}
}

func TestBuildBootstrapFrames_LargeResolver(t *testing.T) {
	t.Parallel()

	resolver := nucleus.NewResolver()
	// Insert 2000 entries → should produce ceil(2000/500) = 4 chunks.
	for i := 0; i < 2000; i++ {
		resolver.AddEntry(
			"host-"+string(rune(i)),
			[]string{"10.0.0.1"},
			0,
		)
	}

	n := &Node{resolver: resolver}
	frames := n.buildBootstrapFrames()

	// expectedChunks = ceil(2000 / bootstrapChunkSize)
	expectedChunks := (2000 + bootstrapChunkSize - 1) / bootstrapChunkSize
	if len(frames) != expectedChunks {
		t.Fatalf("expected %d chunks, got %d", expectedChunks, len(frames))
	}

	// Verify pagination metadata.
	totalHosts := 0
	for i, frame := range frames {
		bf := frame.GetBootstrap()
		if bf == nil {
			t.Fatalf("chunk %d: expected BootstrapFrame", i)
		}
		if bf.Sequence != uint32(i) {
			t.Fatalf("chunk %d: sequence = %d, want %d", i, bf.Sequence, i)
		}
		if bf.Total != uint32(expectedChunks) {
			t.Fatalf("chunk %d: total = %d, want %d", i, bf.Total, expectedChunks)
		}
		totalHosts += len(bf.Hosts)
	}

	if totalHosts != 2000 {
		t.Fatalf("total hosts across chunks = %d, want 2000", totalHosts)
	}
}

func TestBuildBootstrapFrames_EmptyResolver(t *testing.T) {
	t.Parallel()

	resolver := nucleus.NewResolver()
	n := &Node{resolver: resolver}
	frames := n.buildBootstrapFrames()

	if len(frames) != 1 {
		t.Fatalf("empty resolver should produce 1 frame, got %d", len(frames))
	}

	bf := frames[0].GetBootstrap()
	if bf == nil {
		t.Fatal("expected BootstrapFrame payload")
	}
	if len(bf.Hosts) != 0 {
		t.Fatalf("expected 0 hosts, got %d", len(bf.Hosts))
	}
	if bf.Sequence != 0 {
		t.Fatalf("sequence = %d, want 0", bf.Sequence)
	}
	if bf.Total != 1 {
		t.Fatalf("total = %d, want 1", bf.Total)
	}
}

func TestBuildBootstrapFrames_ExactChunkBoundary(t *testing.T) {
	t.Parallel()

	resolver := nucleus.NewResolver()
	// Insert exactly bootstrapChunkSize entries → should produce exactly 1 chunk.
	for i := 0; i < bootstrapChunkSize; i++ {
		resolver.AddEntry(
			"host-"+string(rune(i)),
			[]string{"10.0.0.1"},
			0,
		)
	}

	n := &Node{resolver: resolver}
	frames := n.buildBootstrapFrames()

	if len(frames) != 1 {
		t.Fatalf("exactly chunk-size entries should produce 1 frame, got %d", len(frames))
	}

	bf := frames[0].GetBootstrap()
	if len(bf.Hosts) != bootstrapChunkSize {
		t.Fatalf("expected %d hosts, got %d", bootstrapChunkSize, len(bf.Hosts))
	}
}

func TestHandleBootstrapFrame_MergesChunks(t *testing.T) {
	t.Parallel()

	resolver := nucleus.NewResolver()
	n := &Node{resolver: resolver}

	// Simulate 2 chunks arriving.
	chunk1 := &pb.BootstrapFrame{
		Hosts: map[string]*pb.HostEntry{
			"mds-01": {Addresses: []string{"10.0.1.1"}, Transport: "ssh"},
		},
		Sequence: 0,
		Total:    2,
	}
	chunk2 := &pb.BootstrapFrame{
		Hosts: map[string]*pb.HostEntry{
			"oss-01": {Addresses: []string{"10.0.2.1"}, Transport: "https"},
		},
		Sequence: 1,
		Total:    2,
	}

	n.handleBootstrapFrame(&peerConn{nodeID: "test"}, chunk1)
	n.handleBootstrapFrame(&peerConn{nodeID: "test"}, chunk2)

	// Both should be in resolver.
	addrs := n.resolver.Resolve("mds-01")
	if len(addrs) != 1 || addrs[0] != "10.0.1.1" {
		t.Fatalf("mds-01 expected [10.0.1.1], got %v", addrs)
	}
	addrs = n.resolver.Resolve("oss-01")
	if len(addrs) != 1 || addrs[0] != "10.0.2.1" {
		t.Fatalf("oss-01 expected [10.0.2.1], got %v", addrs)
	}
}

func TestHandleBootstrapFrame_PartialDelivery(t *testing.T) {
	t.Parallel()

	resolver := nucleus.NewResolver()
	n := &Node{resolver: resolver}

	// Only chunk 1 of 3 arrives. Should still merge what we got.
	chunk := &pb.BootstrapFrame{
		Hosts: map[string]*pb.HostEntry{
			"partial-host": {Addresses: []string{"10.0.3.1"}, Transport: "ssh"},
		},
		Sequence: 0,
		Total:    3,
	}

	n.handleBootstrapFrame(&peerConn{nodeID: "test"}, chunk)

	addrs := n.resolver.Resolve("partial-host")
	if len(addrs) != 1 {
		t.Fatalf("partial delivery should still merge, got %v", addrs)
	}
}
