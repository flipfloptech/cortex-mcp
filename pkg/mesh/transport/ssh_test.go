package transport

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// --- SSH Test Helpers ---

// testSSHSigner generates an ECDSA key and returns an ssh.Signer for test use.
func testSSHSigner(t testing.TB) ssh.Signer {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate SSH key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatalf("create SSH signer: %v", err)
	}
	return signer
}

// testSSHServer starts an SSH server that accepts connections, authenticates
// via public key, and for each session runs an echo loop on stdin/stdout.
// This simulates a deployed mesh node echoing data back.
//
// Returns the server address and a cleanup function.
func testSSHServer(t testing.TB, hostSigner ssh.Signer, authorizedKey ssh.PublicKey) (addr string, cleanup func()) {
	t.Helper()

	serverConf := &ssh.ServerConfig{
		PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if bytes.Equal(key.Marshal(), authorizedKey.Marshal()) {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("unknown public key for %s", conn.User())
		},
	}
	serverConf.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	go func() {
		for {
			tcpConn, err := ln.Accept()
			if err != nil {
				return
			}
			go handleSSHConn(tcpConn, serverConf)
		}
	}()

	return ln.Addr().String(), func() {
		if err := ln.Close(); err != nil {
			t.Logf("cleanup: close listener: %v", err)
		}
	}
}

// handleSSHConn processes a single SSH connection: handshake, accept channels,
// and for "session" channels, echo stdin→stdout.
func handleSSHConn(tcpConn net.Conn, config *ssh.ServerConfig) {
	defer func() {
		if err := tcpConn.Close(); err != nil {
			// expected in test
			return
		}
	}()

	sshConn, chans, reqs, err := ssh.NewServerConn(tcpConn, config)
	if err != nil {
		return
	}
	defer func() {
		if err := sshConn.Close(); err != nil {
			// expected in test
			return
		}
	}()

	// Discard global requests.
	go ssh.DiscardRequests(reqs)

	for newChan := range chans {
		if newChan.ChannelType() != "session" && newChan.ChannelType() != "direct-tcpip" {
			if err := newChan.Reject(ssh.UnknownChannelType, "only session and direct-tcpip channels supported"); err != nil {
				return
			}
			continue
		}

		channel, requests, err := newChan.Accept()

		if err != nil {
			return
		}

		// Handle session requests (shell, exec, subsystem).
		go func() {
			for req := range requests {
				// Accept all requests — the mesh doesn't use shell/exec,
				// it just needs the raw byte channel.
				if req.WantReply {
					if err := req.Reply(true, nil); err != nil {
						return
					}
				}
			}
		}()

		// Echo loop: copy stdin to stdout until channel closes.
		go func() {
			defer func() {
				if err := channel.Close(); err != nil {
					// expected in test
					return
				}
			}()
			if _, err := io.Copy(channel, channel); err != nil {
				// expected on close
				return
			}
		}()
	}
}

// --- Happy path: DialSSH connects and roundtrips data ---

func TestDialSSH_Success(t *testing.T) {
	t.Parallel()

	hostSigner := testSSHSigner(t)
	clientSigner := testSSHSigner(t)

	addr, cleanup := testSSHServer(t, hostSigner, clientSigner.PublicKey())
	defer cleanup()

	clientConf := &ssh.ClientConfig{
		User: "mesh",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(clientSigner),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	ctx := context.Background()
	conn, err := DialSSH(ctx, addr, clientConf)
	if err != nil {
		t.Fatalf("DialSSH error: %v", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Logf("close conn: %v", err)
		}
	}()

	// Verify it satisfies net.Conn.
	var _ = conn

	// Verify roundtrip data through the SSH tunnel.
	payload := []byte("hello through SSH")
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

// --- Auth failure ---

func TestDialSSH_AuthFailure(t *testing.T) {
	t.Parallel()

	hostSigner := testSSHSigner(t)
	authorizedSigner := testSSHSigner(t)
	wrongSigner := testSSHSigner(t) // Different key — not authorized.

	addr, cleanup := testSSHServer(t, hostSigner, authorizedSigner.PublicKey())
	defer cleanup()

	clientConf := &ssh.ClientConfig{
		User: "mesh",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(wrongSigner),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	ctx := context.Background()
	conn, err := DialSSH(ctx, addr, clientConf)
	if err == nil {
		if cerr := conn.Close(); cerr != nil {
			t.Logf("close conn: %v", cerr)
		}
		t.Fatal("DialSSH should fail with wrong credentials")
	}
}

// --- Context cancellation ---

func TestDialSSH_ContextCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	clientConf := &ssh.ClientConfig{
		User:            "mesh",
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	// Use a non-routable address.
	_, err := DialSSH(ctx, "192.0.2.1:22", clientConf)
	if err == nil {
		t.Fatal("DialSSH should fail on canceled context")
	}
}

// --- Connection refused ---

func TestDialSSH_ConnectionRefused(t *testing.T) {
	t.Parallel()

	clientConf := &ssh.ClientConfig{
		User:            "mesh",
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	ctx := context.Background()
	_, err := DialSSH(ctx, "127.0.0.1:1", clientConf)
	if err == nil {
		t.Fatal("DialSSH should fail when target is unreachable")
	}
}

// --- Close is idempotent ---

func TestDialSSH_CloseIdempotent(t *testing.T) {
	t.Parallel()

	hostSigner := testSSHSigner(t)
	clientSigner := testSSHSigner(t)

	addr, cleanup := testSSHServer(t, hostSigner, clientSigner.PublicKey())
	defer cleanup()

	clientConf := &ssh.ClientConfig{
		User: "mesh",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(clientSigner),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	ctx := context.Background()
	conn, err := DialSSH(ctx, addr, clientConf)
	if err != nil {
		t.Fatalf("DialSSH error: %v", err)
	}

	// First close should succeed.
	if err := conn.Close(); err != nil {
		t.Fatalf("first Close error: %v", err)
	}

	// Second close should not panic.
	_ = conn.Close()
}

// --- Post-close read/write returns error ---

func TestDialSSH_PostCloseReturnsError(t *testing.T) {
	t.Parallel()

	hostSigner := testSSHSigner(t)
	clientSigner := testSSHSigner(t)

	addr, cleanup := testSSHServer(t, hostSigner, clientSigner.PublicKey())
	defer cleanup()

	clientConf := &ssh.ClientConfig{
		User: "mesh",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(clientSigner),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	ctx := context.Background()
	conn, err := DialSSH(ctx, addr, clientConf)
	if err != nil {
		t.Fatalf("DialSSH error: %v", err)
	}

	if err := conn.Close(); err != nil {
		t.Logf("close conn: %v", err)
	}

	// Read and Write after close should return errors.
	_, err = conn.Read(make([]byte, 1))
	if err == nil {
		t.Error("Read after Close should return error")
	}

	_, err = conn.Write([]byte("data"))
	if err == nil {
		t.Error("Write after Close should return error")
	}
}

// --- Large payload through SSH ---

func TestDialSSH_LargePayload(t *testing.T) {
	t.Parallel()

	hostSigner := testSSHSigner(t)
	clientSigner := testSSHSigner(t)

	addr, cleanup := testSSHServer(t, hostSigner, clientSigner.PublicKey())
	defer cleanup()

	clientConf := &ssh.ClientConfig{
		User: "mesh",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(clientSigner),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	ctx := context.Background()
	conn, err := DialSSH(ctx, addr, clientConf)
	if err != nil {
		t.Fatalf("DialSSH error: %v", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Logf("close conn: %v", err)
		}
	}()

	const size = 256 * 1024 // 256KB
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i % 251)
	}

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
		t.Fatal("payload mismatch through SSH tunnel")
	}
}

// --- Concurrent connections ---

func TestDialSSH_ConcurrentConnections(t *testing.T) {
	t.Parallel()

	hostSigner := testSSHSigner(t)
	clientSigner := testSSHSigner(t)

	addr, cleanup := testSSHServer(t, hostSigner, clientSigner.PublicKey())
	defer cleanup()

	clientConf := &ssh.ClientConfig{
		User: "mesh",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(clientSigner),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	const numConns = 10
	var wg sync.WaitGroup
	wg.Add(numConns)

	errs := make(chan error, numConns)

	for i := 0; i < numConns; i++ {
		go func(id int) {
			defer wg.Done()

			ctx := context.Background()
			conn, err := DialSSH(ctx, addr, clientConf)
			if err != nil {
				errs <- err
				return
			}
			defer func() {
				if err := conn.Close(); err != nil {
					// expected in test
					return
				}
			}()

			payload := []byte(fmt.Sprintf("concurrent-%d", id))
			if _, err := conn.Write(payload); err != nil {
				errs <- err
				return
			}

			buf := make([]byte, len(payload))
			if _, err := io.ReadFull(conn, buf); err != nil {
				errs <- err
				return
			}

			if !bytes.Equal(buf, payload) {
				errs <- fmt.Errorf("conn %d: payload mismatch", id)
			}
		}(i)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent connection error: %v", err)
	}
}

// --- Address semantics ---

func TestDialSSH_ConnAddresses(t *testing.T) {
	t.Parallel()

	hostSigner := testSSHSigner(t)
	clientSigner := testSSHSigner(t)

	addr, cleanup := testSSHServer(t, hostSigner, clientSigner.PublicKey())
	defer cleanup()

	clientConf := &ssh.ClientConfig{
		User: "mesh",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(clientSigner),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	ctx := context.Background()
	conn, err := DialSSH(ctx, addr, clientConf)
	if err != nil {
		t.Fatalf("DialSSH error: %v", err)
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
	// SSH transport wraps the underlying TCP, so network should be "ssh".
	if conn.LocalAddr().Network() != "ssh" {
		t.Errorf("LocalAddr network = %q, want \"ssh\"", conn.LocalAddr().Network())
	}
}

// --- DialSSHTunnel Success ---

func TestDialSSHTunnel_Success(t *testing.T) {
	t.Parallel()

	hostSigner := testSSHSigner(t)
	clientSigner := testSSHSigner(t)

	addr, cleanup := testSSHServer(t, hostSigner, clientSigner.PublicKey())
	defer cleanup()

	clientConf := &ssh.ClientConfig{
		User: "mesh",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(clientSigner),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	ctx := context.Background()
	conn, err := DialSSHTunnel(ctx, addr, "10.0.0.1:4443", clientConf)
	if err != nil {
		t.Fatalf("DialSSHTunnel error: %v", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Logf("close conn: %v", err)
		}
	}()

	payload := []byte("hello through SSH Tunnel")
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
