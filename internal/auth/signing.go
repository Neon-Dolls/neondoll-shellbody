package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"time"

	enrollment "github.com/Neon-Dolls/neondoll-shellbody/internal/enrollment"
)

const nonceSize = 32

// challengeFields builds the signed transcript for body.auth.challenge
// (7 fields: core_id, body_id, session_id, body_nonce, core_nonce,
// expires_at, algorithm; body_nonce and core_nonce are raw 32-byte fields).
func challengeFields(value Challenge) ([]enrollment.SignedField, error) {
	return authFields(value.CoreID, value.BodyID, value.SessionID,
		value.BodyNonce, value.CoreNonce,
		value.ExpiresAt.UTC().Format(time.RFC3339), value.Algorithm)
}

// proofFields builds the signed transcript for body.auth.proof
// (6 fields: core_id, body_id, session_id, body_nonce, core_nonce,
// algorithm; body_nonce and core_nonce are raw 32-byte fields).
func proofFields(value Proof) ([]enrollment.SignedField, error) {
	return authFields(value.CoreID, value.BodyID, value.SessionID,
		value.BodyNonce, value.CoreNonce, value.Algorithm)
}

// acceptFields builds the signed transcript for body.auth.accept
// (4 fields: core_id, body_id, session_id, status — all string fields).
func acceptFields(value Accept) ([]enrollment.SignedField, error) {
	return authFields(value.CoreID, value.BodyID, value.SessionID, value.Status)
}

// authFields mirrors Core's classification rule: for the 7-field challenge and
// 6-field proof transcripts, indices 3 and 4 (body_nonce, core_nonce) are
// decoded raw nonce bytes; all other fields are length-prefixed strings.
func authFields(values ...string) ([]enrollment.SignedField, error) {
	fields := make([]enrollment.SignedField, 0, len(values))
	for index, value := range values {
		var field enrollment.SignedField
		var err error
		if (len(values) == 7 && (index == 3 || index == 4)) ||
			(len(values) == 6 && (index == 3 || index == 4)) {
			var raw []byte
			raw, err = enrollment.DecodeNonce(value)
			if err == nil {
				field = enrollment.RawField(raw)
			}
		} else {
			field, err = enrollment.StringField(value)
		}
		if err != nil {
			return nil, err
		}
		fields = append(fields, field)
	}
	return fields, nil
}

// SignChallenge signs a challenge with Core's identity key. Provided for
// symmetric parity and test use; the Body verifies, it does not sign
// challenges.
func SignChallenge(key ed25519.PrivateKey, value *Challenge) error {
	fields, err := challengeFields(*value)
	if err != nil {
		return err
	}
	return sign(key, TypeChallenge, &value.Signature, fields...)
}

// VerifyChallenge verifies a challenge signature against Core's public key and
// rejects expired challenges.
func VerifyChallenge(key ed25519.PublicKey, value Challenge) error {
	fields, err := challengeFields(value)
	if err != nil {
		return err
	}
	if err := verify(key, TypeChallenge, value.Signature, fields...); err != nil {
		return err
	}
	if !value.ExpiresAt.After(time.Now()) {
		return errors.New("authentication: challenge expired")
	}
	return nil
}

// SignProof signs a proof with the Body's enrolled identity key.
func SignProof(key ed25519.PrivateKey, value *Proof) error {
	fields, err := proofFields(*value)
	if err != nil {
		return err
	}
	return sign(key, TypeProof, &value.Signature, fields...)
}

// VerifyProof verifies a proof against a Body's enrolled public key. Provided
// for symmetric parity and test use.
func VerifyProof(key ed25519.PublicKey, value Proof) error {
	fields, err := proofFields(value)
	if err != nil {
		return err
	}
	return verify(key, TypeProof, value.Signature, fields...)
}

// SignAccept signs an accept with Core's identity key. Provided for symmetric
// parity and test use; the Body verifies accepts.
func SignAccept(key ed25519.PrivateKey, value *Accept) error {
	fields, err := acceptFields(*value)
	if err != nil {
		return err
	}
	return sign(key, TypeAccept, &value.Signature, fields...)
}

// VerifyAccept verifies an accept signature against Core's public key and
// validates the status is "authenticated".
func VerifyAccept(key ed25519.PublicKey, value Accept) error {
	fields, err := acceptFields(value)
	if err != nil {
		return err
	}
	if err := verify(key, TypeAccept, value.Signature, fields...); err != nil {
		return err
	}
	if value.Status != StatusAuthenticated {
		return errors.New("authentication: accept status is not authenticated")
	}
	return nil
}

// BuildHello creates a canonical body.auth.hello for an enrolled body id with a
// fresh random body_nonce.
func BuildHello(bodyID string) (*Hello, error) {
	nonce, err := RandomNonce()
	if err != nil {
		return nil, err
	}
	return &Hello{
		Version:    Version,
		Type:       TypeHello,
		BodyID:     bodyID,
		BodyNonce:  nonce,
		Algorithms: []string{AlgorithmEd25519},
	}, nil
}

// BuildProof creates an unsigned body.auth.proof bound to the given challenge,
// reusing the challenge's session id, nonces, and algorithm verbatim.
func BuildProof(challenge Challenge, coreID string) *Proof {
	if coreID == "" {
		coreID = challenge.CoreID
	}
	return &Proof{
		Version:   Version,
		Type:      TypeProof,
		CoreID:    coreID,
		BodyID:    challenge.BodyID,
		SessionID: challenge.SessionID,
		BodyNonce: challenge.BodyNonce,
		CoreNonce: challenge.CoreNonce,
		Algorithm: challenge.Algorithm,
	}
}

// RandomNonce returns a fresh 32-byte body_nonce encoded in base64url.
func RandomNonce() (string, error) {
	raw := make([]byte, nonceSize)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return enrollment.EncodeRawURL(raw), nil
}

func sign(key ed25519.PrivateKey, kind string, destination *string, fields ...enrollment.SignedField) error {
	message := enrollment.SignedMessage(kind, fields...)
	*destination = enrollment.EncodeRawURL(ed25519.Sign(key, message))
	return nil
}

func verify(key ed25519.PublicKey, kind, signature string, fields ...enrollment.SignedField) error {
	raw, err := enrollment.DecodeSignature(signature)
	if err != nil {
		return err
	}
	message := enrollment.SignedMessage(kind, fields...)
	if !ed25519.Verify(key, message, raw) {
		return errors.New("authentication: invalid signature")
	}
	return nil
}
