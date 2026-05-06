package mesh

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadOrGeneratePKI(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Test generation
	pki1, isNew, err := loadOrGeneratePKI()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isNew {
		t.Errorf("expected isNew to be true")
	}
	if pki1 == nil || pki1.ca == nil {
		t.Fatalf("expected valid pki")
	}

	// Test loading
	pki2, isNew, err := loadOrGeneratePKI()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isNew {
		t.Errorf("expected isNew to be false")
	}
	if string(pki1.ca.Raw) != string(pki2.ca.Raw) {
		t.Errorf("loaded CA does not match generated CA")
	}
}

func TestLegacyCAPathFallback(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Write legacy gateway_ca.json
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

	privDER, err := x509.MarshalPKCS8PrivateKey(caPriv)
	if err != nil {
		t.Fatalf("marshal priv: %v", err)
	}

	legacyCA := MeshCA{
		CaPEM:  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		KeyPEM: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER}),
	}

	data, err := json.Marshal(legacyCA)
	if err != nil {
		t.Fatalf("marshal legacy CA: %v", err)
	}

	cortexDir := filepath.Join(tmpHome, ".cortex-mcp")
	if err := os.MkdirAll(cortexDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(cortexDir, "gateway_ca.json"), data, 0600); err != nil {
		t.Fatalf("write legacy CA: %v", err)
	}

	// Should load the legacy CA and not generate a new one
	pki, isNew, err := loadOrGeneratePKI()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isNew {
		t.Errorf("expected isNew to be false, should have loaded legacy CA")
	}
	if string(pki.ca.Raw) != string(caDER) {
		t.Errorf("loaded CA does not match legacy CA")
	}
}

func BenchmarkLegacyCAPath(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = legacyCAPath()
	}
}
