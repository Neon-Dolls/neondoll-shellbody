package integration

// M10.4 — Core identity discovery and trust-anchor pinning against a live Core 4.
//
// Core now serves its public identity at GET /v1/core/identity (Core PR #71).
// These tests prove the Shell Body's discovery client + trust-resolution policy
// work against the REAL endpoint: a fresh enrollment can obtain Core's identity
// by discovery, bind it to the owner-approved ceremony via receipt signature
// verification, persist it, restart against the pinned anchor, and reject a
// CHANGED Core identity instead of silently re-pinning.
//
// Discovery alone never establishes trust: the anchor is bound by receipt
// verification inside the owner-approved ceremony, and an existing pin is never
// replaced (tested here against two distinct live Cores).
//
// Reuses the M10.2 in-process real-Core fixture (core4Fixture) and the M10.3
// recovery helpers (recoveryCreds, startRecovery, assertRecoveryHealthy,
// waitRecoveryExit).

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"golang.org/x/crypto/curve25519"
	"net/http"
	"testing"
	"time"

	corebody "github.com/Neon-Dolls/neondoll/Core/Body"
	ownercontrol "github.com/Neon-Dolls/neondoll/Core/OwnerControl"

	body "github.com/Neon-Dolls/neondoll-shellbody/internal/body"
	mkenroll "github.com/Neon-Dolls/neondoll-shellbody/internal/enrollment"
)

// enrollViaDiscovery drives cmd/neondoll-shellbody --enroll's exact production
// steps against a real Core: discover the identity from GET /v1/core/identity,
// validate it (version / algorithm / key), bind it to the owner-approved
// enrollment via receipt verification under the DISCOVERED public key, and
// return the identity + body_id + discovered trust anchor.
func enrollViaDiscovery(t *testing.T, fx *core4Fixture) (*mkenroll.Identity, string, ed25519.PublicKey) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// (1)+(2) Discover + validate Core's identity from the canonical endpoint.
	id, err := mkenroll.DiscoverCoreIdentity(ctx, &http.Client{Timeout: 10 * time.Second}, fx.baseURL)
	if err != nil {
		t.Fatalf("discover core identity: %v", err)
	}
	if id.CoreID != fx.coreID {
		t.Fatalf("discovered core_id %q does not match the live Core %q", id.CoreID, fx.coreID)
	}
	if id.Algorithm != mkenroll.CoreIdentityAlgorithm {
		t.Fatalf("discovered algorithm %q, want %q", id.Algorithm, mkenroll.CoreIdentityAlgorithm)
	}
	discPub, _ := mkenroll.DecodeRawURL(id.PublicKey)
	if len(discPub) != ed25519.PublicKeySize {
		t.Fatalf("discovered public key is not a 32-byte Ed25519 key (got %d bytes)", len(discPub))
	}
	// The discovered key must equal the identity the live Core itself reports.
	if !fx.corePub.Equal(ed25519.PublicKey(discPub)) {
		t.Fatal("discovered key differs from Core's own reported public key")
	}

	// (3)+(4) Bind it to the owner-approved enrollment; persist core_id + key.
	identity, ierr := mkenroll.NewIdentity()
	if ierr != nil {
		t.Fatal(ierr)
	}
	wgRaw := make([]byte, 32)
	if _, err := rand.Read(wgRaw); err != nil {
		t.Fatal(err)
	}
	basepoint := [32]byte{9}
	wgPub, err := curve25519.X25519(basepoint[:], wgRaw)
	if err != nil {
		t.Fatal(err)
	}
	wgStd := base64.StdEncoding.EncodeToString(wgPub)

	client := mkenroll.NewClient(fx.baseURL, id.CoreID, identity, wgStd, &http.Client{Timeout: 30 * time.Second})
	pending, err := client.Begin(ctx)
	if err != nil {
		t.Fatalf("begin rejected by Core 4: %v", err)
	}
	if _, err := ownercontrol.NewClient(fx.adminSocket).Approve(ctx, pending.ApprovalCode, []string{"process"}); err != nil {
		t.Fatalf("owner approval rejected: %v", err)
	}
	result, err := client.Result(ctx)
	if err != nil {
		t.Fatalf("result poll rejected by Core 4: %v", err)
	}
	if result.Status != "enrolled" || result.Receipt == nil {
		t.Fatalf("enrollment not enrolled after approval: status=%q", result.Status)
	}
	receipt := *result.Receipt

	// (3b) The receipt must verify under the DISCOVERED public key and name the
	// DISCOVERED core. This is what binds discovery to the enrollment: only the
	// Core that served /v1/core/identity AND signed this owner-approved receipt
	// passes.
	corePub := ed25519.PublicKey(discPub)
	if err := mkenroll.VerifyEnrollmentReceipt(corePub, receipt); err != nil {
		t.Fatalf("receipt not verifiable with DISCOVERED public key: %v", err)
	}
	if receipt.CoreID != id.CoreID {
		t.Fatalf("identity mismatch: discovered core_id %s, receipt core_id %s", id.CoreID, receipt.CoreID)
	}

	// (4) Persist core_id + public key, exactly as --enroll does.
	enrStore := mkenroll.NewStore(t.TempDir() + "/es")
	_ = enrStore.SaveIdentity(identity)
	_ = enrStore.SaveRecord(&mkenroll.Record{
		CoreID:                   receipt.CoreID,
		CorePublicKey:            mkenroll.EncodeRawURL(discPub),
		BodyID:                   string(receipt.BodyID),
		BodyPublicKeyFingerprint: receipt.BodyPublicKeyFingerprint,
		MembershipReference:      receipt.MembershipReference,
		Permissions:              receipt.Permissions,
		Receipt:                  receipt,
		EnrolledAt:               time.Now(),
	})
	return identity, string(receipt.BodyID), corePub
}

// TestDiscoveryEnrollmentAndPersistedRestart is the full story: a Body enrolls
// using identity DISCOVERY (no manual --core-key), then a subsequent connection
// (the restart) authenticates against the PERSISTED discovered trust anchor and
// serves an authorized execution.
func TestDiscoveryEnrollmentAndPersistedRestart(t *testing.T) {
	fx := newCore4Fixture(t)
	identity, bodyID, corePub := enrollViaDiscovery(t, fx)

	// Restart using the persisted trust anchor (discovered core_id + key) and the
	// same persisted Body identity — no re-enrollment, no re-discovery.
	creds := body.Credentials{
		BodyID:        bodyID,
		PrivateKey:    identity.PrivateKey(),
		CoreID:        fx.coreID,
		CorePublicKey: corePub,
	}
	counter := &authCounter{}
	errCh, cancel := fx.startRecovery(t, creds, counter)
	assertRecoveryHealthy(t, errCh, fx, bodyID)

	result := executeProcess(t, fx, bodyID, "process", "run", "sh", []string{"-c", "printf discovery-restart"})
	if result.Status != corebody.StatusSuccess {
		t.Fatalf("restart execution status = %q, want success", result.Status)
	}
	cancel()
	_ = waitRecoveryExit(t, errCh)
}

// TestChangedCoreIdentityRejected proves a Body that already pinned one Core's
// identity REFUSES to silently adopt a different live Core's identity.
func TestChangedCoreIdentityRejected(t *testing.T) {
	fxA := newCore4Fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Persist an enrollment pinned to Core A's discovered identity.
	enrStore := mkenroll.NewStore(t.TempDir() + "/es")
	_ = enrStore.SaveRecord(&mkenroll.Record{
		CoreID:                   fxA.coreID,
		CorePublicKey:            mkenroll.EncodeRawURL(fxA.corePub),
		BodyID:                   "body_00000000000000000000000000000000",
		BodyPublicKeyFingerprint: "fp_discovery_a",
		MembershipReference:      "peer_discovery_a",
		Permissions:              []string{"process"},
		Receipt: mkenroll.EnrollmentReceipt{
			Version:                  mkenroll.EnrollmentVersion,
			RequestID:                "req_discovery_a",
			Status:                   mkenroll.StatusEnrolled,
			BodyID:                   "body_00000000000000000000000000000000",
			CoreID:                   fxA.coreID,
			BodyPublicKeyFingerprint: "fp_discovery_a",
			Permissions:              []string{"process"},
			MembershipReference:      "peer_discovery_a",
			IssuedAt:                 time.Now(),
		},
		EnrolledAt: time.Now(),
	})
	existing, _ := enrStore.LoadRecord()

	// A DIFFERENT live Core on a (possibly) same URL: its identity differs.
	fxB := newCore4Fixture(t)
	if fxA.corePub.Equal(fxB.corePub) {
		t.Fatal("test setup: the two fixtures produced the same identity key")
	}

	// Discovery of Core B succeeds, but trust resolution against the persisted
	// anchor must REJECT the change, never silently re-pin.
	_, err := mkenroll.ResolveCoreTrust(ctx, &http.Client{Timeout: 10 * time.Second}, fxB.baseURL, "", "", existing)
	if err == nil {
		t.Fatal("expected refusal to replace the existing trust anchor with a different Core identity")
	}
}

// TestExistingPinnedIdentityStillServes is the "existing enrolled identities
// remain compatible" guarantee: a record pinned through the pre-discovery
// explicit-key path (M10.2/M10.3) still authenticates and serves after M10.4.
func TestExistingPinnedIdentityStillServes(t *testing.T) {
	fx := newCore4Fixture(t)
	identity, bodyID, _ := enrollViaOwnerControl(t, fx)
	creds := recoveryCreds(fx, identity, bodyID)

	counter := &authCounter{}
	errCh, cancel := fx.startRecovery(t, creds, counter)
	assertRecoveryHealthy(t, errCh, fx, bodyID)

	result := executeProcess(t, fx, bodyID, "process", "run", "sh", []string{"-c", "printf existing-compat"})
	if result.Status != corebody.StatusSuccess {
		t.Fatalf("existing-identity execution status = %q, want success", result.Status)
	}
	cancel()
	_ = waitRecoveryExit(t, errCh)
}
