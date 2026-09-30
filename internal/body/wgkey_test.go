// Tests for Body-owned WireGuard keypair management.
package body

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestGenerateWgKeypair_Produces32BytePublicKey(t *testing.T) {
	t.Helper()
	kp, err := GenerateWgKeypair()
	if err != nil {
		t.Fatalf("GenerateWgKeypair: %v", err)
	}
	if kp == nil {
		t.Fatal("GenerateWgKeypair returned nil keypair")
	}
	pub := kp.PublicKey()
	if pub == nil {
		t.Fatal("public key is nil")
	}
	if len(pub.bytes) != 32 {
		t.Fatalf("public key should be 32 bytes, got %d", len(pub.bytes))
	}
	raw, err := base64.StdEncoding.DecodeString(kp.PublicKeyBase64())
	if err != nil || len(raw) != 32 {
		t.Fatalf("PublicKeyBase64 should be standard base64 of 32 bytes: %v", err)
	}
}

func TestGenerateWgKeypair_TwoKeypairsDiffer(t *testing.T) {
	t.Helper()
	// Two fresh keypairs must not collide; guards against a degenerate CSPRNG
	// or a fixed-key bug.
	seen := make(map[string]bool)
	for i := 0; i < 8; i++ {
		_ = i
		kp, err := GenerateWgKeypair()
		if err != nil {
			t.Fatalf("GenerateWgKeypair: %v", err)
		}
		b64 := kp.PublicKeyBase64()
		if seen[b64] {
			t.Fatalf("duplicate public key across fresh keypairs: %q", b64)
		}
		seen[b64] = true
	}
}

func TestNewWgKeypair_RoundTripPreservesPublicKey(t *testing.T) {
	t.Helper()
	kp, err := GenerateWgKeypair()
	if err != nil {
		t.Fatalf("GenerateWgKeypair: %v", err)
	}
	priv := kp.PrivateKeyBytes()
	rt, err := NewWgKeypair(priv)
	if err != nil {
		t.Fatalf("NewWgKeypair: %v", err)
	}
	if rt.PublicKeyBase64() != kp.PublicKeyBase64() {
		t.Fatal("public key changed after reconstructing from private key")
	}
	Wipe(priv)
}

func TestNewWgKeypair_RejectsWrongLength(t *testing.T) {
	t.Helper()
	if _, err := NewWgKeypair([]byte{1, 2, 3}); err == nil {
		t.Fatal("NewWgKeypair accepted a short private key")
	}
}

func TestPrivateKeyIsOpaque(t *testing.T) {
	t.Helper()
	// The private key must not appear inside the public serialization.
	kp, err := GenerateWgKeypair()
	if err != nil {
		t.Fatalf("GenerateWgKeypair: %v", err)
	}
	pubB64 := kp.PublicKeyBase64()
	privStr := string(kp.PrivateKeyBytes())
	if strings.Contains(pubB64, privStr) {
		t.Fatal("public key serialization leaked private key material")
	}
}
