// Package testutil provides test helpers shared across cortex-mesh packages.
// This package is ONLY for use in test code — it imports testing.T.
package testutil

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/cortex-mesh/cortex-mesh/membrane"
)

// GenerateTestCerts creates a test CA pool, server membrane.Config,
// and client membrane.Config for integration testing.
// All certificates are Ed25519 and signed by the test CA.
func GenerateTestCerts(t *testing.T) (serverCfg *membrane.Config, clientCfg *membrane.Config) {
	t.Helper()

	ca, caKey := generateCA(t)
	pool := x509.NewCertPool()
	pool.AddCert(ca)

	serverCert := generateNodeCert(t, ca, caKey, "node-server", true)
	clientCert := generateNodeCert(t, ca, caKey, "node-client", false)

	serverCfg = &membrane.Config{Certificate: serverCert, CACert: pool}
	clientCfg = &membrane.Config{Certificate: clientCert, CACert: pool}
	return
}

// GenerateTestCertsWithIDs creates certs with specific NodeIDs.
func GenerateTestCertsWithIDs(t *testing.T, serverID, clientID string) (serverCfg *membrane.Config, clientCfg *membrane.Config) {
	t.Helper()

	ca, caKey := generateCA(t)
	pool := x509.NewCertPool()
	pool.AddCert(ca)

	serverCert := generateNodeCert(t, ca, caKey, serverID, true)
	clientCert := generateNodeCert(t, ca, caKey, clientID, false)

	serverCfg = &membrane.Config{Certificate: serverCert, CACert: pool}
	clientCfg = &membrane.Config{Certificate: clientCert, CACert: pool}
	return
}

// GenerateDualCert creates a membrane.Config for a node that can act as
// both TLS server (accept) and TLS client (dial). The certificate has
// both ExtKeyUsageServerAuth and ExtKeyUsageClientAuth.
//
// This is required for nodes that listen for inbound connections AND
// dial out to reconnect — the standard case for production mesh nodes.
func GenerateDualCert(t *testing.T, ca *x509.Certificate, caKey ed25519.PrivateKey, pool *x509.CertPool, nodeID string) *membrane.Config {
	t.Helper()

	cert := generateDualNodeCert(t, ca, caKey, nodeID)
	return &membrane.Config{Certificate: cert, CACert: pool}
}

// GenerateTestCA creates a test CA and returns it with a CA pool.
// Use with GenerateDualCert for multi-node test setups.
func GenerateTestCA(t *testing.T) (ca *x509.Certificate, caKey ed25519.PrivateKey, pool *x509.CertPool) {
	t.Helper()

	ca, caKey = generateCA(t)
	pool = x509.NewCertPool()
	pool.AddCert(ca)
	return
}

func generateCA(t *testing.T) (*x509.Certificate, ed25519.PrivateKey) {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate Ed25519 CA key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"cortex-mesh-site"},
			CommonName:   "cortex-mesh-site-ca",
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, pub, priv)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}

	return cert, priv
}

func generateNodeCert(t *testing.T, ca *x509.Certificate, caKey ed25519.PrivateKey, nodeID string, isServer bool) tls.Certificate {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate node key: %v", err)
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

	certDER, err := x509.CreateCertificate(rand.Reader, template, ca, pub, caKey)
	if err != nil {
		t.Fatalf("create node cert: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}

	return tlsCert
}

// generateDualNodeCert creates a certificate with BOTH ServerAuth and
// ClientAuth extended key usage. Required for nodes that listen (server)
// and dial out (client) for reconnection.
func generateDualNodeCert(t *testing.T, ca *x509.Certificate, caKey ed25519.PrivateKey, nodeID string) tls.Certificate {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate node key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject: pkix.Name{
			Organization: []string{"cortex-mesh-node"},
			CommonName:   nodeID,
		},
		NotBefore: time.Now().Add(-1 * time.Hour),
		NotAfter:  time.Now().Add(24 * time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
			x509.ExtKeyUsageClientAuth,
		},
		KeyUsage:    x509.KeyUsageDigitalSignature,
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:    []string{"localhost"},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, ca, pub, caKey)
	if err != nil {
		t.Fatalf("create dual node cert: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}

	return tlsCert
}
