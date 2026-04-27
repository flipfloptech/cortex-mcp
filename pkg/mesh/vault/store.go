// Package vault provides encrypted-at-rest credential storage and
// peer-to-peer credential delegation for the mesh.
//
// The vault stores SSH and TLS credentials keyed by host pattern.
// Patterns use ClusterShell-compatible nodeset syntax:
// globs ("10.0.1.*"), bracket ranges ("node[1-100]"), lists
// ("mds[01-16],oss[01-72]"), and set operations ("node[1-10]!node[5-7]").
//
// Credentials are encrypted using AES-256-GCM with a key derived from
// the node's Ed25519 private key via HKDF. The vault serializes to a
// single opaque sealed blob for persistence.
//
// The vault is NOT a general-purpose secret store — it holds only what
// the mesh needs to authenticate and deploy.
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/flipfloptech/cortex-mcp/pkg/mesh/nodeset"
	"golang.org/x/crypto/hkdf"
)

// Credential types.
const (
	CredSSHKey      = iota + 1 // SSH private key authentication
	CredSSHPassword            // SSH password authentication
	CredTLSCert                // TLS certificate + key pair
)

// Credential holds authentication material for a host pattern.
type Credential struct {
	Type        int    `json:"type"`
	Username    string `json:"username,omitempty"`
	Password    string `json:"password,omitempty"`
	PrivateKey  []byte `json:"private_key,omitempty"`
	Certificate []byte `json:"certificate,omitempty"`
}

// entry pairs a host pattern with its credential.
type entry struct {
	Pattern    string     `json:"pattern"`
	Credential Credential `json:"credential"`
}

// Vault is an encrypted credential store keyed by host pattern.
// Thread-safe — all methods can be called concurrently.
type Vault struct {
	mu         sync.RWMutex
	entries    []entry // ordered: most recently added last
	aead       cipher.AEAD
	privateKey ed25519.PrivateKey // stored for credential delegation sealing
}

// New creates a new empty vault. The AES-256-GCM encryption key is
// derived from the node's Ed25519 private key via HKDF-SHA256.
func New(privateKey ed25519.PrivateKey) (*Vault, error) {
	if privateKey == nil {
		return nil, fmt.Errorf("vault: private key is required")
	}

	aead, err := deriveAEAD(privateKey)
	if err != nil {
		return nil, fmt.Errorf("vault: derive key: %w", err)
	}

	return &Vault{
		entries:    make([]entry, 0),
		aead:       aead,
		privateKey: privateKey,
	}, nil
}

// Store adds or updates a credential for the given host pattern.
// Patterns support ClusterShell nodeset syntax:
// globs ("10.0.1.*"), ranges ("node[1-100]"), and set operations.
func (v *Vault) Store(pattern string, cred Credential) {
	v.mu.Lock()
	defer v.mu.Unlock()

	// Update if pattern exists.
	for i, e := range v.entries {
		if e.Pattern == pattern {
			v.entries[i].Credential = cred
			return
		}
	}

	v.entries = append(v.entries, entry{Pattern: pattern, Credential: cred})
}

// Match finds the best-matching credential for a hostname.
// The most specific pattern wins (exact > partial glob > wildcard).
// Returns false if no pattern matches.
func (v *Vault) Match(hostname string) (Credential, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()

	var bestCred Credential
	bestSpecificity := -1 << 31 // math.MinInt32 — any match beats this
	found := false

	for _, e := range v.entries {
		matched, err := nodeset.Match(e.Pattern, hostname)
		if err != nil || !matched {
			continue
		}

		spec := patternSpecificity(e.Pattern)
		if spec > bestSpecificity {
			bestSpecificity = spec
			bestCred = e.Credential
			found = true
		}
	}

	return bestCred, found
}

// AllPatterns returns all registered host patterns.
func (v *Vault) AllPatterns() []string {
	v.mu.RLock()
	defer v.mu.RUnlock()

	patterns := make([]string, len(v.entries))
	for i, e := range v.entries {
		patterns[i] = e.Pattern
	}
	return patterns
}

// Seal encrypts the vault contents into a single opaque byte slice.
// The sealed blob can be persisted to disk or transmitted.
func (v *Vault) Seal() ([]byte, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()

	plaintext, err := json.Marshal(v.entries)
	if err != nil {
		return nil, fmt.Errorf("vault: marshal: %w", err)
	}

	// AES-256-GCM: nonce + ciphertext.
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("vault: generate nonce: %w", err)
	}

	sealed := v.aead.Seal(nonce, nonce, plaintext, nil)
	return sealed, nil
}

// Unseal decrypts a sealed vault blob using the provided private key.
func Unseal(privateKey ed25519.PrivateKey, sealed []byte) (*Vault, error) {
	aead, err := deriveAEAD(privateKey)
	if err != nil {
		return nil, fmt.Errorf("vault: derive key: %w", err)
	}

	nonceSize := aead.NonceSize()
	if len(sealed) < nonceSize {
		return nil, fmt.Errorf("vault: sealed data too short")
	}

	nonce := sealed[:nonceSize]
	ciphertext := sealed[nonceSize:]

	plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("vault: unseal: %w", err)
	}

	var entries []entry
	if err := json.Unmarshal(plaintext, &entries); err != nil {
		return nil, fmt.Errorf("vault: unmarshal: %w", err)
	}

	return &Vault{
		entries:    entries,
		aead:       aead,
		privateKey: privateKey,
	}, nil
}

// --- Internal ---

// deriveAEAD creates an AES-256-GCM AEAD from an Ed25519 private key.
// Uses HKDF-SHA256 with a fixed salt and info string.
func deriveAEAD(privateKey ed25519.PrivateKey) (cipher.AEAD, error) {
	// Use the seed (first 32 bytes) of the Ed25519 private key.
	seed := privateKey.Seed()

	// HKDF: extract + expand.
	salt := []byte("cortex-mcp-vault-v1")
	info := []byte("aes-256-gcm")
	hkdfReader := hkdf.New(sha256.New, seed, salt, info)

	key := make([]byte, 32) // AES-256
	if _, err := io.ReadFull(hkdfReader, key); err != nil {
		return nil, fmt.Errorf("hkdf: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes: %w", err)
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}

	return aead, nil
}

// patternSpecificity scores a nodeset pattern: higher = more specific.
// Exact match (no wildcards/brackets) > bracket range > partial glob > full wildcard.
func patternSpecificity(pattern string) int {
	hasWildcard := false
	hasBracket := false

	for _, c := range pattern {
		switch c {
		case '*', '?':
			hasWildcard = true
		case '[':
			hasBracket = true
		}
	}

	switch {
	case !hasWildcard && !hasBracket:
		// Exact match — highest specificity.
		return 10000 + len(pattern)
	case hasBracket && !hasWildcard:
		// Bracket range — more specific than glob.
		return 5000 + len(pattern)
	default:
		// Contains glob characters — least specific.
		// Longer patterns with fewer wildcards are slightly more specific.
		score := len(pattern) * 10
		for _, c := range pattern {
			if c == '*' || c == '?' {
				score -= 100
			}
		}
		return score
	}
}
