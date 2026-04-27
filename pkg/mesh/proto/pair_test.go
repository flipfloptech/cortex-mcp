package proto

import (
	"bytes"
	"testing"
)

// TestPairFrame_Roundtrip verifies that PairFrame can be serialized
// and deserialized through the framing codec.
func TestPairFrame_Roundtrip(t *testing.T) {
	t.Parallel()

	token := make([]byte, PairTokenSize)
	for i := range token {
		token[i] = byte(i)
	}

	original := &PairFrame{
		Token: token,
		Role:  PairRoleControl,
	}

	var buf bytes.Buffer
	if err := WritePairFrame(&buf, original); err != nil {
		t.Fatalf("write: %v", err)
	}

	decoded, err := ReadPairFrame(&buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if !bytes.Equal(decoded.Token, original.Token) {
		t.Fatalf("token mismatch: got %x, want %x", decoded.Token, original.Token)
	}
	if decoded.Role != original.Role {
		t.Fatalf("role mismatch: got %q, want %q", decoded.Role, original.Role)
	}
}

// TestPairFrame_DataRole verifies the data role roundtrips correctly.
func TestPairFrame_DataRole(t *testing.T) {
	t.Parallel()

	original := &PairFrame{
		Token: make([]byte, PairTokenSize),
		Role:  PairRoleData,
	}

	var buf bytes.Buffer
	if err := WritePairFrame(&buf, original); err != nil {
		t.Fatalf("write: %v", err)
	}

	decoded, err := ReadPairFrame(&buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if decoded.Role != PairRoleData {
		t.Fatalf("role: got %q, want %q", decoded.Role, PairRoleData)
	}
}

// TestPairFrame_Constants verifies security constants are correct.
func TestPairFrame_Constants(t *testing.T) {
	t.Parallel()

	if PairTokenSize != 32 {
		t.Fatalf("PairTokenSize = %d, want 32 (256 bits)", PairTokenSize)
	}
	if PairRoleControl != "control" {
		t.Fatalf("PairRoleControl = %q", PairRoleControl)
	}
	if PairRoleData != "data" {
		t.Fatalf("PairRoleData = %q", PairRoleData)
	}
}

// TestPairFrame_ValidateRole verifies that ValidatePairRole rejects
// unknown roles.
func TestPairFrame_ValidateRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		role  string
		valid bool
	}{
		{PairRoleControl, true},
		{PairRoleData, true},
		{"", false},
		{"unknown", false},
		{"CONTROL", false}, // case-sensitive
	}

	for _, tt := range tests {
		if got := ValidatePairRole(tt.role); got != tt.valid {
			t.Errorf("ValidatePairRole(%q) = %v, want %v", tt.role, got, tt.valid)
		}
	}
}

// TestGeneratePairToken verifies that tokens are the correct size
// and appear random (different each call).
func TestGeneratePairToken(t *testing.T) {
	t.Parallel()

	token1, err := GeneratePairToken()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(token1) != PairTokenSize {
		t.Fatalf("token size = %d, want %d", len(token1), PairTokenSize)
	}

	token2, err := GeneratePairToken()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	// Two tokens should be different (probability of collision: 2^-256).
	if bytes.Equal(token1, token2) {
		t.Fatal("two generated tokens are identical — broken RNG")
	}
}
