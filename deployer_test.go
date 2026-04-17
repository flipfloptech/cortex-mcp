package main

import (
	"net"
	"testing"
)

// --- resolveTarget tests ---
// resolveTarget resolves a deployment target to a dialable address.
// Resolution order: direct IP → DNS lookup of hostnames → error.
// It never scrapes /etc/hosts.

func TestResolveTarget_DirectIP(t *testing.T) {
	t.Parallel()

	// A raw IP should be returned as-is (with port appended if missing).
	addr, err := resolveTarget("10.0.2.30")
	if err != nil {
		t.Fatalf("resolveTarget with direct IP: unexpected error: %v", err)
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("resolveTarget result not host:port: %v", err)
	}

	if host != "10.0.2.30" {
		t.Errorf("expected host 10.0.2.30, got %s", host)
	}

	if port != "22" {
		t.Errorf("expected default port 22, got %s", port)
	}
}

func TestResolveTarget_IPWithPort(t *testing.T) {
	t.Parallel()

	// IP with explicit port should be preserved.
	addr, err := resolveTarget("10.0.2.30:2222")
	if err != nil {
		t.Fatalf("resolveTarget with IP:port: unexpected error: %v", err)
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("resolveTarget result not host:port: %v", err)
	}

	if host != "10.0.2.30" {
		t.Errorf("expected host 10.0.2.30, got %s", host)
	}

	if port != "2222" {
		t.Errorf("expected port 2222, got %s", port)
	}
}

func TestResolveTarget_Hostname_FallsBackToDNS(t *testing.T) {
	t.Parallel()

	// "localhost" is universally resolvable via DNS.
	// This tests the DNS fallback path for hostnames.
	addr, err := resolveTarget("localhost")
	if err != nil {
		t.Fatalf("resolveTarget with localhost: unexpected error: %v", err)
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("resolveTarget result not host:port: %v", err)
	}

	// Should resolve to 127.0.0.1 or ::1.
	ip := net.ParseIP(host)
	if ip == nil {
		t.Fatalf("expected resolved IP, got hostname %s", host)
	}

	if port != "22" {
		t.Errorf("expected default port 22, got %s", port)
	}
}

func TestResolveTarget_UnresolvableHostname_Error(t *testing.T) {
	t.Parallel()

	// A hostname that can't be resolved should return an error.
	_, err := resolveTarget("this-host-does-not-exist-at-all.invalid")
	if err == nil {
		t.Fatal("resolveTarget should fail for unresolvable hostname")
	}
}

// --- isRoutableAddress tests ---
// Moved from spreader.go — these behaviors must be preserved.

func TestIsRoutableAddress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		addr     string
		expected bool
	}{
		// Loopback — not routable
		{"loopback IPv4", "127.0.0.1", false},
		{"loopback IPv6", "::1", false},
		{"localhost hostname", "localhost", false},
		{"ip6-localhost", "ip6-localhost", false},
		{"ip6-loopback", "ip6-loopback", false},

		// Link-local — not routable
		{"link-local IPv4", "169.254.1.1", false},
		{"link-local IPv6", "fe80::1", false},

		// Multicast — not routable
		{"multicast IPv4", "224.0.0.1", false},
		{"multicast IPv6", "ff02::1", false},

		// Unspecified — not routable
		{"unspecified IPv4", "0.0.0.0", false},
		{"unspecified IPv6", "::", false},

		// Private networks — routable (within the mesh context)
		{"private 10.x", "10.0.1.5", true},
		{"private 172.x", "172.16.0.1", true},
		{"private 192.168.x", "192.168.1.1", true},

		// Public IPs — routable
		{"public IPv4", "8.8.8.8", true},

		// Hostnames — assume routable
		{"hostname", "oss-01.internal", true},

		// Address with port
		{"IP with port", "10.0.1.5:22", true},
		{"loopback with port", "127.0.0.1:22", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isRoutableAddress(tc.addr)
			if got != tc.expected {
				t.Errorf("isRoutableAddress(%q) = %v, want %v", tc.addr, got, tc.expected)
			}
		})
	}
}

// --- isMembraneTLSError tests ---
// Ensures correct classification of foreign TLS services.

func TestIsMembraneTLSError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil error", nil, false},
		{"generic error", net.ErrClosed, false},
		{"membrane error", &testError{"membrane: handshake failed"}, true},
		{"tls error", &testError{"tls: bad certificate"}, true},
		{"certificate error", &testError{"certificate verification failed"}, true},
		{"connection refused", &testError{"connection refused"}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isMembraneTLSError(tc.err)
			if got != tc.expected {
				t.Errorf("isMembraneTLSError(%v) = %v, want %v", tc.err, got, tc.expected)
			}
		})
	}
}

// testError is a simple error type for testing.
type testError struct {
	msg string
}

func (e *testError) Error() string { return e.msg }
