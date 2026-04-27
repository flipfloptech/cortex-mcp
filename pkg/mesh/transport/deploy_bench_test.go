package transport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

func BenchmarkIsStaticBinary(b *testing.B) {
	exe, err := os.Executable()
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = isStaticBinary(exe)
	}
}

func BenchmarkSignalReady(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = SignalReady(io.Discard)
	}
}

type infiniteReadyReader struct{}

func (r *infiniteReadyReader) Read(p []byte) (n int, err error) {
	copy(p, DeployReadyMagic[:])
	return len(DeployReadyMagic), nil
}

func BenchmarkWaitForReady(b *testing.B) {
	r := &infiniteReadyReader{}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = waitForReady(r)
	}
}

func BenchmarkShellQuote(b *testing.B) {
	input := "some string with 'quotes' in it"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = shellQuote(input)
	}
}

func BenchmarkWasDeployed(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = WasDeployed()
	}
}

func BenchmarkBuildAuthMethods(b *testing.B) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	cred := DeployCredential{
		SSHUser:    "root",
		SSHPass:    "password",
		SSHKeyData: signer,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = buildAuthMethods(cred)
	}
}

func BenchmarkHostKeyCallback(b *testing.B) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	pub := signer.PublicKey()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = hostKeyCallback(pub)
	}
}

func BenchmarkDeploy(b *testing.B) {
	hostSigner := testSSHSigner(b)
	clientSigner := testSSHSigner(b)

	addr, cleanup := deploySSHServer(b, hostSigner, clientSigner.PublicKey())
	defer cleanup()

	tmpFile, err := os.CreateTemp(b.TempDir(), "mesh-binary-*")
	if err != nil {
		b.Fatalf("create temp file: %v", err)
	}
	binaryContent := []byte("#!/bin/sh\necho mesh-node-active\n")
	if _, err := tmpFile.Write(binaryContent); err != nil {
		b.Fatalf("write binary: %v", err)
	}
	if err := tmpFile.Close(); err != nil {
		b.Fatalf("close temp file: %v", err)
	}

	cred := DeployCredential{
		SSHUser:    "mesh",
		SSHKeyData: clientSigner,
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		deployer := &SelfDeployer{
			BinaryPath: tmpFile.Name(),
			RemotePath: filepath.Join(b.TempDir(), "mesh-node"),
			SkipUpload: true, // skip upload to make it fast enough for a benchmark
		}

		ctx := context.Background()
		stream, err := deployer.Deploy(ctx, addr, cred, hostSigner.PublicKey())
		if err == nil {
			_ = stream.Close()
		}
	}
}

func BenchmarkUploadBinary(b *testing.B) {
	hostSigner := testSSHSigner(b)
	clientSigner := testSSHSigner(b)

	addr, cleanup := deploySSHServer(b, hostSigner, clientSigner.PublicKey())
	defer cleanup()

	authMethods, _ := buildAuthMethods(DeployCredential{
		SSHUser:    "mesh",
		SSHKeyData: clientSigner,
	})
	clientConf := &ssh.ClientConfig{
		User:            "mesh",
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	binaryContent := make([]byte, 1024)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		sshConn, err := ssh.Dial("tcp", addr, clientConf)
		if err != nil {
			b.StartTimer()
			continue
		}
		reader := bytes.NewReader(binaryContent)
		deployer := &SelfDeployer{}
		b.StartTimer()

		_ = deployer.uploadBinary(sshConn, reader, filepath.Join(b.TempDir(), "upload-bench"))

		b.StopTimer()
		_ = sshConn.Close()
		b.StartTimer()
	}
}

func BenchmarkExecBinary(b *testing.B) {
	hostSigner := testSSHSigner(b)
	clientSigner := testSSHSigner(b)

	addr, cleanup := deploySSHServer(b, hostSigner, clientSigner.PublicKey())
	defer cleanup()

	authMethods, _ := buildAuthMethods(DeployCredential{
		SSHUser:    "mesh",
		SSHKeyData: clientSigner,
	})
	clientConf := &ssh.ClientConfig{
		User:            "mesh",
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		sshConn, err := ssh.Dial("tcp", addr, clientConf)
		if err != nil {
			b.StartTimer()
			continue
		}
		deployer := &SelfDeployer{}
		b.StartTimer()

		stream, _ := deployer.execBinary(sshConn, "/tmp/fake")
		if stream != nil {
			_ = stream.Close()
		}

		b.StopTimer()
		_ = sshConn.Close()
		b.StartTimer()
	}
}

func BenchmarkStderr(b *testing.B) {
	buf := bytes.NewBufferString("some stderr content")
	s := &deployStream{
		stderrBuf: buf,
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.Stderr()
	}
}
