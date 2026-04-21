package mesh

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
)

// GatewayCA represents the serialized site CA.
type GatewayCA struct {
	CaPEM  []byte `json:"ca_pem"`
	KeyPEM []byte `json:"key_pem"`
}

func caPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return filepath.Join(home, ".cortex-mesh", "gateway_ca.json"), nil
}

// loadOrGeneratePKI attempts to load the gateway's site CA from disk.
// If it does not exist, it generates a fresh site CA and saves it.
func loadOrGeneratePKI() (*ephemeralPKI, error) {
	path, err := caPath()
	if err == nil {
		data, err := os.ReadFile(path)
		if err == nil {
			var gca GatewayCA
			if err := json.Unmarshal(data, &gca); err == nil {
				block, _ := pem.Decode(gca.KeyPEM)
				if block != nil {
					priv, err := x509.ParsePKCS8PrivateKey(block.Bytes)
					if err == nil {
						caBlock, _ := pem.Decode(gca.CaPEM)
						if caBlock != nil {
							ca, err := x509.ParseCertificate(caBlock.Bytes)
							if err == nil {
								pool := x509.NewCertPool()
								pool.AddCert(ca)
								return &ephemeralPKI{ca: ca, caKey: priv.(ed25519.PrivateKey), pool: pool}, nil
							}
						}
					}
				}
			}
		}
	}

	// Fallback to generate
	pki, err := newEphemeralPKI()
	if err != nil {
		return nil, err
	}

	// Save it for future runs
	if path != "" {
		_ = os.MkdirAll(filepath.Dir(path), 0700)
		privDER, _ := x509.MarshalPKCS8PrivateKey(pki.caKey)
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})
		caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: pki.ca.Raw})

		data, _ := json.MarshalIndent(GatewayCA{
			CaPEM:  caPEM,
			KeyPEM: keyPEM,
		}, "", "  ")
		_ = os.WriteFile(path, data, 0600)
	}

	return pki, nil
}
