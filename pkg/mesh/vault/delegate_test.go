package vault

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

// --- Credential delegation protocol (nonce-bound) ---
//
// SealGrant / UnsealGrant (the non-replay-protected pair) were removed
// in P0-3. Every path through the delegation protocol now binds the
// sealed grant to a per-request nonce and a granter-enforced clock-skew
// window. These tests exercise the replacement API via HandleRequest
// and UnsealGrantWithNonce.

// makeRequest is a test helper that builds a well-formed
// CredentialRequest with a fresh 32-byte nonce and a current timestamp.
func makeRequest(t *testing.T, hostPattern string, reqPub ed25519.PublicKey) CredentialRequest {
	t.Helper()
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("generate nonce: %v", err)
	}
	return CredentialRequest{
		HostPattern:     hostPattern,
		RequesterPubKey: reqPub,
		Nonce:           nonce,
		RequestedAt:     time.Now().UnixNano(),
	}
}

func TestDelegation_CredentialRequest_Fields(t *testing.T) {
	t.Parallel()

	reqPub, _, _ := ed25519.GenerateKey(rand.Reader)
	req := makeRequest(t, "10.0.1.*", reqPub)

	if req.HostPattern != "10.0.1.*" {
		t.Fatalf("pattern = %q, want %q", req.HostPattern, "10.0.1.*")
	}
	if len(req.RequesterPubKey) != ed25519.PublicKeySize {
		t.Fatalf("pubkey size = %d, want %d", len(req.RequesterPubKey), ed25519.PublicKeySize)
	}
	if len(req.Nonce) != 32 {
		t.Fatalf("nonce size = %d, want 32", len(req.Nonce))
	}
	if req.RequestedAt == 0 {
		t.Fatal("RequestedAt must be set")
	}
}

func TestDelegation_HandleRequest_Match(t *testing.T) {
	t.Parallel()

	reqPub, reqPriv, _ := ed25519.GenerateKey(rand.Reader)
	_, granterPriv, _ := ed25519.GenerateKey(rand.Reader)
	granterPub := granterPriv.Public().(ed25519.PublicKey)

	granterVault, _ := New(granterPriv)
	granterVault.Store("10.0.1.*", Credential{
		Type:       CredSSHKey,
		Username:   "deploy",
		PrivateKey: []byte("the-key"),
	})

	req := makeRequest(t, "10.0.1.5", reqPub)

	grant, err := granterVault.HandleRequest(req)
	if err != nil {
		t.Fatalf("HandleRequest: %v", err)
	}
	if grant == nil {
		t.Fatal("expected grant, got nil")
	}

	// Requester unseals with its private key, the granter's known public
	// key, and the same nonce it sent.
	payload, err := UnsealGrantWithNonce(
		grant.SealedCredential, reqPriv, granterPub, req.Nonce,
	)
	if err != nil {
		t.Fatalf("UnsealGrantWithNonce: %v", err)
	}

	if payload.Credential.Username != "deploy" {
		t.Fatalf("username = %q, want %q", payload.Credential.Username, "deploy")
	}
	if string(payload.Credential.PrivateKey) != "the-key" {
		t.Fatalf("key = %q, want %q", payload.Credential.PrivateKey, "the-key")
	}
}

func TestDelegation_HandleRequest_NoMatch(t *testing.T) {
	t.Parallel()

	reqPub, _, _ := ed25519.GenerateKey(rand.Reader)
	_, granterPriv, _ := ed25519.GenerateKey(rand.Reader)

	granterVault, _ := New(granterPriv)
	// No credentials stored.

	req := makeRequest(t, "10.0.1.5", reqPub)

	grant, err := granterVault.HandleRequest(req)
	if err != nil {
		t.Fatalf("HandleRequest: %v", err)
	}
	if grant != nil {
		t.Fatal("expected nil grant for no match")
	}
}

// TestDelegation_HandleRequest_MissingNonce verifies that the granter
// rejects a request without a nonce. Without a nonce, the sealed grant
// cannot be bound to this specific request — replay protection would
// silently degrade.
func TestDelegation_HandleRequest_MissingNonce(t *testing.T) {
	t.Parallel()

	reqPub, _, _ := ed25519.GenerateKey(rand.Reader)
	_, granterPriv, _ := ed25519.GenerateKey(rand.Reader)

	granterVault, _ := New(granterPriv)
	granterVault.Store("10.0.1.*", Credential{
		Type:     CredSSHKey,
		Username: "deploy",
	})

	req := CredentialRequest{
		HostPattern:     "10.0.1.5",
		RequesterPubKey: reqPub,
		Nonce:           nil, // malformed
		RequestedAt:     time.Now().UnixNano(),
	}

	grant, err := granterVault.HandleRequest(req)
	if err == nil {
		t.Fatal("HandleRequest must reject request without nonce")
	}
	if grant != nil {
		t.Fatal("no grant should be issued for an invalid request")
	}
}

// TestDelegation_HandleRequest_ShortNonce verifies the minimum-length
// guard. A short nonce is cryptographically weak and must be refused.
func TestDelegation_HandleRequest_ShortNonce(t *testing.T) {
	t.Parallel()

	reqPub, _, _ := ed25519.GenerateKey(rand.Reader)
	_, granterPriv, _ := ed25519.GenerateKey(rand.Reader)

	granterVault, _ := New(granterPriv)
	granterVault.Store("10.0.1.*", Credential{
		Type:     CredSSHKey,
		Username: "deploy",
	})

	req := CredentialRequest{
		HostPattern:     "10.0.1.5",
		RequesterPubKey: reqPub,
		Nonce:           []byte{0x01, 0x02, 0x03}, // far too short
		RequestedAt:     time.Now().UnixNano(),
	}

	grant, err := granterVault.HandleRequest(req)
	if err == nil {
		t.Fatal("HandleRequest must reject request with short nonce")
	}
	if grant != nil {
		t.Fatal("no grant should be issued for a short-nonce request")
	}
}

// TestDelegation_HandleRequest_StaleTimestamp rejects a request whose
// RequestedAt is older than the configured skew window. This defends
// against replay of an old (captured) request frame.
func TestDelegation_HandleRequest_StaleTimestamp(t *testing.T) {
	t.Parallel()

	reqPub, _, _ := ed25519.GenerateKey(rand.Reader)
	_, granterPriv, _ := ed25519.GenerateKey(rand.Reader)

	granterVault, _ := New(granterPriv)
	granterVault.Store("10.0.1.*", Credential{
		Type:     CredSSHKey,
		Username: "deploy",
	})

	// Stale: 1 hour in the past — well beyond the default skew window.
	staleAt := time.Now().Add(-1 * time.Hour).UnixNano()
	nonce := make([]byte, 32)
	_, _ = rand.Read(nonce)

	req := CredentialRequest{
		HostPattern:     "10.0.1.5",
		RequesterPubKey: reqPub,
		Nonce:           nonce,
		RequestedAt:     staleAt,
	}

	grant, err := granterVault.HandleRequest(req)
	if err == nil {
		t.Fatal("HandleRequest must reject stale request")
	}
	if grant != nil {
		t.Fatal("no grant should be issued for a stale request")
	}
}

// TestDelegation_HandleRequest_FutureTimestamp rejects a request whose
// RequestedAt is in the far future — defends against a manipulated or
// badly-synchronized requester.
func TestDelegation_HandleRequest_FutureTimestamp(t *testing.T) {
	t.Parallel()

	reqPub, _, _ := ed25519.GenerateKey(rand.Reader)
	_, granterPriv, _ := ed25519.GenerateKey(rand.Reader)

	granterVault, _ := New(granterPriv)
	granterVault.Store("10.0.1.*", Credential{
		Type:     CredSSHKey,
		Username: "deploy",
	})

	futureAt := time.Now().Add(1 * time.Hour).UnixNano()
	nonce := make([]byte, 32)
	_, _ = rand.Read(nonce)

	req := CredentialRequest{
		HostPattern:     "10.0.1.5",
		RequesterPubKey: reqPub,
		Nonce:           nonce,
		RequestedAt:     futureAt,
	}

	grant, err := granterVault.HandleRequest(req)
	if err == nil {
		t.Fatal("HandleRequest must reject future-dated request")
	}
	if grant != nil {
		t.Fatal("no grant should be issued for a future-dated request")
	}
}

// TestDelegation_HandleRequest_NonceBindsGrant verifies that a grant
// issued for request-nonce A cannot be unsealed by presenting nonce B.
// This is the core replay-binding property: every sealed grant is
// committed to the exact request that solicited it.
func TestDelegation_HandleRequest_NonceBindsGrant(t *testing.T) {
	t.Parallel()

	reqPub, reqPriv, _ := ed25519.GenerateKey(rand.Reader)
	_, granterPriv, _ := ed25519.GenerateKey(rand.Reader)
	granterPub := granterPriv.Public().(ed25519.PublicKey)

	granterVault, _ := New(granterPriv)
	granterVault.Store("10.0.1.*", Credential{
		Type:     CredSSHKey,
		Username: "deploy",
	})

	req := makeRequest(t, "10.0.1.5", reqPub)

	grant, err := granterVault.HandleRequest(req)
	if err != nil {
		t.Fatalf("HandleRequest: %v", err)
	}
	if grant == nil {
		t.Fatal("expected grant")
	}

	// Correct nonce — succeeds.
	if _, err := UnsealGrantWithNonce(
		grant.SealedCredential, reqPriv, granterPub, req.Nonce,
	); err != nil {
		t.Fatalf("unseal with original nonce should succeed: %v", err)
	}

	// Different nonce — must be rejected.
	otherNonce := make([]byte, 32)
	_, _ = rand.Read(otherNonce)
	if _, err := UnsealGrantWithNonce(
		grant.SealedCredential, reqPriv, granterPub, otherNonce,
	); err == nil {
		t.Fatal("unseal with a different nonce must fail (replay binding broken)")
	}
}

// TestDelegation_FullRoundtrip simulates Node A requesting credentials
// from Node B end-to-end against the nonce-bound API.
func TestDelegation_FullRoundtrip(t *testing.T) {
	t.Parallel()

	pubA, privA, _ := ed25519.GenerateKey(rand.Reader)
	_, privB, _ := ed25519.GenerateKey(rand.Reader)
	pubB := privB.Public().(ed25519.PublicKey)

	vaultB, _ := New(privB)
	vaultB.Store("oss-*.internal", Credential{
		Type:       CredSSHKey,
		Username:   "mesh",
		PrivateKey: []byte("ssh-key-for-oss"),
	})

	req := makeRequest(t, "oss-01.internal", pubA)

	grant, err := vaultB.HandleRequest(req)
	if err != nil {
		t.Fatalf("B HandleRequest: %v", err)
	}
	if grant == nil {
		t.Fatal("B should have granted credentials")
	}

	payload, err := UnsealGrantWithNonce(
		grant.SealedCredential, privA, pubB, req.Nonce,
	)
	if err != nil {
		t.Fatalf("A unseal: %v", err)
	}

	// A stores the delegated credential in its own vault.
	vaultA, _ := New(privA)
	vaultA.Store("oss-01.internal", payload.Credential)

	got, ok := vaultA.Match("oss-01.internal")
	if !ok {
		t.Fatal("A should now have the credential")
	}
	if got.Username != "mesh" {
		t.Fatalf("username = %q, want %q", got.Username, "mesh")
	}
}

// TestDelegation_GrantCarriesExpiry verifies that the sealed payload
// the granter issues includes IssuedAt and ExpiresAt. UnsealGrantWithNonce
// already enforces expiry in the guard tests; this test pins the
// granter's side of the contract (TTL must actually be applied).
func TestDelegation_GrantCarriesExpiry(t *testing.T) {
	t.Parallel()

	reqPub, reqPriv, _ := ed25519.GenerateKey(rand.Reader)
	_, granterPriv, _ := ed25519.GenerateKey(rand.Reader)
	granterPub := granterPriv.Public().(ed25519.PublicKey)

	granterVault, _ := New(granterPriv)
	granterVault.Store("10.0.1.*", Credential{
		Type:     CredSSHKey,
		Username: "deploy",
	})

	req := makeRequest(t, "10.0.1.5", reqPub)

	grant, err := granterVault.HandleRequest(req)
	if err != nil {
		t.Fatalf("HandleRequest: %v", err)
	}

	payload, err := UnsealGrantWithNonce(
		grant.SealedCredential, reqPriv, granterPub, req.Nonce,
	)
	if err != nil {
		t.Fatalf("UnsealGrantWithNonce: %v", err)
	}

	if payload.IssuedAt == 0 {
		t.Error("payload.IssuedAt must be set by the granter")
	}
	if payload.ExpiresAt <= payload.IssuedAt {
		t.Errorf("payload.ExpiresAt (%d) must be after IssuedAt (%d)",
			payload.ExpiresAt, payload.IssuedAt)
	}
}

func BenchmarkSealGrantWithNonce(b *testing.B) {
	_, reqPriv, _ := ed25519.GenerateKey(rand.Reader)
	reqPub := reqPriv.Public().(ed25519.PublicKey)
	_, granterPriv, _ := ed25519.GenerateKey(rand.Reader)

	cred := Credential{Type: CredSSHKey, Username: "deploy"}
	nonce := make([]byte, 32)
	rand.Read(nonce)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = SealGrantWithNonce(cred, reqPub, granterPriv, nonce, time.Minute)
	}
}

func BenchmarkUnsealGrantWithNonce(b *testing.B) {
	_, reqPriv, _ := ed25519.GenerateKey(rand.Reader)
	reqPub := reqPriv.Public().(ed25519.PublicKey)
	_, granterPriv, _ := ed25519.GenerateKey(rand.Reader)
	granterPub := granterPriv.Public().(ed25519.PublicKey)

	cred := Credential{Type: CredSSHKey, Username: "deploy"}
	nonce := make([]byte, 32)
	rand.Read(nonce)

	sealed, _ := SealGrantWithNonce(cred, reqPub, granterPriv, nonce, time.Minute)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = UnsealGrantWithNonce(sealed, reqPriv, granterPub, nonce)
	}
}

func BenchmarkHandleRequest(b *testing.B) {
	reqPub, _, _ := ed25519.GenerateKey(rand.Reader)
	_, granterPriv, _ := ed25519.GenerateKey(rand.Reader)

	granterVault, _ := New(granterPriv)
	granterVault.Store("10.0.1.*", Credential{Type: CredSSHKey, Username: "deploy"})

	nonce := make([]byte, 32)
	rand.Read(nonce)

	req := CredentialRequest{
		HostPattern:     "10.0.1.5",
		RequesterPubKey: reqPub,
		Nonce:           nonce,
		RequestedAt:     time.Now().UnixNano(),
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = granterVault.HandleRequest(req)
	}
}
