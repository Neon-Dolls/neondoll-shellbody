// Package auth is Shell Body's independent reimplementation of NeonDoll Core's
// Body Authentication v1 wire frames (DollLink/Authentication in the neondoll
// repository).
//
// NeonDoll Core defines the protocol; Shell Body adapts. The structs in this
// package mirror byte-for-byte Core's canonical authentication frames so that
// Shell Body's /ws session can perform the in-band Ed25519 handshake
// (body.auth.hello → body.auth.challenge → body.auth.proof → body.auth.accept)
// without importing the neondoll repository into production code. Conformance
// is proven against the real Core 4 authenticator in the integration test
// (test/m10_2_core4_conformance_test.go).
package auth

import "time"

// Canonical Body Authentication v1 constants.
const (
	// Version is the authentication protocol version (1).
	Version = 1

	// AlgorithmEd25519 is the canonical in-band algorithm identifier.
	AlgorithmEd25519 = "Ed25519"

	// Wire type discriminators.
	TypeHello     = "body.auth.hello"
	TypeChallenge = "body.auth.challenge"
	TypeProof     = "body.auth.proof"
	TypeAccept    = "body.auth.accept"

	// StatusAuthenticated is the canonical accept status.
	StatusAuthenticated = "authenticated"

	// ChallengeLifetime is the maximum age of a challenge the Body will
	// accept. It mirrors Core's authenticationChallengeLifetime (30s).
	ChallengeLifetime = 30 * time.Second
)

// Hello is the Body → Core opening frame (body.auth.hello).
type Hello struct {
	Version    int      `json:"version"`
	Type       string   `json:"type"`
	BodyID     string   `json:"body_id"`
	BodyNonce  string   `json:"body_nonce"`
	Algorithms []string `json:"algorithms"`
}

// Challenge is the Core → Body challenge (body.auth.challenge), signed by
// Core's identity key.
type Challenge struct {
	Version   int       `json:"version"`
	Type      string    `json:"type"`
	CoreID    string    `json:"core_id"`
	BodyID    string    `json:"body_id"`
	SessionID string    `json:"session_id"`
	BodyNonce string    `json:"body_nonce"`
	CoreNonce string    `json:"core_nonce"`
	ExpiresAt time.Time `json:"expires_at"`
	Algorithm string    `json:"algorithm"`
	Signature string    `json:"signature"`
}

// Proof is the Body → Core response (body.auth.proof), signed by the Body's
// enrolled Ed25519 identity key.
type Proof struct {
	Version   int    `json:"version"`
	Type      string `json:"type"`
	CoreID    string `json:"core_id"`
	BodyID    string `json:"body_id"`
	SessionID string `json:"session_id"`
	BodyNonce string `json:"body_nonce"`
	CoreNonce string `json:"core_nonce"`
	Algorithm string `json:"algorithm"`
	Signature string `json:"signature"`
}

// Accept is the Core → Body terminal frame (body.auth.accept), signed by
// Core's identity key. Status == StatusAuthenticated grants the session
// execution capability.
type Accept struct {
	Version   int    `json:"version"`
	Type      string `json:"type"`
	CoreID    string `json:"core_id"`
	BodyID    string `json:"body_id"`
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
	Signature string `json:"signature"`
}
