// Tests for the independent canonical Doll Network wire types.
package dollnetwork

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestEncodeWgPublicKey_StandardBase64(t *testing.T) {
	t.Helper()
	key := make([]byte, 32)
	for i := 0; i < 32; i++ {
		key[i] = byte(i)
	}
	enc, err := EncodeWgPublicKey(key)
	if err != nil {
		t.Fatalf("EncodeWgPublicKey: %v", err)
	}
	// RFC 4648 standard base64 of 32 bytes is 44 chars with '=' padding.
	dec, err := base64.StdEncoding.DecodeString(enc)
	if err != nil || len(dec) != 32 {
		t.Fatalf("encoding should be standard base64 of 32 bytes: %v", err)
	}
}

func TestEncodeWgPublicKey_RejectsWrongLength(t *testing.T) {
	t.Helper()
	if _, err := EncodeWgPublicKey([]byte{1, 2, 3}); err == nil {
		t.Fatal("EncodeWgPublicKey accepted a non-32-byte key")
	}
}

func TestDecodeWgPublicKey_RoundTrip(t *testing.T) {
	t.Helper()
	key := make([]byte, 32)
	for i := 0; i < 32; i++ {
		key[i] = byte(i + 1)
	}
	enc, err := EncodeWgPublicKey(key)
	if err != nil {
		t.Fatalf("EncodeWgPublicKey: %v", err)
	}
	raw, err := DecodeWgPublicKey(enc)
	if err != nil {
		t.Fatalf("DecodeWgPublicKey: %v", err)
	}
	for i := 0; i < 32; i++ {
		if raw[i] != key[i] {
			t.Fatal("round-trip mismatch")
		}
	}
}

func TestDecodeWgPublicKey_RejectsInvalidBase64(t *testing.T) {
	t.Helper()
	if _, err := DecodeWgPublicKey("not base64 !!"); err == nil {
		t.Fatal("DecodeWgPublicKey accepted invalid base64")
	}
}

func TestDecodeWgPublicKey_RejectsWrongDecodedLength(t *testing.T) {
	t.Helper()
	// Base64 of "abcd" is 3 bytes — must be rejected (not 32).
	enc := base64.StdEncoding.EncodeToString([]byte("abcd"))
	if _, err := DecodeWgPublicKey(enc); err == nil {
		t.Fatal("DecodeWgPublicKey accepted non-32-byte key material")
	}
}

func TestValidatePairRequest_RejectsUnsupportedVersion(t *testing.T) {
	t.Helper()
	req := &PairRequest{
		Version:      99,
		InvitationID: "inv",
		Secret:       "sec",
		Body:         PairRequestBody{BodyID: "b", Implementation: "i", Platform: "p", Arch: "a"},
		Network:      PairingNetwork{WireGuardPublicKey: Encode32(t)},
	}
	if err := req.ValidatePairRequest(); err == nil {
		t.Fatal("unsupported version should fail validation")
	}
}

func TestValidatePairRequest_RejectsInvalidWgKey(t *testing.T) {
	t.Helper()
	req := &PairRequest{
		Version:      ProtocolVersion,
		InvitationID: "inv",
		Secret:       "sec",
		Body:         PairRequestBody{BodyID: "b", Implementation: "i", Platform: "p", Arch: "a"},
		Network:      PairingNetwork{WireGuardPublicKey: "bad-key"},
	}
	if err := req.ValidatePairRequest(); err == nil {
		t.Fatal("invalid wg public key should fail validation")
	}
}

func TestValidatePairRequest_AcceptsValid(t *testing.T) {
	t.Helper()
	req := &PairRequest{
		Version:      ProtocolVersion,
		InvitationID: "inv",
		Secret:       "sec",
		Body:         PairRequestBody{BodyID: "b", Implementation: "i", Platform: "p", Arch: "a"},
		Network:      PairingNetwork{WireGuardPublicKey: Encode32(t)},
	}
	if err := req.ValidatePairRequest(); err != nil {
		t.Fatalf("valid request failed validation: %v", err)
	}
}

func TestPairRequest_ToJSONCarriesCanonicalFields(t *testing.T) {
	t.Helper()
	req := &PairRequest{
		Version:      ProtocolVersion,
		InvitationID: "inv-1",
		Secret:       "sec-1",
		Body:         PairRequestBody{BodyID: "body-x", Implementation: "neondoll-shellbody", Platform: "linux", Arch: "amd64"},
		Network:      PairingNetwork{WireGuardPublicKey: Encode32(t)},
	}
	wire, err := req.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON: %v", err)
	}
	if !strings.Contains(wire, "invitation_id") ||
		!strings.Contains(wire, "wireguard_public_key") ||
		!strings.Contains(wire, "body_id") {
		t.Fatalf("request JSON missing canonical fields: %s", wire)
	}
}

// Encode32 returns valid wire material (base64 of 32 zero bytes).
func Encode32(t *testing.T) string {
	t.Helper()
	enc, err := EncodeWgPublicKey(make([]byte, 32))
	if err != nil {
		t.Fatalf("EncodeWgPublicKey: %v", err)
	}
	return enc
}
