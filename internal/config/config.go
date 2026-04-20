// Package config provides TOML-based configuration loading for the
// cortex-mcp application. It handles mesh.toml parsing, host
// resolution seeding, and credential vault population.
package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/cortex-mesh/cortex-mesh/vault"
)

// MeshConfig is the top-level configuration parsed from mesh.toml.
type MeshConfig struct {
	// Node identity configuration.
	Node NodeConfig `toml:"node"`

	// Seed hosts for mesh discovery.
	// Key is the node ID, value contains addresses.
	Hosts map[string]Host `toml:"hosts"`

	// SSH/TLS credentials for deployment.
	Credentials []CredentialEntry `toml:"credentials"`

	// Groups for logically targeting nodes via the mesh.
	Groups map[string][]string `toml:"groups,omitempty"`
}

// NodeConfig holds node identity settings.
type NodeConfig struct {
	// ID is the node's unique identifier in the mesh.
	// If empty, it falls back to the hostname.
	ID string `toml:"id"`
}

// Host defines the addresses for a known mesh node.
type Host struct {
	// Addresses is a list of IP addresses or hostnames for this node.
	Addresses []string `toml:"addresses"`
}

// CredentialEntry defines a single credential in the config file.
// The Type field determines which other fields are relevant.
type CredentialEntry struct {
	// Pattern is the host glob pattern (e.g., "10.0.1.*", "*.internal.corp").
	Pattern string `toml:"pattern"`

	// Type is the credential type: "ssh_key", "ssh_password", or "tls_cert".
	Type string `toml:"type"`

	// Username for SSH authentication.
	Username string `toml:"username"`

	// Password for SSH password authentication (type = "ssh_password").
	Password string `toml:"password"`

	// KeyFile is the path to an SSH private key file (type = "ssh_key").
	// Supports ~ for home directory expansion.
	KeyFile string `toml:"key_file"`

	// CertFile is the path to a TLS certificate file (type = "tls_cert").
	CertFile string `toml:"cert_file"`
}

// Load reads and parses a mesh.toml configuration file.
func Load(path string) (*MeshConfig, error) {
	cfg := &MeshConfig{
		Hosts: make(map[string]Host),
	}

	if _, err := toml.DecodeFile(path, cfg); err != nil {
		return nil, fmt.Errorf("config: load %s: %w", path, err)
	}

	return cfg, nil
}

// KnownHosts converts the Hosts map into the format expected by
// api.NodeConfig.KnownHosts (map[nodeID][]addresses).
func (mc *MeshConfig) KnownHosts() map[string][]string {
	hosts := make(map[string][]string, len(mc.Hosts))
	for nodeID, h := range mc.Hosts {
		hosts[nodeID] = h.Addresses
	}
	return hosts
}

// LoadCredentials populates a vault with the credentials defined
// in the config file. SSH key files are read from disk.
func (mc *MeshConfig) LoadCredentials(v *vault.Vault) error {
	for i, entry := range mc.Credentials {
		cred, err := entryToCredential(entry)
		if err != nil {
			return fmt.Errorf("config: credential[%d] (%s): %w", i, entry.Pattern, err)
		}
		v.Store(entry.Pattern, cred)
	}
	return nil
}

// entryToCredential converts a TOML credential entry to a vault.Credential.
func entryToCredential(entry CredentialEntry) (vault.Credential, error) {
	switch entry.Type {
	case "ssh_key":
		keyPath := expandTilde(entry.KeyFile)
		keyData, err := os.ReadFile(keyPath)
		if err != nil {
			return vault.Credential{}, fmt.Errorf("read key file %s: %w", keyPath, err)
		}
		return vault.Credential{
			Type:       vault.CredSSHKey,
			Username:   entry.Username,
			PrivateKey: keyData,
		}, nil

	case "ssh_password":
		return vault.Credential{
			Type:     vault.CredSSHPassword,
			Username: entry.Username,
			Password: entry.Password,
		}, nil

	case "tls_cert":
		certPath := expandTilde(entry.CertFile)
		certData, err := os.ReadFile(certPath)
		if err != nil {
			return vault.Credential{}, fmt.Errorf("read cert file %s: %w", certPath, err)
		}
		return vault.Credential{
			Type:        vault.CredTLSCert,
			Certificate: certData,
		}, nil

	default:
		return vault.Credential{}, fmt.Errorf("unknown credential type: %q (expected ssh_key, ssh_password, or tls_cert)", entry.Type)
	}
}

// expandTilde replaces a leading ~ with the user's home directory.
func expandTilde(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path // fallback: return as-is
		}
		return home + path[1:]
	}
	return path
}
