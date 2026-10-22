package enrollment

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// identityStub serves a configurable /v1/core/identity document.
type identityStub struct {
	id     *CoreIdentityResponse
	status int
}

func stub(id *CoreIdentityResponse) *identityStub {
	return &identityStub{id: id, status: 200}
}

func stubbingStatus(id *CoreIdentityResponse, status int) *identityStub {
	return &identityStub{id: id, status: status}
}

func (s *identityStub) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/core/identity", s.handle)
	return mux
}

func (s *identityStub) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if s.status != 200 {
		http.Error(w, "unavailable", s.status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(*s.id)
}

func validIdentity() (*CoreIdentityResponse, string) {
	corePub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	return &CoreIdentityResponse{
		Version:   CoreIdentityVersion,
		CoreID:    "core_discovery_test",
		Algorithm: CoreIdentityAlgorithm,
		PublicKey: EncodeRawURL(corePub),
	}, "core_discovery_test"
}

func nowContext() (context.Context, func()) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func TestDiscoverIdentityHappyPath(t *testing.T) {
	id, coreID := validIdentity()
	srv := httptest.NewServer(stub(id).handler())
	defer srv.Close()
	ctx, cancel := nowContext()
	defer cancel()
	got, err := DiscoverCoreIdentity(ctx, nil, srv.URL)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if got.CoreID != coreID || got.PublicKey != id.PublicKey {
		t.Fatalf("unexpected identity: %+v", got)
	}
}

func TestDiscoverIdentityRejectsWrongVersion(t *testing.T) {
	id, _ := validIdentity()
	id.Version = 2
	srv := httptest.NewServer(stub(id).handler())
	defer srv.Close()
	ctx, cancel := nowContext()
	defer cancel()
	_, err := DiscoverCoreIdentity(ctx, nil, srv.URL)
	if err == nil || !errors.Is(err, ErrCoreIdentityRejected) {
		t.Fatalf("expected identity rejection, got %v", err)
	}
}

func TestDiscoverIdentityRejectsWrongAlgorithm(t *testing.T) {
	id, _ := validIdentity()
	id.Algorithm = "RSA"
	srv := httptest.NewServer(stub(id).handler())
	defer srv.Close()
	ctx, cancel := nowContext()
	defer cancel()
	_, err := DiscoverCoreIdentity(ctx, nil, srv.URL)
	if err == nil || !errors.Is(err, ErrCoreIdentityRejected) {
		t.Fatalf("expected identity rejection, got %v", err)
	}
}

func TestDiscoverIdentityRejectsMalformedKey(t *testing.T) {
	id, _ := validIdentity()
	id.PublicKey = "!!!not-base64url!!!"
	srv := httptest.NewServer(stub(id).handler())
	defer srv.Close()
	ctx, cancel := nowContext()
	defer cancel()
	_, err := DiscoverCoreIdentity(ctx, nil, srv.URL)
	if err == nil || !errors.Is(err, ErrCoreIdentityRejected) {
		t.Fatalf("expected identity rejection, got %v", err)
	}
}

func TestDiscoverIdentityRejectsShortKey(t *testing.T) {
	id, _ := validIdentity()
	// Valid base64url that decodes to 16 bytes, not the required 32.
	id.PublicKey = EncodeRawURL(make([]byte, 16))
	srv := httptest.NewServer(stub(id).handler())
	defer srv.Close()
	ctx, cancel := nowContext()
	defer cancel()
	_, err := DiscoverCoreIdentity(ctx, nil, srv.URL)
	if err == nil || !errors.Is(err, ErrCoreIdentityRejected) {
		t.Fatalf("expected identity rejection, got %v", err)
	}
}

func TestDiscoverIdentityRejectsWrongHttpStatus(t *testing.T) {
	id, _ := validIdentity()
	srv := httptest.NewServer(stubbingStatus(id, 404).handler())
	defer srv.Close()
	ctx, cancel := nowContext()
	defer cancel()
	_, err := DiscoverCoreIdentity(ctx, nil, srv.URL)
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if errors.Is(err, ErrCoreIdentityRejected) {
		t.Fatalf("404 should surface as transport/unavailable, not identity rejection: %v", err)
	}
}

func TestValidateCoreIdentityRequiresCoreID(t *testing.T) {
	id, _ := validIdentity()
	id.CoreID = ""
	err := ValidateCoreIdentity(*id)
	if err == nil {
		t.Fatal("expected rejection for empty core_id")
	}
}

// --- Trust-anchor resolution policy (ResolveCoreTrust) ---

func genKey(t *testing.T) ([]byte, string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, EncodeRawURL(pub)
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// minimalRecord builds a valid persisted enrollment Record (as the production
// store writes one) so ResolveCoreTrust's never-replace policy reads CoreID /
// CorePublicKey from a realistic record.
func minimalRecord(coreID, pub string) *Record {
	return &Record{
		CoreID:                   coreID,
		CorePublicKey:            pub,
		BodyID:                   "body_00000000000000000000000000000000",
		BodyPublicKeyFingerprint: "fp_test",
		MembershipReference:      "peer_test",
		Permissions:              []string{"terminal"},
		Receipt: EnrollmentReceipt{
			Version:                  EnrollmentVersion,
			RequestID:                "req_test",
			Status:                   StatusEnrolled,
			BodyID:                   "body_00000000000000000000000000000000",
			CoreID:                   coreID,
			BodyPublicKeyFingerprint: "fp_test",
			Permissions:              []string{"terminal"},
			MembershipReference:      "peer_test",
			IssuedAt:                 time.Now(),
		},
		EnrolledAt: time.Now(),
	}
}

// TestResolveTrustDiscoveryPath: with no explicit key and no existing record,
// the discovered document is the resolved trust anchor.
func TestResolveTrustDiscoveryPath(t *testing.T) {
	id, _ := validIdentity()
	srv := httptest.NewServer(stub(id).handler())
	defer srv.Close()
	ctx, cancel := nowContext()
	defer cancel()
	res, err := ResolveCoreTrust(ctx, nil, srv.URL, "", "", nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.CoreID != id.CoreID {
		t.Fatalf("resolved core id %s, want %s", res.CoreID, id.CoreID)
	}
	want, _ := DecodeRawURL(id.PublicKey)
	if !bytesEqual(res.PublicKey, want) {
		t.Fatalf("resolved key does not match discovered document")
	}
}

// TestResolveTrustExplicitKeyMatched: an explicit --core-key equal to the
// discovered key is accepted (cross-check passes).
func TestResolveTrustExplicitKeyMatched(t *testing.T) {
	id, _ := validIdentity()
	srv := httptest.NewServer(stub(id).handler())
	defer srv.Close()
	ctx, cancel := nowContext()
	defer cancel()
	res, err := ResolveCoreTrust(ctx, nil, srv.URL, "", id.PublicKey, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.CoreID != id.CoreID {
		t.Fatalf("resolved core id %s, want %s", res.CoreID, id.CoreID)
	}
}

// TestResolveTrustExplicitKeyMismatch: an explicit --core-key that differs from
// the discovered key is a hard rejection (identity mismatch).
func TestResolveTrustExplicitKeyMismatch(t *testing.T) {
	id, _ := validIdentity()
	_, otherKey := genKey(t)
	srv := httptest.NewServer(stub(id).handler())
	defer srv.Close()
	ctx, cancel := nowContext()
	defer cancel()
	_, err := ResolveCoreTrust(ctx, nil, srv.URL, "", otherKey, nil)
	if err == nil {
		t.Fatal("expected rejection for mismatched explicit key")
	}
}

// TestResolveTrustExplicitCoreIDMismatch: an explicit --core-id that differs
// from the discovered Core ID is a hard rejection.
func TestResolveTrustExplicitCoreIDMismatch(t *testing.T) {
	id, _ := validIdentity()
	srv := httptest.NewServer(stub(id).handler())
	defer srv.Close()
	ctx, cancel := nowContext()
	defer cancel()
	_, err := ResolveCoreTrust(ctx, nil, srv.URL, "core_other", "", nil)
	if err == nil {
		t.Fatal("expected rejection for mismatched explicit core id")
	}
}

// TestResolveTrustNeverReplacesDifferentCoreID: an existing record pinned to a
// DIFFERENT core_id is never silently replaced.
func TestResolveTrustNeverReplacesDifferentCoreID(t *testing.T) {
	id, _ := validIdentity()
	srv := httptest.NewServer(stub(id).handler())
	defer srv.Close()
	ctx, cancel := nowContext()
	defer cancel()
	existing := minimalRecord("core_other", id.PublicKey)
	_, err := ResolveCoreTrust(ctx, nil, srv.URL, "", "", existing)
	if err == nil {
		t.Fatal("expected rejection: existing record pinned to a different core")
	}
}

// TestResolveTrustNeverReplacesDifferentKey: an existing record with a
// DIFFERENT trust-anchor public key is never silently replaced.
func TestResolveTrustNeverReplacesDifferentKey(t *testing.T) {
	id, _ := validIdentity()
	_, otherKey := genKey(t)
	srv := httptest.NewServer(stub(id).handler())
	defer srv.Close()
	ctx, cancel := nowContext()
	defer cancel()
	existing := minimalRecord(id.CoreID, otherKey)
	_, err := ResolveCoreTrust(ctx, nil, srv.URL, "", "", existing)
	if err == nil {
		t.Fatal("expected rejection: existing record pins a different key")
	}
}

// TestResolveTrustSameExistingAnchorAllowed: re-enrolling to the SAME Core
// (matching existing anchor) is permitted.
func TestResolveTrustSameExistingAnchorAllowed(t *testing.T) {
	id, _ := validIdentity()
	srv := httptest.NewServer(stub(id).handler())
	defer srv.Close()
	ctx, cancel := nowContext()
	defer cancel()
	existing := minimalRecord(id.CoreID, id.PublicKey)
	res, err := ResolveCoreTrust(ctx, nil, srv.URL, "", "", existing)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.CoreID != id.CoreID {
		t.Fatalf("resolved core id %s, want %s", res.CoreID, id.CoreID)
	}
}

// TestResolveTrustOfflineNoTofu: with no explicit anchor, an unreachable Core
// (discovery down) must NOT silently fall back to trust-on-first-use.
func TestResolveTrustOfflineNoTofu(t *testing.T) {
	id, _ := validIdentity()
	srv := httptest.NewServer(stubbingStatus(id, 503).handler())
	defer srv.Close()
	ctx, cancel := nowContext()
	defer cancel()
	_, err := ResolveCoreTrust(ctx, nil, srv.URL, "", "", nil)
	if err == nil {
		t.Fatal("expected rejection: discovery down must not auto-trust")
	}
}

// TestResolveTrustOfflineExplicitAnchor: with an EXPLICIT key + core id,
// an unreachable Core falls back to the operator-supplied anchor (the documented
// offline path), never on its own.
func TestResolveTrustOfflineExplicitAnchor(t *testing.T) {
	id, _ := validIdentity()
	_, explicitKey := genKey(t)
	srv := httptest.NewServer(stubbingStatus(id, 503).handler())
	defer srv.Close()
	ctx, cancel := nowContext()
	defer cancel()
	res, err := ResolveCoreTrust(ctx, nil, srv.URL, "core_explicit", explicitKey, nil)
	if err != nil {
		t.Fatalf("offline explicit resolve: %v", err)
	}
	if res.CoreID != "core_explicit" {
		t.Fatalf("resolved core id %s, want core_explicit", res.CoreID)
	}
	want, _ := DecodeRawURL(explicitKey)
	if !bytesEqual(res.PublicKey, want) {
		t.Fatalf("offline explicit key not used")
	}
}

// TestResolveTrustOfflineNeverReplacesDifferentKey (regression): the offline
// explicit-anchor fallback must be subject to the SAME pinning checks as the
// discovery path. With Core unreachable, an explicit anchor must still never
// silently redirect an existing enrollment pinned to a different key.
func TestResolveTrustOfflineNeverReplacesDifferentKey(t *testing.T) {
	id, _ := validIdentity()
	_, explicitKey := genKey(t)
	_, storedOtherKey := genKey(t)
	srv := httptest.NewServer(stubbingStatus(id, 503).handler())
	defer srv.Close()
	ctx, cancel := nowContext()
	defer cancel()
	existing := minimalRecord("core_explicit", storedOtherKey)
	_, err := ResolveCoreTrust(ctx, nil, srv.URL, "core_explicit", explicitKey, existing)
	if err == nil {
		t.Fatal("expected rejection: offline fallback must not replace an existing pinned key")
	}
}

// TestResolveTrustMalformedStoredKeyFailsClosed (regression): a persisted but
// malformed CorePublicKey must never be silently ignored at resolution time.
// The resolution fails closed on the corrupt stored anchor rather than
// proceeding against an unverifiable trust anchor.
func TestResolveTrustMalformedStoredKeyFailsClosed(t *testing.T) {
	id, _ := validIdentity()
	srv := httptest.NewServer(stub(id).handler())
	defer srv.Close()
	ctx, cancel := nowContext()
	defer cancel()
	existing := minimalRecord(id.CoreID, "!!!not-a-pubkey!!!")
	_, err := ResolveCoreTrust(ctx, nil, srv.URL, "", "", existing)
	if err == nil {
		t.Fatal("expected rejection: malformed persisted trust anchor must fail closed")
	}
}
