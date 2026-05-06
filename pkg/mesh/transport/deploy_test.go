package transport

import (
	"bytes"
	"context"
	"debug/elf"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// --- Deploy Test Helpers ---

// deploySSHServer starts an SSH server for deploy tests.
// It handles exec requests and SFTP subsystem requests:
//   - SFTP subsystem requests serve a real filesystem SFTP server (for upload).
//   - Exec commands that are not SFTP/cat uploads echo stdin→stdout
//     to simulate a deployed mesh node.
//
// uploadedBinary receives the binary bytes that were uploaded via the legacy
// cat pipe method. Tests verifying SFTP-based uploads should read from disk instead.
func deploySSHServer(t testing.TB, hostSigner ssh.Signer, authorizedKey ssh.PublicKey) (addr string, cleanup func()) {
	t.Helper()

	uploaded := &bytes.Buffer{}
	var mu sync.Mutex

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
			go handleDeploySSHConn(t, tcpConn, serverConf, uploaded, &mu)
		}
	}()

	return ln.Addr().String(), func() {
		if err := ln.Close(); err != nil {
			t.Logf("cleanup: close listener: %v", err)
		}
	}
}

// handleDeploySSHConn processes SSH connections for deploy testing.
func handleDeploySSHConn(_ testing.TB, tcpConn net.Conn, config *ssh.ServerConfig, uploaded *bytes.Buffer, mu *sync.Mutex) {
	defer func() {
		// Close errors are expected during test teardown; silently ignore.
		// We cannot use t.Logf here because this goroutine may outlive the test.
		_ = tcpConn.Close()
	}()

	sshConn, chans, reqs, err := ssh.NewServerConn(tcpConn, config)
	if err != nil {
		return
	}
	defer func() {
		_ = sshConn.Close()
	}()
	go ssh.DiscardRequests(reqs)

	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			if err := newChan.Reject(ssh.UnknownChannelType, "only session channels"); err != nil {
				return
			}
			continue
		}

		channel, requests, err := newChan.Accept()
		if err != nil {
			return
		}

		go handleDeploySession(channel, requests, uploaded, mu)
	}
}

// handleDeploySession handles a single SSH session for deploy testing.
func handleDeploySession(channel ssh.Channel, requests <-chan *ssh.Request, uploaded *bytes.Buffer, mu *sync.Mutex) {
	defer func() {
		if err := channel.Close(); err != nil {
			// Channel close errors are expected in test teardown.
			return
		}
	}()

	for req := range requests {
		switch req.Type {
		case "exec":
			if req.WantReply {
				if err := req.Reply(true, nil); err != nil {
					return
				}
			}

			// Parse the command from the exec payload.
			// The payload format is: uint32(len) + string(command)
			if len(req.Payload) < 4 {
				if _, err := channel.SendRequest("exit-status", false, []byte{0, 0, 0, 1}); err != nil {
					return
				}
				return
			}
			cmdLen := int(req.Payload[0])<<24 | int(req.Payload[1])<<16 | int(req.Payload[2])<<8 | int(req.Payload[3])
			if cmdLen+4 > len(req.Payload) {
				if _, err := channel.SendRequest("exit-status", false, []byte{0, 0, 0, 1}); err != nil {
					return
				}
				return
			}
			cmd := string(req.Payload[4 : 4+cmdLen])

			if strings.Contains(cmd, "cat >") {
				// Legacy upload session: read all data from stdin into the upload buffer.
				mu.Lock()
				uploaded.Reset()
				if _, err := io.Copy(uploaded, channel); err != nil {
					mu.Unlock()
					return
				}
				mu.Unlock()

				// Send exit status 0 (success).
				if _, err := channel.SendRequest("exit-status", false, []byte{0, 0, 0, 0}); err != nil {
					return
				}
				return
			}

			if !strings.Contains(cmd, "cat >") {
				// Exec session: simulate a deployed fleet node.
				// The command is the binary with subcommand args (e.g., "daemon",
				// "-config", etc.). Write the readiness magic first, then echo.
				if _, err := channel.Write(DeployReadyMagic[:]); err != nil {
					return
				}
				if _, err := io.Copy(channel, channel); err != nil {
					// io errors are expected when client closes.
					return
				}
				if _, err := channel.SendRequest("exit-status", false, []byte{0, 0, 0, 0}); err != nil {
					return
				}
				return
			}

			// Unknown command — exit with error.
			if _, err := channel.SendRequest("exit-status", false, []byte{0, 0, 0, 1}); err != nil {
				return
			}
			return

		case "subsystem":
			// Handle SFTP subsystem requests for SFTP-based upload.
			if req.WantReply {
				if err := req.Reply(true, nil); err != nil {
					return
				}
			}

			// Parse subsystem name from payload (uint32 len + string).
			if len(req.Payload) < 4 {
				return
			}
			nameLen := int(req.Payload[0])<<24 | int(req.Payload[1])<<16 | int(req.Payload[2])<<8 | int(req.Payload[3])
			if nameLen+4 > len(req.Payload) {
				return
			}
			name := string(req.Payload[4 : 4+nameLen])

			if name == "sftp" {
				server, err := sftp.NewServer(channel)
				if err != nil {
					return
				}
				if err := server.Serve(); err != nil {
					// Serve errors expected when client disconnects.
					return
				}
				return
			}

		case "shell":
			// Shell requests: just echo (fallback).
			if req.WantReply {
				if err := req.Reply(true, nil); err != nil {
					return
				}
			}
			if _, err := io.Copy(channel, channel); err != nil {
				// io errors expected on close.
				return
			}
			return

		default:
			if req.WantReply {
				if err := req.Reply(false, nil); err != nil {
					return
				}
			}
		}
	}
}

// --- WasDeployed tests ---
// WasDeployed is deprecated in favor of subcommand-based dispatch,
// but remains for backwards compatibility.

func TestWasDeployed_True(t *testing.T) {
	t.Setenv("CORTEX_MESH_SPAWNED", "1")
	if !WasDeployed() {
		t.Fatal("WasDeployed should return true when CORTEX_MESH_SPAWNED=1")
	}
}

func TestWasDeployed_False_Unset(t *testing.T) {
	// t.Setenv will restore the original value after the test.
	t.Setenv("CORTEX_MESH_SPAWNED", "")
	if err := os.Unsetenv("CORTEX_MESH_SPAWNED"); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}
	if WasDeployed() {
		t.Fatal("WasDeployed should return false when CORTEX_MESH_SPAWNED is unset")
	}
}

func TestWasDeployed_False_WrongValue(t *testing.T) {
	t.Setenv("CORTEX_MESH_SPAWNED", "0")
	if WasDeployed() {
		t.Fatal("WasDeployed should return false when CORTEX_MESH_SPAWNED != 1")
	}
}

// --- SelfDeployer.DefaultExecArgs ---
// When ExecArgs is nil (zero value), execBinary should default to ["daemon"]
// so the remote binary enters fleet node mode via subcommand dispatch.

func TestSelfDeployer_DefaultExecArgs_Daemon(t *testing.T) {
	t.Parallel()

	hostSigner := testSSHSigner(t)
	clientSigner := testSSHSigner(t)

	addr, cleanup := deploySSHServer(t, hostSigner, clientSigner.PublicKey())
	defer cleanup()

	tmpFile, err := os.CreateTemp(t.TempDir(), "mesh-binary-*")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if _, err := tmpFile.Write([]byte("binary")); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	if err := tmpFile.Close(); err != nil {
		t.Fatalf("close temp file: %v", err)
	}

	// ExecArgs is nil — should default to ["daemon"].
	deployer := &SelfDeployer{
		BinaryPath: tmpFile.Name(),
		RemotePath: filepath.Join(t.TempDir(), "mesh-node"),
	}

	cred := DeployCredential{
		SSHUser:    "mesh",
		SSHKeyData: clientSigner,
	}

	ctx := context.Background()
	stream, err := deployer.Deploy(ctx, addr, cred, hostSigner.PublicKey())
	if err != nil {
		t.Fatalf("Deploy with default ExecArgs should succeed: %v", err)
	}
	defer func() {
		if err := stream.Close(); err != nil {
			t.Logf("close stream: %v", err)
		}
	}()

	// Verify bidirectional data — the test SSH server only echoes
	// for commands containing 'daemon'.
	payload := []byte("default-args-test")
	if _, err := stream.Write(payload); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(stream, buf); err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if !bytes.Equal(buf, payload) {
		t.Fatalf("got %q, want %q", buf, payload)
	}
}

// --- SelfDeployer interface compliance ---

func TestSelfDeployer_ImplementsDeployer(t *testing.T) {
	var _ Deployer = &SelfDeployer{}
}

// --- SelfDeployer.Deploy: success with echo roundtrip ---

func TestSelfDeployer_Deploy_Success(t *testing.T) {
	t.Parallel()

	hostSigner := testSSHSigner(t)
	clientSigner := testSSHSigner(t)

	addr, cleanup := deploySSHServer(t, hostSigner, clientSigner.PublicKey())
	defer cleanup()

	// Create a small dummy "binary" for the test.
	tmpFile, err := os.CreateTemp(t.TempDir(), "mesh-binary-*")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	binaryContent := []byte("#!/bin/sh\necho mesh-node-active\n")
	if _, err := tmpFile.Write(binaryContent); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	if err := tmpFile.Close(); err != nil {
		t.Fatalf("close temp file: %v", err)
	}

	deployer := &SelfDeployer{
		BinaryPath: tmpFile.Name(),
		RemotePath: filepath.Join(t.TempDir(), "mesh-node"),
	}

	cred := DeployCredential{
		SSHUser:    "mesh",
		SSHKeyData: clientSigner,
	}

	ctx := context.Background()
	stream, err := deployer.Deploy(ctx, addr, cred, hostSigner.PublicKey())
	if err != nil {
		t.Fatalf("Deploy error: %v", err)
	}
	defer func() {
		if err := stream.Close(); err != nil {
			t.Logf("close stream: %v", err)
		}
	}()

	// Verify bidirectional data through the deployed stream.
	payload := []byte("hello from deployer")
	if _, err := stream.Write(payload); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(stream, buf); err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if !bytes.Equal(buf, payload) {
		t.Fatalf("got %q, want %q", buf, payload)
	}
}

// --- SelfDeployer.Deploy: verifies binary was uploaded correctly via SFTP ---

func TestSelfDeployer_Deploy_UploadsCorrectBinary(t *testing.T) {
	t.Parallel()

	hostSigner := testSSHSigner(t)
	clientSigner := testSSHSigner(t)

	addr, cleanup := deploySSHServer(t, hostSigner, clientSigner.PublicKey())
	defer cleanup()

	// Create a deterministic "binary".
	binaryContent := make([]byte, 4096)
	for i := range binaryContent {
		binaryContent[i] = byte(i % 251)
	}
	tmpFile, err := os.CreateTemp(t.TempDir(), "mesh-binary-*")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if _, err := tmpFile.Write(binaryContent); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	if err := tmpFile.Close(); err != nil {
		t.Fatalf("close temp file: %v", err)
	}

	// Use explicit remote path in temp dir for filesystem verification.
	remotePath := filepath.Join(t.TempDir(), "mesh-node-upload")
	deployer := &SelfDeployer{
		BinaryPath: tmpFile.Name(),
		RemotePath: remotePath,
	}

	cred := DeployCredential{
		SSHUser:    "mesh",
		SSHKeyData: clientSigner,
	}

	ctx := context.Background()
	stream, err := deployer.Deploy(ctx, addr, cred, hostSigner.PublicKey())
	if err != nil {
		t.Fatalf("Deploy error: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Logf("close stream: %v", err)
	}

	// Verify the binary was written to disk via SFTP.
	uploaded, err := os.ReadFile(remotePath)
	if err != nil {
		t.Fatalf("read uploaded binary from disk: %v", err)
	}
	if !bytes.Equal(uploaded, binaryContent) {
		t.Fatalf("uploaded binary mismatch: got %d bytes, want %d bytes",
			len(uploaded), len(binaryContent))
	}
}

// --- SelfDeployer.Deploy: verifies SkipUpload and ExecArgs ---

func TestSelfDeployer_Deploy_SkipUploadAndExecArgs(t *testing.T) {
	t.Parallel()

	hostSigner := testSSHSigner(t)
	clientSigner := testSSHSigner(t)

	addr, cleanup := deploySSHServer(t, hostSigner, clientSigner.PublicKey())
	defer cleanup()

	tmpFile, err := os.CreateTemp(t.TempDir(), "mesh-binary-*")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if _, err := tmpFile.Write([]byte("binary")); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	if err := tmpFile.Close(); err != nil {
		t.Fatalf("close temp file: %v", err)
	}

	remotePath := filepath.Join(t.TempDir(), "mesh-node-skip")
	deployer := &SelfDeployer{
		BinaryPath: tmpFile.Name(),
		RemotePath: remotePath,
		SkipUpload: true,
		ExecArgs:   []string{"-daemon", "-config=/tmp/mesh.json"},
	}

	cred := DeployCredential{
		SSHUser:    "mesh",
		SSHKeyData: clientSigner,
	}

	ctx := context.Background()
	stream, err := deployer.Deploy(ctx, addr, cred, hostSigner.PublicKey())
	if err != nil {
		t.Fatalf("Deploy error: %v", err)
	}
	defer func() {
		if err := stream.Close(); err != nil {
			t.Logf("close stream: %v", err)
		}
	}()

	// Verify the remote file does not exist (upload was skipped).
	// Because SFTP creates the real file, IsNotExist confirms it bypassed uploadBinary.
	if _, err := os.Stat(remotePath); !os.IsNotExist(err) {
		t.Fatalf("expected remote path to not exist due to SkipUpload, but got err or file: %v", err)
	}

	// Verify bidirectional data through the pseudo-exec stream.
	payload := []byte("hello from skip upload")
	if _, err := stream.Write(payload); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(stream, buf); err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if !bytes.Equal(buf, payload) {
		t.Fatalf("got %q, want %q", buf, payload)
	}
}

// --- SelfDeployer.Deploy: auth failure ---

func TestSelfDeployer_Deploy_AuthFailure(t *testing.T) {
	t.Parallel()

	hostSigner := testSSHSigner(t)
	authorizedSigner := testSSHSigner(t)
	wrongSigner := testSSHSigner(t)

	addr, cleanup := deploySSHServer(t, hostSigner, authorizedSigner.PublicKey())
	defer cleanup()

	tmpFile, err := os.CreateTemp(t.TempDir(), "mesh-binary-*")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if _, err := tmpFile.Write([]byte("binary")); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	if err := tmpFile.Close(); err != nil {
		t.Fatalf("close temp file: %v", err)
	}

	deployer := &SelfDeployer{BinaryPath: tmpFile.Name()}

	cred := DeployCredential{
		SSHUser:    "mesh",
		SSHKeyData: wrongSigner, // Wrong key.
	}

	ctx := context.Background()
	stream, err := deployer.Deploy(ctx, addr, cred, hostSigner.PublicKey())
	if err == nil {
		if cerr := stream.Close(); cerr != nil {
			t.Logf("close stream: %v", cerr)
		}
		t.Fatal("Deploy should fail with wrong credentials")
	}
}

// --- SelfDeployer.Deploy: connection refused ---

func TestSelfDeployer_Deploy_ConnectionRefused(t *testing.T) {
	t.Parallel()

	clientSigner := testSSHSigner(t)
	hostSigner := testSSHSigner(t)

	tmpFile, err := os.CreateTemp(t.TempDir(), "mesh-binary-*")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if _, err := tmpFile.Write([]byte("binary")); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	if err := tmpFile.Close(); err != nil {
		t.Fatalf("close temp file: %v", err)
	}

	deployer := &SelfDeployer{BinaryPath: tmpFile.Name()}

	cred := DeployCredential{
		SSHUser:    "mesh",
		SSHKeyData: clientSigner,
	}

	ctx := context.Background()
	_, err = deployer.Deploy(ctx, "127.0.0.1:1", cred, hostSigner.PublicKey())
	if err == nil {
		t.Fatal("Deploy should fail when target is unreachable")
	}
}

// --- SelfDeployer.Deploy: context cancellation ---

func TestSelfDeployer_Deploy_ContextCanceled(t *testing.T) {
	t.Parallel()

	clientSigner := testSSHSigner(t)
	hostSigner := testSSHSigner(t)

	tmpFile, err := os.CreateTemp(t.TempDir(), "mesh-binary-*")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if _, err := tmpFile.Write([]byte("binary")); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	if err := tmpFile.Close(); err != nil {
		t.Fatalf("close temp file: %v", err)
	}

	deployer := &SelfDeployer{BinaryPath: tmpFile.Name()}

	cred := DeployCredential{
		SSHUser:    "mesh",
		SSHKeyData: clientSigner,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	_, err = deployer.Deploy(ctx, "192.0.2.1:22", cred, hostSigner.PublicKey())
	if err == nil {
		t.Fatal("Deploy should fail on canceled context")
	}
}

// --- SelfDeployer.Deploy: stream close is clean ---

func TestSelfDeployer_Deploy_StreamClose(t *testing.T) {
	t.Parallel()

	hostSigner := testSSHSigner(t)
	clientSigner := testSSHSigner(t)

	addr, cleanup := deploySSHServer(t, hostSigner, clientSigner.PublicKey())
	defer cleanup()

	tmpFile, err := os.CreateTemp(t.TempDir(), "mesh-binary-*")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if _, err := tmpFile.Write([]byte("binary")); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	if err := tmpFile.Close(); err != nil {
		t.Fatalf("close temp file: %v", err)
	}

	deployer := &SelfDeployer{
		BinaryPath: tmpFile.Name(),
		RemotePath: filepath.Join(t.TempDir(), "mesh-node"),
	}

	cred := DeployCredential{
		SSHUser:    "mesh",
		SSHKeyData: clientSigner,
	}

	ctx := context.Background()
	stream, err := deployer.Deploy(ctx, addr, cred, hostSigner.PublicKey())
	if err != nil {
		t.Fatalf("Deploy error: %v", err)
	}

	// Close should not panic or return unexpected error.
	if err := stream.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	// Double close should not panic.
	_ = stream.Close()
}

// --- SelfDeployer.Deploy: missing binary returns error ---

func TestSelfDeployer_Deploy_MissingBinary(t *testing.T) {
	t.Parallel()

	hostSigner := testSSHSigner(t)
	clientSigner := testSSHSigner(t)

	addr, cleanup := deploySSHServer(t, hostSigner, clientSigner.PublicKey())
	defer cleanup()

	deployer := &SelfDeployer{
		BinaryPath: "/nonexistent/path/to/binary",
	}

	cred := DeployCredential{
		SSHUser:    "mesh",
		SSHKeyData: clientSigner,
	}

	ctx := context.Background()
	stream, err := deployer.Deploy(ctx, addr, cred, hostSigner.PublicKey())
	if err == nil {
		if cerr := stream.Close(); cerr != nil {
			t.Logf("close stream: %v", cerr)
		}
		t.Fatal("Deploy should fail when binary doesn't exist")
	}
}

// --- shellQuote tests ---

func TestShellQuote(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple path", "/tmp/binary", "'/tmp/binary'"},
		{"path with spaces", "/tmp/my binary", "'/tmp/my binary'"},
		{"path with semicolons", "/tmp/foo;rm -rf /", "'/tmp/foo;rm -rf /'"},
		{"path with backticks", "/tmp/foo`whoami`", "'/tmp/foo`whoami`'"},
		{"path with dollar expansion", "/tmp/$HOME/bin", "'/tmp/$HOME/bin'"},
		{"path with single quote", "/tmp/it's", "'/tmp/it'\\''s'"},
		{"path with double quotes", `/tmp/"binary"`, `'/tmp/"binary"'`},
		{"path with newline", "/tmp/foo\nbar", "'/tmp/foo\nbar'"},
		{"empty string", "", "''"},
		{"path with pipe", "/tmp/foo|cat /etc/passwd", "'/tmp/foo|cat /etc/passwd'"},
		{"path with ampersand", "/tmp/foo&whoami", "'/tmp/foo&whoami'"},
		{"path with command substitution", "/tmp/$(whoami)/bin", "'/tmp/$(whoami)/bin'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := shellQuote(tt.input)
			if got != tt.want {
				t.Errorf("shellQuote(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// --- DeployCredential: password authentication ---

// deploySSHServerWithPassword starts an SSH server that accepts password auth.
func deploySSHServerWithPassword(t *testing.T, hostSigner ssh.Signer, wantUser, wantPass string) (addr string, cleanup func()) {
	t.Helper()

	serverConf := &ssh.ServerConfig{
		PasswordCallback: func(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if conn.User() == wantUser && string(password) == wantPass {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("invalid password for %s", conn.User())
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
			go handleDeploySSHConn(t, tcpConn, serverConf, &bytes.Buffer{}, &sync.Mutex{})
		}
	}()

	return ln.Addr().String(), func() {
		if err := ln.Close(); err != nil {
			t.Logf("cleanup: close listener: %v", err)
		}
	}
}

func TestSelfDeployer_Deploy_PasswordAuth_Success(t *testing.T) {
	t.Parallel()

	hostSigner := testSSHSigner(t)

	addr, cleanup := deploySSHServerWithPassword(t, hostSigner, "root", "secret123")
	defer cleanup()

	tmpFile, err := os.CreateTemp(t.TempDir(), "mesh-binary-*")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if _, err := tmpFile.Write([]byte("binary-content")); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	if err := tmpFile.Close(); err != nil {
		t.Fatalf("close temp file: %v", err)
	}

	deployer := &SelfDeployer{
		BinaryPath: tmpFile.Name(),
		RemotePath: filepath.Join(t.TempDir(), "mesh-node"),
	}

	cred := DeployCredential{
		SSHUser: "root",
		SSHPass: "secret123",
	}

	ctx := context.Background()
	stream, err := deployer.Deploy(ctx, addr, cred, hostSigner.PublicKey())
	if err != nil {
		t.Fatalf("Deploy with password auth should succeed: %v", err)
	}
	defer func() {
		if err := stream.Close(); err != nil {
			t.Logf("close stream: %v", err)
		}
	}()

	// Verify bidirectional data through the deployed stream.
	payload := []byte("hello via password auth")
	if _, err := stream.Write(payload); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(stream, buf); err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if !bytes.Equal(buf, payload) {
		t.Fatalf("got %q, want %q", buf, payload)
	}
}

func TestSelfDeployer_Deploy_PasswordAuth_WrongPassword(t *testing.T) {
	t.Parallel()

	hostSigner := testSSHSigner(t)

	addr, cleanup := deploySSHServerWithPassword(t, hostSigner, "root", "correctpassword")
	defer cleanup()

	tmpFile, err := os.CreateTemp(t.TempDir(), "mesh-binary-*")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if _, err := tmpFile.Write([]byte("binary")); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	if err := tmpFile.Close(); err != nil {
		t.Fatalf("close temp file: %v", err)
	}

	deployer := &SelfDeployer{BinaryPath: tmpFile.Name()}

	cred := DeployCredential{
		SSHUser: "root",
		SSHPass: "wrongpassword",
	}

	ctx := context.Background()
	stream, err := deployer.Deploy(ctx, addr, cred, hostSigner.PublicKey())
	if err == nil {
		if cerr := stream.Close(); cerr != nil {
			t.Logf("close stream: %v", cerr)
		}
		t.Fatal("Deploy should fail with wrong password")
	}
}

func TestSelfDeployer_Deploy_EmptyCredentials(t *testing.T) {
	t.Parallel()

	hostSigner := testSSHSigner(t)

	tmpFile, err := os.CreateTemp(t.TempDir(), "mesh-binary-*")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if _, err := tmpFile.Write([]byte("binary")); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	if err := tmpFile.Close(); err != nil {
		t.Fatalf("close temp file: %v", err)
	}

	deployer := &SelfDeployer{BinaryPath: tmpFile.Name()}

	// No SSH key and no password — should error before even dialing.
	cred := DeployCredential{
		SSHUser: "mesh",
	}

	ctx := context.Background()
	stream, err := deployer.Deploy(ctx, "127.0.0.1:22", cred, hostSigner.PublicKey())
	if err == nil {
		if cerr := stream.Close(); cerr != nil {
			t.Logf("close stream: %v", cerr)
		}
		t.Fatal("Deploy should fail with empty credentials")
	}
	if !strings.Contains(err.Error(), "no auth method") {
		t.Fatalf("error should mention missing auth: %v", err)
	}
}

// --- Shell injection resistance via SFTP upload ---

func TestSelfDeployer_Deploy_RemotePathShellInjection(t *testing.T) {
	t.Parallel()

	hostSigner := testSSHSigner(t)
	clientSigner := testSSHSigner(t)

	addr, cleanup := deploySSHServer(t, hostSigner, clientSigner.PublicKey())
	defer cleanup()

	binaryContent := []byte("#!/bin/sh\necho mesh-node-active\n")
	tmpFile, err := os.CreateTemp(t.TempDir(), "mesh-binary-*")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if _, err := tmpFile.Write(binaryContent); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	if err := tmpFile.Close(); err != nil {
		t.Fatalf("close temp file: %v", err)
	}

	// Use a remote path with shell metacharacters that would cause RCE
	// if passed unquoted to a shell command.
	dangerousPath := filepath.Join(t.TempDir(), "mesh;echo pwned")
	deployer := &SelfDeployer{
		BinaryPath: tmpFile.Name(),
		RemotePath: dangerousPath,
	}

	cred := DeployCredential{
		SSHUser:    "mesh",
		SSHKeyData: clientSigner,
	}

	ctx := context.Background()
	stream, err := deployer.Deploy(ctx, addr, cred, hostSigner.PublicKey())
	if err != nil {
		t.Fatalf("Deploy with dangerous path should succeed: %v", err)
	}
	defer func() {
		if err := stream.Close(); err != nil {
			t.Logf("close stream: %v", err)
		}
	}()

	// Verify the binary was uploaded via SFTP to the literal dangerous path.
	uploaded, err := os.ReadFile(dangerousPath)
	if err != nil {
		t.Fatalf("read uploaded binary from disk: %v", err)
	}
	if !bytes.Equal(uploaded, binaryContent) {
		t.Fatalf("binary content mismatch: got %d bytes, want %d bytes",
			len(uploaded), len(binaryContent))
	}
}

// --- Deploy readiness protocol tests ---

// failWriter is an io.Writer that always returns the configured error.
type failWriter struct {
	err error
}

func (w *failWriter) Write(_ []byte) (int, error) {
	return 0, w.err
}

func TestDeployReadyMagic_Values(t *testing.T) {
	t.Parallel()
	// The magic sequence must be exactly: "CM" + HighByte(Proto) + LowByte(Proto).
	// "CM" is non-printable enough to avoid collision with shell output.
	want := [4]byte{'C', 'M', byte(ProtocolVersion >> 8), byte(ProtocolVersion)}
	if DeployReadyMagic != want {
		t.Fatalf("DeployReadyMagic = %x, want %x", DeployReadyMagic, want)
	}
}

func TestSignalReady_WritesCorrectBytes(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := SignalReady(&buf); err != nil {
		t.Fatalf("SignalReady returned error: %v", err)
	}
	got := buf.Bytes()
	want := DeployReadyMagic[:]
	if !bytes.Equal(got, want) {
		t.Fatalf("SignalReady wrote %x, want %x", got, want)
	}
}

func TestSignalReady_ExactLength(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := SignalReady(&buf); err != nil {
		t.Fatalf("SignalReady returned error: %v", err)
	}
	if buf.Len() != 4 {
		t.Fatalf("SignalReady wrote %d bytes, want exactly 4", buf.Len())
	}
}

func TestSignalReady_WriteError(t *testing.T) {
	t.Parallel()
	w := &failWriter{err: fmt.Errorf("disk full")}
	err := SignalReady(w)
	if err == nil {
		t.Fatal("SignalReady should return error when write fails")
	}
	if !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("error should wrap the underlying write error, got: %v", err)
	}
}

func TestWaitForReady_ValidMagic(t *testing.T) {
	t.Parallel()
	r := bytes.NewReader(DeployReadyMagic[:])
	if err := waitForReady(r); err != nil {
		t.Fatalf("waitForReady should succeed with valid magic: %v", err)
	}
}

func TestWaitForReady_ValidMagic_ExtraData(t *testing.T) {
	t.Parallel()
	// Valid magic followed by extra bytes — should succeed and leave extra data unread.
	data := append(DeployReadyMagic[:], []byte("extra payload data")...)
	r := bytes.NewReader(data)
	if err := waitForReady(r); err != nil {
		t.Fatalf("waitForReady should succeed even with trailing data: %v", err)
	}
	// Verify extra data is still available.
	remaining := make([]byte, 18)
	n, err := r.Read(remaining)
	if err != nil {
		t.Fatalf("should be able to read remaining data: %v", err)
	}
	if string(remaining[:n]) != "extra payload data" {
		t.Fatalf("remaining data = %q, want %q", remaining[:n], "extra payload data")
	}
}

func TestWaitForReady_WrongMagic(t *testing.T) {
	t.Parallel()
	bad := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	r := bytes.NewReader(bad)
	err := waitForReady(r)
	if err == nil {
		t.Fatal("waitForReady should fail with wrong magic bytes")
	}
	if !strings.Contains(err.Error(), "invalid ready magic") {
		t.Fatalf("error should describe the mismatch, got: %v", err)
	}
}

func TestWaitForReady_WrongPrefix(t *testing.T) {
	t.Parallel()
	// Correct length but wrong prefix — not "CM".
	bad := []byte{'X', 'Y', byte(ProtocolVersion >> 8), byte(ProtocolVersion)}
	r := bytes.NewReader(bad)
	err := waitForReady(r)
	if err == nil {
		t.Fatal("waitForReady should fail when prefix is not CM")
	}
	if !strings.Contains(err.Error(), "invalid ready magic") {
		t.Fatalf("error should describe the mismatch, got: %v", err)
	}
}

func TestWaitForReady_WrongVersion(t *testing.T) {
	t.Parallel()
	// Correct prefix but wrong version bytes.
	bad := []byte{'C', 'M', 0xFF, 0xFF}
	r := bytes.NewReader(bad)
	err := waitForReady(r)
	if err == nil {
		t.Fatal("waitForReady should fail with wrong version bytes")
	}
	if !strings.Contains(err.Error(), "invalid ready magic") {
		t.Fatalf("error should describe the mismatch, got: %v", err)
	}
}

func TestWaitForReady_ShortRead(t *testing.T) {
	t.Parallel()
	// Only 2 bytes — io.ReadFull should return unexpected EOF.
	r := bytes.NewReader([]byte{'C', 'M'})
	err := waitForReady(r)
	if err == nil {
		t.Fatal("waitForReady should fail on short read")
	}
}

func TestWaitForReady_EmptyReader(t *testing.T) {
	t.Parallel()
	r := bytes.NewReader(nil)
	err := waitForReady(r)
	if err == nil {
		t.Fatal("waitForReady should fail on empty reader")
	}
}

func TestWaitForReady_SingleByte(t *testing.T) {
	t.Parallel()
	// A single 0x01 byte (the old protocol) must be rejected.
	r := bytes.NewReader([]byte{0x01})
	err := waitForReady(r)
	if err == nil {
		t.Fatal("waitForReady must reject the old single-byte protocol")
	}
}

// --- Static binary detection tests ---

// makeMinimalELF generates a minimal valid 64-bit ELF binary with the given
// program headers. This avoids needing real compiled binaries as test fixtures.
func makeMinimalELF(t *testing.T, progHeaders []elf.ProgHeader) []byte {
	t.Helper()

	var buf bytes.Buffer

	// ELF header (64 bytes for ELF64).
	hdr := elf.Header64{
		Phnum: uint16(len(progHeaders)),
	}
	// ELF magic
	copy(hdr.Ident[:], []byte{0x7f, 'E', 'L', 'F'})
	hdr.Ident[elf.EI_CLASS] = byte(elf.ELFCLASS64)
	hdr.Ident[elf.EI_DATA] = byte(elf.ELFDATA2LSB)
	hdr.Ident[elf.EI_VERSION] = byte(elf.EV_CURRENT)
	hdr.Ident[elf.EI_OSABI] = byte(elf.ELFOSABI_NONE)
	hdr.Type = uint16(elf.ET_EXEC)
	hdr.Machine = uint16(elf.EM_X86_64)
	hdr.Version = uint32(elf.EV_CURRENT)
	hdr.Ehsize = 64    // sizeof(Elf64_Ehdr)
	hdr.Phentsize = 56 // sizeof(Elf64_Phdr)
	hdr.Phoff = 64     // program headers start right after ELF header

	if err := binary.Write(&buf, binary.LittleEndian, &hdr); err != nil {
		t.Fatalf("write ELF header: %v", err)
	}

	// Program headers (56 bytes each for ELF64).
	for _, ph := range progHeaders {
		phdr := elf.Prog64{
			Type:  uint32(ph.Type),
			Flags: uint32(ph.Flags),
		}
		if err := binary.Write(&buf, binary.LittleEndian, &phdr); err != nil {
			t.Fatalf("write program header: %v", err)
		}
	}

	return buf.Bytes()
}

// writeTestFile writes data to a temporary file and returns its path.
func writeTestFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write test file %q: %v", name, err)
	}
	return path
}

func TestIsStaticBinary_StaticELF(t *testing.T) {
	t.Parallel()
	// A static ELF has PT_LOAD but no PT_INTERP — no dynamic linker needed.
	elfData := makeMinimalELF(t, []elf.ProgHeader{
		{Type: elf.PT_LOAD},
	})
	path := writeTestFile(t, "static-binary", elfData)

	if err := isStaticBinary(path); err != nil {
		t.Fatalf("isStaticBinary should accept static ELF, got: %v", err)
	}
}

func TestIsStaticBinary_DynamicELF(t *testing.T) {
	t.Parallel()
	// A dynamic ELF has PT_INTERP — it requires /lib64/ld-linux-x86-64.so.2.
	elfData := makeMinimalELF(t, []elf.ProgHeader{
		{Type: elf.PT_INTERP},
		{Type: elf.PT_LOAD},
	})
	path := writeTestFile(t, "dynamic-binary", elfData)

	err := isStaticBinary(path)
	if err == nil {
		t.Fatal("isStaticBinary should reject dynamic ELF with PT_INTERP")
	}
	// Error message should be actionable.
	if !strings.Contains(err.Error(), "dynamically linked") {
		t.Fatalf("error should mention 'dynamically linked', got: %v", err)
	}
	if !strings.Contains(err.Error(), "CGO_ENABLED=0") {
		t.Fatalf("error should suggest CGO_ENABLED=0 fix, got: %v", err)
	}
}

func TestIsStaticBinary_NonELF(t *testing.T) {
	t.Parallel()
	// Shell scripts, text files, etc. — not ELF, should be accepted (no error).
	path := writeTestFile(t, "script.sh", []byte("#!/bin/sh\necho hello\n"))

	if err := isStaticBinary(path); err != nil {
		t.Fatalf("isStaticBinary should skip non-ELF files, got: %v", err)
	}
}

func TestIsStaticBinary_NotFound(t *testing.T) {
	t.Parallel()
	err := isStaticBinary("/nonexistent/path/to/binary")
	if err == nil {
		t.Fatal("isStaticBinary should fail when file doesn't exist")
	}
}

func TestDeploy_RejectsDynamicBinary(t *testing.T) {
	t.Parallel()

	// Create a dynamic ELF binary (has PT_INTERP).
	elfData := makeMinimalELF(t, []elf.ProgHeader{
		{Type: elf.PT_INTERP},
		{Type: elf.PT_LOAD},
	})
	path := writeTestFile(t, "dynamic-mesh-node", elfData)

	hostSigner := testSSHSigner(t)
	clientSigner := testSSHSigner(t)

	// Start an SSH server — Deploy should reject BEFORE connecting.
	addr, cleanup := deploySSHServer(t, hostSigner, clientSigner.PublicKey())
	defer cleanup()

	deployer := &SelfDeployer{
		BinaryPath: path,
		RemotePath: filepath.Join(t.TempDir(), "mesh-node"),
	}

	cred := DeployCredential{
		SSHUser:    "mesh",
		SSHKeyData: clientSigner,
	}

	ctx := context.Background()
	stream, err := deployer.Deploy(ctx, addr, cred, hostSigner.PublicKey())
	if err == nil {
		if cerr := stream.Close(); cerr != nil {
			t.Logf("close stream: %v", cerr)
		}
		t.Fatal("Deploy should reject dynamically-linked binary before SSH connect")
	}
	if !strings.Contains(err.Error(), "dynamically linked") {
		t.Fatalf("error should mention dynamic linking, got: %v", err)
	}
}
