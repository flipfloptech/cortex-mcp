package membrane

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"testing"
	"time"
)

// --- Test PKI Helpers (Ed25519) ---

// testEd25519CA generates a self-signed Ed25519 CA certificate for testing.
func testEd25519CA(t testing.TB) (*x509.Certificate, ed25519.PrivateKey, *x509.CertPool) {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate Ed25519 CA key: %v", err)
	}

	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"cortex-mcp-site"},
			CommonName:   "cortex-mcp-site-ca",
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}

	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, pub, priv)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}

	caCert, err := x509.ParseCertificate(caCertDER)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	return caCert, priv, pool
}

// testNodeCert generates an Ed25519 node certificate signed by the given CA.
func testNodeCert(t testing.TB, ca *x509.Certificate, caKey ed25519.PrivateKey, nodeID string, isServer bool) tls.Certificate {
	t.Helper()

	usage := x509.ExtKeyUsageClientAuth
	if isServer {
		usage = x509.ExtKeyUsageServerAuth
	}

	return testNodeCertWithOptions(t, ca, caKey, nodeID, []x509.ExtKeyUsage{usage}, 24*time.Hour)
}

// testNodeCertWithOptions generates an Ed25519 node certificate with custom
// ExtKeyUsage and lifetime, signed by the given CA. This enables testing
// of certificate policy enforcement (role, expiry, CommonName).
func testNodeCertWithOptions(
	t testing.TB,
	ca *x509.Certificate,
	caKey ed25519.PrivateKey,
	nodeID string,
	extKeyUsage []x509.ExtKeyUsage,
	lifetime time.Duration,
) tls.Certificate {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate node key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject: pkix.Name{
			Organization: []string{"cortex-mcp-node"},
			CommonName:   nodeID,
		},
		NotBefore:   time.Now().Add(-1 * time.Hour),
		NotAfter:    time.Now().Add(lifetime),
		ExtKeyUsage: extKeyUsage,
		KeyUsage:    x509.KeyUsageDigitalSignature,
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:    []string{"localhost"},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, ca, pub, caKey)
	if err != nil {
		t.Fatalf("create node cert: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}

	return tlsCert
}

// testMembranePair creates a matching server and client Config for testing.
func testMembranePair(t testing.TB) (serverCfg *Config, clientCfg *Config) {
	t.Helper()

	ca, caKey, pool := testEd25519CA(t)
	serverCert := testNodeCert(t, ca, caKey, "node-server", true)
	clientCert := testNodeCert(t, ca, caKey, "node-client", false)

	serverCfg = &Config{Certificate: serverCert, CACert: pool}
	clientCfg = &Config{Certificate: clientCert, CACert: pool}
	return serverCfg, clientCfg
}

// connResult holds the result of a handshake goroutine.
type connResult struct {
	conn net.Conn
	err  error
}

// doHandshakePair runs both server and client handshakes concurrently
// over a net.Pipe and returns both connections. Fails the test on error.
//
// Cleanup is registered automatically: raw pipes are closed first to abort
// any TLS close_notify blocking, then TLS conns are closed. Callers should
// NOT defer Close() on the returned connections.
func doHandshakePair(t testing.TB, serverCfg, clientCfg *Config) (serverConn, clientConn net.Conn) {
	t.Helper()

	clientRaw, serverRaw := net.Pipe()

	serverCh := make(chan connResult, 1)
	clientCh := make(chan connResult, 1)

	go func() {
		conn, err := HandshakeServer(context.Background(), serverRaw, serverCfg)
		serverCh <- connResult{conn, err}
	}()

	go func() {
		conn, err := HandshakeClient(context.Background(), clientRaw, clientCfg)
		clientCh <- connResult{conn, err}
	}()

	sr := <-serverCh
	cr := <-clientCh

	if sr.err != nil {
		t.Fatalf("server handshake: %v", sr.err)
	}
	if cr.err != nil {
		t.Fatalf("client handshake: %v", cr.err)
	}

	// Register cleanup that kills the raw pipes FIRST, then closes TLS conns.
	// This prevents the TLS close_notify from blocking on the synchronous pipe.
	// t.Cleanup runs after deferred calls, so callers must NOT defer Close().
	t.Cleanup(func() {
		if err := clientRaw.Close(); err != nil {
			t.Logf("cleanup: close clientRaw: %v", err)
		}
		if err := serverRaw.Close(); err != nil {
			t.Logf("cleanup: close serverRaw: %v", err)
		}
		if err := sr.conn.Close(); err != nil {
			t.Logf("cleanup: close server TLS: %v", err)
		}
		if err := cr.conn.Close(); err != nil {
			t.Logf("cleanup: close client TLS: %v", err)
		}
	})

	return sr.conn, cr.conn
}

// doHandshakeExpectFailure runs both server and client handshakes concurrently
// over a net.Pipe and expects at least one side to fail. Fails the test if
// both sides succeed.
func doHandshakeExpectFailure(t testing.TB, serverCfg, clientCfg *Config) {
	t.Helper()

	clientRaw, serverRaw := net.Pipe()
	defer func() { _ = clientRaw.Close() }()
	defer func() { _ = serverRaw.Close() }()

	errCh := make(chan error, 2)
	go func() {
		conn, err := HandshakeServer(context.Background(), serverRaw, serverCfg)
		if conn != nil {
			_ = conn.Close()
		}
		errCh <- err
	}()
	go func() {
		conn, err := HandshakeClient(context.Background(), clientRaw, clientCfg)
		if conn != nil {
			_ = conn.Close()
		}
		errCh <- err
	}()

	err1 := <-errCh
	err2 := <-errCh
	if err1 == nil && err2 == nil {
		t.Fatal("handshake should have failed but both sides succeeded")
	}
}

// --- Happy path: handshake over net.Pipe, data roundtrips ---

func TestHandshake_Success(t *testing.T) {
	t.Parallel()

	serverCfg, clientCfg := testMembranePair(t)
	serverConn, clientConn := doHandshakePair(t, serverCfg, clientCfg)

	// Verify bidirectional data using concurrent goroutines.
	// TLS over net.Pipe requires concurrent read/write to avoid deadlock.
	payload := []byte("membrane handshake works")
	errCh := make(chan error, 2)

	// Client writes, server reads.
	go func() {
		_, err := clientConn.Write(payload)
		errCh <- err
	}()

	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(serverConn, buf); err != nil {
		t.Fatalf("server Read: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("client Write: %v", err)
	}
	if !bytes.Equal(buf, payload) {
		t.Fatalf("got %q, want %q", buf, payload)
	}

	// Reverse: server writes, client reads.
	response := []byte("ack from server")
	go func() {
		_, err := serverConn.Write(response)
		errCh <- err
	}()

	rbuf := make([]byte, len(response))
	if _, err := io.ReadFull(clientConn, rbuf); err != nil {
		t.Fatalf("client Read: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("server Write: %v", err)
	}
	if !bytes.Equal(rbuf, response) {
		t.Fatalf("got %q, want %q", rbuf, response)
	}
}

// --- Reject peer with wrong CA ---

func TestHandshake_RejectWrongCA(t *testing.T) {
	t.Parallel()

	ca1, ca1Key, pool1 := testEd25519CA(t)
	ca2, ca2Key, pool2 := testEd25519CA(t)

	serverCert := testNodeCert(t, ca1, ca1Key, "server-ca1", true)
	clientCert := testNodeCert(t, ca2, ca2Key, "client-ca2", false)

	serverCfg := &Config{Certificate: serverCert, CACert: pool1}
	clientCfg := &Config{Certificate: clientCert, CACert: pool2}

	doHandshakeExpectFailure(t, serverCfg, clientCfg)
}

// --- Reject peer without certificate ---

func TestHandshake_RejectNoCert(t *testing.T) {
	t.Parallel()

	serverCfg, _ := testMembranePair(t)

	_, _, pool := testEd25519CA(t) // different CA, no cert
	noCertCfg := &Config{CACert: pool}

	doHandshakeExpectFailure(t, serverCfg, noCertCfg)
}

// --- Context cancellation ---

func TestHandshake_ContextCanceled(t *testing.T) {
	t.Parallel()

	_, clientCfg := testMembranePair(t)

	clientRaw, _ := net.Pipe()
	defer func() { _ = clientRaw.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := HandshakeClient(ctx, clientRaw, clientCfg)
	if err == nil {
		t.Fatal("handshake should fail on canceled context")
	}
}

// --- Peer NodeID extraction ---

func TestPeerNodeID_FromHandshake(t *testing.T) {
	t.Parallel()

	serverCfg, clientCfg := testMembranePair(t)
	serverConn, clientConn := doHandshakePair(t, serverCfg, clientCfg)

	if id := PeerNodeID(serverConn); id != "node-client" {
		t.Errorf("server sees peer NodeID %q, want %q", id, "node-client")
	}
	if id := PeerNodeID(clientConn); id != "node-server" {
		t.Errorf("client sees peer NodeID %q, want %q", id, "node-server")
	}
}

// --- PeerNodeID on non-TLS conn ---

func TestPeerNodeID_NonTLSConn(t *testing.T) {
	t.Parallel()

	a, b := net.Pipe()
	defer func() { _ = a.Close() }()
	defer func() { _ = b.Close() }()

	if id := PeerNodeID(a); id != "" {
		t.Errorf("PeerNodeID on raw pipe = %q, want empty", id)
	}
}

// --- Large payload through membrane ---

func TestHandshake_LargePayload(t *testing.T) {
	t.Parallel()

	serverCfg, clientCfg := testMembranePair(t)
	serverConn, clientConn := doHandshakePair(t, serverCfg, clientCfg)

	const size = 256 * 1024 // 256KB
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i % 251)
	}

	writeErr := make(chan error, 1)
	go func() {
		_, err := clientConn.Write(payload)
		writeErr <- err
	}()

	received := make([]byte, size)
	if _, err := io.ReadFull(serverConn, received); err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if !bytes.Equal(received, payload) {
		t.Fatal("payload mismatch through membrane")
	}
}

// --- Nil config ---

func TestHandshake_NilConfig(t *testing.T) {
	t.Parallel()

	clientRaw, serverRaw := net.Pipe()
	defer func() { _ = clientRaw.Close() }()
	defer func() { _ = serverRaw.Close() }()

	_, err := HandshakeClient(context.Background(), clientRaw, nil)
	if err == nil {
		t.Fatal("HandshakeClient should fail with nil config")
	}

	_, err = HandshakeServer(context.Background(), serverRaw, nil)
	if err == nil {
		t.Fatal("HandshakeServer should fail with nil config")
	}
}

// --- ExtKeyUsage enforcement ---

// TestHandshake_RejectClientCertAsServer verifies that a certificate issued
// for ClientAuth cannot be used on the server side. The membrane must enforce
// strict role separation — client certs serve clients, server certs serve servers.
func TestHandshake_RejectClientCertAsServer(t *testing.T) {
	t.Parallel()

	ca, caKey, pool := testEd25519CA(t)

	// Issue a client-only certificate and try to use it as the server.
	clientOnlyCert := testNodeCert(t, ca, caKey, "node-impersonator", false) // ClientAuth only
	normalClientCert := testNodeCert(t, ca, caKey, "node-client", false)

	serverCfg := &Config{Certificate: clientOnlyCert, CACert: pool} // wrong role!
	clientCfg := &Config{Certificate: normalClientCert, CACert: pool}

	doHandshakeExpectFailure(t, serverCfg, clientCfg)
}

// TestHandshake_RejectServerCertAsClient verifies that a certificate issued
// for ServerAuth cannot be used as a client identity. This prevents a node
// from reusing its server certificate for outbound mesh connections.
func TestHandshake_RejectServerCertAsClient(t *testing.T) {
	t.Parallel()

	ca, caKey, pool := testEd25519CA(t)

	normalServerCert := testNodeCert(t, ca, caKey, "node-server", true)
	serverOnlyCert := testNodeCert(t, ca, caKey, "node-impersonator", true) // ServerAuth only

	serverCfg := &Config{Certificate: normalServerCert, CACert: pool}
	clientCfg := &Config{Certificate: serverOnlyCert, CACert: pool} // wrong role!

	doHandshakeExpectFailure(t, serverCfg, clientCfg)
}

// --- CommonName (NodeID) validation ---

// TestHandshake_RejectEmptyCommonName verifies that certificates with an empty
// CommonName are rejected. Every mesh node must have a non-empty NodeID.
func TestHandshake_RejectEmptyCommonName(t *testing.T) {
	t.Parallel()

	ca, caKey, pool := testEd25519CA(t)

	// Issue certificates with empty CommonName.
	serverCert := testNodeCert(t, ca, caKey, "node-server", true)
	emptyClientCert := testNodeCert(t, ca, caKey, "", false) // empty CN!

	serverCfg := &Config{Certificate: serverCert, CACert: pool}
	clientCfg := &Config{Certificate: emptyClientCert, CACert: pool}

	doHandshakeExpectFailure(t, serverCfg, clientCfg)
}

// --- Certificate lifetime enforcement ---

// MaxCertLifetime defines the maximum allowed certificate validity period.
// Certificates valid for longer than this are rejected as a short-lived
// certificate policy enforcement. This limits the damage window if a node's
// private key is compromised.
// maxCertLifetime is defined in production code (membrane/mtls.go).

// TestHandshake_RejectLongLivedCert verifies that certificates with validity
// periods exceeding the maximum allowed lifetime are rejected. This enforces
// a short-lived certificate policy, limiting the damage window if a node's
// private key is compromised.
func TestHandshake_RejectLongLivedCert(t *testing.T) {
	t.Parallel()

	ca, caKey, pool := testEd25519CA(t)

	serverCert := testNodeCert(t, ca, caKey, "node-server", true)
	// Issue a client cert valid for 11 years — exceeds the 10-year max.
	longLivedClientCert := testNodeCertWithOptions(
		t, ca, caKey, "node-long-lived",
		[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		11*365*24*time.Hour,
	)

	serverCfg := &Config{Certificate: serverCert, CACert: pool}
	clientCfg := &Config{Certificate: longLivedClientCert, CACert: pool}

	doHandshakeExpectFailure(t, serverCfg, clientCfg)
}

// TestHandshake_AcceptShortLivedCert verifies that certificates within the
// allowed lifetime are accepted.
func TestHandshake_AcceptShortLivedCert(t *testing.T) {
	t.Parallel()

	ca, caKey, pool := testEd25519CA(t)

	// 24 hours is well within the 90-day max.
	serverCert := testNodeCertWithOptions(
		t, ca, caKey, "node-server",
		[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		24*time.Hour,
	)
	clientCert := testNodeCertWithOptions(
		t, ca, caKey, "node-client",
		[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		24*time.Hour,
	)

	serverCfg := &Config{Certificate: serverCert, CACert: pool}
	clientCfg := &Config{Certificate: clientCert, CACert: pool}

	// Should succeed — lifetime is within limits.
	_, _ = doHandshakePair(t, serverCfg, clientCfg)
}

// --- verifyCertChain unit tests ---

// TestVerifyCertChain_RejectsNoCerts verifies that an empty cert chain
// is rejected.
func TestVerifyCertChain_RejectsNoCerts(t *testing.T) {
	t.Parallel()

	_, _, pool := testEd25519CA(t)
	err := verifyCertChain(nil, pool, x509.ExtKeyUsageClientAuth)
	if err == nil {
		t.Fatal("expected error for empty cert chain")
	}
}

// TestVerifyCertChain_EnforcesExtKeyUsage verifies that the cert chain
// validator rejects certificates with the wrong ExtKeyUsage.
func TestVerifyCertChain_EnforcesExtKeyUsage(t *testing.T) {
	t.Parallel()

	ca, caKey, pool := testEd25519CA(t)

	// Create a client-only cert.
	clientCert := testNodeCert(t, ca, caKey, "node-client", false)

	// Parse the leaf to get raw bytes.
	leaf, _ := x509.ParseCertificate(clientCert.Certificate[0])

	// Should succeed with ClientAuth.
	if err := verifyCertChain([][]byte{leaf.Raw}, pool, x509.ExtKeyUsageClientAuth); err != nil {
		t.Fatalf("expected ClientAuth to pass: %v", err)
	}

	// Should fail with ServerAuth — this cert only has ClientAuth.
	if err := verifyCertChain([][]byte{leaf.Raw}, pool, x509.ExtKeyUsageServerAuth); err == nil {
		t.Fatal("expected error when verifying client cert as ServerAuth")
	}
}

// TestVerifyCertChain_RejectsEmptyCommonName verifies that the cert chain
// validator rejects certificates with an empty CommonName.
func TestVerifyCertChain_RejectsEmptyCommonName(t *testing.T) {
	t.Parallel()

	ca, caKey, pool := testEd25519CA(t)
	emptyCNCert := testNodeCert(t, ca, caKey, "", false)
	leaf, _ := x509.ParseCertificate(emptyCNCert.Certificate[0])

	err := verifyCertChain([][]byte{leaf.Raw}, pool, x509.ExtKeyUsageClientAuth)
	if err == nil {
		t.Fatal("expected error for empty CommonName")
	}
}

// TestVerifyCertChain_RejectsLongLivedCert verifies that the cert chain
// validator rejects certificates exceeding MaxCertLifetime.
func TestVerifyCertChain_RejectsLongLivedCert(t *testing.T) {
	t.Parallel()

	ca, caKey, pool := testEd25519CA(t)
	longCert := testNodeCertWithOptions(
		t, ca, caKey, "node-long",
		[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		11*365*24*time.Hour,
	)
	leaf, _ := x509.ParseCertificate(longCert.Certificate[0])

	err := verifyCertChain([][]byte{leaf.Raw}, pool, x509.ExtKeyUsageClientAuth)
	if err == nil {
		t.Fatal("expected error for long-lived certificate")
	}
}
