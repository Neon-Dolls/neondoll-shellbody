package integration

// M10.2 — Shell Body (independent reimplementation) → NeonDoll Core 4
// conformance.
//
// NeonDoll Core defines the protocol; Shell Body adapts. This is the ONE place
// Shell Body's test suite is allowed to import the neondoll repository (the
// same carve-out rendezvous_test.go uses) so it can prove that Shell Body's
// independently implemented enrollment signing, Body Authentication v1
// handshake, capability advertisement and execution result messages are
// byte-for-byte accepted by real Core 4 — over real HTTP enrollment and a real
// authenticated WebSocket execution session — rather than against isolated
// validators.
//
// The fixture mirrors Core's own reference harness (test/referencebody) but
// runs the entire Core in-process: owner-controlled enrollment via the admin
// socket, in-band Body Authentication v1 via SetBodyAuthenticator, and
// capability-gated execution dispatch through the Body guard.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corebody "github.com/Neon-Dolls/neondoll/Core/Body"
	enrollment "github.com/Neon-Dolls/neondoll/Core/Enrollment"
	invitation "github.com/Neon-Dolls/neondoll/Core/Invitation"
	network "github.com/Neon-Dolls/neondoll/Core/Network"
	ownercontrol "github.com/Neon-Dolls/neondoll/Core/OwnerControl"
	persistence "github.com/Neon-Dolls/neondoll/Core/Persistence"
	capabilities "github.com/Neon-Dolls/neondoll/DollLink/Capabilities"
	execution "github.com/Neon-Dolls/neondoll/DollLink/Execution"
	webexec "github.com/Neon-Dolls/neondoll/DollLink/WebSocket"
	dollnetwork "github.com/Neon-Dolls/neondoll/DollNetwork"
	"golang.org/x/crypto/curve25519"

	auth "github.com/Neon-Dolls/neondoll-shellbody/internal/auth"
	body "github.com/Neon-Dolls/neondoll-shellbody/internal/body"
	mkenroll "github.com/Neon-Dolls/neondoll-shellbody/internal/enrollment"
	exec4 "github.com/Neon-Dolls/neondoll-shellbody/internal/execution"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/execution/process"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/link/core4"
)

// testLogger is a no-op Logger satisfying webexec.Logger so the in-process Core
// can be started without writing test output.
type testLogger struct{}

func (testLogger) Info(string, ...map[string]any)  {}
func (testLogger) Warn(string, ...map[string]any)  {}
func (testLogger) Error(string, ...map[string]any) {}

// processAuthority allows exactly the process/run operation. It is the test's
// stand-in for a production authority; everything else is denied, which proves
// the round-trip only succeeds for the authorized capability+operation.
type processAuthority struct{}

func (processAuthority) Evaluate(request corebody.AuthorityRequest) corebody.AuthorityResult {
	if request.Capability == "process" && request.Operation == "run" {
		return corebody.AuthorityResult{Decision: corebody.AuthorityAllow}
	}
	return corebody.AuthorityResult{Decision: corebody.AuthorityDeny, Reason: "not_allowed"}
}

// core4Fixture is a full real Core 4 running in-process exactly as Core's
// reference harness wires it: enrollment with owner-controlled approval,
// in-band Body Authentication v1, capability-gated execution via the guard.
type core4Fixture struct {
	baseURL     string
	wsURL       string
	adminSocket string
	coreID      string
	corePub     ed25519.PublicKey
	service     *enrollment.Service
	net         *network.Network
	registry    *corebody.Registry
	guard       *corebody.Guard
	srv         *webexec.Server
	owner       *ownercontrol.Server
	ctx         context.Context
	cancel      context.CancelFunc
	errCh       chan error
}

func newCore4Fixture(t *testing.T) *core4Fixture {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	dir := t.TempDir()

	store, err := persistence.NewStore(filepath.Join(dir, "core.db"))
	if err != nil {
		t.Fatal(err)
	}

	nw, err := network.NewNetwork("m10-2-conformance-net")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.(network.NetworkStore).SaveNetwork(ctx, nw); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	registry := corebody.NewRegistry()
	srv := webexec.New(webexec.Config{}, testLogger{}, nil)
	invSvc := invitation.NewService(store.(invitation.Store), invitation.RealClock{})
	ownerBoundary := ownercontrol.NewBoundary()
	enrollSvc, err := enrollment.NewService(ctx, store.(enrollment.Store), nw, invSvc, ownerBoundary, nil, enrollment.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	enrollment.RegisterBodyEndpoints(srv, enrollSvc)
	srv.SetBodyAuthenticator(enrollSvc)
	registrar, err := corebody.NewRemoteRegistrar(registry, store.(network.NetworkStore), srv)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetExecutionSessionLifecycle(registrar)
	enrollSvc.SetRevocationLifecycle(srv, nil)

	fx := &core4Fixture{
		net:      nw,
		registry: registry,
		ctx:      ctx,
		cancel:   cancel,
		errCh:    make(chan error, 3),
	}
	fx.guard = corebody.NewGuard(registry, processAuthority{})
	srv.RegisterHandler("/process/execute", fx.handleProcessExecute)
	srv.SetListener(listener)

	adminSocket := filepath.Join(dir, "neondoll-admin.sock")
	ownerServer, err := ownercontrol.NewServer(adminSocket, ownercontrol.NewService(enrollSvc, invSvc, ownerBoundary))
	if err != nil {
		t.Fatal(err)
	}
	fx.owner = ownerServer

	go func() { fx.errCh <- ownerServer.Serve() }()
	go func() { fx.errCh <- srv.Start(ctx) }()

	identity := enrollSvc.CoreIdentity()
	fx.service = enrollSvc
	fx.srv = srv
	fx.coreID = string(identity.CoreID)
	fx.corePub = append(ed25519.PublicKey(nil), identity.PublicKey...)
	fx.baseURL = "http://" + listener.Addr().String()
	fx.wsURL = "ws://" + listener.Addr().String() + "/ws"
	fx.adminSocket = adminSocket

	// Owner admin socket must be dialable before approval flows.
	awaitSocket(t, adminSocket)

	t.Cleanup(func() {
		cancel()
		shutdownCtx, sh := context.WithTimeout(context.Background(), 3*time.Second)
		defer sh()
		_ = fx.owner.Shutdown(shutdownCtx)
		_ = fx.srv.Shutdown(shutdownCtx)
		store.Close()
	})
	return fx
}

func awaitSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", path)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("admin socket %s never became dialable", path)
}

func (fx *core4Fixture) handleProcessExecute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var value corebody.ExecutionRequest
	if err := json.NewDecoder(r.Body).Decode(&value); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(fx.guard.ExecuteContext(r.Context(), value))
}

// executeProcess drives one guard-based execution dispatch and decodes the
// result. It enriches the request with fixed correlation fields so the test
// reads only a real outcome, never a synthesized default.
func executeProcess(t *testing.T, fx *core4Fixture, bodyID, capability, operation string, command string, args []string) *corebody.ExecutionResult {
	t.Helper()
	value := corebody.ExecutionRequest{
		ExecutionID: "ex_" + time.Now().Format("150405.000000000") + "_" + bodyID,
		Doll:        "doll_test",
		Body:        corebody.BodyID(bodyID),
		Capability:  capability,
		Operation:   operation,
		Arguments: map[string]any{
			"command": command,
			"args":    args,
		},
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(fx.baseURL+"/process/execute", "application/json", strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("process/execute: %v", err)
	}
	defer resp.Body.Close()
	var out corebody.ExecutionResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return &out
}

// awaitCapability polls Core's Body registry until the enrolled Body has
// advertised the process capability with the run operation available. This
// turns "capability advertisement" into a real, verifiable Core-side state
// transition rather than just a frame we believe we sent.
func awaitCapability(t *testing.T, registry *corebody.Registry, bodyID string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if caps, err := registry.GetCapabilities(corebody.BodyID(bodyID)); err == nil {
			for _, c := range caps {
				if c.ID == "process" && c.Available {
					for _, op := range c.Operations {
						if op == "run" {
							return
						}
					}
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("registered body %s never advertised process/run capability", bodyID)
}

// enrollViaOwnerControl runs Shell Body's independent enrollment client through
// the complete Core owner-control path: begin (challenge) → owner approval over
// the admin socket → result poll. It returns the enrolled identity, body id and
// the Core-signed receipt, after proving the receipt is verifiable with Core's
// own public key.
func enrollViaOwnerControl(t *testing.T, fx *core4Fixture) (*mkenroll.Identity, string, mkenroll.EnrollmentReceipt) {
	t.Helper()
	identity, err := mkenroll.NewIdentity()
	if err != nil {
		t.Fatal(err)
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

	client := mkenroll.NewClient(fx.baseURL, fx.coreID, identity, wgStd, &http.Client{Timeout: 30 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pending, err := client.Begin(ctx)
	if err != nil {
		t.Fatalf("Shell Body begin rejected by Core 4: %v", err)
	}
	// Prove Shell Body's transcript is signed the way Core signs its own: the
	// pending challenge must verify against Core's public key.
	if err := mkenroll.VerifyEnrollmentPending(fx.corePub, *pending); err != nil {
		t.Fatalf("pending challenge not verifiable with Core's public key: %v", err)
	}

	// Owner approval is the gate between "begin" and "enrolled".
	if _, err := ownercontrol.NewClient(fx.adminSocket).Approve(ctx, pending.ApprovalCode, []string{"process"}); err != nil {
		t.Fatalf("owner approval rejected: %v", err)
	}

	result, err := client.Result(ctx)
	if err != nil {
		t.Fatalf("Shell Body result poll rejected by Core 4: %v", err)
	}
	if result.Status != "enrolled" || result.Receipt == nil {
		t.Fatalf("enrollment not enrolled after approval: status=%q", result.Status)
	}
	receipt := *result.Receipt
	if err := mkenroll.VerifyEnrollmentReceipt(fx.corePub, receipt); err != nil {
		t.Fatalf("receipt not verifiable with Core's public key: %v", err)
	}

	// The receipt must also round-trip into Core's canonical type and verify
	// with Core's own verifier — a second, independent byte-parity check.
	receiptRaw, _ := json.Marshal(receipt)
	var coreReceipt dollnetwork.EnrollmentReceipt
	if err := json.Unmarshal(receiptRaw, &coreReceipt); err != nil {
		t.Fatalf("Shell Body receipt does not unmarshal into Core 4 dollnetwork.EnrollmentReceipt: %v", err)
	}
	if err := dollnetwork.VerifyEnrollmentReceipt(fx.corePub, coreReceipt); err != nil {
		t.Errorf("Core 4 rejected the enrollment receipt signature: %v", err)
	}

	// The membership derived from Shell Body's independent wg key is active.
	mem := fx.net.GetMembership(network.PeerID(receipt.MembershipReference))
	if mem == nil {
		t.Fatalf("no membership for reference %q", receipt.MembershipReference)
	}
	if mem.Status != network.MembershipActive {
		t.Errorf("membership status = %q, want active", mem.Status)
	}
	return identity, string(receipt.BodyID), receipt
}

// TestShellBodyEnrollmentConformsToCore4 drives Shell Body's independent
// enrollment client through Core 4's owner-controlled body_request path.
func TestShellBodyEnrollmentConformsToCore4(t *testing.T) {
	fx := newCore4Fixture(t)
	enrollViaOwnerControl(t, fx)
}

// TestShellBodyAuthenticationAndProcessExecutionAgainstCore is the end-to-end
// proof: after owner-controlled enrollment, Shell Body's independent session
// authenticates over a real authenticated Doll Link WebSocket (Body Auth v1),
// advertises its process capability, and executes one real authorized host
// process command whose result round-trips back through Core's guard.
func TestShellBodyAuthenticationAndProcessExecutionAgainstCore(t *testing.T) {
	fx := newCore4Fixture(t)
	identity, bodyID, _ := enrollViaOwnerControl(t, fx)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	adapter := exec4.New(process.NewExecutor(), 30*time.Second)
	sess, err := body.DialCore4(fx.wsURL, body.Credentials{
		BodyID:        bodyID,
		PrivateKey:    identity.PrivateKey(),
		CoreID:        fx.coreID,
		CorePublicKey: fx.corePub,
	}, adapter)
	if err != nil {
		t.Fatalf("Shell Body dial /ws: %v", err)
	}
	defer sess.Close()

	accept, err := sess.Authenticate(ctx)
	if err != nil {
		t.Fatalf("Shell Body Body Auth v1 handshake rejected by Core: %v", err)
	}
	if accept.Status != auth.StatusAuthenticated {
		t.Fatalf("accept status = %q, want %q", accept.Status, auth.StatusAuthenticated)
	}
	if accept.BodyID != bodyID {
		t.Fatalf("accept body_id = %q, want %q", accept.BodyID, bodyID)
	}
	if accept.CoreID != fx.coreID {
		t.Fatalf("accept core_id = %q, want %q", accept.CoreID, fx.coreID)
	}

	if err := sess.AdvertiseCapabilities(ctx, exec4.AdvertisedCore4()); err != nil {
		t.Fatalf("advertise capabilities: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- sess.Run(ctx) }()
	defer sess.Close()

	// Capability advertisement must land in Core's registry.
	awaitCapability(t, fx.registry, bodyID)

	// Authorized round-trip: an authorized process/run execution.
	result := executeProcess(t, fx, bodyID, "process", "run", "sh", []string{"-c", "printf shellbody-ci"})
	if result.Status != corebody.StatusSuccess {
		t.Fatalf("authorized process execution status = %q, want success (output=%q)", result.Status, result.Output)
	}
	var out struct {
		ExitCode int    `json:"exit_code"`
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
	}
	if err := json.Unmarshal([]byte(result.Output), &out); err != nil {
		t.Fatalf("execution output %q not the adapter's JSON document: %v", result.Output, err)
	}
	if !strings.Contains(out.Stdout, "shellbody-ci") {
		t.Fatalf("stdout %q does not contain shellbody-ci", out.Stdout)
	}

	// Unauthorized operation must not execute: the authority denies process/kill.
	denied := executeProcess(t, fx, bodyID, "process", "kill", "sh", []string{"-c", "printf pwnd"})
	if denied.Status == corebody.StatusSuccess {
		t.Fatalf("unauthorized process/kill executed successfully: %+v", denied)
	}

	// Unknown Body must not execute: even though the arguments are valid, the
	// guard cannot resolve a Body that never authenticated.
	unknown := executeProcess(t, fx, "body_unknown", "process", "run", "sh", []string{"-c", "printf nope"})
	if unknown.Status == corebody.StatusSuccess {
		t.Fatalf("execution succeeded against an unknown Body: %+v", unknown)
	}

	// Session must still be healthy after the negative cases: Run must not have
	// exited prematurely. Non-blocking, so the session's read deadline does not
	// stall the test.
	select {
	case runErr := <-done:
		if ctx.Err() == nil {
			t.Errorf("session run exited prematurely: %v", runErr)
		}
	default:
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
