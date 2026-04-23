// Package main provides automatic upgrade support for deployed fleet nodes.
//
// During development iteration, the gateway detects existing nodes that
// are already running (listening on :4443), pushes the new binary via
// SFTP, and restarts the systemd service — avoiding a full redeploy
// with cert exchange.
//
// Upgrade flow per seed host:
//
//  1. Probe TCP :4443 — is the node already running?
//  2. If yes: SSH in, SFTP new binary, systemctl restart, reconnect via mTLS
//  3. If no: full deploy (SelfDeployer + cert bootstrap)
package mesh

import (
	"context"
	"fmt"
	"go.uber.org/zap"
	"io"
	"net"
	"os"
	"time"

	"github.com/cortex-mesh/cortex-mesh/transport"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const (
	// upgradeWaitAfterRestart is how long to wait for the service to
	// come back up after systemctl restart.
	upgradeWaitAfterRestart = 3 * time.Second
)

// needsUpgrade returns true if we should push a new binary to the node.
// When skipDeploy is true, the binary is already in place (developer
// pre-copied it) so no upload is needed.
func needsUpgrade(skipDeploy bool) bool {
	return !skipDeploy
}

// upgradeRestartCommand returns the command to restart the service
// after a binary upgrade. Execs the binary with the local-op install subcommand
// which handles daemon-reload + enable + restart.
func upgradeRestartCommand(remotePath string) string {
	return fmt.Sprintf("sudo %s local-op install", remotePath)
}

// upgradeRemoteNode pushes a new binary to an existing node via SSH/SFTP
// and restarts the systemd service. This is the fast path for development
// iteration — avoids the full deploy + cert exchange flow.
//
// Flow:
//  1. SSH to targetHost using cred
//  2. SFTP the local binary to remotePath (overwrites existing)
//  3. Exec the binary with the install subcommand to re-register and restart
//  4. Close SSH connection
func upgradeRemoteNode(ctx context.Context, targetHost string, cred transport.DeployCredential, remotePath string) error {
	// Resolve the local binary path.
	binaryPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("upgrade: resolve executable: %w", err)
	}

	binaryFile, err := os.Open(binaryPath)
	if err != nil {
		return fmt.Errorf("upgrade: open binary: %w", err)
	}
	defer func() {
		if cerr := binaryFile.Close(); cerr != nil {
			zap.S().Debugw("upgrade: close binary", "error", cerr)
		}
	}()

	client, err := dialSSH(ctx, targetHost, cred)
	if err != nil {
		return fmt.Errorf("upgrade: %w", err)
	}
	defer func() {
		if cerr := client.Close(); cerr != nil {
			zap.S().Debugw("upgrade: close SSH", "error", cerr)
		}
	}()

	// Phase 1: Upload the new binary via SFTP to a temporary path.
	tmpPath := remotePath + ".tmp"
	if err := uploadBinaryViaSFTP(client, binaryFile, tmpPath); err != nil {
		return fmt.Errorf("upgrade: upload to %q: %w", targetHost, err)
	}

	// Phase 2: Atomically replace the existing binary.
	mvCmd := fmt.Sprintf("sudo mv -f %q %q", tmpPath, remotePath)
	if err := execSSHCommand(client, mvCmd); err != nil {
		return fmt.Errorf("upgrade: atomic replace on %q: %w", targetHost, err)
	}

	// Phase 3: Restart via the binary's install subcommand.
	if err := execSSHCommand(client, upgradeRestartCommand(remotePath)); err != nil {
		return fmt.Errorf("upgrade: restart on %q: %w", targetHost, err)
	}

	return nil
}

// uploadBinaryViaSFTP copies the local binary to remotePath on the remote host.
func uploadBinaryViaSFTP(client *ssh.Client, binary io.Reader, remotePath string) (err error) {
	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		return fmt.Errorf("open SFTP session: %w", err)
	}
	defer func() {
		if cerr := sftpClient.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close SFTP session: %w", cerr)
		}
	}()

	// Ensure parent directory exists.
	if err := sftpClient.MkdirAll(remotePath[:len(remotePath)-len("/cortex-mesh")]); err != nil {
		zap.S().Debugw("upgrade: mkdir (may already exist)", "error", err)
	}

	remoteFile, err := sftpClient.Create(remotePath)
	if err != nil {
		return fmt.Errorf("create remote file %q: %w", remotePath, err)
	}

	if _, err := io.Copy(remoteFile, binary); err != nil {
		_ = remoteFile.Close()
		return fmt.Errorf("copy binary to %q: %w", remotePath, err)
	}

	if err := remoteFile.Close(); err != nil {
		return fmt.Errorf("close remote file %q: %w", remotePath, err)
	}

	if err := sftpClient.Chmod(remotePath, 0755); err != nil {
		return fmt.Errorf("chmod remote file %q: %w", remotePath, err)
	}

	return nil
}

// execSSHCommand runs a command on the remote host via SSH.
func execSSHCommand(client *ssh.Client, cmd string) error {
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("open SSH session: %w", err)
	}
	defer func() {
		if cerr := session.Close(); cerr != nil && cerr != io.EOF {
			zap.S().Debugw("upgrade: close session", "error", cerr)
		}
	}()

	output, err := session.CombinedOutput(cmd)
	if err != nil {
		return fmt.Errorf("exec %q: %w (output: %s)", cmd, err, string(output))
	}

	return nil
}

// buildSSHAuth constructs SSH auth methods from a deploy credential.
// This duplicates the logic from transport.buildAuthMethods but avoids
// exporting that function from the library.
func buildSSHAuth(cred transport.DeployCredential) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	if cred.SSHKeyData != nil {
		methods = append(methods, ssh.PublicKeys(cred.SSHKeyData))
	}

	if cred.SSHPass != "" {
		methods = append(methods, ssh.Password(cred.SSHPass))
	}

	if len(methods) == 0 {
		return nil, fmt.Errorf("no SSH auth method: provide SSHKeyData or SSHPass")
	}

	return methods, nil
}

// dialSSH establishes an SSH connection to targetHost using cred.
// The caller must close the returned client.
func dialSSH(ctx context.Context, targetHost string, cred transport.DeployCredential) (*ssh.Client, error) {
	authMethods, err := buildSSHAuth(cred)
	if err != nil {
		return nil, err
	}

	clientConf := &ssh.ClientConfig{
		User:            cred.SSHUser,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	var dialer net.Dialer
	tcpConn, err := dialer.DialContext(ctx, "tcp", targetHost)
	if err != nil {
		return nil, fmt.Errorf("dial %q: %w", targetHost, err)
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(tcpConn, targetHost, clientConf)
	if err != nil {
		_ = tcpConn.Close()
		return nil, fmt.Errorf("SSH handshake with %q: %w", targetHost, err)
	}

	return ssh.NewClient(sshConn, chans, reqs), nil
}
