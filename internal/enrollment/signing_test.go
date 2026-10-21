package enrollment

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"
)

// TestSignedMessageTranscriptBytes pins the exact NEONDOLL-SIGN-V1 transcript
// encoding so accidental field-order/encoding drift is caught at the source.
func TestSignedMessageTranscriptBytes(t *testing.T) {
	got := signedMessage("enrollment.begin", []signedField{
		rawField([]byte("core")),
		rawField([]byte("req")),
	})
	want := []byte("NEONDOLL-SIGN-V1\x00" +
		"\x00\x00\x00\x10" + "enrollment.begin" +
		"\x00\x00\x00\x04" + "core" +
		"\x00\x00\x00\x03" + "req")
	if string(got) != string(want) {
		t.Fatalf("transcript mismatch:\n got %x\nwant %x", got, want)
	}
}

// TestSignVerifyBeginRoundTrip verifies self-consistency: a begin proof signed
// by the independent implementation verifies under its own Verify.
func TestSignVerifyBeginRoundTrip(t *testing.T) {
	req := validBegin(t)
	if err := SignEnrollmentBegin(req.Identity.PrivateKey(), &req.req); err != nil {
		t.Fatalf("sign begin: %v", err)
	}
	if req.req.Signature == "" {
		t.Fatal("signature not set")
	}
	if err := VerifyEnrollmentBegin(req.req); err != nil {
		t.Fatalf("verify begin: %v", err)
	}

	// Tamper a signed field → must fail verification.
	bad := req.req
	bad.BodyNonce = EncodeRawURL(randomBytes(nonceSize))
	if err := VerifyEnrollmentBegin(bad); err == nil {
		t.Fatal("tampered begin accepted")
	}
}

func TestSignVerifyRedeemRoundTrip(t *testing.T) {
	fx := validRedeem(t)
	if err := SignEnrollmentRedeem(fx.Identity.PrivateKey(), &fx.req); err != nil {
		t.Fatalf("sign redeem: %v", err)
	}
	if err := VerifyEnrollmentRedeem(fx.req); err != nil {
		t.Fatalf("verify redeem: %v", err)
	}
}

func TestSignVerifyResultRoundTrip(t *testing.T) {
	fx := validResult(t)
	if err := SignEnrollmentResult(fx.Identity.PrivateKey(), &fx.req); err != nil {
		t.Fatalf("sign result: %v", err)
	}
	if err := VerifyEnrollmentResult(fx.req); err != nil {
		t.Fatalf("verify result: %v", err)
	}
}

// TestReceiptVerificationEnsuresIntegrity verifies a Core-signed receipt with
// Core's public key and rejects a tampered one.
func TestReceiptVerificationEnsuresIntegrity(t *testing.T) {
	corePub, corePriv, _ := ed25519.GenerateKey(rand.Reader)
	fx := validResult(t)
	receipt := EnrollmentReceipt{
		Version:                  EnrollmentVersion,
		RequestID:                fx.req.RequestID,
		Status:                   StatusEnrolled,
		BodyID:                   "body_00000000000000000000000000000000",
		CoreID:                   fx.req.CoreID,
		BodyPublicKeyFingerprint: fx.Identity.Fingerprint(),
		Permissions:              []string{"terminal"},
		MembershipReference:      "peer_xyz",
		IssuedAt:                 time.Now().UTC().Truncate(time.Second),
	}
	if err := SignEnrollmentReceipt(corePriv, &receipt); err != nil {
		t.Fatalf("sign receipt: %v", err)
	}
	if err := VerifyEnrollmentReceipt(corePub, receipt); err != nil {
		t.Fatalf("verify receipt: %v", err)
	}
	tampered := receipt
	tampered.BodyID = "body_tampered"
	if err := VerifyEnrollmentReceipt(corePub, tampered); err == nil {
		t.Fatal("tampered receipt accepted")
	}
}

// --- fixtures ---------------------------------------------------------------

type beginFixture struct {
	Identity *Identity
	req      EnrollmentBeginRequest
}

func validBegin(t *testing.T) beginFixture {
	t.Helper()
	id, err := NewIdentity()
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	return beginFixture{
		Identity: id,
		req: EnrollmentBeginRequest{
			Version:       EnrollmentVersion,
			RequestID:     "erq_test",
			CoreID:        "core_test",
			BodyPublicKey: id.PublicKeyRawURL(),
			WGPublicKey:   base64.StdEncoding.EncodeToString(randomBytes(nonceSize)),
			BodyNonce:     EncodeRawURL(randomBytes(nonceSize)),
		},
	}
}

type redeemFixture struct {
	Identity *Identity
	req      EnrollmentRedeemRequest
}

func validRedeem(t *testing.T) redeemFixture {
	t.Helper()
	id, err := NewIdentity()
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	return redeemFixture{
		Identity: id,
		req: EnrollmentRedeemRequest{
			Version:          EnrollmentVersion,
			RequestID:        "erq_test",
			CoreID:           "core_test",
			InvitationID:     "inv_test",
			InvitationSecret: "secret_test",
			BodyPublicKey:    id.PublicKeyRawURL(),
			WGPublicKey:      base64.StdEncoding.EncodeToString(randomBytes(nonceSize)),
			BodyNonce:        EncodeRawURL(randomBytes(nonceSize)),
		},
	}
}

type resultFixture struct {
	Identity *Identity
	req      EnrollmentResultRequest
}

func validResult(t *testing.T) resultFixture {
	t.Helper()
	id, err := NewIdentity()
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	return resultFixture{
		Identity: id,
		req: EnrollmentResultRequest{
			Version:       EnrollmentVersion,
			RequestID:     "erq_test",
			CoreID:        "core_test",
			BodyPublicKey: id.PublicKeyRawURL(),
			BodyNonce:     EncodeRawURL(randomBytes(nonceSize)),
		},
	}
}
