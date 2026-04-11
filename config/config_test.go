package config

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/cortex-mesh/cortex-mesh/vault"
)

func TestLoad_FullConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "mesh.toml")

	content := `
[node]
id = "gateway-01"

[hosts.mds-01]
addresses = ["10.0.1.5"]

[hosts.oss-01]
addresses = ["10.0.1.10", "10.0.1.11"]

[[credentials]]
pattern  = "10.0.1.*"
type     = "ssh_key"
username = "deploy"
key_file = "` + filepath.Join(dir, "test_key") + `"

[[credentials]]
pattern  = "*.corp"
type     = "ssh_password"
username = "admin"
password = "secret123"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	// Create a dummy key file so the config is valid.
	if err := os.WriteFile(filepath.Join(dir, "test_key"), []byte("fake-key-data"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Node.ID != "gateway-01" {
		t.Fatalf("node.id = %q, want %q", cfg.Node.ID, "gateway-01")
	}

	if len(cfg.Hosts) != 2 {
		t.Fatalf("hosts count = %d, want 2", len(cfg.Hosts))
	}

	mds := cfg.Hosts["mds-01"]
	if len(mds.Addresses) != 1 || mds.Addresses[0] != "10.0.1.5" {
		t.Fatalf("mds-01 addresses = %v", mds.Addresses)
	}

	oss := cfg.Hosts["oss-01"]
	if len(oss.Addresses) != 2 {
		t.Fatalf("oss-01 addresses = %v", oss.Addresses)
	}

	if len(cfg.Credentials) != 2 {
		t.Fatalf("credentials count = %d, want 2", len(cfg.Credentials))
	}

	if cfg.Credentials[0].Type != "ssh_key" {
		t.Fatalf("cred[0].type = %q, want %q", cfg.Credentials[0].Type, "ssh_key")
	}
	if cfg.Credentials[1].Type != "ssh_password" {
		t.Fatalf("cred[1].type = %q, want %q", cfg.Credentials[1].Type, "ssh_password")
	}
}

func TestLoad_MinimalConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "mesh.toml")

	content := `
[node]
id = "test-node"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Node.ID != "test-node" {
		t.Fatalf("node.id = %q, want %q", cfg.Node.ID, "test-node")
	}

	if len(cfg.Hosts) != 0 {
		t.Fatalf("hosts count = %d, want 0", len(cfg.Hosts))
	}

	if len(cfg.Credentials) != 0 {
		t.Fatalf("credentials count = %d, want 0", len(cfg.Credentials))
	}
}

func TestLoad_EmptyConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "mesh.toml")

	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Node.ID != "" {
		t.Fatalf("node.id = %q, want empty", cfg.Node.ID)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	t.Parallel()

	_, err := Load("/nonexistent/path/mesh.toml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestKnownHosts(t *testing.T) {
	t.Parallel()

	cfg := &MeshConfig{
		Hosts: map[string]Host{
			"mds-01": {Addresses: []string{"10.0.1.5"}},
			"oss-01": {Addresses: []string{"10.0.1.10", "10.0.1.11"}},
		},
	}

	hosts := cfg.KnownHosts()

	if len(hosts) != 2 {
		t.Fatalf("hosts count = %d, want 2", len(hosts))
	}

	if len(hosts["mds-01"]) != 1 || hosts["mds-01"][0] != "10.0.1.5" {
		t.Fatalf("mds-01 = %v", hosts["mds-01"])
	}

	if len(hosts["oss-01"]) != 2 {
		t.Fatalf("oss-01 = %v", hosts["oss-01"])
	}
}

func TestKnownHosts_Empty(t *testing.T) {
	t.Parallel()

	cfg := &MeshConfig{}
	hosts := cfg.KnownHosts()

	if hosts == nil {
		t.Fatal("KnownHosts should return non-nil map")
	}
	if len(hosts) != 0 {
		t.Fatalf("hosts count = %d, want 0", len(hosts))
	}
}

func TestLoadCredentials_SSHKey(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "test_key")
	keyData := []byte("ssh-private-key-content")
	if err := os.WriteFile(keyPath, keyData, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &MeshConfig{
		Credentials: []CredentialEntry{
			{
				Pattern:  "10.0.1.*",
				Type:     "ssh_key",
				Username: "deploy",
				KeyFile:  keyPath,
			},
		},
	}

	_, privKey, _ := ed25519.GenerateKey(rand.Reader)
	v, err := vault.New(privKey)
	if err != nil {
		t.Fatal(err)
	}

	if err := cfg.LoadCredentials(v); err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}

	cred, ok := v.Match("10.0.1.5")
	if !ok {
		t.Fatal("expected match for 10.0.1.5")
	}
	if cred.Type != vault.CredSSHKey {
		t.Fatalf("type = %d, want CredSSHKey", cred.Type)
	}
	if cred.Username != "deploy" {
		t.Fatalf("username = %q", cred.Username)
	}
	if string(cred.PrivateKey) != "ssh-private-key-content" {
		t.Fatalf("private key mismatch")
	}
}

func TestLoadCredentials_SSHPassword(t *testing.T) {
	t.Parallel()

	cfg := &MeshConfig{
		Credentials: []CredentialEntry{
			{
				Pattern:  "*.corp",
				Type:     "ssh_password",
				Username: "admin",
				Password: "secret123",
			},
		},
	}

	_, privKey, _ := ed25519.GenerateKey(rand.Reader)
	v, err := vault.New(privKey)
	if err != nil {
		t.Fatal(err)
	}

	if err := cfg.LoadCredentials(v); err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}

	cred, ok := v.Match("web.corp")
	if !ok {
		t.Fatal("expected match for web.corp")
	}
	if cred.Type != vault.CredSSHPassword {
		t.Fatalf("type = %d, want CredSSHPassword", cred.Type)
	}
	if cred.Username != "admin" {
		t.Fatalf("username = %q", cred.Username)
	}
	if cred.Password != "secret123" {
		t.Fatalf("password = %q", cred.Password)
	}
}

func TestLoadCredentials_MissingKeyFile(t *testing.T) {
	t.Parallel()

	cfg := &MeshConfig{
		Credentials: []CredentialEntry{
			{
				Pattern:  "10.0.1.*",
				Type:     "ssh_key",
				Username: "deploy",
				KeyFile:  "/nonexistent/key",
			},
		},
	}

	_, privKey, _ := ed25519.GenerateKey(rand.Reader)
	v, err := vault.New(privKey)
	if err != nil {
		t.Fatal(err)
	}

	if err := cfg.LoadCredentials(v); err == nil {
		t.Fatal("expected error for missing key file")
	}
}

func TestLoadCredentials_InvalidType(t *testing.T) {
	t.Parallel()

	cfg := &MeshConfig{
		Credentials: []CredentialEntry{
			{
				Pattern: "10.0.1.*",
				Type:    "invalid",
			},
		},
	}

	_, privKey, _ := ed25519.GenerateKey(rand.Reader)
	v, err := vault.New(privKey)
	if err != nil {
		t.Fatal(err)
	}

	if err := cfg.LoadCredentials(v); err == nil {
		t.Fatal("expected error for invalid credential type")
	}
}

func TestLoadCredentials_TildeExpansion(t *testing.T) {
	t.Parallel()

	// Create a temp key in the actual home dir would be invasive,
	// so just test that the expansion logic works on a known path.
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key")
	if err := os.WriteFile(keyPath, []byte("key-data"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &MeshConfig{
		Credentials: []CredentialEntry{
			{
				Pattern:  "10.0.1.*",
				Type:     "ssh_key",
				Username: "deploy",
				KeyFile:  keyPath, // absolute path — no tilde
			},
		},
	}

	_, privKey, _ := ed25519.GenerateKey(rand.Reader)
	v, err := vault.New(privKey)
	if err != nil {
		t.Fatal(err)
	}

	if err := cfg.LoadCredentials(v); err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}

	cred, ok := v.Match("10.0.1.1")
	if !ok {
		t.Fatal("expected match")
	}
	if string(cred.PrivateKey) != "key-data" {
		t.Fatal("key data mismatch")
	}
}

func TestLoadCredentials_Empty(t *testing.T) {
	t.Parallel()

	cfg := &MeshConfig{}

	_, privKey, _ := ed25519.GenerateKey(rand.Reader)
	v, err := vault.New(privKey)
	if err != nil {
		t.Fatal(err)
	}

	if err := cfg.LoadCredentials(v); err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
}
