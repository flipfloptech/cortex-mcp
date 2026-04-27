package membrane

import (
	"bytes"
	"crypto/subtle"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// --- PendingPairs tests ---

// TestPendingPairs_MatchesCorrectly verifies that a control and data
// connection with the same PeerID and token are correctly paired.
func TestPendingPairs_MatchesCorrectly(t *testing.T) {
	t.Parallel()

	pp := NewPendingPairs()
	defer pp.Close()

	token := make([]byte, 32)
	token[0] = 0xAB

	// Register control side.
	controlConn, _ := net.Pipe()
	defer func() { _ = controlConn.Close() }()

	pp.RegisterControl("node-a", token, controlConn)

	// Complete with data side.
	dataConn, _ := net.Pipe()
	defer func() { _ = dataConn.Close() }()

	result, err := pp.CompleteData("node-a", token, dataConn)
	if err != nil {
		t.Fatalf("CompleteData: %v", err)
	}

	if result.Control != controlConn {
		t.Fatal("control conn mismatch")
	}
	if result.Data != dataConn {
		t.Fatal("data conn mismatch")
	}
}

// TestPendingPairs_RejectsMismatchedToken verifies that a data connection
// with a different token than the registered control is rejected.
func TestPendingPairs_RejectsMismatchedToken(t *testing.T) {
	t.Parallel()

	pp := NewPendingPairs()
	defer pp.Close()

	controlToken := make([]byte, 32)
	controlToken[0] = 0xAA

	controlConn, _ := net.Pipe()
	defer func() { _ = controlConn.Close() }()

	pp.RegisterControl("node-a", controlToken, controlConn)

	badToken := make([]byte, 32)
	badToken[0] = 0xBB

	dataConn, _ := net.Pipe()
	defer func() { _ = dataConn.Close() }()

	_, err := pp.CompleteData("node-a", badToken, dataConn)
	if err == nil {
		t.Fatal("should reject mismatched token")
	}
}

// TestPendingPairs_RejectsDifferentPeerID verifies that connections
// from different peers cannot be paired even with the same token.
func TestPendingPairs_RejectsDifferentPeerID(t *testing.T) {
	t.Parallel()

	pp := NewPendingPairs()
	defer pp.Close()

	token := make([]byte, 32)

	controlConn, _ := net.Pipe()
	defer func() { _ = controlConn.Close() }()

	pp.RegisterControl("node-a", token, controlConn)

	dataConn, _ := net.Pipe()
	defer func() { _ = dataConn.Close() }()

	// Try to complete with a different PeerID.
	_, err := pp.CompleteData("node-b", token, dataConn)
	if err == nil {
		t.Fatal("should reject data from different peer")
	}
}

// TestPendingPairs_SingleUse verifies that a pairing token is consumed
// after successful pairing and cannot be reused.
func TestPendingPairs_SingleUse(t *testing.T) {
	t.Parallel()

	pp := NewPendingPairs()
	defer pp.Close()

	token := make([]byte, 32)
	token[0] = 0xCC

	controlConn, _ := net.Pipe()
	defer func() { _ = controlConn.Close() }()
	pp.RegisterControl("node-a", token, controlConn)

	dataConn1, _ := net.Pipe()
	defer func() { _ = dataConn1.Close() }()
	_, err := pp.CompleteData("node-a", token, dataConn1)
	if err != nil {
		t.Fatalf("first complete: %v", err)
	}

	// Second attempt with same token should fail.
	dataConn2, _ := net.Pipe()
	defer func() { _ = dataConn2.Close() }()
	_, err = pp.CompleteData("node-a", token, dataConn2)
	if err == nil {
		t.Fatal("should reject reuse of consumed token")
	}
}

// TestPendingPairs_ExpiresAfterTimeout verifies that pending entries
// are cleaned up after the timeout expires.
func TestPendingPairs_ExpiresAfterTimeout(t *testing.T) {
	t.Parallel()

	pp := NewPendingPairsWithTimeout(100 * time.Millisecond)
	defer pp.Close()

	token := make([]byte, 32)

	controlConn, _ := net.Pipe()
	defer func() { _ = controlConn.Close() }()
	pp.RegisterControl("node-a", token, controlConn)

	// Wait for expiration.
	time.Sleep(200 * time.Millisecond)

	dataConn, _ := net.Pipe()
	defer func() { _ = dataConn.Close() }()
	_, err := pp.CompleteData("node-a", token, dataConn)
	if err == nil {
		t.Fatal("should reject expired pending entry")
	}
}

// TestPendingPairs_RateLimitsPerPeer verifies that a peer can only
// have one pending pairing at a time.
func TestPendingPairs_RateLimitsPerPeer(t *testing.T) {
	t.Parallel()

	pp := NewPendingPairs()
	defer pp.Close()

	token1 := make([]byte, 32)
	token1[0] = 0x01
	token2 := make([]byte, 32)
	token2[0] = 0x02

	controlConn1, _ := net.Pipe()
	defer func() { _ = controlConn1.Close() }()
	controlConn2, _ := net.Pipe()
	defer func() { _ = controlConn2.Close() }()

	pp.RegisterControl("node-a", token1, controlConn1)

	// Second registration for same peer should replace the first.
	pp.RegisterControl("node-a", token2, controlConn2)

	dataConn, _ := net.Pipe()
	defer func() { _ = dataConn.Close() }()

	// Original token1 should no longer work.
	_, err := pp.CompleteData("node-a", token1, dataConn)
	if err == nil {
		t.Fatal("old token should be replaced")
	}

	// token2 should work.
	result, err := pp.CompleteData("node-a", token2, dataConn)
	if err != nil {
		t.Fatalf("new token should work: %v", err)
	}
	if result.Control != controlConn2 {
		t.Fatal("should be paired with second control conn")
	}
}

// TestPendingPairs_ConcurrentAccess verifies thread safety.
func TestPendingPairs_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	pp := NewPendingPairs()
	defer pp.Close()

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()

			token := make([]byte, 32)
			token[0] = byte(id)
			peerID := "node-" + string(rune('a'+id))

			c, _ := net.Pipe()
			defer func() { _ = c.Close() }()
			pp.RegisterControl(peerID, token, c)

			d, _ := net.Pipe()
			defer func() { _ = d.Close() }()
			_, _ = pp.CompleteData(peerID, token, d)
		}(i)
	}

	wg.Wait()
}

// --- MultiplexDual tests ---

// TestMultiplexDual_CreatesIsolatedPlanes verifies that MultiplexDual
// creates a MultiplexedConn where control and data are on separate
// underlying connections.
func TestMultiplexDual_CreatesIsolatedPlanes(t *testing.T) {
	t.Parallel()

	serverCfg, clientCfg := testMembranePair(t)

	// Create two connection pairs: one for control, one for data.
	controlClient, controlServer := net.Pipe()
	dataClient, dataServer := net.Pipe()
	t.Cleanup(func() {
		_ = controlClient.Close()
		_ = controlServer.Close()
		_ = dataClient.Close()
		_ = dataServer.Close()
	})

	sMC, cMC := doDualMultiplexPair(t,
		controlServer, dataServer, serverCfg,
		controlClient, dataClient, clientCfg,
	)

	// Control plane should work.
	controlMsg := []byte("gossip-update")
	go func() { _, _ = cMC.Control.Write(controlMsg) }()

	buf := make([]byte, len(controlMsg))
	if _, err := io.ReadFull(sMC.Control, buf); err != nil {
		t.Fatalf("Control read: %v", err)
	}
	if !bytes.Equal(buf, controlMsg) {
		t.Fatalf("Control: got %q, want %q", buf, controlMsg)
	}

	// Data plane (yamux) should work.
	dataStream, err := cMC.Session.Open()
	if err != nil {
		t.Fatalf("Open data stream: %v", err)
	}

	acceptCh := make(chan net.Conn, 1)
	go func() {
		s, err := sMC.Session.Accept()
		if err != nil {
			return
		}
		acceptCh <- s
	}()

	dataMsg := []byte("stitch-traffic")
	if _, err := dataStream.Write(dataMsg); err != nil {
		t.Fatalf("Data write: %v", err)
	}

	srvStream := <-acceptCh
	dbuf := make([]byte, len(dataMsg))
	if _, err := io.ReadFull(srvStream, dbuf); err != nil {
		t.Fatalf("Data read: %v", err)
	}
	if !bytes.Equal(dbuf, dataMsg) {
		t.Fatalf("Data: got %q, want %q", dbuf, dataMsg)
	}

	_ = dataStream.Close()
	_ = srvStream.Close()
}

// TestMultiplexDual_ControlUnaffectedByDataSaturation is the core
// proof that HOL blocking is eliminated. It floods data streams and
// verifies that the control plane remains responsive.
func TestMultiplexDual_ControlUnaffectedByDataSaturation(t *testing.T) {
	t.Parallel()

	serverCfg, clientCfg := testMembranePair(t)

	controlClient, controlServer := net.Pipe()
	dataClient, dataServer := net.Pipe()
	t.Cleanup(func() {
		_ = controlClient.Close()
		_ = controlServer.Close()
		_ = dataClient.Close()
		_ = dataServer.Close()
	})

	sMC, cMC := doDualMultiplexPair(t,
		controlServer, dataServer, serverCfg,
		controlClient, dataClient, clientCfg,
	)

	// Saturate 5 data streams.
	const numStreams = 5
	const dataSize = 512 * 1024
	var streamWg sync.WaitGroup
	streamWg.Add(numStreams)

	go func() {
		for i := 0; i < numStreams; i++ {
			s, err := sMC.Session.Accept()
			if err != nil {
				return
			}
			go func(s net.Conn) {
				_, _ = io.Copy(io.Discard, s)
				_ = s.Close()
			}(s)
		}
	}()

	for i := 0; i < numStreams; i++ {
		go func() {
			defer streamWg.Done()
			stream, err := cMC.Session.Open()
			if err != nil {
				return
			}
			defer func() { _ = stream.Close() }()
			data := make([]byte, dataSize)
			_, _ = stream.Write(data)
		}()
	}

	// While data is flooding, send a control frame.
	controlMsg := []byte("heartbeat-under-load")
	done := make(chan bool, 1)

	go func() { _, _ = cMC.Control.Write(controlMsg) }()

	go func() {
		buf := make([]byte, len(controlMsg))
		_, err := io.ReadFull(sMC.Control, buf)
		done <- (err == nil && bytes.Equal(buf, controlMsg))
	}()

	// Control should respond nearly instantly since it's on a
	// completely separate TCP connection.
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("control message corrupted under data load")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("control plane blocked by data saturation — HOL blocking not eliminated")
	}

	streamWg.Wait()
}

// TestMultiplexDual_Close verifies that both connections are cleaned up.
func TestMultiplexDual_Close(t *testing.T) {
	t.Parallel()

	serverCfg, clientCfg := testMembranePair(t)

	controlClient, controlServer := net.Pipe()
	dataClient, dataServer := net.Pipe()
	t.Cleanup(func() {
		_ = controlClient.Close()
		_ = controlServer.Close()
		_ = dataClient.Close()
		_ = dataServer.Close()
	})

	sMC, cMC := doDualMultiplexPair(t,
		controlServer, dataServer, serverCfg,
		controlClient, dataClient, clientCfg,
	)

	// Close client side.
	// Note: mc.Close() may return a TLS close_notify error on net.Pipe
	// (synchronous pipes cause TLS to time out sending the alert).
	// This is a test artifact — in production, TCP connections handle
	// close_notify asynchronously.
	_ = cMC.Close()

	// Server control should see closure.
	_, err := sMC.Control.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("server Control.Read should fail after client close")
	}

	// Server data should see session closure.
	_, err = sMC.Session.Accept()
	if err == nil {
		t.Fatal("server Session.Accept should fail after client close")
	}
}

// TestMultiplexDual_NilConns verifies error handling for nil connections.
func TestMultiplexDual_NilConns(t *testing.T) {
	t.Parallel()

	conn, _ := net.Pipe()
	defer func() { _ = conn.Close() }()

	_, err := MultiplexDual(nil, conn, true)
	if err == nil {
		t.Fatal("should reject nil control conn")
	}

	_, err = MultiplexDual(conn, nil, true)
	if err == nil {
		t.Fatal("should reject nil data conn")
	}
}

// --- doDualMultiplexPair helper ---

// doDualMultiplexPair runs UpgradeDual on both sides of two connection pairs.
func doDualMultiplexPair(t *testing.T,
	controlServer, dataServer net.Conn, serverCfg *Config,
	controlClient, dataClient net.Conn, clientCfg *Config,
) (server, client *MultiplexedConn) {
	t.Helper()

	type result struct {
		mc  *MultiplexedConn
		err error
	}

	serverCh := make(chan result, 1)
	clientCh := make(chan result, 1)

	go func() {
		mc, err := UpgradeDual(t.Context(), controlServer, dataServer, serverCfg, true)
		serverCh <- result{mc, err}
	}()

	go func() {
		mc, err := UpgradeDual(t.Context(), controlClient, dataClient, clientCfg, false)
		clientCh <- result{mc, err}
	}()

	sr := <-serverCh
	cr := <-clientCh

	if sr.err != nil {
		t.Fatalf("server UpgradeDual: %v", sr.err)
	}
	if cr.err != nil {
		t.Fatalf("client UpgradeDual: %v", cr.err)
	}

	t.Cleanup(func() {
		_ = sr.mc.Close()
		_ = cr.mc.Close()
	})

	return sr.mc, cr.mc
}

// Silence the linter for imported packages used only in type assertions.
var _ = subtle.ConstantTimeCompare
