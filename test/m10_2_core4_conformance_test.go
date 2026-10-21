package integration

// M10.2 — Shell Body (independent reimplementation) → NeonDoll Core 4
// conformance.
//
// NeonDoll Core defines the protocol; Shell Body adapts. This is the ONE place
// Shell Body's test suite is allowed to import the neondoll repository (the
// same carve-out rendezvous_test.go uses) so it can prove that Shell Body's
// independently implemented enrollment signing and Doll Link wire messages are
// byte-for-byte accepted by Core 4's canonical structures and its real
// enrollment service.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	enrollment "github.com/Neon-Dolls/neondoll/Core/Enrollment"
	invitation "github.com/Neon-Dolls/neondoll/Core/Invitation"
	network "github.com/Neon-Dolls/neondoll/Core/Network"
	persistence "github.com/Neon-Dolls/neondoll/Core/Persistence"
	capabilities "github.com/Neon-Dolls/neondoll/DollLink/Capabilities"
	execution "github.com/Neon-Dolls/neondoll/DollLink/Execution"
	dollnetwork "github.com/Neon-Dolls/neondoll/DollNetwork"

	mkenroll "github.com/Neon-Dolls/neondoll-shellbody/internal/enrollment"
	exec4 "github.com/Neon-Dolls/neondoll-shellbody/internal/execution"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/execution/process"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/link/core4"
)

type registerMux struct{ m *http.ServeMux }

func (r registerMux) RegisterHandler(path string, h http.HandlerFunc) { r.m.HandleFunc(path, h) }

func (r registerMux) ServeHTTP(w http.ResponseWriter, req *http.Request) { r.m.ServeHTTP(w, req) }

// enrollmentFixture wires a real Core 4 enrollment service exactly as Core's
// own service_integration_test.go harness does, and exposes it over HTTP.
type enrollmentFixture struct {
	service *enrollment.Service
	invSvc  *invitation.Service
	net     *network.Network
	srv     *httptest.Server
}

func newEnrollmentFixture(t *testing.T) *enrollmentFixture {
	t.Helper()
	ctx := context.Background()
	store, err := persistence.NewStore(t.TempDir() + "/core.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	net, err := network.NewNetwork("m10-2-conformance-net")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.(network.NetworkStore).SaveNetwork(ctx, net); err != nil {
		t.Fatal(err)
	}

	clock := invitation.RealClock{}
	invSvc := invitation.NewService(store.(invitation.Store), clock)

	svc, err := enrollment.NewService(ctx, store.(enrollment.Store), net, invSvc,
		enrollment.AllowOwnerAuthorizer{}, clock, enrollment.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}

	mux := registerMux{m: http.NewServeMux()}
	enrollment.RegisterBodyEndpoints(mux, svc)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return &enrollmentFixture{service: svc, invSvc: invSvc, net: net, srv: srv}
}

func (f *enrollmentFixture) newInvitation(t *testing.T) (id, secret string) {
	t.Helper()
	created, err := f.invSvc.Create(context.Background(), invitation.CreateParams{
		Lifetime:           invitation.DefaultLifetime,
		BootstrapEndpoints: dollnetwork.Endpoints{{URL: f.srv.URL}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return created.ID, created.Secret
}

// TestShellBodyEnrollmentConformsToCore4 runs Shell Body's independent
// enrollment client against a REAL Core 4 enrollment service over HTTP and
// asserts Core accepts the signed transcript and mints an enrolled identity.
func TestShellBodyEnrollmentConformsToCore4(t *testing.T) {
	fx := newEnrollmentFixture(t)
	invID, invSecret := fx.newInvitation(t)

	identity, err := mkenroll.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	wgRaw := make([]byte, 32)
	if _, err := rand.Read(wgRaw); err != nil {
		t.Fatal(err)
	}
	wgStd := base64.StdEncoding.EncodeToString(wgRaw)

	coreID := string(fx.service.CoreIdentity().CoreID)
	client := mkenroll.NewClient(fx.srv.URL, coreID, identity, wgStd, &http.Client{Timeout: 30 * time.Second})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	receipt, err := client.Redeem(ctx, invID, invSecret)
	if err != nil {
		t.Fatalf("Shell Body redeem rejected by Core 4: %v", err)
	}

	if receipt.Status != "enrolled" {
		t.Errorf("receipt status = %q, want enrolled", receipt.Status)
	}
	if receipt.BodyID == "" || receipt.CoreID != coreID {
		t.Errorf("unexpected receipt identity: body=%q core=%q", receipt.BodyID, receipt.CoreID)
	}
	if want := mkenroll.BodyKeyFingerprint(identity.PublicKey()); receipt.BodyPublicKeyFingerprint != want {
		t.Errorf("fingerprint mismatch: core issued %q, shell computed %q",
			receipt.BodyPublicKeyFingerprint, want)
	}

	// The receipt must be verifiable with Core's own public key. First confirm
	// the Shell Body JSON unmarshals into Core's canonical receipt type
	// (another byte-parity check), then verify Core's signature on it.
	receiptRaw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	var coreReceipt dollnetwork.EnrollmentReceipt
	if err := json.Unmarshal(receiptRaw, &coreReceipt); err != nil {
		t.Fatalf("Shell Body receipt does not unmarshal into Core 4 dollnetwork.EnrollmentReceipt: %v", err)
	}
	if err := dollnetwork.VerifyEnrollmentReceipt(fx.service.CoreIdentity().PublicKey, coreReceipt); err != nil {
		t.Errorf("Core 4 rejected its own receipt signature: %v", err)
	}

	// The membership derived from Shell Body's independent wg key must be active.
	mem := fx.net.GetMembership(network.PeerID(receipt.MembershipReference))
	if mem == nil {
		t.Fatalf("no membership for reference %q", receipt.MembershipReference)
	}
	if mem.Status != network.MembershipActive {
		t.Errorf("membership status = %q, want active", mem.Status)
	}
}

// TestShellBodyCapabilitiesConformToCore4 proves Shell Body's capability
// advertisement validates against Core 4's canonical capabilities validator.
func TestShellBodyCapabilitiesConformToCore4(t *testing.T) {
	adv := core4.NewCapabilitiesAdvertisement("body_test", []core4.Capability{
		{ID: "process", Operations: []string{"run"}, Available: true},
	})
	adv.ID = "adv_test"

	raw, err := json.Marshal(adv)
	if err != nil {
		t.Fatal(err)
	}
	var coreAdv capabilities.Advertisement
	if err := json.Unmarshal(raw, &coreAdv); err != nil {
		t.Fatalf("Shell Body advertisement does not unmarshal into Core 4 capabilities.Advertisement: %v", err)
	}
	if err := coreAdv.Validate(); err != nil {
		t.Errorf("Shell Body advertisement rejected by Core 4 validator: %v", err)
	}
}

// TestShellBodyExecutionConformsToCore4 proves Shell Body's execution result
// (produced by its real adapter) satisfies Core 4's canonical execution
// validation and correlation rules.
func TestShellBodyExecutionConformsToCore4(t *testing.T) {
	adapter := exec4.New(process.NewExecutor(), 10*time.Second)
	req := &core4.ExecutionRequest{
		Type:      core4.TypeExecutionRequest,
		ID:        "req_test",
		Timestamp: time.Now().UTC(),
		BodyID:    "body_test",
		Payload: core4.ExecutionRequestPayload{
			ExecutionID: "ex_test",
			Capability:  "process",
			Operation:   "run",
			Arguments:   map[string]any{"command": "echo", "args": []string{"conform"}},
		},
	}
	reqRaw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var coreReq execution.Request
	if err := json.Unmarshal(reqRaw, &coreReq); err != nil {
		t.Fatalf("Shell Body request does not unmarshal into Core 4 execution.Request: %v", err)
	}

	res := adapter.Handle(context.Background(), req)
	resRaw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var coreRes execution.Result
	if err := json.Unmarshal(resRaw, &coreRes); err != nil {
		t.Fatalf("Shell Body result does not unmarshal into Core 4 execution.Result: %v", err)
	}
	if err := coreRes.Validate(execution.SenderBody); err != nil {
		t.Errorf("Shell Body result rejected by Core 4 Result.Validate(SenderBody): %v", err)
	}
	if err := coreRes.ValidateResponseTo(coreReq); err != nil {
		t.Errorf("Shell Body result not correlated by Core 4: %v", err)
	}
	if coreRes.Payload.Status != execution.StatusSuccess {
		t.Errorf("status = %q, want success", coreRes.Payload.Status)
	}
}
