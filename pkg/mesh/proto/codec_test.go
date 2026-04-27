package proto

import (
	"bytes"
	"testing"
)

func TestControlFrame_Gossip_Roundtrip(t *testing.T) {
	t.Parallel()

	original := &ControlFrame{
		Payload: &ControlFrame_Gossip{
			Gossip: &GossipFrame{
				FromNode: "node-a",
				Routes: map[string]float64{
					"node-b": 5.0,
					"node-c": 12.5,
				},
			},
		},
	}

	data, err := MarshalFrame(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	decoded, err := UnmarshalFrame(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	gossip := decoded.GetGossip()
	if gossip == nil {
		t.Fatal("expected gossip payload")
	}
	if gossip.FromNode != "node-a" {
		t.Fatalf("from_node = %q, want %q", gossip.FromNode, "node-a")
	}
	if gossip.Routes["node-b"] != 5.0 {
		t.Fatalf("route[node-b] = %f, want 5.0", gossip.Routes["node-b"])
	}
}

func TestControlFrame_WhoHas_Roundtrip(t *testing.T) {
	t.Parallel()

	original := &ControlFrame{
		Payload: &ControlFrame_WhoHas{
			WhoHas: &WhoHasFrame{
				Uuid:       "test-uuid-123",
				Capability: "tool:lustre_health",
				OriginNode: "origin-node",
			},
		},
	}

	data, err := MarshalFrame(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	decoded, err := UnmarshalFrame(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	whoHas := decoded.GetWhoHas()
	if whoHas == nil {
		t.Fatal("expected who_has payload")
	}
	if whoHas.Uuid != "test-uuid-123" {
		t.Fatalf("uuid = %q, want %q", whoHas.Uuid, "test-uuid-123")
	}
	if whoHas.Capability != "tool:lustre_health" {
		t.Fatalf("capability = %q, want %q", whoHas.Capability, "tool:lustre_health")
	}
}

func TestControlFrame_IHave_Roundtrip(t *testing.T) {
	t.Parallel()

	original := &ControlFrame{
		Payload: &ControlFrame_IHave{
			IHave: &IHaveFrame{
				Uuid:      "test-uuid-456",
				NodeId:    "responder-node",
				Impedance: 3.7,
			},
		},
	}

	data, err := MarshalFrame(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	decoded, err := UnmarshalFrame(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	iHave := decoded.GetIHave()
	if iHave == nil {
		t.Fatal("expected i_have payload")
	}
	if iHave.Impedance != 3.7 {
		t.Fatalf("impedance = %f, want 3.7", iHave.Impedance)
	}
}

func TestControlFrame_RelayOpen_Roundtrip(t *testing.T) {
	t.Parallel()

	original := &ControlFrame{
		Payload: &ControlFrame_RelayOpen{
			RelayOpen: &RelayOpenFrame{
				TargetNodeId: "remote-node",
				CircuitId:    "circuit-abc",
			},
		},
	}

	data, err := MarshalFrame(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	decoded, err := UnmarshalFrame(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	relay := decoded.GetRelayOpen()
	if relay == nil {
		t.Fatal("expected relay_open payload")
	}
	if relay.TargetNodeId != "remote-node" {
		t.Fatalf("target = %q, want %q", relay.TargetNodeId, "remote-node")
	}
}

func TestControlFrame_CredentialRequest_Roundtrip(t *testing.T) {
	t.Parallel()

	pubKey := make([]byte, 32)
	for i := range pubKey {
		pubKey[i] = byte(i)
	}

	original := &ControlFrame{
		Payload: &ControlFrame_CredRequest{
			CredRequest: &CredentialRequestFrame{
				HostPattern:     "10.0.1.*",
				RequesterPubKey: pubKey,
			},
		},
	}

	data, err := MarshalFrame(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	decoded, err := UnmarshalFrame(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	cred := decoded.GetCredRequest()
	if cred == nil {
		t.Fatal("expected cred_request payload")
	}
	if cred.HostPattern != "10.0.1.*" {
		t.Fatalf("pattern = %q, want %q", cred.HostPattern, "10.0.1.*")
	}
	if !bytes.Equal(cred.RequesterPubKey, pubKey) {
		t.Fatal("public key mismatch")
	}
}

// P0-3: CredentialRequestFrame must carry a per-request nonce and a
// RequestedAt timestamp so the granter can bind each sealed grant to
// the specific request (prevents replay) and reject stale requests
// outside a clock-skew window.
//
// This test fails to build against the current schema (fields absent)
// until proto/mesh.proto is extended and `task proto` regenerates
// mesh.pb.go.
func TestControlFrame_CredentialRequest_NonceAndTimestamp_Roundtrip(t *testing.T) {
	t.Parallel()

	pubKey := make([]byte, 32)
	for i := range pubKey {
		pubKey[i] = byte(i)
	}
	nonce := make([]byte, 32)
	for i := range nonce {
		nonce[i] = byte(0xA0 + i)
	}
	const requestedAt int64 = 1_700_000_000_000_000_000 // arbitrary UnixNano

	original := &ControlFrame{
		Payload: &ControlFrame_CredRequest{
			CredRequest: &CredentialRequestFrame{
				HostPattern:     "10.0.1.*",
				RequesterPubKey: pubKey,
				Nonce:           nonce,
				RequestedAt:     requestedAt,
			},
		},
	}

	data, err := MarshalFrame(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	decoded, err := UnmarshalFrame(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	cred := decoded.GetCredRequest()
	if cred == nil {
		t.Fatal("expected cred_request payload")
	}
	if !bytes.Equal(cred.Nonce, nonce) {
		t.Fatalf("nonce roundtrip failed: got %x, want %x", cred.Nonce, nonce)
	}
	if cred.RequestedAt != requestedAt {
		t.Fatalf("requested_at roundtrip failed: got %d, want %d",
			cred.RequestedAt, requestedAt)
	}
}

func TestToolRequest_Roundtrip(t *testing.T) {
	t.Parallel()

	original := &ToolRequest{
		Name:     "lustre_health",
		ArgsJson: []byte(`{"node":"mds-01"}`),
	}

	data, err := MarshalToolRequest(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	decoded, err := UnmarshalToolRequest(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Name != "lustre_health" {
		t.Fatalf("name = %q, want %q", decoded.Name, "lustre_health")
	}
	if string(decoded.ArgsJson) != `{"node":"mds-01"}` {
		t.Fatalf("args = %q", decoded.ArgsJson)
	}
}

func TestToolResponse_Roundtrip(t *testing.T) {
	t.Parallel()

	original := &ToolResponse{
		IsError:     false,
		ContentJson: []byte(`{"status":"ok"}`),
	}

	data, err := MarshalToolResponse(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	decoded, err := UnmarshalToolResponse(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.IsError != false {
		t.Fatal("should not be error")
	}
	if string(decoded.ContentJson) != `{"status":"ok"}` {
		t.Fatalf("content = %q", decoded.ContentJson)
	}
}

// --- Stream codec tests ---

func TestStreamCodec_WriteRead_Roundtrip(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	frame := &ControlFrame{
		Payload: &ControlFrame_Gossip{
			Gossip: &GossipFrame{
				FromNode: "test-node",
				Routes:   map[string]float64{"peer": 1.0},
			},
		},
	}

	if err := WriteFrame(&buf, frame); err != nil {
		t.Fatalf("write: %v", err)
	}

	fr := NewFrameReader(2 * 1024 * 1024)
	decoded, err := fr.ReadFrame(&buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if decoded.GetGossip().FromNode != "test-node" {
		t.Fatalf("from_node = %q", decoded.GetGossip().FromNode)
	}
}

func TestStreamCodec_MultipleFrames(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	for i := 0; i < 5; i++ {
		frame := &ControlFrame{
			Payload: &ControlFrame_IHave{
				IHave: &IHaveFrame{
					Uuid:      "uuid",
					NodeId:    "node",
					Impedance: float64(i),
				},
			},
		}
		if err := WriteFrame(&buf, frame); err != nil {
			t.Fatalf("write[%d]: %v", i, err)
		}
	}

	for i := 0; i < 5; i++ {
		fr := NewFrameReader(2 * 1024 * 1024)
		decoded, err := fr.ReadFrame(&buf)
		if err != nil {
			t.Fatalf("read[%d]: %v", i, err)
		}
		if decoded.GetIHave().Impedance != float64(i) {
			t.Fatalf("impedance[%d] = %f", i, decoded.GetIHave().Impedance)
		}
	}
}

func TestStreamCodec_ReadEmpty(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	fr := NewFrameReader(2 * 1024 * 1024)
	_, err := fr.ReadFrame(&buf)
	if err == nil {
		t.Fatal("expected error reading from empty buffer")
	}
}
