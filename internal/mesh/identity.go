package mesh

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/membrane"
)

// NodeIdentity represents the serialized identity of a persistent mesh node.
type NodeIdentity struct {
	NodeID  string `json:"node_id"`
	CertPEM []byte `json:"cert_pem"`
	KeyPEM  []byte `json:"key_pem"`
	CaPEM   []byte `json:"ca_pem"`
}

// identityPath returns the absolute path to the node's identity file.
func identityPath() (string, error) {
	// For system-wide installations (installer or daemon running as root),
	// we store the identity in the system installation directory.
	if os.Getuid() == 0 {
		return "/opt/cortex-mesh/etc/identity.json", nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return filepath.Join(home, ".cortex-mesh", "identity.json"), nil
}

// SaveIdentity writes the node identity and certificate bundle to disk securely.
func SaveIdentity(nodeID string, bundle *certBundle) error {
	path, err := identityPath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	ident := NodeIdentity{
		NodeID:  nodeID,
		CertPEM: bundle.CertPEM,
		KeyPEM:  bundle.KeyPEM,
		CaPEM:   bundle.CaPEM,
	}

	data, err := json.MarshalIndent(ident, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal identity: %w", err)
	}

	// Restrict permissions to the user only, as this contains the private key.
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write identity file: %w", err)
	}

	return nil
}

// LoadIdentity reads the node identity from disk and returns the config and nodeID.
func LoadIdentity() (string, *membrane.Config, error) {
	path, err := identityPath()
	if err != nil {
		return "", nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, fmt.Errorf("read identity file: %w", err)
	}

	var ident NodeIdentity
	if err := json.Unmarshal(data, &ident); err != nil {
		return "", nil, fmt.Errorf("unmarshal identity: %w", err)
	}

	cert, err := tls.X509KeyPair(ident.CertPEM, ident.KeyPEM)
	if err != nil {
		return "", nil, fmt.Errorf("x509 key pair: %w", err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ident.CaPEM) {
		return "", nil, fmt.Errorf("failed to parse CA cert")
	}

	config := &membrane.Config{
		Certificate: cert,
		CACert:      pool,
	}

	return ident.NodeID, config, nil
}

// ConfigPath returns the absolute path to the node's saved configuration file.
func ConfigPath() (string, error) {
	if os.Getuid() == 0 {
		return "/opt/cortex-mesh/etc/mesh.toml", nil
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".cortex-mesh", "mesh.toml"), nil
	}
	return "/opt/cortex-mesh/bin/mesh.toml", nil
}

// SaveConfig writes the TOML configuration to disk securely.
func SaveConfig(data []byte) error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	// 0600 because it might be sensitive, though credentials should be stripped
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write config file: %w", err)
	}

	return nil
}
