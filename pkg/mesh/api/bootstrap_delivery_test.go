package api

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/nucleus"
	pb "github.com/flipfloptech/cortex-mcp/pkg/mesh/proto"
)

// --- P1-4: sendBootstrap must deliver ALL chunks with retry ---
//
// The old sendBootstrap silently truncated at the 64-slot per-peer write
// queue — chunk 65+ was never sent. The fix makes sendBootstrap retry
// with backoff on queue-full, exit on peer death or context cancellation.
//
// Contract:
//   1. sendBootstrapReliable delivers every chunk regardless of resolver size.
//   2. When the peer dies mid-delivery, sendBootstrapReliable exits promptly.
//   3. When the context is cancelled, sendBootstrapReliable exits promptly.
//   4. Under-queue-size bootstraps (small resolver) still work with zero retries.

// waitForWriteQueueDrain blocks until the write channel has drained
// (length == 0). Used in tests to coordinate between sendBootstrapReliable
// returning and stopping the writer.
func waitForWriteQueueDrain(pc *peerConn, timeout time.Duration) {
	deadline := time.After(timeout)
	for {
		if len(pc.writeCh) == 0 {
			// Give the writer one more moment to finish the current write.
			time.Sleep(50 * time.Millisecond)
			return
		}
		select {
		case <-deadline:
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestSendBootstrapReliable_LargeResolverFullyDelivered(t *testing.T) {
	t.Parallel()

	// Build a resolver with 50k entries — 100 chunks at 500 entries each.
	// This is the acceptance test from the P1-4 audit spec.
	resolver := nucleus.NewResolver()
	const totalHosts = 50_000
	for i := 0; i < totalHosts; i++ {
		resolver.AddEntry(
			"host-"+padIntB(i, 6),
			[]string{"10.0.0.1"},
			0,
		)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	n := &Node{
		resolver: resolver,
		ctx:      ctx,
	}

	serverConn, clientConn := net.Pipe()

	pc := newPeerConnWithWriter("test-peer", clientConn, nil, nil)

	// Count all bootstrap frames received on the wire.
	var received atomic.Int64
	var totalHostsReceived atomic.Int64
	var readerDone sync.WaitGroup
	readerDone.Add(1)
	go func() {
		defer readerDone.Done()
		reader := pb.NewFrameReader(10 * 1024 * 1024) // 10MB budget
		for {
			frame, err := reader.ReadFrame(serverConn)
			if err != nil {
				return
			}
			bf := frame.GetBootstrap()
			if bf != nil {
				received.Add(1)
				totalHostsReceived.Add(int64(len(bf.Hosts)))
			}
		}
	}()

	// Send bootstrap with reliable delivery (blocks until all enqueued).
	n.sendBootstrapReliable(pc)

	// Wait for the write queue to drain so all frames are written
	// to the pipe before we stop the writer.
	waitForWriteQueueDrain(pc, 10*time.Second)

	// Now stop the writer — closes the pipe, reader gets EOF.
	pc.stopWriter()
	readerDone.Wait()
	_ = serverConn.Close()

	expectedChunks := (totalHosts + bootstrapChunkSize - 1) / bootstrapChunkSize
	if got := int(received.Load()); got != expectedChunks {
		t.Fatalf("expected %d bootstrap chunks, got %d", expectedChunks, got)
	}
	if got := int(totalHostsReceived.Load()); got != totalHosts {
		t.Fatalf("expected %d total hosts, got %d", totalHosts, got)
	}
}

func TestSendBootstrapReliable_ExitsOnPeerDeath(t *testing.T) {
	t.Parallel()

	// Need enough entries to overflow the 64-slot write queue to trigger
	// the retry path. 50k entries = 100 chunks > 64 queue slots.
	resolver := nucleus.NewResolver()
	const totalHosts = 50_000
	for i := 0; i < totalHosts; i++ {
		resolver.AddEntry("host-"+padIntB(i, 6), []string{"10.0.0.1"}, 0)
	}

	ctx := context.Background()
	n := &Node{
		resolver: resolver,
		ctx:      ctx,
	}

	// Use a pipe but don't drain it — the writer will fill up the queue
	// and then hit the retry path (backoff + select on dead).
	serverConn, clientConn := net.Pipe()
	defer func() { _ = serverConn.Close() }()
	defer func() { _ = clientConn.Close() }()

	pc := newPeerConnWithWriter("dying-peer", clientConn, nil, nil)

	// Signal death after a brief delay to let a few chunks enqueue first.
	go func() {
		time.Sleep(50 * time.Millisecond)
		pc.closeDead()
	}()

	done := make(chan struct{})
	go func() {
		n.sendBootstrapReliable(pc)
		close(done)
	}()

	select {
	case <-done:
		// Good — the sender exited after peer death.
	case <-time.After(5 * time.Second):
		t.Fatal("sendBootstrapReliable did not exit after peer death within 5s")
	}

	pc.stopWriter()
}

func TestSendBootstrapReliable_ExitsOnContextCancel(t *testing.T) {
	t.Parallel()

	// Need enough entries to overflow the queue.
	resolver := nucleus.NewResolver()
	const totalHosts = 50_000
	for i := 0; i < totalHosts; i++ {
		resolver.AddEntry("host-"+padIntB(i, 6), []string{"10.0.0.1"}, 0)
	}

	ctx, cancel := context.WithCancel(context.Background())
	n := &Node{
		resolver: resolver,
		ctx:      ctx,
	}

	serverConn, clientConn := net.Pipe()
	defer func() { _ = serverConn.Close() }()
	defer func() { _ = clientConn.Close() }()

	pc := newPeerConnWithWriter("cancelled-peer", clientConn, nil, nil)

	// Cancel context after a brief delay.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	done := make(chan struct{})
	go func() {
		n.sendBootstrapReliable(pc)
		close(done)
	}()

	select {
	case <-done:
		// Good — the sender exited after context cancel.
	case <-time.After(5 * time.Second):
		t.Fatal("sendBootstrapReliable did not exit after context cancel within 5s")
	}

	pc.stopWriter()
}

func TestSendBootstrapReliable_SmallResolverNoRetry(t *testing.T) {
	t.Parallel()

	resolver := nucleus.NewResolver()
	for i := 0; i < 10; i++ {
		resolver.AddEntry("node-"+padIntB(i, 2), []string{"10.0.0.1"}, 0)
	}

	ctx := context.Background()
	n := &Node{
		resolver: resolver,
		ctx:      ctx,
	}

	serverConn, clientConn := net.Pipe()

	pc := newPeerConnWithWriter("small-peer", clientConn, nil, nil)

	var received atomic.Int64
	var readerDone sync.WaitGroup
	readerDone.Add(1)
	go func() {
		defer readerDone.Done()
		reader := pb.NewFrameReader(1024 * 1024)
		for {
			frame, err := reader.ReadFrame(serverConn)
			if err != nil {
				return
			}
			if frame.GetBootstrap() != nil {
				received.Add(1)
			}
		}
	}()

	n.sendBootstrapReliable(pc)

	// Wait for the write queue to drain.
	waitForWriteQueueDrain(pc, 5*time.Second)

	// Now stop and close.
	pc.stopWriter()
	readerDone.Wait()
	_ = serverConn.Close()

	if got := int(received.Load()); got != 1 {
		t.Fatalf("small resolver: expected 1 chunk, got %d", got)
	}
}

func TestPeerConn_DeadChannel_ClosesOnDeath(t *testing.T) {
	t.Parallel()

	serverConn, clientConn := net.Pipe()
	defer func() { _ = serverConn.Close() }()
	defer func() { _ = clientConn.Close() }()

	pc := newPeerConnWithWriter("dead-test", clientConn, nil, nil)
	defer pc.stopWriter()

	// Dead channel should not be closed yet.
	select {
	case <-pc.dead():
		t.Fatal("dead channel should not be closed before death")
	default:
		// Good.
	}

	// Signal death.
	pc.closeDead()

	// Now it should be closed.
	select {
	case <-pc.dead():
		// Good.
	case <-time.After(1 * time.Second):
		t.Fatal("dead channel should be closed after closeDead()")
	}

	// Multiple closeDead calls must be safe (idempotent via sync.Once).
	pc.closeDead()
}

// --- helpers ---

func padIntB(n, width int) string {
	s := ""
	for i := 0; i < width; i++ {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}
