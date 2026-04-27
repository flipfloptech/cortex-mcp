package proto

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"

	pb "google.golang.org/protobuf/proto"
)

// --- Pair frame constants ---

// PairTokenSize is the size of the correlation token in bytes.
// 32 bytes = 256 bits of entropy — infeasible to brute-force.
const PairTokenSize = 32

// PairRoleControl identifies the control plane connection.
const PairRoleControl = "control"

// PairRoleData identifies the data plane connection.
const PairRoleData = "data"

// ValidatePairRole returns true if the role is a valid pair role.
func ValidatePairRole(role string) bool {
	return role == PairRoleControl || role == PairRoleData
}

// GeneratePairToken generates a cryptographically random token for
// connection pairing. Uses crypto/rand for security.
func GeneratePairToken() ([]byte, error) {
	token := make([]byte, PairTokenSize)
	if _, err := rand.Read(token); err != nil {
		return nil, fmt.Errorf("proto: generate pair token: %w", err)
	}
	return token, nil
}

// WritePairFrame writes a length-prefixed PairFrame to a writer.
// Uses the same 4-byte big-endian framing as control frames.
func WritePairFrame(w io.Writer, frame *PairFrame) error {
	data, err := pb.Marshal(frame)
	if err != nil {
		return fmt.Errorf("proto: marshal pair frame: %w", err)
	}

	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	if _, err := w.Write(header[:]); err != nil {
		return fmt.Errorf("proto: write pair header: %w", err)
	}

	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("proto: write pair payload: %w", err)
	}

	return nil
}

// ReadPairFrame reads a length-prefixed PairFrame from a reader.
// PairFrames are small (< 100 bytes), so no pooling or budget needed.
// A conservative 1KB limit prevents abuse.
func ReadPairFrame(r io.Reader) (*PairFrame, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, fmt.Errorf("proto: read pair header: %w", err)
	}

	length := binary.BigEndian.Uint32(header[:])

	// PairFrames are tiny. A 1KB limit is generous.
	const maxPairFrameSize = 1024
	if length > maxPairFrameSize {
		_, _ = io.CopyN(io.Discard, r, int64(length))
		return nil, fmt.Errorf("proto: pair frame too large: %d bytes (max %d)", length, maxPairFrameSize)
	}

	data := make([]byte, length)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, fmt.Errorf("proto: read pair payload: %w", err)
	}

	frame := &PairFrame{}
	if err := pb.Unmarshal(data, frame); err != nil {
		return nil, fmt.Errorf("proto: unmarshal pair frame: %w", err)
	}

	return frame, nil
}
