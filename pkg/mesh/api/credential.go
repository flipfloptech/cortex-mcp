package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"time"

	pb "github.com/flipfloptech/cortex-mcp/pkg/mesh/proto"
	"github.com/flipfloptech/cortex-mcp/pkg/mesh/vault"
)

// RequestCredential requests SSH/TLS credentials for a specific host pattern from the mesh.
// It sends a CredentialRequestFrame to all connected peers and blocks until a
// CredentialGrantFrame is received or the context is canceled.
func (n *Node) RequestCredential(ctx context.Context, hostPattern string) (*vault.Credential, error) {
	if n.vault == nil {
		return nil, fmt.Errorf("api: credential delegation requires a configured Vault")
	}

	privKey, ok := n.membraneCfg.Certificate.PrivateKey.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("api: node private key is not Ed25519")
	}
	pubKey := privKey.Public().(ed25519.PublicKey)

	// Generate 32-byte nonce
	nonce := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("api: generate nonce: %w", err)
	}

	// Create protobuf request
	reqProto := &pb.CredentialRequestFrame{
		HostPattern:     hostPattern,
		RequesterPubKey: pubKey,
		Nonce:           nonce,
		RequestedAt:     time.Now().UnixNano(),
	}

	frame := &pb.ControlFrame{
		Payload: &pb.ControlFrame_CredRequest{
			CredRequest: reqProto,
		},
	}

	// Create resolution channel bound to the exact nonce
	nonceStr := string(nonce)
	resultCh := make(chan CredentialGrantResult, 1)

	n.credResolversMu.Lock()
	n.credResolvers[nonceStr] = resultCh
	n.credResolversMu.Unlock()

	defer func() {
		n.credResolversMu.Lock()
		delete(n.credResolvers, nonceStr)
		n.credResolversMu.Unlock()
	}()

	n.broadcastAll(frame)

	// Wait for a response. We loop because another concurrent request might
	// receive our grant frame, or we might receive theirs (since nonces are sealed).
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case res := <-resultCh:
			// Attempt to unseal the grant
			payload, err := vault.UnsealGrantWithNonce(
				res.Frame.SealedCredential,
				privKey,
				res.SenderPub,
				nonce,
			)
			if err == nil {
				return &payload.Credential, nil
			}
			// Unseal failed (wrong key/nonce) — likely meant for another pending
			// request. Ignore and keep waiting.
		}
	}
}
