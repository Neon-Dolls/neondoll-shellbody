package enrollment

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// coreStub mirrors Core's enrollment handler behavior well enough to prove the
// client constructs wire-valid, correctly-signed requests. (Cross-verification
// against the real Core 4 package is in test/m10_2_core4_conformance_test.go.)
type coreStub struct {
	corePub  ed25519.PublicKey
	corePriv ed25519.PrivateKey
	coreID   string
	receipt  EnrollmentReceipt
	seen     *int
	// lastNonce/secondLastNonce track recently-seen result nonces and
	// resultReplays mirrors Core's per-nonce anti-replay (ConsumeResultNonce):
	// a poll reusing a nonce must be rejected, proving fresh-nonce polling.
	lastNonce       string
	secondLastNonce string
	resultReplays   int
}

func newTestCoreStub(t *testing.T) *coreStub {
	t.Helper()
	corePub, corePriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("core key: %v", err)
	}
	s := &coreStub{corePub: corePub, corePriv: corePriv, coreID: "core_test", seen: new(int)}
	s.receipt = EnrollmentReceipt{
		Version:     EnrollmentVersion,
		RequestID:   "erq_test",
		Status:      StatusEnrolled,
		BodyID:      "body_00000000000000000000000000000000",
		CoreID:      s.coreID,
		IssuedAt:    time.Now().UTC().Truncate(time.Second),
		Permissions: []string{"terminal"},
	}
	return s
}

func (s *coreStub) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/body/enrollment/begin", s.handleBegin)
	mux.HandleFunc("/v1/body/enrollment/redeem", s.handleRedeem)
	mux.HandleFunc("/v1/body/enrollment/result", s.handleResult)
	return mux
}

func (s *coreStub) handleBegin(w http.ResponseWriter, r *http.Request) {
	*s.seen++
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var req EnrollmentBeginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := VerifyEnrollmentBegin(req); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid_proof", err.Error())
		return
	}
	resp := EnrollmentPendingResponse{
		Version:            EnrollmentVersion,
		RequestID:          req.RequestID,
		CoreID:             s.coreID,
		BodyKeyFingerprint: BodyKeyFingerprint(mustPub(req.BodyPublicKey)),
		CoreNonce:          EncodeRawURL(randomBytes(nonceSize)),
		ExpiresAt:          time.Now().UTC().Truncate(time.Second).Add(time.Minute),
	}
	if err := SignEnrollmentPending(s.corePriv, &resp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *coreStub) handleRedeem(w http.ResponseWriter, r *http.Request) {
	*s.seen++
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var req EnrollmentRedeemRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := VerifyEnrollmentRedeem(req); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid_proof", err.Error())
		return
	}
	receipt := s.receipt
	receipt.RequestID = req.RequestID
	receipt.CoreID = s.coreID
	receipt.BodyPublicKeyFingerprint = BodyKeyFingerprint(mustPub(req.BodyPublicKey))
	receipt.MembershipReference = "peer_" + req.InvitationID
	if err := SignEnrollmentReceipt(s.corePriv, &receipt); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(w).Encode(receipt)
}

func (s *coreStub) handleResult(w http.ResponseWriter, r *http.Request) {
	*s.seen++
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var req EnrollmentResultRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := VerifyEnrollmentResult(req); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid_proof", err.Error())
		return
	}
	// Anti-replay: Core refuses to consume a result nonce twice. Mirror that
	// so reusing a poll nonce fails the round trip exactly as Core would.
	if req.BodyNonce == s.lastNonce || req.BodyNonce == s.secondLastNonce {
		s.resultReplays++
		writeError(w, 409, "replayed", "reused result nonce")
		return
	}
	s.secondLastNonce = s.lastNonce
	s.lastNonce = req.BodyNonce
	receipt := s.receipt
	receipt.RequestID = req.RequestID
	receipt.CoreID = s.coreID
	receipt.BodyPublicKeyFingerprint = BodyKeyFingerprint(mustPub(req.BodyPublicKey))
	if err := SignEnrollmentReceipt(s.corePriv, &receipt); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(w).Encode(EnrollmentResultResponse{
		Version:   EnrollmentVersion,
		RequestID: req.RequestID,
		Status:    StatusEnrolled,
		Receipt:   &receipt,
	})
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(EnrollmentErrorResponse{
		Version: EnrollmentVersion,
		Error:   EnrollmentError{Code: code, Message: message},
	})
}

func mustPub(b64 string) ed25519.PublicKey {
	raw, err := DecodeRawURL(b64)
	if err != nil {
		panic(err)
	}
	return ed25519.PublicKey(raw)
}

func TestClientRedeemProducesReceipt(t *testing.T) {
	stub := newTestCoreStub(t)
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	id, err := NewIdentity()
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	client := NewClient(srv.URL, stub.coreID, id, base64.StdEncoding.EncodeToString(randomBytes(nonceSize)), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Begin returns a Core-signed challenge for the client's identity.
	pending, err := client.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if pending.CoreNonce == "" {
		t.Fatal("Core challenge missing core_nonce")
	}
	if err := VerifyEnrollmentPending(stub.corePub, *pending); err != nil {
		t.Fatalf("failed to verify Core-signed challenge: %v", err)
	}

	// Redeem path (the canonical register path) produces a signed receipt.
	receipt, err := client.Redeem(ctx, "inv_test", "secret_test")
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if receipt.BodyID == "" {
		t.Fatal("receipt has empty body_id (Core mints it)")
	}
	if receipt.BodyPublicKeyFingerprint != id.Fingerprint() {
		t.Fatalf("fingerprint mismatch: got %s want %s", receipt.BodyPublicKeyFingerprint, id.Fingerprint())
	}
	if err := VerifyEnrollmentReceipt(stub.corePub, *receipt); err != nil {
		t.Fatalf("failed to verify Core-signed receipt: %v", err)
	}

	// Result poll reuses the same request attempt and returns the receipt.
	res, err := client.Result(ctx)
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	if res.Status != StatusEnrolled || res.Receipt == nil {
		t.Fatalf("unexpected result status: %+v", res)
	}
	if *stub.seen < 3 {
		t.Fatalf("expected >=3 requests, saw %d", *stub.seen)
	}
}

func TestClientBeginThenResult(t *testing.T) {
	stub := newTestCoreStub(t)
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	id, err := NewIdentity()
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	client := NewClient(srv.URL, stub.coreID, id, base64.StdEncoding.EncodeToString(randomBytes(nonceSize)), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pending, err := client.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := VerifyEnrollmentPending(stub.corePub, *pending); err != nil {
		t.Fatalf("failed to verify Core-signed challenge: %v", err)
	}

	res, err := client.Result(ctx)
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	if res.Status != StatusEnrolled {
		t.Fatalf("expected enrolled, got %q", res.Status)
	}
}

// TestClientResultUsesFreshNoncePerPoll guards the Core 4 contract that the
// /v1/body/enrollment/result route consumes and anti-replays each poll nonce
// (ConsumeResultNonce). The client must reuse the pinned requestID but present
// a FRESH nonce on every poll; a prior implementation reused the Begin nonce,
// which the real Core rejects as `replayed` on the second poll. The stub
// mirrors Core by answering 409 when a poll nonce is repeated, so this test
// fails on the regression and passes once each poll mints a new nonce.
func TestClientResultUsesFreshNoncePerPoll(t *testing.T) {
	stub := newTestCoreStub(t)
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	id, err := NewIdentity()
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	client := NewClient(srv.URL, stub.coreID, id, base64.StdEncoding.EncodeToString(randomBytes(nonceSize)), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := client.Begin(ctx); err != nil {
		t.Fatalf("begin: %v", err)
	}
	for i := 0; i < 3; i++ {
		res, err := client.Result(ctx)
		if err != nil {
			t.Fatalf("result poll %d: %v", i, err)
		}
		if res.Status != StatusEnrolled {
			t.Fatalf("poll %d status = %q, want enrolled", i, res.Status)
		}
	}
	if stub.resultReplays != 0 {
		t.Fatalf("Result reused a poll nonce (real Core would 409): %d rejections", stub.resultReplays)
	}
}
