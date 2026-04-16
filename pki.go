// Package main provides ephemeral PKI generation for the E2E example.
// This generates a site CA and node certificates for mTLS mesh admission.
//
// In production, the site CA would be managed externally (offline HSM,
// Vault, etc.). This example generates ephemeral certs that last 24 hours
// for demonstration purposes.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"time"

	"github.com/cortex-mesh/cortex-mesh/membrane"
)

// ephemeralPKI holds the site CA and is used to generate node certificates.
type ephemeralPKI struct {
	ca    *x509.Certificate
	caKey ed25519.PrivateKey
	pool  *x509.CertPool
}

// certBundle is a serializable certificate bundle for bootstrapping
// a deployed fleet node over the deploy stream.
type certBundle struct {
	CertPEM []byte
	KeyPEM  []byte
	CaPEM   []byte
}

// newEphemeralPKI generates a fresh site CA for the mesh.
func newEphemeralPKI() (*ephemeralPKI, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("pki: generate CA key: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"cortex-mesh-site"},
			CommonName:   "cortex-mesh-ephemeral-ca",
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, pub, priv)
	if err != nil {
		return nil, fmt.Errorf("pki: create CA cert: %w", err)
	}

	ca, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("pki: parse CA cert: %w", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(ca)

	return &ephemeralPKI{ca: ca, caKey: priv, pool: pool}, nil
}

// generateNodeCert creates a TLS certificate for a mesh node, signed by the site CA.
func (p *ephemeralPKI) generateNodeCert(nodeID string, isServer bool) (tls.Certificate, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("pki: generate node key: %w", err)
	}

	usage := x509.ExtKeyUsageClientAuth
	if isServer {
		usage = x509.ExtKeyUsageServerAuth
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject: pkix.Name{
			Organization: []string{"cortex-mesh-node"},
			CommonName:   nodeID,
		},
		NotBefore:   time.Now().Add(-1 * time.Hour),
		NotAfter:    time.Now().Add(24 * time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{usage},
		KeyUsage:    x509.KeyUsageDigitalSignature,
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:    []string{"localhost"},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, p.ca, pub, p.caKey)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("pki: create node cert: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("pki: marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("pki: x509 key pair: %w", err)
	}

	return tlsCert, nil
}

// generateNodeBundle creates a serializable cert bundle for a fleet node.
// The bundle is sent over the deploy stream before the membrane handshake.
func (p *ephemeralPKI) generateNodeBundle(nodeID string) (*certBundle, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("pki: generate key: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject: pkix.Name{
			Organization: []string{"cortex-mesh-node"},
			CommonName:   nodeID,
		},
		NotBefore: time.Now().Add(-1 * time.Hour),
		NotAfter:  time.Now().Add(24 * time.Hour),
		// Fleet nodes get both client+server auth for bidirectional mTLS.
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
			x509.ExtKeyUsageClientAuth,
		},
		KeyUsage:    x509.KeyUsageDigitalSignature,
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:    []string{"localhost"},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, p.ca, pub, p.caKey)
	if err != nil {
		return nil, fmt.Errorf("pki: create cert: %w", err)
	}

	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("pki: marshal key: %w", err)
	}

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: p.ca.Raw})

	return &certBundle{
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}),
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER}),
		CaPEM:   caPEM,
	}, nil
}

// membraneConfig creates a membrane.Config from a TLS certificate and CA pool.
func (p *ephemeralPKI) membraneConfig(cert tls.Certificate) *membrane.Config {
	return &membrane.Config{
		Certificate: cert,
		CACert:      p.pool,
	}
}

// --- Cert bootstrap over deploy streams ---
//
// Before the membrane handshake, the gateway writes the fleet node's
// cert bundle over the raw deploy stream. The fleet node reads it from
// stdin. Format: [4-byte length][PEM data] for each of cert, key, CA.

// writeCertBundle writes a cert bundle to a stream.
func writeCertBundle(w io.Writer, bundle *certBundle) error {
	for _, data := range [][]byte{bundle.CertPEM, bundle.KeyPEM, bundle.CaPEM} {
		if err := writeField(w, data); err != nil {
			return fmt.Errorf("write cert bundle: %w", err)
		}
	}
	return nil
}

// readCertBundle reads a cert bundle from a stream, returning the raw bundle and the parsed membrane.Config.
func readCertBundle(r io.Reader) (*certBundle, *membrane.Config, error) {
	certPEM, err := readField(r)
	if err != nil {
		return nil, nil, fmt.Errorf("read cert: %w", err)
	}
	keyPEM, err := readField(r)
	if err != nil {
		return nil, nil, fmt.Errorf("read key: %w", err)
	}
	caPEM, err := readField(r)
	if err != nil {
		return nil, nil, fmt.Errorf("read CA: %w", err)
	}

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, nil, fmt.Errorf("x509 key pair: %w", err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, nil, fmt.Errorf("failed to parse CA cert")
	}

	bundle := &certBundle{
		CertPEM: certPEM,
		KeyPEM:  keyPEM,
		CaPEM:   caPEM,
	}

	return bundle, &membrane.Config{
		Certificate: cert,
		CACert:      pool,
	}, nil
}

func writeField(w io.Writer, data []byte) error {
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(data)))
	if _, err := w.Write(lenBuf[:]); err != nil {
		return err
	}
	_, err := w.Write(data)
	return err
}

func readField(r io.Reader) ([]byte, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	if n > 1<<20 { // 1MB sanity limit
		return nil, fmt.Errorf("field too large: %d bytes", n)
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, err
	}
	return data, nil
}
