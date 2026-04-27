package vault

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

// --- H-2: Credential replay protection ---

func TestSealGrantWithNonce_RoundTrip(t *testing.T) {
	t.Parallel()

	recipientPub, recipientPriv, _ := ed25519.GenerateKey(rand.Reader)
	senderPub, senderPriv, _ := ed25519.GenerateKey(rand.Reader)

	cred := Credential{
		Type:     CredSSHKey,
		Username: "deploy",
	}

	nonce := make([]byte, 32)
	rand.Read(nonce)

	sealed, err := SealGrantWithNonce(cred, recipientPub, senderPriv, nonce, 5*time.Minute)
	if err != nil {
		t.Fatalf("SealGrantWithNonce: %v", err)
	}

	got, err := UnsealGrantWithNonce(sealed, recipientPriv, senderPub, nonce)
	if err != nil {
		t.Fatalf("UnsealGrantWithNonce: %v", err)
	}

	if got.Credential.Username != "deploy" {
		t.Fatalf("credential username = %q, want %q", got.Credential.Username, "deploy")
	}
}

func TestSealGrantWithNonce_WrongNonce(t *testing.T) {
	t.Parallel()

	recipientPub, recipientPriv, _ := ed25519.GenerateKey(rand.Reader)
	senderPub, senderPriv, _ := ed25519.GenerateKey(rand.Reader)

	cred := Credential{
		Type:     CredSSHKey,
		Username: "deploy",
	}

	nonce := make([]byte, 32)
	rand.Read(nonce)

	sealed, err := SealGrantWithNonce(cred, recipientPub, senderPriv, nonce, 5*time.Minute)
	if err != nil {
		t.Fatalf("SealGrantWithNonce: %v", err)
	}

	// Use a different nonce to unseal — should fail.
	wrongNonce := make([]byte, 32)
	rand.Read(wrongNonce)

	_, err = UnsealGrantWithNonce(sealed, recipientPriv, senderPub, wrongNonce)
	if err == nil {
		t.Fatal("UnsealGrantWithNonce should fail with wrong nonce")
	}
}

func TestSealGrantWithNonce_Expired(t *testing.T) {
	t.Parallel()

	recipientPub, recipientPriv, _ := ed25519.GenerateKey(rand.Reader)
	senderPub, senderPriv, _ := ed25519.GenerateKey(rand.Reader)

	cred := Credential{
		Type:     CredSSHKey,
		Username: "deploy",
	}

	nonce := make([]byte, 32)
	rand.Read(nonce)

	// Seal with 1 nanosecond TTL — effectively already expired.
	sealed, err := SealGrantWithNonce(cred, recipientPub, senderPriv, nonce, 1*time.Nanosecond)
	if err != nil {
		t.Fatalf("SealGrantWithNonce: %v", err)
	}

	// Wait a bit to ensure expiry.
	time.Sleep(10 * time.Millisecond)

	_, err = UnsealGrantWithNonce(sealed, recipientPriv, senderPub, nonce)
	if err == nil {
		t.Fatal("UnsealGrantWithNonce should fail with expired grant")
	}
}

func TestSealGrantWithNonce_ReplayDetection(t *testing.T) {
	t.Parallel()

	recipientPub, recipientPriv, _ := ed25519.GenerateKey(rand.Reader)
	senderPub, senderPriv, _ := ed25519.GenerateKey(rand.Reader)

	cred := Credential{
		Type:     CredSSHKey,
		Username: "deploy",
	}

	nonce := make([]byte, 32)
	rand.Read(nonce)

	sealed, err := SealGrantWithNonce(cred, recipientPub, senderPriv, nonce, 5*time.Minute)
	if err != nil {
		t.Fatalf("SealGrantWithNonce: %v", err)
	}

	// First unseal: should succeed.
	_, err = UnsealGrantWithNonce(sealed, recipientPriv, senderPub, nonce)
	if err != nil {
		t.Fatalf("first unseal should succeed: %v", err)
	}

	// The same sealed bytes with a DIFFERENT nonce (simulating replay
	// where attacker doesn't know the original nonce): should fail.
	differentNonce := make([]byte, 32)
	rand.Read(differentNonce)
	_, err = UnsealGrantWithNonce(sealed, recipientPriv, senderPub, differentNonce)
	if err == nil {
		t.Fatal("replay with different nonce should fail")
	}
}
