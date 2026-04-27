package transport

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"
)

// --- Test PKI Helpers ---

// testCA generates a self-signed CA certificate and key for test use.
// Returns the CA cert, CA key, and a PEM-encoded cert pool.
func testCA(t testing.TB) (*x509.Certificate, *ecdsa.PrivateKey, *x509.CertPool) {
	t.Helper()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}

	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"cortex-mcp-test-ca"},
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}

	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}

	caCert, err := x509.ParseCertificate(caCertDER)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	return caCert, caKey, pool
}

// testCert generates a TLS certificate signed by the given CA.
// The cert is valid for 127.0.0.1 and localhost.
func testCert(t testing.TB, ca *x509.Certificate, caKey *ecdsa.PrivateKey, isServer bool) tls.Certificate {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate cert key: %v", err)
	}

	usage := x509.ExtKeyUsageClientAuth
	if isServer {
		usage = x509.ExtKeyUsageServerAuth
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject: pkix.Name{
			Organization: []string{"cortex-mcp-test"},
		},
		NotBefore:   time.Now().Add(-1 * time.Hour),
		NotAfter:    time.Now().Add(24 * time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{usage},
		KeyUsage:    x509.KeyUsageDigitalSignature,
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:    []string{"localhost"},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}

	return tlsCert
}

// testMTLSPair generates a CA, server cert, and client cert for mTLS tests.
// Returns server TLS config (requires client certs), client TLS config, and the listener.
func testMTLSPair(t testing.TB) (serverTLSConf *tls.Config, clientTLSConf *tls.Config) {
	t.Helper()

	ca, caKey, pool := testCA(t)
	serverCert := testCert(t, ca, caKey, true)
	clientCert := testCert(t, ca, caKey, false)

	serverTLSConf = &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS13,
	}

	clientTLSConf = &tls.Config{
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      pool,
		MinVersion:   tls.VersionTLS13,
	}

	return serverTLSConf, clientTLSConf
}

// startTLSEchoServer starts a TLS listener that echoes data back.
// Returns the listener address and a cleanup function.
func startTLSEchoServer(t testing.TB, tlsConf *tls.Config) (addr string, cleanup func()) {
	t.Helper()

	ln, err := tls.Listen("tcp", "127.0.0.1:0", tlsConf)
	if err != nil {
		t.Fatalf("TLS listen: %v", err)
	}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() {
					if err := conn.Close(); err != nil {
						// expected on shutdown
						return
					}
				}()
				if _, err := io.Copy(conn, conn); err != nil {
					// expected on client close
					return
				}
			}()
		}
	}()

	return ln.Addr().String(), func() {
		if err := ln.Close(); err != nil {
			t.Logf("cleanup: close listener: %v", err)
		}
	}
}

// --- Happy path: DialHTTPS connects and roundtrips data ---

func TestDialHTTPS_Success(t *testing.T) {
	t.Parallel()

	serverConf, clientConf := testMTLSPair(t)
	addr, cleanup := startTLSEchoServer(t, serverConf)
	defer cleanup()

	ctx := context.Background()
	conn, err := DialHTTPS(ctx, addr, clientConf)
	if err != nil {
		t.Fatalf("DialHTTPS error: %v", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Logf("close conn: %v", err)
		}
	}()

	// Verify it satisfies net.Conn.
	var _ = conn

	// Verify roundtrip data through the TLS tunnel.
	payload := []byte("hello through TLS")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if !bytes.Equal(buf, payload) {
		t.Fatalf("got %q, want %q", buf, payload)
	}
}

// --- Cert validation: reject untrusted server ---

func TestDialHTTPS_RejectUntrustedServer(t *testing.T) {
	t.Parallel()

	// Server uses a different CA than the client trusts.
	serverCA, serverCAKey, _ := testCA(t)
	serverCert := testCert(t, serverCA, serverCAKey, true)
	serverConf := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		MinVersion:   tls.VersionTLS13,
	}

	addr, cleanup := startTLSEchoServer(t, serverConf)
	defer cleanup()

	// Client trusts a DIFFERENT CA — should reject the server.
	_, _, clientPool := testCA(t)
	clientConf := &tls.Config{
		RootCAs:    clientPool,
		MinVersion: tls.VersionTLS13,
	}

	ctx := context.Background()
	conn, err := DialHTTPS(ctx, addr, clientConf)
	if err == nil {
		if cerr := conn.Close(); cerr != nil {
			t.Logf("close conn: %v", cerr)
		}
		t.Fatal("DialHTTPS should reject untrusted server certificate")
	}
}

// --- mTLS: server rejects client without cert ---

func TestDialHTTPS_ServerRejectsNoCert(t *testing.T) {
	t.Parallel()

	serverConf, _ := testMTLSPair(t)
	addr, cleanup := startTLSEchoServer(t, serverConf)
	defer cleanup()

	// Client provides no certificate — server requires mTLS.
	_, _, clientPool := testCA(t) // different CA, no client cert
	clientConf := &tls.Config{
		RootCAs:    clientPool,
		MinVersion: tls.VersionTLS13,
		// No Certificates field — no client cert.
	}

	ctx := context.Background()
	conn, err := DialHTTPS(ctx, addr, clientConf)
	if err == nil {
		// Connection might succeed at TCP level but fail on TLS handshake.
		// Try to use it — the handshake error should surface.
		_, writeErr := conn.Write([]byte("test"))
		_, readErr := io.ReadFull(conn, make([]byte, 4))
		if cerr := conn.Close(); cerr != nil {
			t.Logf("close conn: %v", cerr)
		}
		if writeErr == nil && readErr == nil {
			t.Fatal("should fail when client provides no certificate to mTLS server")
		}
	}
	// Either DialHTTPS or subsequent I/O should have failed.
}

// --- Context cancellation ---

func TestDialHTTPS_ContextCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	// Use a non-routable address to ensure the test doesn't
	// accidentally succeed via fast connection to localhost.
	_, err := DialHTTPS(ctx, "192.0.2.1:443", &tls.Config{})
	if err == nil {
		t.Fatal("DialHTTPS should fail on canceled context")
	}
}

// --- Connection refused ---

func TestDialHTTPS_ConnectionRefused(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, err := DialHTTPS(ctx, "127.0.0.1:1", &tls.Config{})
	if err == nil {
		t.Fatal("DialHTTPS should fail when target is unreachable")
	}
}

// --- ListenHTTPS: accept inbound and roundtrip ---

func TestListenHTTPS_AcceptAndRoundtrip(t *testing.T) {
	t.Parallel()

	serverConf, clientConf := testMTLSPair(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ln, err := ListenHTTPS(ctx, "127.0.0.1:0", serverConf)
	if err != nil {
		t.Fatalf("ListenHTTPS error: %v", err)
	}
	defer func() {
		if err := ln.Close(); err != nil {
			t.Logf("close listener: %v", err)
		}
	}()

	// Accept in a goroutine. The TLS listener returns lazy-handshake conns,
	// so we must trigger the handshake server-side to unblock the client's
	// HandshakeContext() call.
	connCh := make(chan net.Conn, 1)
	errCh := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			errCh <- err
			return
		}
		// Force the server-side TLS handshake.
		if tlsConn, ok := conn.(*tls.Conn); ok {
			if err := tlsConn.Handshake(); err != nil {
				// Close conn on handshake failure; close error is non-critical.
				if cerr := conn.Close(); cerr != nil {
					errCh <- fmt.Errorf("handshake: %w (close: %v)", err, cerr)
					return
				}
				errCh <- err
				return
			}
		}
		connCh <- conn
	}()

	// Dial the listener.
	clientConn, err := DialHTTPS(context.Background(), ln.Addr().String(), clientConf)
	if err != nil {
		t.Fatalf("DialHTTPS to listener error: %v", err)
	}
	defer func() {
		if err := clientConn.Close(); err != nil {
			t.Logf("close client conn: %v", err)
		}
	}()

	// Get the server-side connection.
	var serverConn net.Conn
	select {
	case serverConn = <-connCh:
		defer func() {
			if err := serverConn.Close(); err != nil {
				t.Logf("close server conn: %v", err)
			}
		}()
	case err := <-errCh:
		t.Fatalf("Accept error: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("Accept timed out — handshake may have failed silently")
	}

	// Verify bidirectional communication.
	payload := []byte("hello from client to listener")
	if _, err := clientConn.Write(payload); err != nil {
		t.Fatalf("client Write: %v", err)
	}

	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(serverConn, buf); err != nil {
		t.Fatalf("server Read: %v", err)
	}
	if !bytes.Equal(buf, payload) {
		t.Fatalf("server got %q, want %q", buf, payload)
	}

	// Send response back.
	response := []byte("ack from server")
	if _, err := serverConn.Write(response); err != nil {
		t.Fatalf("server Write: %v", err)
	}

	rbuf := make([]byte, len(response))
	if _, err := io.ReadFull(clientConn, rbuf); err != nil {
		t.Fatalf("client Read: %v", err)
	}
	if !bytes.Equal(rbuf, response) {
		t.Fatalf("client got %q, want %q", rbuf, response)
	}
}

// --- ListenHTTPS: context cancellation closes listener ---

func TestListenHTTPS_ContextCancelClosesListener(t *testing.T) {
	t.Parallel()

	serverConf, _ := testMTLSPair(t)

	ctx, cancel := context.WithCancel(context.Background())

	ln, err := ListenHTTPS(ctx, "127.0.0.1:0", serverConf)
	if err != nil {
		t.Fatalf("ListenHTTPS error: %v", err)
	}

	// Cancel the context — should close the listener.
	cancel()

	// Accept should return an error after context cancellation.
	// Use a short deadline to avoid hanging if cancel doesn't propagate.
	done := make(chan struct{})
	go func() {
		_, err := ln.Accept()
		if err == nil {
			t.Error("Accept should fail after context cancellation")
		}
		close(done)
	}()

	select {
	case <-done:
		// Success — Accept returned with an error.
	case <-time.After(5 * time.Second):
		t.Fatal("Accept did not return after context cancellation")
	}
}

// --- Large payload through mTLS ---

func TestDialHTTPS_LargePayload(t *testing.T) {
	t.Parallel()

	serverConf, clientConf := testMTLSPair(t)
	addr, cleanup := startTLSEchoServer(t, serverConf)
	defer cleanup()

	ctx := context.Background()
	conn, err := DialHTTPS(ctx, addr, clientConf)
	if err != nil {
		t.Fatalf("DialHTTPS error: %v", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Logf("close conn: %v", err)
		}
	}()

	const size = 256 * 1024 // 256KB through TLS
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i % 251)
	}

	// Write in a goroutine since the echo server buffers.
	writeErr := make(chan error, 1)
	go func() {
		_, err := conn.Write(payload)
		writeErr <- err
	}()

	received := make([]byte, size)
	if _, err := io.ReadFull(conn, received); err != nil {
		t.Fatalf("Read error: %v", err)
	}

	if err := <-writeErr; err != nil {
		t.Fatalf("Write error: %v", err)
	}

	if !bytes.Equal(received, payload) {
		t.Fatal("payload mismatch through TLS tunnel")
	}
}

// --- Concurrent connections ---

func TestDialHTTPS_ConcurrentConnections(t *testing.T) {
	t.Parallel()

	serverConf, clientConf := testMTLSPair(t)
	addr, cleanup := startTLSEchoServer(t, serverConf)
	defer cleanup()

	const numConns = 10
	var wg sync.WaitGroup
	wg.Add(numConns)

	errors := make(chan error, numConns)

	for i := 0; i < numConns; i++ {
		go func(id int) {
			defer wg.Done()

			ctx := context.Background()
			conn, err := DialHTTPS(ctx, addr, clientConf)
			if err != nil {
				errors <- err
				return
			}
			defer func() {
				if err := conn.Close(); err != nil {
					// expected in test
					return
				}
			}()

			payload := []byte("concurrent-" + string(rune('A'+id)))
			if _, err := conn.Write(payload); err != nil {
				errors <- err
				return
			}

			buf := make([]byte, len(payload))
			if _, err := io.ReadFull(conn, buf); err != nil {
				errors <- err
				return
			}

			if !bytes.Equal(buf, payload) {
				errors <- io.ErrUnexpectedEOF
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Errorf("concurrent connection error: %v", err)
	}
}

// --- Conn addresses are meaningful ---

func TestDialHTTPS_ConnAddresses(t *testing.T) {
	t.Parallel()

	serverConf, clientConf := testMTLSPair(t)
	addr, cleanup := startTLSEchoServer(t, serverConf)
	defer cleanup()

	ctx := context.Background()
	conn, err := DialHTTPS(ctx, addr, clientConf)
	if err != nil {
		t.Fatalf("DialHTTPS error: %v", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Logf("close conn: %v", err)
		}
	}()

	if conn.LocalAddr() == nil {
		t.Error("LocalAddr should not be nil")
	}
	if conn.RemoteAddr() == nil {
		t.Error("RemoteAddr should not be nil")
	}
	if conn.LocalAddr().Network() != "tcp" {
		t.Errorf("LocalAddr network = %q, want \"tcp\"", conn.LocalAddr().Network())
	}
}
