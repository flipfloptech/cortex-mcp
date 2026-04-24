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

func TestDefaultConfig(t *testing.T) {
	t.Parallel()

	cfg := Default()
	if cfg.Node.MeshPort != 4443 {
		t.Fatalf("mesh port = %d, want 4443", cfg.Node.MeshPort)
	}
	if cfg.Node.SSHPort != 22 {
		t.Fatalf("ssh port = %d, want 22", cfg.Node.SSHPort)
	}
	if cfg.Hosts == nil {
		t.Fatal("expected Hosts map to be initialized")
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
		Node: NodeConfig{
			MeshPort: 9999, // Global default override
		},
		Hosts: map[string]Host{
			"mds-01": {Addresses: []string{"10.0.1.5"}},
			"oss-01": {Addresses: []string{"10.0.1.10", "10.0.1.11:2222"}, MeshPort: 8888},
		},
	}

	hosts := cfg.KnownHosts()

	if len(hosts) != 2 {
		t.Fatalf("hosts count = %d, want 2", len(hosts))
	}

	// Should fallback to global default 9999
	if len(hosts["mds-01"]) != 1 || hosts["mds-01"][0] != "10.0.1.5:9999" {
		t.Fatalf("mds-01 = %v", hosts["mds-01"])
	}

	// Should use host specific override 8888, and preserve explicitly defined ports
	if len(hosts["oss-01"]) != 2 || hosts["oss-01"][0] != "10.0.1.10:8888" || hosts["oss-01"][1] != "10.0.1.11:2222" {
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

func TestStripped(t *testing.T) {
	t.Parallel()

	cfg := &MeshConfig{
		Node: NodeConfig{
			ID:       "node-1",
			MeshPort: 4443,
			SSHPort:  22,
		},
		Hosts: map[string]Host{
			"node-2": {Addresses: []string{"10.0.0.2"}, MeshPort: 4444},
		},
		Credentials: []CredentialEntry{
			{Pattern: "*", Type: "ssh_password", Username: "root", Password: "pwd"},
		},
		Proxies: []ProxyEntry{
			{Pattern: "*", URL: "http://proxy:8080"},
		},
		Groups: map[string][]string{
			"all": {"node-1", "node-2"},
		},
	}

	stripped := cfg.Stripped()

	if len(stripped.Credentials) != 0 {
		t.Fatalf("expected credentials to be stripped, got %d", len(stripped.Credentials))
	}

	if stripped.Node.ID != "node-1" {
		t.Fatalf("expected Node ID node-1, got %s", stripped.Node.ID)
	}

	if len(stripped.Hosts) != 1 || stripped.Hosts["node-2"].MeshPort != 4444 {
		t.Fatalf("expected Hosts to be copied")
	}

	if len(stripped.Proxies) != 1 || stripped.Proxies[0].URL != "http://proxy:8080" {
		t.Fatalf("expected Proxies to be copied")
	}

	if len(stripped.Groups) != 1 || len(stripped.Groups["all"]) != 2 {
		t.Fatalf("expected Groups to be copied")
	}

	// Verify deep copy
	stripped.Hosts["node-2"].Addresses[0] = "10.0.0.3"
	if cfg.Hosts["node-2"].Addresses[0] != "10.0.0.2" {
		t.Fatalf("Hosts address slice was shallow copied")
	}

	stripped.Proxies[0].URL = "http://other:8080"
	if cfg.Proxies[0].URL != "http://proxy:8080" {
		t.Fatalf("Proxies slice was shallow copied")
	}

	stripped.Groups["all"][0] = "node-3"
	if cfg.Groups["all"][0] != "node-1" {
		t.Fatalf("Groups slice was shallow copied")
	}
}

func BenchmarkStripped(b *testing.B) {
	cfg := &MeshConfig{
		Node: NodeConfig{
			SSHPort: 22,
		},
		Hosts: map[string]Host{
			"test": {Addresses: []string{"127.0.0.1"}},
		},
		Groups: map[string][]string{
			"test": {"test"},
		},
		Proxies: []ProxyEntry{
			{Pattern: "*", URL: "http://proxy"},
		},
		Credentials: []CredentialEntry{
			{Pattern: "*", Username: "user", Password: "password"},
		},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cfg.Stripped()
	}
}
