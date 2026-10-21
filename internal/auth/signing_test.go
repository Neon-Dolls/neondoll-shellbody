package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"
)

func mustNonce(t *testing.T) string {
	t.Helper()
	s, err := RandomNonce()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// runHandshake simulates the full in-band handshake with independent Core and
// Body keypairs, exercising BuildHello, SignChallenge/VerifyChallenge,
// BuildProof, SignProof/VerifyProof, and SignAccept/VerifyAccept.
func runHandshake(t *testing.T) (Hello, Challenge, Proof, Accept, ed25519.PublicKey) {
	t.Helper()
	corePub, corePriv, _ := ed25519.GenerateKey(rand.Reader)
	bodyPub, bodyPriv, _ := ed25519.GenerateKey(rand.Reader)

	hello, err := BuildHello("body_test")
	if err != nil {
		t.Fatalf("BuildHello: %v", err)
	}
	if hello.Type != TypeHello || hello.Version != Version || len(hello.Algorithms) != 1 || hello.Algorithms[0] != AlgorithmEd25519 {
		t.Fatalf("unexpected hello %+v", hello)
	}

	challenge := Challenge{
		Version:   Version,
		Type:      TypeChallenge,
		CoreID:    "core_test",
		BodyID:    hello.BodyID,
		SessionID: "session_test",
		BodyNonce: hello.BodyNonce,
		CoreNonce: mustNonce(t),
		ExpiresAt: time.Now().UTC().Add(20 * time.Second).Truncate(time.Second),
		Algorithm: AlgorithmEd25519,
	}
	if err := SignChallenge(corePriv, &challenge); err != nil {
		t.Fatalf("SignChallenge: %v", err)
	}
	if err := VerifyChallenge(corePub, challenge); err != nil {
		t.Fatalf("VerifyChallenge: %v", err)
	}

	proof := BuildProof(challenge, "")
	if proof.CoreID != challenge.CoreID || proof.SessionID != challenge.SessionID ||
		proof.BodyNonce != challenge.BodyNonce || proof.CoreNonce != challenge.CoreNonce ||
		proof.Algorithm != challenge.Algorithm {
		t.Fatalf("BuildProof mismatched challenge: %+v", proof)
	}
	if err := SignProof(bodyPriv, proof); err != nil {
		t.Fatalf("SignProof: %v", err)
	}
	if err := VerifyProof(bodyPub, *proof); err != nil {
		t.Fatalf("VerifyProof: %v", err)
	}

	accept := Accept{
		Version:   Version,
		Type:      TypeAccept,
		CoreID:    challenge.CoreID,
		BodyID:    challenge.BodyID,
		SessionID: challenge.SessionID,
		Status:    StatusAuthenticated,
	}
	if err := SignAccept(corePriv, &accept); err != nil {
		t.Fatalf("SignAccept: %v", err)
	}
	if err := VerifyAccept(corePub, accept); err != nil {
		t.Fatalf("VerifyAccept: %v", err)
	}
	return *hello, challenge, *proof, accept, corePub
}

func TestFullAuthenticationHandshake(t *testing.T) {
	runHandshake(t)
}

func TestChallengeTamperingRejected(t *testing.T) {
	_, challenge, _, _, corePub := runHandshake(t)
	for name, mutate := range map[string]func(*Challenge){
		"core_id":    func(c *Challenge) { c.CoreID = "evil" },
		"body_id":    func(c *Challenge) { c.BodyID = "evil" },
		"session_id": func(c *Challenge) { c.SessionID = "other" },
		"body_nonce": func(c *Challenge) { c.BodyNonce = mustNonce(t) },
		"core_nonce": func(c *Challenge) { c.CoreNonce = mustNonce(t) },
		"algorithm":  func(c *Challenge) { c.Algorithm = "RSA" },
		"signature":  func(c *Challenge) { c.Signature = strings.Repeat("A", len(c.Signature)) },
	} {
		copied := challenge
		mutate(&copied)
		if err := VerifyChallenge(corePub, copied); err == nil {
			t.Errorf("%s: expected verification failure", name)
		}
	}
}

func TestChallengeExpiredRejected(t *testing.T) {
	_, _, _, _, _ = runHandshake(t)
	corePub, corePriv, _ := ed25519.GenerateKey(rand.Reader)
	challenge := Challenge{
		Version:   Version,
		Type:      TypeChallenge,
		CoreID:    "core",
		BodyID:    "body",
		SessionID: "s",
		BodyNonce: mustNonce(t),
		CoreNonce: mustNonce(t),
		ExpiresAt: time.Now().UTC().Add(-time.Minute), // already past
		Algorithm: AlgorithmEd25519,
	}
	if err := SignChallenge(corePriv, &challenge); err != nil {
		t.Fatal(err)
	}
	if err := VerifyChallenge(corePub, challenge); err == nil {
		t.Error("expired challenge accepted")
	}
}

func TestAcceptNonAuthenticatedStatusRejected(t *testing.T) {
	corePub, corePriv, _ := ed25519.GenerateKey(rand.Reader)
	accept := Accept{
		Version: Version, Type: TypeAccept, CoreID: "core", BodyID: "body",
		SessionID: "s", Status: "pending",
	}
	if err := SignAccept(corePriv, &accept); err != nil {
		t.Fatal(err)
	}
	if err := VerifyAccept(corePub, accept); err == nil {
		t.Error("accept with non-authenticated status accepted")
	}
}

// TestChallengeRejectsMalformedNonce confirms the raw 32-byte field handling:
// a challenge whose body_nonce is not a valid 32-byte base64url value must not
// sign/verify (DecodeNonce fails), proving the nonce fields take the raw path.
func TestChallengeRejectsMalformedNonce(t *testing.T) {
	_, corePriv, _ := ed25519.GenerateKey(rand.Reader)
	challenge := Challenge{
		Version: Version, Type: TypeChallenge, CoreID: "core", BodyID: "body",
		SessionID: "s", BodyNonce: "not-a-valid-base64url-nonce",
		CoreNonce: mustNonce(t), ExpiresAt: time.Now().Add(time.Minute), Algorithm: AlgorithmEd25519,
	}
	if err := SignChallenge(corePriv, &challenge); err == nil {
		t.Error("expected SignChallenge to fail on malformed nonce")
	}
}
