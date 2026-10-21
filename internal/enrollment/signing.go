// Independently implemented reproduction of NeonDoll Core 4's enrollment
// signing scheme (DollNetwork/enrollment_signing.go). The transcript is a
// byte-exact match so signatures Shell Body produces are verifiable by Core's
// canonical verification functions and vice-versa.
//
// Signing model (unambiguous, no JSON bytes are ever signed):
//
//	domain  = "NEONDOLL-SIGN-V1" || 0x00
//	message = domain || u32be(len(type)) || type || (u32be(len(field)) || field)*
//	sig     = ed25519.Sign(privateKey, message)
//	wire    = base64.RawURLEncoding(sig)
package enrollment

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
)

// signingDomain is the canonical domain-separator prefix.
var signingDomain = []byte("NEONDOLL-SIGN-V1\x00")

const (
	// maxSignedString bounds the length of a string transcript field.
	maxSignedString = 256
	// nonceSize is the fixed size of a body/core nonce.
	nonceSize = 32
	// timeLayout renders a signed timestamp precisely as Core does (whole
	// seconds, UTC RFC3339 with zone). Core canonicalizes to whole-second UTC
	// before signing so JSON transport cannot introduce a fractional variant.
	timeLayout = "2006-01-02T15:04:05Z07:00"
)

// signedField is one length-prefixed byte field in the transcript.
type signedField []byte

// stringField wraps a string as a transcript field.
func stringField(value string) (signedField, error) {
	if len(value) > maxSignedString {
		return nil, fmt.Errorf("enrollment: field exceeds max length %d", maxSignedString)
	}
	return signedField(value), nil
}

// rawField wraps arbitrary bytes (public keys, nonces) as a transcript field.
func rawField(value []byte) signedField { return signedField(value) }

// signedMessage builds the canonical transcript for a message type + fields.
func signedMessage(messageType string, fields []signedField) []byte {
	buf := make([]byte, 0, 64+len(messageType))
	buf = append(buf, signingDomain...)
	buf = appendField(buf, []byte(messageType))
	for _, f := range fields {
		buf = appendField(buf, f)
	}
	return buf
}

// appendField appends u32be(len(field)) || field to dst.
func appendField(dst, field []byte) []byte {
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(field)))
	dst = append(dst, header[:]...)
	dst = append(dst, field...)
	return dst
}

// The canonical per-message signed field lists. Field order is significant
// and must not change (Core verifies against the identical construction).
func enrollmentKeyProofFields(coreID, requestID string, publicKey, wgKey, nonce []byte) ([]signedField, error) {
	core, err := stringField(coreID)
	if err != nil {
		return nil, err
	}
	req, err := stringField(requestID)
	if err != nil {
		return nil, err
	}
	return []signedField{core, req, rawField(publicKey), rawField(wgKey), rawField(nonce)}, nil
}

func redeemFields(coreID, requestID, invitationID, invitationSecret string, publicKey, wgKey, nonce []byte) ([]signedField, error) {
	core, err := stringField(coreID)
	if err != nil {
		return nil, err
	}
	req, err := stringField(requestID)
	if err != nil {
		return nil, err
	}
	inv, err := stringField(invitationID)
	if err != nil {
		return nil, err
	}
	sec, err := stringField(invitationSecret)
	if err != nil {
		return nil, err
	}
	return []signedField{core, req, inv, sec, rawField(publicKey), rawField(wgKey), rawField(nonce)}, nil
}

func resultFields(coreID, requestID string, publicKey, nonce []byte) ([]signedField, error) {
	core, err := stringField(coreID)
	if err != nil {
		return nil, err
	}
	req, err := stringField(requestID)
	if err != nil {
		return nil, err
	}
	return []signedField{core, req, rawField(publicKey), rawField(nonce)}, nil
}

// EncodeRawURL is a re-export of the canonical base64 encoding used for keys,
// nonces, and signatures on the wire.
func EncodeRawURL(raw []byte) string { return base64.RawURLEncoding.EncodeToString(raw) }

// DecodeRawURL decodes canonical wire bytes.
func DecodeRawURL(encoded string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(encoded)
}

// BodyKeyFingerprint is hex(sha256(publicKey)); the stable wire identity label.
func BodyKeyFingerprint(publicKey ed25519.PublicKey) string {
	sum := sha256.Sum256(publicKey)
	return hex.EncodeToString(sum[:])
}

// decodeBodyPublicKey parses a base64url ed25519 public key (32 bytes).
func decodeBodyPublicKey(encoded string) (ed25519.PublicKey, error) {
	raw, err := DecodeRawURL(encoded)
	if err != nil {
		return nil, fmt.Errorf("enrollment: invalid body_public_key encoding: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("enrollment: body_public_key must be %d bytes", ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// decodeWgPublicKey parses the wireguard_public_key field. Note: unlike
// body_public_key/body_nonce/signature (RawURL), Doll Network's wg_public_key
// uses STANDARD base64 (RFC 4648), matching Core's DecodeWgPublicKey and the
// WireGuard key serialization convention.
func decodeWgPublicKey(encoded string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("enrollment: invalid wg_public_key encoding: %w", err)
	}
	if len(raw) != nonceSize {
		return nil, fmt.Errorf("enrollment: wg_public_key must be %d bytes", nonceSize)
	}
	return raw, nil
}

func decodeNonce(encoded string) ([]byte, error) {
	raw, err := DecodeRawURL(encoded)
	if err != nil {
		return nil, fmt.Errorf("enrollment: invalid nonce encoding: %w", err)
	}
	if len(raw) != nonceSize {
		return nil, fmt.Errorf("enrollment: nonce must be %d bytes", nonceSize)
	}
	return raw, nil
}

// SignEnrollmentBegin signs a begin request in place (sets Signature).
func SignEnrollmentBegin(privateKey ed25519.PrivateKey, request *EnrollmentBeginRequest) error {
	publicKey, err := decodeBodyPublicKey(request.BodyPublicKey)
	if err != nil {
		return err
	}
	wgKey, err := decodeWgPublicKey(request.WGPublicKey)
	if err != nil {
		return err
	}
	nonce, err := decodeNonce(request.BodyNonce)
	if err != nil {
		return err
	}
	fields, err := enrollmentKeyProofFields(request.CoreID, request.RequestID, publicKey, wgKey, nonce)
	if err != nil {
		return err
	}
	return sign(privateKey, "enrollment.begin", &request.Signature, fields...)
}

// VerifyEnrollmentBegin checks a begin request proof.
func VerifyEnrollmentBegin(request EnrollmentBeginRequest) error {
	if request.Version != EnrollmentVersion {
		return fmt.Errorf("dollnetwork: unsupported enrollment version")
	}
	publicKey, err := decodeBodyPublicKey(request.BodyPublicKey)
	if err != nil {
		return err
	}
	wgKey, err := decodeWgPublicKey(request.WGPublicKey)
	if err != nil {
		return err
	}
	nonce, err := decodeNonce(request.BodyNonce)
	if err != nil {
		return err
	}
	fields, err := enrollmentKeyProofFields(request.CoreID, request.RequestID, publicKey, wgKey, nonce)
	if err != nil {
		return err
	}
	return verify(publicKey, "enrollment.begin", request.Signature, fields...)
}

// SignEnrollmentRedeem signs a redeem request in place (sets Signature).
func SignEnrollmentRedeem(privateKey ed25519.PrivateKey, request *EnrollmentRedeemRequest) error {
	publicKey, err := decodeBodyPublicKey(request.BodyPublicKey)
	if err != nil {
		return err
	}
	wgKey, err := decodeWgPublicKey(request.WGPublicKey)
	if err != nil {
		return err
	}
	nonce, err := decodeNonce(request.BodyNonce)
	if err != nil {
		return err
	}
	f, err := redeemFields(request.CoreID, request.RequestID, request.InvitationID, request.InvitationSecret, publicKey, wgKey, nonce)
	if err != nil {
		return err
	}
	return sign(privateKey, "enrollment.redeem", &request.Signature, f...)
}

// VerifyEnrollmentRedeem checks a redeem request proof.
func VerifyEnrollmentRedeem(request EnrollmentRedeemRequest) error {
	if request.Version != EnrollmentVersion {
		return fmt.Errorf("dollnetwork: unsupported enrollment version")
	}
	publicKey, err := decodeBodyPublicKey(request.BodyPublicKey)
	if err != nil {
		return err
	}
	wgKey, err := decodeWgPublicKey(request.WGPublicKey)
	if err != nil {
		return err
	}
	nonce, err := decodeNonce(request.BodyNonce)
	if err != nil {
		return err
	}
	f, err := redeemFields(request.CoreID, request.RequestID, request.InvitationID, request.InvitationSecret, publicKey, wgKey, nonce)
	if err != nil {
		return err
	}
	return verify(publicKey, "enrollment.redeem", request.Signature, f...)
}

// SignEnrollmentResult signs a result-poll request in place.
func SignEnrollmentResult(privateKey ed25519.PrivateKey, request *EnrollmentResultRequest) error {
	publicKey, err := decodeBodyPublicKey(request.BodyPublicKey)
	if err != nil {
		return err
	}
	nonce, err := decodeNonce(request.BodyNonce)
	if err != nil {
		return err
	}
	f, err := resultFields(request.CoreID, request.RequestID, publicKey, nonce)
	if err != nil {
		return err
	}
	return sign(privateKey, "enrollment.result.request", &request.Signature, f...)
}

// VerifyEnrollmentResult checks a result-poll request proof.
func VerifyEnrollmentResult(request EnrollmentResultRequest) error {
	if request.Version != EnrollmentVersion {
		return fmt.Errorf("dollnetwork: unsupported enrollment version")
	}
	publicKey, err := decodeBodyPublicKey(request.BodyPublicKey)
	if err != nil {
		return err
	}
	nonce, err := decodeNonce(request.BodyNonce)
	if err != nil {
		return err
	}
	f, err := resultFields(request.CoreID, request.RequestID, publicKey, nonce)
	if err != nil {
		return err
	}
	return verify(publicKey, "enrollment.result.request", request.Signature, f...)
}

func sign(privateKey ed25519.PrivateKey, messageType string, destination *string, fields ...signedField) error {
	message := signedMessage(messageType, fields)
	sig := ed25519.Sign(privateKey, message)
	*destination = EncodeRawURL(sig)
	return nil
}

func verify(publicKey ed25519.PublicKey, messageType, encodedSignature string, fields ...signedField) error {
	sig, err := DecodeRawURL(encodedSignature)
	if err != nil {
		return fmt.Errorf("enrollment: invalid signature encoding: %w", err)
	}
	message := signedMessage(messageType, fields)
	if !ed25519.Verify(publicKey, message, sig) {
		return fmt.Errorf("enrollment: invalid signature for %q", messageType)
	}
	return nil
}

// SignedField is one length-prefixed byte field in a transcript.
type SignedField = signedField

// StringField wraps a string as a canonical transcript field.
func StringField(value string) (SignedField, error) { return stringField(value) }

// RawField wraps arbitrary bytes (public keys, nonces) as a canonical
// transcript field.
func RawField(value []byte) SignedField { return rawField(value) }

// DecodeNonce decodes the canonical base64url nonce representation and
// validates it is exactly 32 bytes, as required for body_nonce/core_nonce.
func DecodeNonce(encoded string) ([]byte, error) { return decodeNonce(encoded) }

// DecodeSignature decodes a canonical base64url Ed25519 signature. Provided
// for parity with DollNetwork's verify path.
func DecodeSignature(encoded string) ([]byte, error) { return DecodeRawURL(encoded) }

// SignedMessage builds the canonical length-prefixed transcript for a message
// type + fields, mirroring DollNetwork.SignedMessage. This is the shared
// transcript engine reused by the enrollment and body authentication
// protocols (independent production reimplementation).
func SignedMessage(messageType string, fields ...SignedField) []byte {
	return signedMessage(messageType, fields)
}

// SignEnrollmentPending signs the Core challenge. Provided for symmetric
// parity and test use; a Body client does not sign Core responses.
func SignEnrollmentPending(privateKey ed25519.PrivateKey, response *EnrollmentPendingResponse) error {
	f, err := pendingFields(response)
	if err != nil {
		return err
	}
	return sign(privateKey, "enrollment.pending", &response.Signature, f...)
}

func pendingFields(response *EnrollmentPendingResponse) ([]signedField, error) {
	// Mirror Core's DollNetwork.SignEnrollmentPending transcript byte-for-byte:
	// core_nonce is a RAW field (decoded bytes), all others are string fields,
	// expires_at is formatted without truncation.
	coreNonce, err := decodeNonce(response.CoreNonce)
	if err != nil {
		return nil, fmt.Errorf("enrollment: invalid core_nonce: %w", err)
	}
	var f0, f1, f2, f4 signedField
	if f0, err = stringField(response.CoreID); err != nil {
		return nil, err
	}
	if f1, err = stringField(response.RequestID); err != nil {
		return nil, err
	}
	if f2, err = stringField(response.BodyKeyFingerprint); err != nil {
		return nil, err
	}
	if f4, err = stringField(response.ApprovalCode); err != nil {
		return nil, err
	}
	f5, err := stringField(response.ExpiresAt.UTC().Format(timeLayout))
	if err != nil {
		return nil, err
	}
	return []signedField{f0, f1, f2, rawField(coreNonce), f4, f5}, nil
}

// VerifyEnrollmentPending verifies a Core-signed challenge using Core's
// public key (only useful when the Body holds Core's public key out-of-band).
func VerifyEnrollmentPending(publicKey ed25519.PublicKey, response EnrollmentPendingResponse) error {
	f, err := pendingFields(&response)
	if err != nil {
		return err
	}
	return verify(publicKey, "enrollment.pending", response.Signature, f...)
}

// VerifyEnrollmentReceipt verifies a Core-signed receipt using Core's public
// key (only useful when the Body holds Core's public key out-of-band).
func VerifyEnrollmentReceipt(publicKey ed25519.PublicKey, receipt EnrollmentReceipt) error {
	fields, err := receiptFields(&receipt)
	if err != nil {
		return err
	}
	return verify(publicKey, "enrollment.receipt", receipt.Signature, fields...)
}

// SignEnrollmentReceipt signs a receipt (Core-side parity helper; usable by
// tests and integration harnesses that stand in for Core).
func SignEnrollmentReceipt(privateKey ed25519.PrivateKey, receipt *EnrollmentReceipt) error {
	fields, err := receiptFields(receipt)
	if err != nil {
		return err
	}
	return sign(privateKey, "enrollment.receipt", &receipt.Signature, fields...)
}

func receiptFields(receipt *EnrollmentReceipt) ([]signedField, error) {
	values := []string{
		receipt.CoreID,
		receipt.RequestID,
		receipt.BodyID,
		receipt.BodyPublicKeyFingerprint,
		receipt.MembershipReference,
		receipt.IssuedAt.UTC().Format(timeLayout),
	}
	fields := make([]signedField, 0, len(values))
	for _, v := range values {
		f, err := stringField(v)
		if err != nil {
			return nil, err
		}
		fields = append(fields, f)
	}
	return fields, nil
}
