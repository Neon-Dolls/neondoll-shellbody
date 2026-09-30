// Real M2 rendezvous proof: the reference Shell Body (neondoll-shellbody,
// independently implemented) pairs against Rin's real NeonDoll Core M2
// implementation (Core/Invitation + Core/Pairing /v1/pair handler) over a
// real HTTP server, using a fresh Body identity + WG keypair. It proves that
// the Shell Body and Core agree on the canonical membership after a live
// rendezvous, that Core activates the membership, and that the Shell Body's
// identity + WG keypair survive pairing unchanged.
//
// This test imports Core packages — that is the one place the Shell Body's
// test suite may do so. Production Shell Body code (internal/*, cmd/*) never
// imports the neondoll repository. It runs as part of `go test -race ./...`
// when the sibling workspace provides the real Core (see the repository
// README for the required checkout layout).
package integration

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/body"
	sbdoll "github.com/Neon-Dolls/neondoll-shellbody/internal/dollnetwork"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/identity"
	"github.com/Neon-Dolls/neondoll/Core/Invitation"
	corenetwork "github.com/Neon-Dolls/neondoll/Core/Network"
	"github.com/Neon-Dolls/neondoll/Core/Pairing"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	dollnetwork "github.com/Neon-Dolls/neondoll/DollNetwork"
)

// testNetStore opens a real SQLite-backed Core NetworkStore in a temp dir.
func testNetStore(t *testing.T, dir string) corenetwork.NetworkStore {
	t.Helper()
	s, err := persistence.NewStore(dir + "/core_state.db")
	if err != nil {
		t.Fatalf("persistence.NewStore: %v", err)
	}
	ns, ok := s.(corenetwork.NetworkStore)
	if !ok {
		t.Fatalf("persistence Store %T does not implement network.NetworkStore", s)
	}
	return ns
}

// TestShellBodyPairsWithRealCoreRendezvous proves the full live rendezvous
// between the Shell Body and the real Core, and that durable Shell Body state
// (identity + WG keypair + membership) survives a simulated restart.
func TestShellBodyPairsWithRealCoreRendezvous(t *testing.T) {
	// ── Real Core: network + SQLite-backed persistence store ────────────
	netID := corenetwork.NetworkID("shellbody-rendezvous-net-" + strconv.FormatInt(time.Now().UnixNano(), 10))
	net, err := corenetwork.NewNetwork(netID)
	if err != nil {
		t.Fatalf("network.NewNetwork: %v", err)
	}
	coreNetStore := testNetStore(t, t.TempDir())

	// ── Real Core: invitation service (fresh, real clock) ───────────────
	clock := invitation.RealClock{}
	invSvc := invitation.NewService(invitation.NewMemoryStore(), clock)

	// ── Real Core: pairing service + real /v1/pair HTTP handler ────────
	allowAll := pairing.AllowAuthorizer{}
	coreEndpoints := dollnetwork.Endpoints{{URL: "http://core-endpoint.invalid"}}
	pairingSvc := pairing.NewService(net, coreNetStore, invSvc, allowAll, clock, coreEndpoints)
	handler := pairing.NewHandler(pairingSvc, slog.Default())

	srv := httptest.NewServer(http.HandlerFunc(handler.HandlePair))
	defer srv.Close()

	// ── Core creates a real invitation pointing back at its own origin ─
	created, err := invSvc.Create(context.Background(), invitation.CreateParams{
		Lifetime:           invitation.DefaultLifetime,
		BootstrapEndpoints: dollnetwork.Endpoints{{URL: srv.URL}},
	})
	if err != nil {
		t.Fatalf("invSvc.Create: %v", err)
	}

	// Translate the Core-created invitation into the Shell Body's OWN
	// canonical wire type (independent implementation): copy the scalar
	// identity fields — id, secret, expiry (RFC3339 string), and the direct
	// bootstrap URL — rather than importing the Core type into the client.
	var boots []sbdoll.BootstrapEndpoint
	for _, be := range created.BootstrapEndpoints {
		boots = append(boots, sbdoll.BootstrapEndpoint{URL: be.URL})
	}
	if len(boots) == 0 {
		t.Fatal("Core invitation has no bootstrap endpoints to mirror")
	}
	inv := &sbdoll.Invitation{
		Version:            sbdoll.ProtocolVersion,
		InvitationID:       created.ID,
		InvitationSecret:   created.Secret,
		ExpiresAt:          created.ExpiresAt.Format(time.RFC3339),
		BootstrapEndpoints: boots,
	}

	// ── Shell Body: fresh persistent store (identity + WG keypair) ─────
	stateDir := t.TempDir() + "/body"
	store, err := body.NewStore(stateDir)
	if err != nil {
		t.Fatalf("body.NewStore: %v", err)
	}
	if _, err := store.CreateFresh("RendezvousShell", identity.BodyMetadata{
		Implementation: "neondoll-shellbody",
		Platform:       "test",
		Architecture:   "test",
		Build:          "integration",
	}); err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}

	stBefore, kpBefore, err := store.LoadOrError()
	if err != nil || stBefore == nil || kpBefore == nil {
		t.Fatalf("LoadOrError before pairing: %v", err)
	}
	bodyIDBefore := string(stBefore.Identity.BodyID)
	wgPubBefore := kpBefore.PublicKeyBase64()

	// ── Live rendezvous: Shell Body posts a canonical PairRequest ──────
	now := clock.Now().Unix()
	res, err := body.PairWithInvitation(context.Background(), store, inv, now, srv.Client())
	if err != nil {
		t.Fatalf("PairWithInvitation against real Core: %v", err)
	}
	if res == nil || res.Membership == nil {
		t.Fatal("expected non-nil membership from live rendezvous")
	}
	m := res.Membership

	// ── State read back exactly as Core the address-authority assigned ─
	coreMember := net.GetMembership(corenetwork.PeerID(m.BodyPeerID))
	if coreMember == nil {
		t.Fatalf("Core does not know body_peer_id %q", m.BodyPeerID)
	}

	// Agreement on the network ID.
	if m.NetworkID != string(net.NetworkID) {
		t.Errorf("network_id: Body=%q Core=%q", m.NetworkID, net.NetworkID)
	}
	// Agreement on the Body peer ID.
	if string(coreMember.PeerID) != m.BodyPeerID {
		t.Errorf("body peer id: Body=%q Core=%q", m.BodyPeerID, coreMember.PeerID)
	}
	// Agreement on the Body overlay IPv6 (bare address, no /mask).
	if m.BodyIPv6 != coreMember.OverlayAddress.String() {
		t.Errorf("body_ipv6: Body=%q Core=%q", m.BodyIPv6, coreMember.OverlayAddress.String())
	}
	// Agreement on the Core peer ID.
	if m.CorePeerID != string(net.Core.PeerID) {
		t.Errorf("core_peer_id: Body=%q Core=%q", m.CorePeerID, net.Core.PeerID)
	}
	// Agreement on the Core WG public key.
	coreWG, err := dollnetwork.EncodeWgPublicKey(net.Core.PublicKey[:])
	if err != nil {
		t.Fatalf("EncodeWgPublicKey(core pub): %v", err)
	}
	if m.CoreWGKeyB64 != coreWG {
		t.Errorf("core_wg_public_key: Body=%q Core=%q", m.CoreWGKeyB64, coreWG)
	}

	if m.BodyIPv6 == "" || m.CorePeerID == "" || m.CoreWGKeyB64 == "" {
		t.Error("membership missing a required rendezvous field")
	}

	// ── Core created + activated the membership ─────────────────────────
	if coreMember.Status != corenetwork.MembershipActive {
		t.Errorf("Core membership status = %v, want active", coreMember.Status)
	}

	// ── Not-replaced / no accidental re-pair: identity + key unchanged ──
	stAfter, kpAfter, err := store.LoadOrError()
	if err != nil {
		t.Fatalf("LoadOrError after pairing: %v", err)
	}
	if string(stAfter.Identity.BodyID) != bodyIDBefore {
		t.Errorf("Body ID changed across rendezvous: %q → %q", bodyIDBefore, string(stAfter.Identity.BodyID))
	}
	if kpAfter.PublicKeyBase64() != wgPubBefore {
		t.Errorf("WG public key changed across rendezvous")
	}

	// ── Durable membership agrees with the returned membership ──────────
	persisted, err := store.LoadMembership()
	if err != nil {
		t.Fatalf("LoadMembership: %v", err)
	}
	if persisted.NetworkID != m.NetworkID || persisted.BodyPeerID != m.BodyPeerID ||
		persisted.CorePeerID != m.CorePeerID || persisted.CoreWGKeyB64 != m.CoreWGKeyB64 {
		t.Errorf("durable membership %+v does not match returned membership %+v", persisted, m)
	}
}

// TestShellBodyReplayRejectedByRealCore proves that reusing an already-consumed
// invitation against the real Core is rejected (invitation_already_consumed),
// after a first pairing consumed it.
func TestShellBodyReplayRejectedByRealCore(t *testing.T) {
	netID := corenetwork.NetworkID("shellbody-replay-net-" + strconv.FormatInt(time.Now().UnixNano(), 10))
	net, err := corenetwork.NewNetwork(netID)
	if err != nil {
		t.Fatalf("network.NewNetwork: %v", err)
	}
	coreNetStore := testNetStore(t, t.TempDir())
	clock := invitation.RealClock{}
	invSvc := invitation.NewService(invitation.NewMemoryStore(), clock)
	allowAll := pairing.AllowAuthorizer{}
	coreEndpoints := dollnetwork.Endpoints{{URL: "http://core-endpoint.invalid"}}
	pairingSvc := pairing.NewService(net, coreNetStore, invSvc, allowAll, clock, coreEndpoints)
	handler := pairing.NewHandler(pairingSvc, slog.Default())
	srv := httptest.NewServer(http.HandlerFunc(handler.HandlePair))
	defer srv.Close()

	created, err := invSvc.Create(context.Background(), invitation.CreateParams{
		Lifetime:           invitation.DefaultLifetime,
		BootstrapEndpoints: dollnetwork.Endpoints{{URL: srv.URL}},
	})
	if err != nil {
		t.Fatalf("invSvc.Create: %v", err)
	}
	var boots []sbdoll.BootstrapEndpoint
	for _, be := range created.BootstrapEndpoints {
		boots = append(boots, sbdoll.BootstrapEndpoint{URL: be.URL})
	}
	inv := &sbdoll.Invitation{
		Version:            sbdoll.ProtocolVersion,
		InvitationID:       created.ID,
		InvitationSecret:   created.Secret,
		ExpiresAt:          created.ExpiresAt.Format(time.RFC3339),
		BootstrapEndpoints: boots,
	}

	mkBody := func() *body.Store {
		store, err := body.NewStore(t.TempDir() + "/body")
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		if _, err := store.CreateFresh("ReplayShell", identity.BodyMetadata{
			Implementation: "neondoll-shellbody",
			Platform:       "test",
			Architecture:   "test",
			Build:          "integration",
		}); err != nil {
			t.Fatalf("CreateFresh: %v", err)
		}
		return store
	}

	// First pairing succeeds and consumes the invitation.
	now := clock.Now().Unix()
	if _, err := body.PairWithInvitation(context.Background(), mkBody(), inv, now, srv.Client()); err != nil {
		t.Fatalf("first pairing should succeed: %v", err)
	}

	// Replay: a different fresh Body reuses the SAME (now consumed) invitation.
	if _, err := body.PairWithInvitation(context.Background(), mkBody(), inv, now+1, srv.Client()); err == nil {
		t.Fatal("replayed consumed invitation should be rejected by the real Core")
	} else if !strings.Contains(err.Error(), "invitation_already_consumed") {
		t.Errorf("replay error %v, want invitation_already_consumed", err)
	}
}
