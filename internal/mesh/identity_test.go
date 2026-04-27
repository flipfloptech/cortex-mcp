package mesh

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIdentityPersistence(t *testing.T) {
	// Create a temporary home directory
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// We need valid certificates to test x509KeyPair loading.
	caPub, caPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}

	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "cortex-mcp-ephemeral-ca"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}

	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caPub, caPriv)
	if err != nil {
		t.Fatalf("create CA: %v", err)
	}

	nodePub, nodePriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate node key: %v", err)
	}

	nodeTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "test-node-123"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, nodeTemplate, caTemplate, nodePub, caPriv)
	if err != nil {
		t.Fatalf("create node cert: %v", err)
	}

	privDER, err := x509.MarshalPKCS8PrivateKey(nodePriv)
	if err != nil {
		t.Fatalf("marshal priv: %v", err)
	}

	bundle := &certBundle{
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}),
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER}),
		CaPEM:   pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
	}

	nodeID := "test-node-123"

	// 1. Save Identity
	err = SaveIdentity(nodeID, bundle)
	if err != nil {
		t.Fatalf("SaveIdentity failed: %v", err)
	}

	// Verify permissions
	path := filepath.Join(tmpHome, ".cortex-mcp", "identity.json")
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("expected file at %s: %v", path, err)
	}
	if stat.Mode().Perm() != 0600 {
		t.Errorf("expected permissions 0600, got %o", stat.Mode().Perm())
	}

	// 2. Load Identity
	loadedNodeID, cfg, err := LoadIdentity()
	if err != nil {
		t.Fatalf("LoadIdentity failed: %v", err)
	}

	if loadedNodeID != nodeID {
		t.Errorf("expected nodeID %s, got %s", nodeID, loadedNodeID)
	}

	if cfg == nil || len(cfg.Certificate.Certificate) == 0 {
		t.Fatalf("invalid membrane config loaded")
	}
}
