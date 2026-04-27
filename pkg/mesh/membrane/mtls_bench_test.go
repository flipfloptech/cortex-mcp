package membrane

import (
	"context"
	"crypto/x509"
	"net"
	"testing"
)

func BenchmarkHandshakeServer(b *testing.B) {
	serverCfg, clientCfg := testMembranePair(b)
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		clientRaw, serverRaw := net.Pipe()
		go func() {
			conn, _ := HandshakeClient(context.Background(), clientRaw, clientCfg)
			if conn != nil {
				_ = clientRaw.Close()
				_ = conn.Close()
			}
		}()
		b.StartTimer()

		conn, _ := HandshakeServer(context.Background(), serverRaw, serverCfg)
		if conn != nil {
			_ = serverRaw.Close()
			_ = conn.Close()
		}
	}
}

func BenchmarkHandshakeClient(b *testing.B) {
	serverCfg, clientCfg := testMembranePair(b)
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		clientRaw, serverRaw := net.Pipe()
		go func() {
			conn, _ := HandshakeServer(context.Background(), serverRaw, serverCfg)
			if conn != nil {
				_ = serverRaw.Close()
				_ = conn.Close()
			}
		}()
		b.StartTimer()

		conn, _ := HandshakeClient(context.Background(), clientRaw, clientCfg)
		if conn != nil {
			_ = clientRaw.Close()
			_ = conn.Close()
		}
	}
}

func BenchmarkVerifyCertChain(b *testing.B) {
	ca, caKey, pool := testEd25519CA(b)
	clientCert := testNodeCert(b, ca, caKey, "node-client", false)
	leaf, _ := x509.ParseCertificate(clientCert.Certificate[0])
	rawCerts := [][]byte{leaf.Raw}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = verifyCertChain(rawCerts, pool, x509.ExtKeyUsageClientAuth)
	}
}

func BenchmarkPeerNodeID(b *testing.B) {
	serverCfg, clientCfg := testMembranePair(b)
	serverConn, _ := doHandshakePair(b, serverCfg, clientCfg)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = PeerNodeID(serverConn)
	}
}

func BenchmarkPeerPubKey(b *testing.B) {
	serverCfg, clientCfg := testMembranePair(b)
	serverConn, _ := doHandshakePair(b, serverCfg, clientCfg)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = PeerPubKey(serverConn)
	}
}
