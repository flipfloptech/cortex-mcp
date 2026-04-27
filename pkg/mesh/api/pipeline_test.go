package api

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/membrane"
	"github.com/flipfloptech/cortex-mcp/pkg/mesh/transport"
	"github.com/flipfloptech/cortex-mcp/testutil"
)

// --- Full peer connection pipeline ---

// TestPeerPipeline_StdioToYamux verifies the complete connection lifecycle:
// transport (net.Pipe) → membrane (mTLS) → yamux → Stream 0 control + data streams.
func TestPeerPipeline_StdioToYamux(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	serverCfg, clientCfg := testutil.GenerateTestCerts(t)

	// Create a pipe as the transport (simulating stdio).
	rawA, rawB := net.Pipe()
	connA := transport.NewStdioConn(rawA, rawA)
	connB := transport.NewStdioConn(rawB, rawB)

	// Run membrane upgrade in parallel (server + client).
	type upgradeResult struct {
		mc  *membrane.MultiplexedConn
		err error
	}
	serverCh := make(chan upgradeResult, 1)
	clientCh := make(chan upgradeResult, 1)

	go func() {
		mc, err := membrane.Upgrade(ctx, connA, serverCfg, true)
		serverCh <- upgradeResult{mc, err}
	}()
	go func() {
		mc, err := membrane.Upgrade(ctx, connB, clientCfg, false)
		clientCh <- upgradeResult{mc, err}
	}()

	sResult := <-serverCh
	cResult := <-clientCh
	if sResult.err != nil {
		t.Fatalf("server upgrade: %v", sResult.err)
	}
	if cResult.err != nil {
		t.Fatalf("client upgrade: %v", cResult.err)
	}
	defer func() {
		if err := sResult.mc.Close(); err != nil {
			t.Logf("close server mc: %v", err)
		}
	}()
	defer func() {
		if err := cResult.mc.Close(); err != nil {
			t.Logf("close client mc: %v", err)
		}
	}()

	// Verify control stream works (Stream 0).
	go func() {
		if _, err := sResult.mc.Control.Write([]byte("ping")); err != nil {
			// expected in test
			return
		}
	}()
	buf := make([]byte, 4)
	if _, err := io.ReadFull(cResult.mc.Control, buf); err != nil {
		t.Fatalf("control read: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("control data = %q, want %q", buf, "ping")
	}

	// Open a data stream (simulating gRPC).
	dataStream, err := cResult.mc.Session.Open()
	if err != nil {
		t.Fatalf("open data stream: %v", err)
	}

	// Accept on server side.
	accepted, err := sResult.mc.Session.Accept()
	if err != nil {
		t.Fatalf("accept data stream: %v", err)
	}

	// Bidirectional data transfer on the data stream.
	go func() {
		if _, err := dataStream.Write([]byte("hello mesh")); err != nil {
			// expected in test
			return
		}
	}()

	dataBuf := make([]byte, 10)
	if _, err := io.ReadFull(accepted, dataBuf); err != nil {
		t.Fatalf("data read: %v", err)
	}
	if string(dataBuf) != "hello mesh" {
		t.Fatalf("data = %q, want %q", dataBuf, "hello mesh")
	}

	if err := dataStream.Close(); err != nil {
		t.Logf("close dataStream: %v", err)
	}
	if err := accepted.Close(); err != nil {
		t.Logf("close accepted: %v", err)
	}
}

// TestPeerPipeline_GrpcListener verifies that yamux data streams
// are delivered to the meshListener for gRPC server consumption.
func TestPeerPipeline_GrpcListener(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	serverCfg, clientCfg := testutil.GenerateTestCerts(t)

	rawA, rawB := net.Pipe()
	connA := transport.NewStdioConn(rawA, rawA)
	connB := transport.NewStdioConn(rawB, rawB)

	type upgradeResult struct {
		mc  *membrane.MultiplexedConn
		err error
	}
	serverCh := make(chan upgradeResult, 1)
	clientCh := make(chan upgradeResult, 1)

	go func() {
		mc, err := membrane.Upgrade(ctx, connA, serverCfg, true)
		serverCh <- upgradeResult{mc, err}
	}()
	go func() {
		mc, err := membrane.Upgrade(ctx, connB, clientCfg, false)
		clientCh <- upgradeResult{mc, err}
	}()

	sResult := <-serverCh
	cResult := <-clientCh
	if sResult.err != nil {
		t.Fatalf("server upgrade: %v", sResult.err)
	}
	if cResult.err != nil {
		t.Fatalf("client upgrade: %v", cResult.err)
	}
	defer func() {
		if err := sResult.mc.Close(); err != nil {
			t.Logf("close server mc: %v", err)
		}
	}()
	defer func() {
		if err := cResult.mc.Close(); err != nil {
			t.Logf("close client mc: %v", err)
		}
	}()

	// Create a meshListener and feed yamux data streams into it.
	ml := newMeshListener()
	defer func() {
		if err := ml.Close(); err != nil {
			t.Logf("close ml: %v", err)
		}
	}()

	// Server side: accept data streams and deliver to meshListener.
	go func() {
		for {
			stream, err := sResult.mc.Session.Accept()
			if err != nil {
				return
			}
			ml.Deliver(stream)
		}
	}()

	// Client opens a data stream — simulating a gRPC call.
	clientStream, err := cResult.mc.Session.Open()
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}

	// The meshListener should accept it.
	accepted, err := ml.Accept()
	if err != nil {
		t.Fatalf("listener accept: %v", err)
	}

	// Verify bidirectional through the listener.
	go func() {
		if _, err := clientStream.Write([]byte("request")); err != nil {
			// expected in test
			return
		}
	}()

	buf := make([]byte, 7)
	if _, err := io.ReadFull(accepted, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != "request" {
		t.Fatalf("data = %q, want %q", buf, "request")
	}

	// Response back.
	go func() {
		if _, err := accepted.Write([]byte("response")); err != nil {
			// expected in test
			return
		}
	}()

	rbuf := make([]byte, 8)
	if _, err := io.ReadFull(clientStream, rbuf); err != nil {
		t.Fatalf("response read: %v", err)
	}
	if string(rbuf) != "response" {
		t.Fatalf("response = %q, want %q", rbuf, "response")
	}

	if err := clientStream.Close(); err != nil {
		t.Logf("close clientStream: %v", err)
	}
	if err := accepted.Close(); err != nil {
		t.Logf("close accepted: %v", err)
	}
}
