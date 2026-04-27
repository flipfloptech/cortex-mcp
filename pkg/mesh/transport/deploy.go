package transport

import (
	"bytes"
	"context"
	"debug/elf"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// isStaticBinary checks whether the binary at path is statically linked.
// Dynamically linked ELF binaries will fail on targets with different libc
// versions (e.g., deploying from GLIBC 2.34 to a host with GLIBC 2.17).
//
// Returns nil if the binary is safe to deploy (static ELF or non-ELF).
// Returns an error if the binary is dynamically linked.
func isStaticBinary(path string) error {
	f, err := elf.Open(path)
	if err != nil {
		// Not an ELF binary (e.g., shell script, macOS Mach-O) — skip check.
		if os.IsNotExist(err) {
			return fmt.Errorf("transport: deploy: binary not found: %w", err)
		}
		// ELF parse error for a non-ELF file is expected — not an error.
		return nil
	}
	defer func() {
		_ = f.Close()
	}()

	for _, prog := range f.Progs {
		if prog.Type == elf.PT_INTERP {
			return fmt.Errorf(
				"transport: deploy: binary %q is dynamically linked (has PT_INTERP). "+
					"Deployed nodes may crash on hosts with different libc versions. "+
					"Rebuild with: CGO_ENABLED=0 go build",
				path,
			)
		}
	}

	return nil
}

// ProtocolVersion defines the current wire-level protocol version.
// A mismatch here generally warrants connection termination and node re-deployment.
const ProtocolVersion uint16 = 1

// DeployReadyMagic is the 4-byte handshake sequence that a deployed node writes
// to stdout to signal it is ready to receive data from the deployer.
//
// Format: [0x43 0x4D High(Proto) Low(Proto)] = "CM" (cortex-mcp) + 2-byte version
//
// Why 4 bytes instead of 1:
//   - "CM" prefix won't collide with shell errors or Go runtime panics (ASCII text)
//   - Version enables protocol evolution without silent breakage
var DeployReadyMagic = [4]byte{'C', 'M', byte(ProtocolVersion >> 8), byte(ProtocolVersion)}

// SignalReady writes the deploy readiness magic to w. Fleet nodes call this
// in their WasDeployed() preamble to tell the deployer they're ready for
// cert exchange or other pre-membrane bootstrapping.
//
// Usage:
//
//	if transport.WasDeployed() {
//	    transport.SignalReady(os.Stdout)
//	    // ... read certs, AcceptStdio, etc.
//	}
func SignalReady(w io.Writer) error {
	_, err := w.Write(DeployReadyMagic[:])
	if err != nil {
		return fmt.Errorf("transport: signal ready: %w", err)
	}
	return nil
}

// waitForReady reads and validates the deploy readiness magic from r.
// Returns nil if the magic matches, or a descriptive error otherwise.
func waitForReady(r io.Reader) error {
	var got [4]byte
	if _, err := io.ReadFull(r, got[:]); err != nil {
		return fmt.Errorf("transport: deploy: readiness read: %w", err)
	}
	if got != DeployReadyMagic {
		return fmt.Errorf("transport: deploy: invalid ready magic: got %x, want %x", got, DeployReadyMagic)
	}
	return nil
}

// shellQuote wraps s in POSIX-safe single quotes, escaping any embedded
// single quotes. This prevents shell injection when s is used as an argument
// in a shell command executed on a remote host.
//
// The quoting strategy: wrap the entire string in single quotes. Within single
// quotes, all characters are literal except single quote itself. To include a
// literal single quote, we end the quoted region, insert an escaped single quote
// (\'), and restart a new quoted region: 'before'\”after'
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// WasDeployed checks whether this process was launched by a SelfDeployer.
//
// Deprecated: Use subcommand-based dispatch (e.g., "serve" subcommand) instead.
// SelfDeployer now passes "serve" as the default ExecArgs, so the consumer's
// main() should dispatch on subcommands rather than checking this env var.
// Retained for backwards compatibility.
func WasDeployed() bool {
	return os.Getenv("CORTEX_MESH_SPAWNED") == "1"
}

// DeployCredential holds the credentials needed to SSH into a target host
// and deploy a mesh node. In production, these come from the vault.
type DeployCredential struct {
	// SSHUser is the username for SSH authentication.
	SSHUser string

	// SSHKeyData is the SSH private key signer for public key auth.
	// If non-nil, public key authentication is used.
	SSHKeyData ssh.Signer

	// SSHPass is the password for SSH password authentication.
	// Used when SSHKeyData is nil.
	SSHPass string
}

// Deployer places a mesh node on a target host and returns the byte stream
// to it. The returned io.ReadWriteCloser is fed into a StdioConn and then
// through the membrane handshake.
//
// The default implementation is SelfDeployer, which copies the running binary
// to the target via SFTP and executes it. Consumers can override this for
// containers, package managers, or pre-deployed fleets.
type Deployer interface {
	Deploy(ctx context.Context, targetHost string, cred DeployCredential, hostKey ssh.PublicKey) (io.ReadWriteCloser, error)
}

// SelfDeployer is the default Deployer implementation. It uses os.Executable()
// to locate the current binary, uploads it to the target host via SSH, and
// executes it with the "serve" subcommand. The resulting SSH session's
// stdin/stdout becomes the first mesh connection to the new node.
type SelfDeployer struct {
	// BinaryPath overrides os.Executable() for the source binary path.
	// Used in testing to avoid copying the test binary itself.
	// If empty, os.Executable() is used.
	BinaryPath string

	// RemotePath is the destination path on the remote host.
	// If empty, defaults to "/tmp/cortex-mcp-node".
	RemotePath string

	// SkipUpload bypasses the SFTP binary upload phase when true.
	// This is useful for rapid iterative testing when the binary is
	// already present on the remote host.
	SkipUpload bool

	// ExecArgs is a list of extra arguments passed to the binary when
	// it is executed on the remote host.
	ExecArgs []string

	// NoWaitReady skips waiting for the DeployReadyMagic signal from the remote
	// binary. This is useful when using the deployer to execute short-lived
	// operations (like 'uninstall') rather than starting a persistent mesh node.
	NoWaitReady bool
}

// Deploy uploads the mesh binary to targetHost and executes it.
// The flow is:
//  1. SSH to targetHost using cred.
//  2. Open session, exec "cat > <remotePath> && chmod +x <remotePath>",
//     pipe the binary through stdin, close stdin.
//  3. Open session, exec "<remotePath> serve" (or custom ExecArgs).
//  4. Return the second session's stdin/stdout as io.ReadWriteCloser.
//
// The returned stream is the byte-level mesh connection to the new node.
func (d *SelfDeployer) Deploy(ctx context.Context, targetHost string, cred DeployCredential, hostKey ssh.PublicKey) (_ io.ReadWriteCloser, err error) {
	// Resolve binary path.
	binaryPath := d.BinaryPath
	if binaryPath == "" {
		var err error
		binaryPath, err = os.Executable()
		if err != nil {
			return nil, fmt.Errorf("transport: deploy: resolve executable: %w", err)
		}
	}

	// Verify the binary is statically linked before uploading.
	// Dynamically linked binaries crash on hosts with different libc.
	if err := isStaticBinary(binaryPath); err != nil {
		return nil, err
	}

	// Verify the binary exists and is readable.
	binaryFile, err := os.Open(binaryPath)
	if err != nil {
		return nil, fmt.Errorf("transport: deploy: open binary %q: %w", binaryPath, err)
	}
	defer func() {
		if cerr := binaryFile.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("transport: deploy: close binary %q: %w", binaryPath, cerr)
		}
	}()

	if _, err := binaryFile.Stat(); err != nil {
		return nil, fmt.Errorf("transport: deploy: stat binary %q: %w", binaryPath, err)
	}

	// Resolve remote path.
	remotePath := d.RemotePath
	if remotePath == "" {
		remotePath = "/tmp/cortex-mcp-node"
	}

	// Build auth methods from the credential.
	authMethods, err := buildAuthMethods(cred)
	if err != nil {
		return nil, fmt.Errorf("transport: deploy: %w", err)
	}

	// Build SSH client config.
	clientConf := &ssh.ClientConfig{
		User:            cred.SSHUser,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback(hostKey),
	}

	// Dial TCP with context support.
	var dialer net.Dialer
	tcpConn, err := dialer.DialContext(ctx, "tcp", targetHost)
	if err != nil {
		return nil, fmt.Errorf("transport: deploy: dial %q: %w", targetHost, err)
	}

	// SSH handshake.
	sshConn, chans, reqs, err := ssh.NewClientConn(tcpConn, targetHost, clientConf)
	if err != nil {
		if cerr := tcpConn.Close(); cerr != nil {
			return nil, fmt.Errorf("transport: deploy: SSH handshake with %q: %w (close: %v)", targetHost, err, cerr)
		}
		return nil, fmt.Errorf("transport: deploy: SSH handshake with %q: %w", targetHost, err)
	}

	client := ssh.NewClient(sshConn, chans, reqs)

	// Phase 1: Upload the binary.
	if !d.SkipUpload {
		if err := d.uploadBinary(client, binaryFile, remotePath); err != nil {
			if cerr := client.Close(); cerr != nil {
				return nil, fmt.Errorf("transport: deploy: upload to %q: %w (close: %v)", targetHost, err, cerr)
			}
			return nil, fmt.Errorf("transport: deploy: upload to %q: %w", targetHost, err)
		}
	}

	// Rewind not needed — we've already read the file.

	// Phase 2: Execute the binary.
	stream, err := d.execBinary(client, remotePath)
	if err != nil {
		if cerr := client.Close(); cerr != nil {
			return nil, fmt.Errorf("transport: deploy: exec on %q: %w (close: %v)", targetHost, err, cerr)
		}
		return nil, fmt.Errorf("transport: deploy: exec on %q: %w", targetHost, err)
	}

	// Phase 3: Wait for the deployed binary to signal readiness.
	// The remote binary writes DeployReadyMagic to stdout when it's ready
	// to receive data (cert bundles, etc.). This blocks until the magic
	// arrives, so the returned stream is guaranteed to be ready for use.
	if !d.NoWaitReady {
		if err := waitForReady(stream); err != nil {
			// Include any stderr output for crash diagnostics.
			if ds, ok := stream.(*deployStream); ok {
				if stderr := ds.Stderr(); stderr != "" {
					err = fmt.Errorf("%w\nremote stderr:\n%s", err, stderr)
				}
			}
			_ = stream.Close()
			return nil, fmt.Errorf("transport: deploy: readiness on %q: %w", targetHost, err)
		}
	}

	return stream, nil
}

// uploadBinary uses SFTP to write the binary to remotePath on the remote host
// and sets executable permissions. This avoids shell injection vulnerabilities
// that would exist if remotePath were interpolated into a shell command.
func (d *SelfDeployer) uploadBinary(client *ssh.Client, binary io.Reader, remotePath string) (err error) {
	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		return fmt.Errorf("open SFTP session: %w", err)
	}
	defer func() {
		if cerr := sftpClient.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close SFTP session: %w", cerr)
		}
	}()

	// Create the remote file (truncates if exists).
	remoteFile, err := sftpClient.Create(remotePath)
	if err != nil {
		return fmt.Errorf("create remote file %q: %w", remotePath, err)
	}

	// Copy binary contents to the remote file.
	if _, err := io.Copy(remoteFile, binary); err != nil {
		if cerr := remoteFile.Close(); cerr != nil {
			return fmt.Errorf("copy binary to %q: %w (close: %v)", remotePath, err, cerr)
		}
		return fmt.Errorf("copy binary to %q: %w", remotePath, err)
	}

	// Close the file to flush writes.
	if err := remoteFile.Close(); err != nil {
		return fmt.Errorf("close remote file %q: %w", remotePath, err)
	}

	// Set executable permissions via SFTP (no shell command needed).
	if err := sftpClient.Chmod(remotePath, 0755); err != nil {
		return fmt.Errorf("chmod remote file %q: %w", remotePath, err)
	}

	return nil
}

// execBinary opens a new SSH session and executes the deployed binary with
// the "serve" subcommand (or custom ExecArgs). Returns a deployStream wrapping
// the session's stdin/stdout pipes.
func (d *SelfDeployer) execBinary(client *ssh.Client, remotePath string) (io.ReadWriteCloser, error) {
	session, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("open exec session: %w", err)
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		if cerr := session.Close(); cerr != nil {
			return nil, fmt.Errorf("exec stdin pipe: %w (close session: %v)", err, cerr)
		}
		return nil, fmt.Errorf("exec stdin pipe: %w", err)
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		if cerr := stdin.Close(); cerr != nil {
			return nil, fmt.Errorf("exec stdout pipe: %w (close stdin: %v)", err, cerr)
		}
		if cerr := session.Close(); cerr != nil {
			return nil, fmt.Errorf("exec stdout pipe: %w (close session: %v)", err, cerr)
		}
		return nil, fmt.Errorf("exec stdout pipe: %w", err)
	}

	stderr, err := session.StderrPipe()
	if err != nil {
		if cerr := stdin.Close(); cerr != nil {
			return nil, fmt.Errorf("exec stderr pipe: %w (close stdin: %v)", err, cerr)
		}
		if cerr := session.Close(); cerr != nil {
			return nil, fmt.Errorf("exec stderr pipe: %w (close session: %v)", err, cerr)
		}
		return nil, fmt.Errorf("exec stderr pipe: %w", err)
	}

	// Execute the binary with the serve subcommand (default) or custom args.
	args := d.ExecArgs
	if len(args) == 0 {
		args = []string{"serve"}
	}
	var quotedArgs []string
	for _, arg := range args {
		quotedArgs = append(quotedArgs, shellQuote(arg))
	}
	cmd := fmt.Sprintf("%s %s", shellQuote(remotePath), strings.Join(quotedArgs, " "))
	if err := session.Start(cmd); err != nil {
		if cerr := stdin.Close(); cerr != nil {
			return nil, fmt.Errorf("start binary: %w (close stdin: %v)", err, cerr)
		}
		if cerr := session.Close(); cerr != nil {
			return nil, fmt.Errorf("start binary: %w (close session: %v)", err, cerr)
		}
		return nil, fmt.Errorf("start binary: %w", err)
	}

	ds := &deployStream{
		stdin:     stdin,
		stdout:    stdout,
		stderrBuf: &bytes.Buffer{},
		session:   session,
		client:    client,
	}

	// Drain the deployed binary's stderr into a buffer in the background.
	// This captures crash output and slog messages from the fleet node.
	go func() {
		_, _ = io.Copy(ds.stderrBuf, stderr)
	}()

	return ds, nil
}

// buildAuthMethods constructs SSH auth methods from a DeployCredential.
// Supports public key auth (SSHKeyData) and password auth (SSHPass).
// Returns an error if neither authentication method is provided.
func buildAuthMethods(cred DeployCredential) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	if cred.SSHKeyData != nil {
		methods = append(methods, ssh.PublicKeys(cred.SSHKeyData))
	}

	if cred.SSHPass != "" {
		methods = append(methods, ssh.Password(cred.SSHPass))
	}

	if len(methods) == 0 {
		return nil, fmt.Errorf("no auth method: provide SSHKeyData or SSHPass")
	}

	return methods, nil
}

// hostKeyCallback returns an ssh.HostKeyCallback that validates the server's
// host key against the expected key. If hostKey is nil, any key is accepted
// (insecure, but useful for testing or trust-on-first-use).
func hostKeyCallback(hostKey ssh.PublicKey) ssh.HostKeyCallback {
	if hostKey == nil {
		return ssh.InsecureIgnoreHostKey()
	}
	return ssh.FixedHostKey(hostKey)
}

// deployStream wraps the exec session's stdin/stdout as an io.ReadWriteCloser.
// This is the byte stream that becomes the first mesh connection to the
// newly deployed node.
type deployStream struct {
	stdin     io.WriteCloser
	stdout    io.Reader
	stderrBuf *bytes.Buffer // captures fleet node's stderr (crash output + slog)
	session   *ssh.Session
	client    *ssh.Client
	once      sync.Once
	closeErr  error
}

func (s *deployStream) Read(b []byte) (int, error) {
	return s.stdout.Read(b)
}

func (s *deployStream) Write(b []byte) (int, error) {
	return s.stdin.Write(b)
}

// Stderr returns the captured stderr output from the deployed binary.
// This is useful for diagnosing crashes — if the binary exits before
// completing the membrane handshake, its error output is here.
func (s *deployStream) Stderr() string {
	if s.stderrBuf == nil {
		return ""
	}
	return s.stderrBuf.String()
}

// Close closes the stdin pipe, SSH session, and underlying SSH client.
// Close is idempotent.
func (s *deployStream) Close() error {
	s.once.Do(func() {
		var firstErr error

		if err := s.stdin.Close(); err != nil && firstErr == nil {
			firstErr = err
		}

		if err := s.session.Close(); err != nil && firstErr == nil {
			if err != io.EOF {
				firstErr = err
			}
		}

		if err := s.client.Close(); err != nil && firstErr == nil {
			firstErr = err
		}

		s.closeErr = firstErr
	})
	return s.closeErr
}
