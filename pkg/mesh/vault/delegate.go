package vault

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"golang.org/x/crypto/nacl/box"
)

// CredentialRequest is sent point-to-point to a peer when this node
// needs credentials for a host it doesn't have access to.
//
// Never broadcast or forwarded — strictly peer-to-peer.
//
// Replay protection (P0-3): every request carries a per-request nonce
// and a requester-supplied timestamp. HandleRequest binds the sealed
// grant to the nonce (see SealGrantWithNonce) and rejects requests
// outside a clock-skew window so captured-and-replayed request frames
// cannot be reused past credential rotation.
type CredentialRequest struct {
	HostPattern     string            `json:"host_pattern"`
	RequesterPubKey ed25519.PublicKey `json:"requester_pub_key"`

	// Nonce is a per-request random value, 32 bytes from crypto/rand.
	// Embedded in the sealed payload so the requester can verify the
	// grant was issued for this specific request. Required.
	Nonce []byte `json:"nonce"`

	// RequestedAt is the UnixNano timestamp when the requester built
	// this request. The granter rejects requests outside a configurable
	// clock-skew window (DelegationSkewWindow). Required.
	RequestedAt int64 `json:"requested_at"`
}

// CredentialGrant is the response to a CredentialRequest.
// The credential is sealed (NaCl box) to the requester's public key —
// only the requester can decrypt it, even though the channel is already
// mTLS-protected. Belt-and-suspenders.
//
// The sealed bytes are a SealedPayload produced by SealGrantWithNonce;
// the requester opens them with UnsealGrantWithNonce, passing the same
// nonce they sent in the originating CredentialRequest.
type CredentialGrant struct {
	SealedCredential []byte `json:"sealed_credential"`
}

// Delegation-protocol tunables. Expressed as variables (not constants)
// so deployments with unusual clock conditions (e.g. air-gapped sites
// running without NTP) can widen the window without a fork. The defaults
// are conservative and apply regardless of mesh size — they are time
// budgets, not node-count caps.
var (
	// DelegationSkewWindow is the maximum absolute difference between
	// the granter's wall clock and CredentialRequest.RequestedAt. A
	// request outside this window is rejected without issuing a grant,
	// defending against replay of a captured request frame.
	DelegationSkewWindow = 2 * time.Minute

	// DelegationGrantTTL is the lifetime of a sealed grant after issue.
	// UnsealGrantWithNonce refuses to open a grant past this TTL. Kept
	// short: a grant is meant to travel one hop over an already-mTLS
	// channel, not to be warehoused.
	DelegationGrantTTL = 5 * time.Minute

	// DelegationMinNonceLen is the minimum acceptable CredentialRequest
	// nonce length. 16 bytes (128 bits) is the floor for collision
	// resistance; requesters should use 32.
	DelegationMinNonceLen = 16
)

// SealedPayload wraps a credential with replay protection metadata.
// This is the actual plaintext structure sealed inside the NaCl box.
type SealedPayload struct {
	Credential Credential `json:"credential"`
	Nonce      []byte     `json:"nonce"`      // Request-binding nonce from CredentialRequest
	IssuedAt   int64      `json:"issued_at"`  // UnixNano timestamp
	ExpiresAt  int64      `json:"expires_at"` // UnixNano timestamp
}

// SealGrantWithNonce encrypts a credential with replay protection.
// The sealed payload includes the request nonce, issue time, and expiry.
// This prevents replay attacks — the receiver must present the same nonce
// to unseal, and the grant expires after the specified TTL.
func SealGrantWithNonce(cred Credential, recipientPub ed25519.PublicKey, senderPriv ed25519.PrivateKey, requestNonce []byte, ttl time.Duration) ([]byte, error) {
	now := time.Now()
	payload := SealedPayload{
		Credential: cred,
		Nonce:      requestNonce,
		IssuedAt:   now.UnixNano(),
		ExpiresAt:  now.Add(ttl).UnixNano(),
	}

	plaintext, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("vault: marshal payload: %w", err)
	}

	recipientCurve, err := ed25519PubToCurve25519(recipientPub)
	if err != nil {
		return nil, fmt.Errorf("vault: convert recipient key: %w", err)
	}

	senderCurve, err := ed25519PrivToCurve25519(senderPriv)
	if err != nil {
		return nil, fmt.Errorf("vault: convert sender key: %w", err)
	}

	var nonce [24]byte
	if _, err := io.ReadFull(rand.Reader, nonce[:]); err != nil {
		return nil, fmt.Errorf("vault: generate nonce: %w", err)
	}

	sealed := box.Seal(nil, plaintext, &nonce, recipientCurve, senderCurve)

	result := make([]byte, 0, 24+len(sealed))
	result = append(result, nonce[:]...)
	result = append(result, sealed...)

	return result, nil
}

// UnsealGrantWithNonce decrypts and validates a replay-protected grant.
// Verifies:
//  1. NaCl box decryption succeeds (authentication)
//  2. The request nonce matches (replay binding)
//  3. The grant has not expired (freshness)
func UnsealGrantWithNonce(sealed []byte, recipientPriv ed25519.PrivateKey, senderPub ed25519.PublicKey, expectedNonce []byte) (*SealedPayload, error) {
	if len(sealed) < 24+box.Overhead {
		return nil, fmt.Errorf("vault: sealed data too short")
	}

	var nonce [24]byte
	copy(nonce[:], sealed[:24])

	ciphertext := sealed[24:]

	recipientCurve, err := ed25519PrivToCurve25519(recipientPriv)
	if err != nil {
		return nil, fmt.Errorf("vault: convert recipient key: %w", err)
	}

	senderCurve, err := ed25519PubToCurve25519(senderPub)
	if err != nil {
		return nil, fmt.Errorf("vault: convert sender key: %w", err)
	}

	plaintext, ok := box.Open(nil, ciphertext, &nonce, senderCurve, recipientCurve)
	if !ok {
		return nil, fmt.Errorf("vault: unseal failed (wrong key or corrupted)")
	}

	var payload SealedPayload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return nil, fmt.Errorf("vault: unmarshal payload: %w", err)
	}

	// Verify nonce binding.
	if !nonceEqual(payload.Nonce, expectedNonce) {
		return nil, fmt.Errorf("vault: nonce mismatch (possible replay attack)")
	}

	// Verify expiry.
	if time.Now().UnixNano() > payload.ExpiresAt {
		return nil, fmt.Errorf("vault: grant expired at %d", payload.ExpiresAt)
	}

	return &payload, nil
}

// nonceEqual compares two nonces in constant time.
func nonceEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var result byte
	for i := range a {
		result |= a[i] ^ b[i]
	}
	return result == 0
}

// validateRequest enforces the replay-protection preconditions on an
// incoming CredentialRequest before any credential material is touched.
// Returns nil if the request is acceptable, or a descriptive error that
// HandleRequest propagates to the caller. Callers should treat any
// error as "silently deny" at the protocol layer — the error text is
// for logs, not the peer.
func validateRequest(req CredentialRequest) error {
	if len(req.RequesterPubKey) != ed25519.PublicKeySize {
		return fmt.Errorf("vault: requester public key wrong size: %d", len(req.RequesterPubKey))
	}
	if len(req.Nonce) < DelegationMinNonceLen {
		return fmt.Errorf("vault: request nonce too short: %d bytes (min %d)",
			len(req.Nonce), DelegationMinNonceLen)
	}
	if req.RequestedAt == 0 {
		return fmt.Errorf("vault: request missing RequestedAt timestamp")
	}

	// Clock-skew window: |now - RequestedAt| must be ≤ DelegationSkewWindow.
	// Covers both stale (replay of old request) and future-dated (bad
	// clock or manipulation) cases.
	delta := time.Now().UnixNano() - req.RequestedAt
	if delta < 0 {
		delta = -delta
	}
	if time.Duration(delta) > DelegationSkewWindow {
		return fmt.Errorf("vault: request timestamp outside skew window: skew=%s (max %s)",
			time.Duration(delta), DelegationSkewWindow)
	}
	return nil
}

// HandleRequest processes a CredentialRequest against the local vault.
// Returns a CredentialGrant sealed to the requester's public key,
// authenticated by this vault's long-term identity, and bound to the
// request's nonce + expiry — consumable only by UnsealGrantWithNonce
// with the same nonce.
//
// Returns (nil, error) when the request is malformed or outside the
// clock-skew window. Returns (nil, nil) when the request is well-formed
// but no matching credential is stored.
func (v *Vault) HandleRequest(req CredentialRequest) (*CredentialGrant, error) {
	if err := validateRequest(req); err != nil {
		return nil, err
	}

	cred, ok := v.Match(req.HostPattern)
	if !ok {
		return nil, nil // no matching credentials
	}

	sealed, err := SealGrantWithNonce(cred, req.RequesterPubKey, v.privateKey, req.Nonce, DelegationGrantTTL)
	if err != nil {
		return nil, fmt.Errorf("vault: seal grant: %w", err)
	}

	return &CredentialGrant{SealedCredential: sealed}, nil
}

// --- Ed25519 → Curve25519 conversion ---

// ed25519PubToCurve25519 converts an Ed25519 public key to Curve25519.
// Uses the standard conversion defined in RFC 7748.
func ed25519PubToCurve25519(pub ed25519.PublicKey) (*[32]byte, error) {
	if len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid public key size: %d", len(pub))
	}

	// Use x/crypto's conversion.
	var curve [32]byte
	edwardsToMontgomeryPub(&curve, pub)
	return &curve, nil
}

// ed25519PrivToCurve25519 converts an Ed25519 private key to Curve25519.
func ed25519PrivToCurve25519(priv ed25519.PrivateKey) (*[32]byte, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid private key size: %d", len(priv))
	}

	var curve [32]byte
	edwardsToMontgomeryPriv(&curve, priv)
	return &curve, nil
}
