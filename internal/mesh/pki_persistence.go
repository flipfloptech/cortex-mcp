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

// MeshCA represents the serialized site/mesh CA.
type MeshCA struct {
	CaPEM  []byte `json:"ca_pem"`
	KeyPEM []byte `json:"key_pem"`
}

// caPath returns the path to the Mesh CA file.
func caPath() (string, error) {
	if os.Getuid() == 0 {
		return "/opt/cortex-mcp/etc/mesh_ca.json", nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return filepath.Join(home, ".cortex-mcp", "mesh_ca.json"), nil
}

// legacyCAPath returns the old Gateway CA path for backward compatibility.
func legacyCAPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return filepath.Join(home, ".cortex-mcp", "gateway_ca.json"), nil
}

// loadOrGeneratePKI attempts to load the local Mesh CA from disk.
// If it does not exist, it generates a fresh Mesh CA and saves it.
// Returns the PKI, a boolean indicating if it was newly generated, and any error.
func loadOrGeneratePKI() (*ephemeralPKI, bool, error) {
	path, err := caPath()
	var loadErr error

	if err == nil {
		data, err := os.ReadFile(path)
		if err != nil {
			// Try legacy path
			legacy, lErr := legacyCAPath()
			if lErr == nil {
				data, err = os.ReadFile(legacy)
			}
		}

		if err == nil {
			var mca MeshCA
			if err := json.Unmarshal(data, &mca); err == nil {
				block, _ := pem.Decode(mca.KeyPEM)
				if block != nil {
					priv, err := x509.ParsePKCS8PrivateKey(block.Bytes)
					if err == nil {
						caBlock, _ := pem.Decode(mca.CaPEM)
						if caBlock != nil {
							ca, err := x509.ParseCertificate(caBlock.Bytes)
							if err == nil {
								pool := x509.NewCertPool()
								pool.AddCert(ca)
								return &ephemeralPKI{ca: ca, caKey: priv.(ed25519.PrivateKey), pool: pool}, false, nil
							}
						}
					}
				}
			}
		}
		loadErr = err
	}

	_ = loadErr // just for debugging if needed

	// Fallback to generate
	pki, err := newEphemeralPKI()
	if err != nil {
		return nil, false, err
	}

	// Save it for future runs
	if path != "" {
		_ = os.MkdirAll(filepath.Dir(path), 0700)
		privDER, _ := x509.MarshalPKCS8PrivateKey(pki.caKey)
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})
		caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: pki.ca.Raw})

		data, _ := json.MarshalIndent(MeshCA{
			CaPEM:  caPEM,
			KeyPEM: keyPEM,
		}, "", "  ")
		_ = os.WriteFile(path, data, 0600)
	}

	return pki, true, nil
}
