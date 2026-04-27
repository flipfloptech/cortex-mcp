package membrane

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"testing"
)

// --- Happy path: Multiplex creates server/client sessions ---

func TestMultiplex_CreatesSessions(t *testing.T) {
	t.Parallel()

	serverCfg, clientCfg := testMembranePair(t)
	serverTLS, clientTLS := doHandshakePair(t, serverCfg, clientCfg)

	sMC, cMC := doMultiplexPair(t, serverTLS, clientTLS)

	if sMC.Session == nil {
		t.Fatal("server Session is nil")
	}
	if cMC.Session == nil {
		t.Fatal("client Session is nil")
	}
	if sMC.Control == nil {
		t.Fatal("server Control is nil")
	}
	if cMC.Control == nil {
		t.Fatal("client Control is nil")
	}
}

// --- Stream 0: control plane reserved and usable ---

func TestMultiplex_Stream0ControlPlane(t *testing.T) {
	t.Parallel()

	serverCfg, clientCfg := testMembranePair(t)
	serverTLS, clientTLS := doHandshakePair(t, serverCfg, clientCfg)

	sMC, cMC := doMultiplexPair(t, serverTLS, clientTLS)

	// Stream 0 should be available on both sides.
	if sMC.Control == nil {
		t.Fatal("server Control stream is nil")
	}
	if cMC.Control == nil {
		t.Fatal("client Control stream is nil")
	}

	// Write control message from client → server.
	controlMsg := []byte("gossip-vector-update")
	writeErr := make(chan error, 1)
	go func() {
		_, err := cMC.Control.Write(controlMsg)
		writeErr <- err
	}()

	buf := make([]byte, len(controlMsg))
	if _, err := io.ReadFull(sMC.Control, buf); err != nil {
		t.Fatalf("server Control Read: %v", err)
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("client Control Write: %v", err)
	}
	if !bytes.Equal(buf, controlMsg) {
		t.Fatalf("got %q, want %q", buf, controlMsg)
	}
}

// --- Data streams: open and roundtrip ---

func TestMultiplex_DataStreamRoundtrip(t *testing.T) {
	t.Parallel()

	serverCfg, clientCfg := testMembranePair(t)
	serverTLS, clientTLS := doHandshakePair(t, serverCfg, clientCfg)

	sMC, cMC := doMultiplexPair(t, serverTLS, clientTLS)

	// Client opens a data stream.
	clientStream, err := cMC.Session.Open()
	if err != nil {
		t.Fatalf("Open stream: %v", err)
	}

	// Server accepts the stream.
	acceptCh := make(chan net.Conn, 1)
	go func() {
		s, err := sMC.Session.Accept()
		if err != nil {
			return
		}
		acceptCh <- s
	}()

	// Write from client stream.
	payload := []byte("data-plane-traffic")
	if _, err := clientStream.Write(payload); err != nil {
		t.Fatalf("Write: %v", err)
	}

	serverStream := <-acceptCh

	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(serverStream, buf); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Equal(buf, payload) {
		t.Fatalf("got %q, want %q", buf, payload)
	}

	_ = clientStream.Close()
	_ = serverStream.Close()
}

// --- Multiple concurrent streams ---

func TestMultiplex_ConcurrentStreams(t *testing.T) {
	t.Parallel()

	serverCfg, clientCfg := testMembranePair(t)
	serverTLS, clientTLS := doHandshakePair(t, serverCfg, clientCfg)

	sMC, cMC := doMultiplexPair(t, serverTLS, clientTLS)

	const numStreams = 5
	var wg sync.WaitGroup
	wg.Add(numStreams)

	// Server accepts streams concurrently.
	go func() {
		for i := 0; i < numStreams; i++ {
			s, err := sMC.Session.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = s.Close() }()
				_, _ = io.Copy(s, s) // echo
			}()
		}
	}()

	// Client opens multiple streams and verifies roundtrips.
	errors := make(chan error, numStreams)
	for i := 0; i < numStreams; i++ {
		go func(id int) {
			defer wg.Done()

			stream, err := cMC.Session.Open()
			if err != nil {
				errors <- err
				return
			}
			defer func() { _ = stream.Close() }()

			msg := []byte{byte(id), byte(id + 1), byte(id + 2)}
			if _, err := stream.Write(msg); err != nil {
				errors <- err
				return
			}

			buf := make([]byte, 3)
			if _, err := io.ReadFull(stream, buf); err != nil {
				errors <- err
				return
			}
			if !bytes.Equal(buf, msg) {
				errors <- io.ErrUnexpectedEOF
				return
			}
		}(i)
	}

	wg.Wait()
	close(errors)
	for err := range errors {
		t.Fatalf("stream error: %v", err)
	}
}

// --- Large payload through yamux stream ---

func TestMultiplex_LargePayload(t *testing.T) {
	t.Parallel()

	serverCfg, clientCfg := testMembranePair(t)
	serverTLS, clientTLS := doHandshakePair(t, serverCfg, clientCfg)

	sMC, cMC := doMultiplexPair(t, serverTLS, clientTLS)

	const size = 256 * 1024 // 256KB
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i % 251)
	}

	clientStream, err := cMC.Session.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	acceptCh := make(chan net.Conn, 1)
	go func() {
		s, _ := sMC.Session.Accept()
		acceptCh <- s
	}()

	// Write in background (large writes block on sync pipe).
	writeErr := make(chan error, 1)
	go func() {
		_, err := clientStream.Write(payload)
		writeErr <- err
	}()

	serverStream := <-acceptCh
	received := make([]byte, size)
	if _, err := io.ReadFull(serverStream, received); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !bytes.Equal(received, payload) {
		t.Fatal("payload mismatch through yamux")
	}

	_ = clientStream.Close()
	_ = serverStream.Close()
}

// --- Session close ---

func TestMultiplex_SessionClose(t *testing.T) {
	t.Parallel()

	serverCfg, clientCfg := testMembranePair(t)
	serverTLS, clientTLS := doHandshakePair(t, serverCfg, clientCfg)

	sMC, cMC := doMultiplexPair(t, serverTLS, clientTLS)

	// Close the client session.
	if err := cMC.Close(); err != nil {
		t.Fatalf("client Close: %v", err)
	}

	// Server should see the session close: Accept should fail.
	_, err := sMC.Session.Accept()
	if err == nil {
		t.Fatal("server Accept should fail after client close")
	}
}

// --- doMultiplexPair helper ---

// doMultiplexPair runs Multiplex on both sides of a TLS connection pair.
func doMultiplexPair(t testing.TB, serverTLS, clientTLS net.Conn) (server, client *MultiplexedConn) {
	t.Helper()

	type muxResult struct {
		mc  *MultiplexedConn
		err error
	}

	serverCh := make(chan muxResult, 1)
	clientCh := make(chan muxResult, 1)

	go func() {
		mc, err := Multiplex(serverTLS, true)
		serverCh <- muxResult{mc, err}
	}()

	go func() {
		mc, err := Multiplex(clientTLS, false)
		clientCh <- muxResult{mc, err}
	}()

	sr := <-serverCh
	cr := <-clientCh

	if sr.err != nil {
		t.Fatalf("server Multiplex: %v", sr.err)
	}
	if cr.err != nil {
		t.Fatalf("client Multiplex: %v", cr.err)
	}

	t.Cleanup(func() {
		_ = sr.mc.Close()
		_ = cr.mc.Close()
	})

	return sr.mc, cr.mc
}

// --- Nil conn ---

func TestMultiplex_NilConn(t *testing.T) {
	t.Parallel()

	_, err := Multiplex(nil, true)
	if err == nil {
		t.Fatal("Multiplex should fail with nil conn")
	}
}

// --- Upgrade: full pipeline transport → mTLS → yamux ---

func TestUpgrade_FullPipeline(t *testing.T) {
	t.Parallel()

	serverCfg, clientCfg := testMembranePair(t)

	// Simulate a raw transport connection via net.Pipe.
	clientRaw, serverRaw := net.Pipe()
	t.Cleanup(func() {
		if err := clientRaw.Close(); err != nil {
			t.Logf("cleanup: close clientRaw: %v", err)
		}
		if err := serverRaw.Close(); err != nil {
			t.Logf("cleanup: close serverRaw: %v", err)
		}
	})

	type upgradeResult struct {
		mc  *MultiplexedConn
		err error
	}

	serverCh := make(chan upgradeResult, 1)
	clientCh := make(chan upgradeResult, 1)

	go func() {
		mc, err := Upgrade(context.Background(), serverRaw, serverCfg, true)
		serverCh <- upgradeResult{mc, err}
	}()

	go func() {
		mc, err := Upgrade(context.Background(), clientRaw, clientCfg, false)
		clientCh <- upgradeResult{mc, err}
	}()

	sr := <-serverCh
	cr := <-clientCh

	if sr.err != nil {
		t.Fatalf("server Upgrade: %v", sr.err)
	}
	if cr.err != nil {
		t.Fatalf("client Upgrade: %v", cr.err)
	}
	defer func() { _ = sr.mc.Close() }()
	defer func() { _ = cr.mc.Close() }()

	// Verify control plane works.
	if sr.mc.Control == nil || cr.mc.Control == nil {
		t.Fatal("Control stream should not be nil after Upgrade")
	}

	// Verify data streams work.
	stream, err := cr.mc.Session.Open()
	if err != nil {
		t.Fatalf("Open stream: %v", err)
	}

	serverStream, err := sr.mc.Session.Accept()
	if err != nil {
		t.Fatalf("Accept stream: %v", err)
	}

	payload := []byte("full-pipeline-test")
	go func() { _, _ = stream.Write(payload) }()

	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(serverStream, buf); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Equal(buf, payload) {
		t.Fatalf("got %q, want %q", buf, payload)
	}

	if err := stream.Close(); err != nil {
		t.Logf("close stream: %v", err)
	}
	if err := serverStream.Close(); err != nil {
		t.Logf("close serverStream: %v", err)
	}
}
