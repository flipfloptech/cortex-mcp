package vault

import (
	"crypto/ed25519"
	"crypto/sha512"
	"filippo.io/edwards25519"
)

// edwardsToMontgomeryPub converts an Ed25519 public key to Curve25519.
// This implements the standard birational mapping from Edwards to Montgomery form.
func edwardsToMontgomeryPub(out *[32]byte, pub ed25519.PublicKey) {
	// Parse the Ed25519 point.
	p, err := new(edwards25519.Point).SetBytes(pub)
	if err != nil {
		// Invalid public key — zero output.
		*out = [32]byte{}
		return
	}

	// Convert: u = (1+y)/(1-y) in the field.
	// The edwards25519 package provides BytesMontgomery for this.
	copy(out[:], p.BytesMontgomery())
}

// edwardsToMontgomeryPriv converts an Ed25519 private key to Curve25519.
// The Curve25519 private key is the clamped SHA-512 hash of the seed.
func edwardsToMontgomeryPriv(out *[32]byte, priv ed25519.PrivateKey) {
	// Hash the seed (first 32 bytes of the private key).
	h := sha512.Sum512(priv.Seed())

	// Clamp: standard Curve25519 clamping.
	h[0] &= 248
	h[31] &= 127
	h[31] |= 64

	copy(out[:], h[:32])
}
