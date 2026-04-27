// Package membrane provides the security and multiplexing layer for the mesh.
// Every connection — regardless of transport — passes through the membrane
// for mTLS authentication and yamux stream multiplexing.
//
// The membrane is the mesh's immune system: only nodes presenting a certificate
// signed by the Site CA can complete the handshake. No valid cert means no entry.
package membrane

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"time"
)

// MaxCertLifetime is the maximum allowed certificate validity period.
// Certificates valid for longer than this are rejected during handshake
// as a short-lived certificate policy enforcement. This limits the damage
// window if a node's private key is compromised — since the mesh operates
// offline (no CRL/OCSP), short lifetimes are the revocation mechanism.
const MaxCertLifetime = 10 * 365 * 24 * time.Hour // 10 years

// Config holds the mTLS configuration for a mesh node.
// The certificate and CA come from the vault.
type Config struct {
	// Certificate is this node's TLS certificate and private key,
	// signed by the Site CA. The certificate's CommonName is the node's
	// unique identifier (NodeID) in the mesh.
	Certificate tls.Certificate

	// CACert is the Site CA certificate pool. All peers must present
	// certificates signed by a CA in this pool. This is the sole trust
	// anchor — no external PKI, no OCSP. Works fully air-gapped.
	CACert *x509.CertPool
}

// HandshakeServer upgrades a raw net.Conn to mTLS as the server (accepting) side.
// The server requires the client to present a valid certificate signed by
// the Site CA (RequireAndVerifyClientCert), with ExtKeyUsageClientAuth,
// a non-empty CommonName (NodeID), and a lifetime within MaxCertLifetime.
//
// Returns the secured net.Conn on success. The caller is responsible for
// closing the returned connection.
func HandshakeServer(ctx context.Context, conn net.Conn, cfg *Config) (net.Conn, error) {
	if cfg == nil {
		return nil, fmt.Errorf("membrane: handshake: nil config")
	}

	tlsConf := &tls.Config{
		Certificates: []tls.Certificate{cfg.Certificate},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    cfg.CACert,
		MinVersion:   tls.VersionTLS13,
		// Enforce that client certificates have ExtKeyUsageClientAuth,
		// a non-empty CommonName, and comply with certificate lifetime policy.
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			return verifyCertChain(rawCerts, cfg.CACert, x509.ExtKeyUsageClientAuth)
		},
		SessionTicketsDisabled: true, // Prevent post-handshake writes on synchronous transports.
	}

	tlsConn := tls.Server(conn, tlsConf)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("membrane: server handshake: %w", err)
	}

	return tlsConn, nil
}

// HandshakeClient upgrades a raw net.Conn to mTLS as the client (initiating) side.
// The client validates the server's certificate against the Site CA and presents
// its own certificate for mutual authentication.
//
// The server certificate must have ExtKeyUsageServerAuth, a non-empty CommonName
// (NodeID), and a lifetime within MaxCertLifetime.
//
// Returns the secured net.Conn on success. The caller is responsible for
// closing the returned connection.
func HandshakeClient(ctx context.Context, conn net.Conn, cfg *Config) (net.Conn, error) {
	if cfg == nil {
		return nil, fmt.Errorf("membrane: handshake: nil config")
	}

	tlsConf := &tls.Config{
		Certificates: []tls.Certificate{cfg.Certificate},
		RootCAs:      cfg.CACert,
		MinVersion:   tls.VersionTLS13,
		// ServerName is left empty — for pipe-based connections (stdio, proxy)
		// there is no DNS name. We verify via the Site CA chain instead.
		// Since we use net.Pipe in many cases (no real hostname),
		// we need to use a custom verification that validates the cert
		// chain against our CA but doesn't require ServerName matching.
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			return verifyCertChain(rawCerts, cfg.CACert, x509.ExtKeyUsageServerAuth)
		},
	}

	// Skip the built-in server name check — we do chain validation ourselves
	// via VerifyPeerCertificate. This is necessary because many mesh transports
	// (stdio, pipes) don't have a meaningful server name.
	tlsConf.InsecureSkipVerify = true

	tlsConn := tls.Client(conn, tlsConf)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("membrane: client handshake: %w", err)
	}

	return tlsConn, nil
}

// verifyCertChain validates that the raw certificate chain is signed by a CA
// in the trusted pool and enforces the following policies:
//
//  1. Chain validation: certificate must be signed by a CA in caPool.
//  2. ExtKeyUsage: leaf cert must contain the expectedUsage (Client or Server).
//  3. CommonName: leaf cert must have a non-empty CN (the NodeID).
//  4. Lifetime: certificate validity period must not exceed MaxCertLifetime.
//
// This replaces the standard hostname-based verification because mesh
// transports often don't have DNS names.
func verifyCertChain(rawCerts [][]byte, caPool *x509.CertPool, expectedUsage x509.ExtKeyUsage) error {
	if len(rawCerts) == 0 {
		return fmt.Errorf("membrane: peer presented no certificates")
	}

	// Parse the leaf certificate.
	leaf, err := x509.ParseCertificate(rawCerts[0])
	if err != nil {
		return fmt.Errorf("membrane: parse peer certificate: %w", err)
	}

	// Policy: CommonName (NodeID) must not be empty.
	if leaf.Subject.CommonName == "" {
		return fmt.Errorf("membrane: peer certificate has empty CommonName (NodeID)")
	}

	// Policy: certificate lifetime must not exceed MaxCertLifetime.
	lifetime := leaf.NotAfter.Sub(leaf.NotBefore)
	if lifetime > MaxCertLifetime {
		return fmt.Errorf("membrane: certificate lifetime %s exceeds maximum %s",
			lifetime.Round(time.Hour), MaxCertLifetime.Round(time.Hour))
	}

	// Build intermediate pool from remaining certs in the chain.
	intermediates := x509.NewCertPool()
	for _, raw := range rawCerts[1:] {
		cert, err := x509.ParseCertificate(raw)
		if err != nil {
			return fmt.Errorf("membrane: parse intermediate certificate: %w", err)
		}
		intermediates.AddCert(cert)
	}

	// Verify the chain against our CA pool with strict ExtKeyUsage.
	opts := x509.VerifyOptions{
		Roots:         caPool,
		Intermediates: intermediates,
		KeyUsages:     []x509.ExtKeyUsage{expectedUsage},
	}

	if _, err := leaf.Verify(opts); err != nil {
		return fmt.Errorf("membrane: certificate chain verification: %w", err)
	}

	return nil
}

// PeerNodeID extracts the peer's NodeID from a membrane-secured connection.
// The NodeID is the CommonName of the peer's TLS certificate.
// Returns an empty string if the connection is not a *tls.Conn or the peer
// presented no certificates.
func PeerNodeID(conn net.Conn) string {
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return ""
	}

	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return ""
	}

	return state.PeerCertificates[0].Subject.CommonName
}

// PeerPubKey extracts the peer's Ed25519 public key from a membrane-secured connection.
// Returns nil if the connection is not a *tls.Conn, the peer presented no certificates,
// or the certificate is not Ed25519.
func PeerPubKey(conn net.Conn) ed25519.PublicKey {
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return nil
	}

	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return nil
	}

	pub, _ := state.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
	return pub
}
