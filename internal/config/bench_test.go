package config

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"

	"github.com/cortex-mesh/cortex-mesh/vault"
)

func BenchmarkLoad(b *testing.B) {
	tmpFile := filepath.Join(b.TempDir(), "mesh.toml")
	content := `
[node]
id = "test-node"

[hosts]
[hosts.node1]
addresses = ["10.0.0.1:4001"]

[[credentials]]
pattern = "*"
type = "ssh_password"
username = "admin"
password = "password"
`
	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := Load(tmpFile)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkKnownHosts(b *testing.B) {
	cfg := &MeshConfig{
		Hosts: map[string]Host{
			"node1": {Addresses: []string{"10.0.0.1:4001"}},
			"node2": {Addresses: []string{"10.0.0.2:4001"}},
			"node3": {Addresses: []string{"10.0.0.3:4001"}},
		},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cfg.KnownHosts()
	}
}

func BenchmarkLoadCredentials(b *testing.B) {
	cfg := &MeshConfig{
		Credentials: []CredentialEntry{
			{Pattern: "node1", Type: "ssh_password", Username: "user", Password: "pwd"},
			{Pattern: "node2", Type: "ssh_password", Username: "user", Password: "pwd"},
		},
	}
	_, privKey, _ := ed25519.GenerateKey(nil)
	v, _ := vault.New(privKey)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cfg.LoadCredentials(v)
	}
}

func BenchmarkEntryToCredential(b *testing.B) {
	entry := CredentialEntry{
		Pattern:  "*",
		Type:     "ssh_password",
		Username: "admin",
		Password: "password",
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = entryToCredential(entry)
	}
}

func BenchmarkExpandTilde(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = expandTilde("~/some/path")
	}
}

func BenchmarkDefault(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = Default()
	}
}

func BenchmarkGetMeshPort(b *testing.B) {
	host := Host{}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = host.GetMeshPort(4001)
	}
}

func BenchmarkGetSSHPort(b *testing.B) {
	host := Host{}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = host.GetSSHPort(22)
	}
}
