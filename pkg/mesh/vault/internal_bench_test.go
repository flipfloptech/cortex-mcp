package vault

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

func BenchmarkEdwardsToMontgomeryPub(b *testing.B) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	var out [32]byte

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		edwardsToMontgomeryPub(&out, pub)
	}
}

func BenchmarkEdwardsToMontgomeryPriv(b *testing.B) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	var out [32]byte

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		edwardsToMontgomeryPriv(&out, priv)
	}
}

func BenchmarkEd25519PubToCurve25519(b *testing.B) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = ed25519PubToCurve25519(pub)
	}
}

func BenchmarkEd25519PrivToCurve25519(b *testing.B) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = ed25519PrivToCurve25519(priv)
	}
}

func BenchmarkNonceEqual(b *testing.B) {
	n1 := make([]byte, 32)
	n2 := make([]byte, 32)
	rand.Read(n1)
	rand.Read(n2)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = nonceEqual(n1, n2)
	}
}

func BenchmarkDeriveAEAD(b *testing.B) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = deriveAEAD(priv)
	}
}

func BenchmarkPatternSpecificity(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = patternSpecificity("node[1-100]*.prod")
	}
}

func BenchmarkValidateRequest(b *testing.B) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	nonce := make([]byte, 32)
	rand.Read(nonce)

	req := CredentialRequest{
		HostPattern:     "10.0.1.5",
		RequesterPubKey: pub,
		Nonce:           nonce,
		RequestedAt:     time.Now().UnixNano(),
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = validateRequest(req)
	}
}
