// Package enrollment implements the NeonDoll Core 4 Body enrollment client
// contract. The wire schemas here mirror (byte-for-byte) the canonical
// enrollment message types defined and served by Core at
// POST /v1/body/enrollment/{begin,redeem,result} (DollNetwork/enrollment.go
// in the neondoll repository).
//
// NeonDoll Core defines the protocol; Shell Body adapts. The structs in this
// package are an independent, production-local reimplementation of Core's
// wire contract so that production Shell Body code never imports the
// neondoll repository. Conformance is proven against the real Core 4
// structures in the integration test (test/m10_2_core4_conformance_test.go).
package enrollment

import "time"

const (
	// EnrollmentVersion is the canonical enrollment protocol version (1).
	EnrollmentVersion = 1

	// CoreIdentityVersion is the canonical public identity document version.
	CoreIdentityVersion = 1

	// CoreIdentityAlgorithm is the only accepted signature algorithm for the
	// Core signing identity served at GET /v1/core/identity.
	CoreIdentityAlgorithm = "Ed25519"

	// ModeInvitation and ModeBodyRequest are the two canonical enrollment
	// modes. Redeem with an invitation is the canonical register path.
	ModeInvitation  = "invitation"
	ModeBodyRequest = "body_request"

	// Statuses observed on EnrollmentResultResponse.
	StatusPending  = "pending"
	StatusDenied   = "denied"
	StatusExpired  = "expired"
	StatusEnrolled = "enrolled"
)

// EnrollmentMetadata is free-form, non-signed auxiliary metadata.
type EnrollmentMetadata map[string]string

// EnrollmentBeginRequest is the Body → Core "begin" request (challenge setup).
// JSON tags and field order match Core's canonical schema.
type EnrollmentBeginRequest struct {
	Version       int                `json:"version"`
	RequestID     string             `json:"request_id"`
	CoreID        string             `json:"core_id"`
	BodyPublicKey string             `json:"body_public_key"`
	WGPublicKey   string             `json:"wg_public_key"`
	BodyNonce     string             `json:"body_nonce"`
	Metadata      EnrollmentMetadata `json:"metadata,omitempty"`
	Signature     string             `json:"signature"`
}

// EnrollmentPendingResponse is the Core → Body challenge carrying core_nonce
// and (in body_request mode) an owner approval code.
type EnrollmentPendingResponse struct {
	Version            int       `json:"version"`
	RequestID          string    `json:"request_id"`
	CoreID             string    `json:"core_id"`
	BodyKeyFingerprint string    `json:"body_key_fingerprint"`
	CoreNonce          string    `json:"core_nonce"`
	ApprovalCode       string    `json:"approval_code"`
	ExpiresAt          time.Time `json:"expires_at"`
	Signature          string    `json:"signature"`
}

// EnrollmentRedeemRequest is the Body → Core "redeem" request, presenting an
// invitation id + secret to register. This is the canonical register path.
type EnrollmentRedeemRequest struct {
	Version          int                `json:"version"`
	RequestID        string             `json:"request_id"`
	CoreID           string             `json:"core_id"`
	InvitationID     string             `json:"invitation_id"`
	InvitationSecret string             `json:"invitation_secret"`
	BodyPublicKey    string             `json:"body_public_key"`
	WGPublicKey      string             `json:"wg_public_key"`
	BodyNonce        string             `json:"body_nonce"`
	Metadata         EnrollmentMetadata `json:"metadata,omitempty"`
	Signature        string             `json:"signature"`
}

// EnrollmentResultRequest is the Body → Core poll for terminal status.
type EnrollmentResultRequest struct {
	Version       int    `json:"version"`
	RequestID     string `json:"request_id"`
	CoreID        string `json:"core_id"`
	BodyPublicKey string `json:"body_public_key"`
	BodyNonce     string `json:"body_nonce"`
	Signature     string `json:"signature"`
}

// EnrollmentReceipt is the Core-signed document granting enrollment. Core
// mints body_id at completion; the Body does not propose its own ID.
type EnrollmentReceipt struct {
	Version                  int       `json:"version"`
	RequestID                string    `json:"request_id"`
	Status                   string    `json:"status"`
	BodyID                   string    `json:"body_id"`
	CoreID                   string    `json:"core_id"`
	BodyPublicKeyFingerprint string    `json:"body_public_key_fingerprint"`
	Permissions              []string  `json:"permissions"`
	MembershipReference      string    `json:"membership_reference"`
	IssuedAt                 time.Time `json:"issued_at"`
	Signature                string    `json:"signature"`
}

// EnrollmentResultResponse wraps the receipt.
type EnrollmentResultResponse struct {
	Version   int                `json:"version"`
	RequestID string             `json:"request_id"`
	Status    string             `json:"status"`
	Receipt   *EnrollmentReceipt `json:"receipt,omitempty"`
}

// EnrollmentError is a canonical enrollment error.
type EnrollmentError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// EnrollmentErrorResponse is returned by Core on any enrollment failure.
type EnrollmentErrorResponse struct {
	Version int             `json:"version"`
	Error   EnrollmentError `json:"error"`
}

// CoreIdentityResponse mirrors Core's public, read-only bootstrap identity
// document served at GET /v1/core/identity (DollNetwork/core_identity.go in
// the neondoll repository). It is discovery material for the enrollment
// ceremony — never proof that a caller is an enrolled Body. The public_key is
// the unpadded-base64url encoding of Core's 32-byte Ed25519 public key.
type CoreIdentityResponse struct {
	Version   int    `json:"version"`
	CoreID    string `json:"core_id"`
	Algorithm string `json:"algorithm"`
	PublicKey string `json:"public_key"`
}
