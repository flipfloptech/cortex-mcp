package transport

import (
	"context"
	"testing"

	"golang.org/x/crypto/ssh"
)

func BenchmarkDialSSH(b *testing.B) {
	hostSigner := testSSHSigner(b)
	clientSigner := testSSHSigner(b)

	addr, cleanup := testSSHServer(b, hostSigner, clientSigner.PublicKey())
	defer cleanup()

	clientConf := &ssh.ClientConfig{
		User: "mesh",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(clientSigner),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		conn, err := DialSSH(ctx, addr, clientConf)
		if err == nil {
			_ = conn.Close()
		}
	}
}

func BenchmarkDialSSHTunnel(b *testing.B) {
	b.Skip("Networking: involves live network I/O, goroutines, or blocking channels")
}
