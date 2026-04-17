// Package main provides explicit, config-driven fleet deployment.
//
// The deployer works strictly from seed information in mesh.toml.
// It does NOT scrape /etc/hosts or autonomously discover targets.
//
// Resolution order for each seed host:
//
//  1. Direct IP from config (most common in HPC/datacenter)
//  2. DNS resolution of configured hostnames
//  3. Error if unreachable
//
// This replaces the former "spreader" pattern which autonomously
// propagated to every reachable host. The deployer is deliberate:
// it does exactly what the config says, no more.
package main

import (
	"fmt"
	"net"
	"strings"
)

// --- Address resolution ---

// resolveTarget resolves a deployment target to a dialable "host:port" address.
//
// Resolution order:
//  1. If target is already "host:port", validate and return as-is.
//  2. If target is a bare IP, append default SSH port (:22).
//  3. If target is a hostname, resolve via DNS (net.LookupHost), return first result with :22.
//  4. If DNS fails, return an error. Never scrape /etc/hosts.
func resolveTarget(target string) (string, error) {
	// Check if target already has a port.
	host, port, err := net.SplitHostPort(target)
	if err == nil {
		// Already has port — validate the host part.
		if net.ParseIP(host) != nil {
			return net.JoinHostPort(host, port), nil
		}
		// Hostname with port — resolve the hostname.
		ips, lookupErr := net.LookupHost(host)
		if lookupErr != nil {
			return "", fmt.Errorf("deployer: resolve %q: %w", target, lookupErr)
		}
		return net.JoinHostPort(ips[0], port), nil
	}

	// No port — treat as bare host.
	const defaultPort = "22"

	// If it's already an IP, just append the default port.
	if net.ParseIP(target) != nil {
		return net.JoinHostPort(target, defaultPort), nil
	}

	// Hostname — resolve via DNS.
	ips, lookupErr := net.LookupHost(target)
	if lookupErr != nil {
		return "", fmt.Errorf("deployer: resolve %q: %w", target, lookupErr)
	}

	return net.JoinHostPort(ips[0], defaultPort), nil
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

// isMembraneTLSError returns true if the error indicates that a port
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
