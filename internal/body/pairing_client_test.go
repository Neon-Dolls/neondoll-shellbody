// Tests for the Shell Body pairing client: success path and failure coverage.
package body

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/dollnetwork"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/identity"
)

func testBodyMeta() identity.BodyMetadata {
	return identity.BodyMetadata{
		Implementation: "neondoll-shellbody",
		Platform:       "test",
		Architecture:   "test",
		Build:          "test",
	}
}

// newPairingStore creates a fresh Body store (identity + WG keypair).
func newPairingStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := store.CreateFresh("SparkBody", testBodyMeta()); err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}
	return store
}

// validPairResponse builds a well-formed canonical PairResponse.
func validPairResponse(t *testing.T) dollnetwork.PairResponse {
	t.Helper()
	coreKP, err := GenerateWgKeypair()
	if err != nil {
		t.Fatalf("GenerateWgKeypair: %v", err)
	}
	return dollnetwork.PairResponse{
		Version:         dollnetwork.ProtocolVersion,
		NetworkID:       "net_test_1",
		BodyPeerID:      "peer_body_1",
		BodyAddresses:   []string{"fd00::1/128"},
		CorePeerID:      "peer_core_1",
		CoreWGPublicKey: coreKP.PublicKeyBase64(),
		CoreAddresses:   []string{"fd00::2/128"},
	}
}

// validInvitation builds an invitation pointing at a direct bootstrap URL.
func validInvitation(now int64, bootstrapURL string) *dollnetwork.Invitation {
	return &dollnetwork.Invitation{
		Version:          dollnetwork.ProtocolVersion,
		InvitationID:     "inv_test_1",
		InvitationSecret: "invitation-secret-value",
		ExpiresAt:        time.Unix(now+3600, 0).UTC().Format(time.RFC3339),
		BootstrapEndpoints: []dollnetwork.BootstrapEndpoint{
			{URL: bootstrapURL},
		},
	}
}

// ── Success path ─────────────────────────────────────────────────────────

func TestPairWithInvitation_Success(t *testing.T) {
	store := newPairingStore(t)

	var gotReq dollnetwork.PairRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if !strings.HasSuffix(r.URL.Path, pairingEndpointPath) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("server read body: %v", err)
		}
		if err := json.Unmarshal(raw, &gotReq); err != nil {
			t.Errorf("server failed to parse PairRequest: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(validPairResponse(t))
	}))
	defer srv.Close()

	now := time.Now().Unix()
	res, err := PairWithInvitation(context.Background(), store, validInvitation(now, srv.URL), now, srv.Client())
	if err != nil {
		t.Fatalf("PairWithInvitation: %v", err)
	}
	if res == nil || res.Membership == nil {
		t.Fatal("expected non-nil PairResult with membership")
	}
	m := res.Membership
	if m.NetworkID != "net_test_1" || m.BodyPeerID != "peer_body_1" || m.CorePeerID != "peer_core_1" {
		t.Fatalf("membership mismatch: %+v", m)
	}
	if m.Status != MembershipActive {
		t.Errorf("status = %q, want active", m.Status)
	}

	// The request wire shape must be canonical: id/secret pass through, and
	// only the public WG key is carried (never the private key).
	if gotReq.Version != dollnetwork.ProtocolVersion {
		t.Errorf("request version = %d", gotReq.Version)
	}
	if gotReq.InvitationID != "inv_test_1" {
		t.Errorf("request invitation_id = %q", gotReq.InvitationID)
	}
	if gotReq.Secret != "invitation-secret-value" {
		t.Errorf("request secret not carried through")
	}
	if gotReq.Body.BodyID == "" || gotReq.Body.Implementation != "neondoll-shellbody" {
		t.Errorf("request body missing identity/implementation")
	}
	if gotReq.Network.WireGuardPublicKey == "" {
		t.Errorf("request missing wireguard public key")
	}
}

func TestPairWithInvitation_PersistsMembership(t *testing.T) {
	store := newPairingStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(validPairResponse(t))
	}))
	defer srv.Close()

	now := time.Now().Unix()
	res, err := PairWithInvitation(context.Background(), store, validInvitation(now, srv.URL), now, srv.Client())
	if err != nil {
		t.Fatalf("PairWithInvitation: %v", err)
	}
	persisted, err := store.LoadMembership()
	if err != nil {
		t.Fatalf("LoadMembership after success: %v", err)
	}
	if persisted.NetworkID != res.Membership.NetworkID || persisted.BodyPeerID != res.Membership.BodyPeerID {
		t.Fatalf("durable membership %+v does not match returned %+v", persisted, res.Membership)
	}
}

// ── Failure coverage ─────────────────────────────────────────────────────

func TestPairWithInvitation_MalformedInvitation(t *testing.T) {
	if _, err := LoadInvitation("{ not json"); err == nil {
		t.Fatal("LoadInvitation accepted malformed JSON")
	}
}

func TestPairWithInvitation_ExpiredInvitation(t *testing.T) {
	store := newPairingStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not be contacted for an expired invitation")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	now := time.Now().Unix()
	inv := validInvitation(now, srv.URL)
	inv.ExpiresAt = time.Unix(now-100, 0).UTC().Format(time.RFC3339)
	if _, err := PairWithInvitation(context.Background(), store, inv, now, srv.Client()); err == nil {
		t.Fatal("expired invitation should fail")
	}
}

func TestPairWithInvitation_DeniedByCore(t *testing.T) {
	store := newPairingStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(dollnetwork.PairErrorResponse{
			Version:  dollnetwork.ProtocolVersion,
			Error:    "authorization denied",
			Reason:   "authorization_denied",
			Consumed: true,
		})
	}))
	defer srv.Close()

	now := time.Now().Unix()
	if _, err := PairWithInvitation(context.Background(), store, validInvitation(now, srv.URL), now, srv.Client()); err == nil {
		t.Fatal("denied pairing should fail")
	}
	// Denial must not leave any membership.
	_, err := store.LoadMembership()
	if err == nil || !errors.Is(err, ErrStateNotFound) {
		t.Fatal("denied pairing left a membership")
	}
}

func TestPairWithInvitation_MalformedCoreResponse(t *testing.T) {
	store := newPairingStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(dollnetwork.PairResponse{
			Version: dollnetwork.ProtocolVersion,
			// Missing network_id, no addresses, etc.
		})
	}))
	defer srv.Close()

	now := time.Now().Unix()
	if _, err := PairWithInvitation(context.Background(), store, validInvitation(now, srv.URL), now, srv.Client()); err == nil {
		t.Fatal("malformed Core response should fail")
	}
	_, err := store.LoadMembership()
	if err == nil || !errors.Is(err, ErrStateNotFound) {
		t.Fatal("malformed response left a membership")
	}
}

func TestPairWithInvitation_InvalidWgPublicKey(t *testing.T) {
	store := newPairingStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(dollnetwork.PairResponse{
			Version:         dollnetwork.ProtocolVersion,
			NetworkID:       "net_test_1",
			BodyPeerID:      "peer_body_1",
			BodyAddresses:   []string{"fd00::1/128"},
			CorePeerID:      "peer_core_1",
			CoreWGPublicKey: "!!not-base64-or-32-bytes!!",
			CoreAddresses:   []string{"fd00::2/128"},
		})
	}))
	defer srv.Close()

	now := time.Now().Unix()
	if _, err := PairWithInvitation(context.Background(), store, validInvitation(now, srv.URL), now, srv.Client()); err == nil {
		t.Fatal("invalid Core WG key should fail")
	}
}

func TestPairWithInvitation_UnsupportedProtocolVersion(t *testing.T) {
	store := newPairingStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := validPairResponse(t)
		resp.Version = 2 // unsupported
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	now := time.Now().Unix()
	if _, err := PairWithInvitation(context.Background(), store, validInvitation(now, srv.URL), now, srv.Client()); err == nil {
		t.Fatal("unsupported protocol version should fail")
	}
}

func TestPairWithInvitation_ExistingMembershipRefusesRepair(t *testing.T) {
	store := newPairingStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(validPairResponse(t))
	}))
	defer srv.Close()

	now := time.Now().Unix()
	inv := validInvitation(now, srv.URL)
	if _, err := PairWithInvitation(context.Background(), store, inv, now, srv.Client()); err != nil {
		t.Fatalf("first pairing should succeed: %v", err)
	}
	// A second pairing against an already-paired Body must refuse, not replace.
	if _, err := PairWithInvitation(context.Background(), store, inv, now, srv.Client()); err == nil {
		t.Fatal("existing membership should refuse accidental re-pair")
	}
	// The original membership must be untouched.
	persisted, err := store.LoadMembership()
	if err != nil {
		t.Fatalf("LoadMembership: %v", err)
	}
	if persisted.NetworkID != "net_test_1" {
		t.Fatalf("existing membership was replaced: %+v", persisted)
	}
}

func TestPairWithInvitation_CorruptMembershipFailsClosed(t *testing.T) {
	store := newPairingStore(t)
	if err := os.MkdirAll(store.Dir(), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	f, err := os.OpenFile(store.membershipPath(), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open membership: %v", err)
	}
	if _, werr := f.Write([]byte("corrupt")); werr != nil {
		t.Fatalf("write corrupt membership: %v", werr)
	}
	_ = f.Close()

	// With a corrupt/ambiguous membership, pairing must fail closed rather than
	// assume unpaired and overwrite.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(validPairResponse(t))
	}))
	defer srv.Close()

	now := time.Now().Unix()
	if _, err := PairWithInvitation(context.Background(), store, validInvitation(now, srv.URL), now, srv.Client()); err == nil {
		t.Fatal("corrupt membership should fail closed")
	}
}

func TestPairWithInvitation_FailedPairingLeavesIdentityAndKeyUnchanged(t *testing.T) {
	store := newPairingStore(t)
	stBefore, kpBefore, err := store.LoadOrError()
	if err != nil {
		t.Fatalf("LoadOrError: %v", err)
	}
	bodyIDBefore := string(stBefore.Identity.BodyID)
	wgBefore := kpBefore.PublicKeyBase64()

	// Deny the pairing.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(dollnetwork.PairErrorResponse{
			Version:  dollnetwork.ProtocolVersion,
			Reason:   "authorization_denied",
			Consumed: true,
		})
	}))
	defer srv.Close()

	now := time.Now().Unix()
	if _, err := PairWithInvitation(context.Background(), store, validInvitation(now, srv.URL), now, srv.Client()); err == nil {
		t.Fatal("denied pairing should fail")
	}

	stAfter, kpAfter, err := store.LoadOrError()
	if err != nil {
		t.Fatalf("LoadOrError after failed pairing: %v", err)
	}
	if string(stAfter.Identity.BodyID) != bodyIDBefore {
		t.Fatalf("Body ID changed after failed pairing")
	}
	if kpAfter.PublicKeyBase64() != wgBefore {
		t.Fatalf("WG key changed after failed pairing")
	}
}

// ── Build helpers ────────────────────────────────────────────────────────

func TestBuildPairRequest_OmitsPrivateKey(t *testing.T) {
	store := newPairingStore(t)
	st, kp, err := store.LoadOrError()
	if err != nil {
		t.Fatalf("LoadOrError: %v", err)
	}
	kp2, err := GenerateWgKeypair()
	if err != nil {
		t.Fatalf("GenerateWgKeypair: %v", err)
	}
	req := BuildPairRequest("inv", "secret", st, kp2)
	wire, err := req.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON: %v", err)
	}
	privStr := string(kp.PrivateKeyBytes())
	if privStr != "" && strings.Contains(string(wire), privStr) {
		t.Fatal("pair request serialization leaked the private WG key")
	}
	// Private-key material must not appear as any wireguard_private_key field.
	if strings.Contains(string(wire), "private") {
		t.Fatal("pair request wire contains a private-key field")
	}
}
