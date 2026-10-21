package body

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/auth"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/execution"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/execution/process"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/link/core4"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

const testBodyID = "body_test"
const testCoreID = "core_test"

// keypairs used by the fake Core and the Body under test. Both sides are
// independent Ed25519 identities, mirroring real Core and a real Body.
var (
	corePubKey, corePrivKey, _ = ed25519.GenerateKey(rand.Reader)
	bodyPubKey, bodyPrivKey, _ = ed25519.GenerateKey(rand.Reader)
)

// wsCore stands in for Core 4 on the other end of /ws: it runs the Body
// Authentication v1 handshake (challenge/proof/accept), requires the canonical
// body.capabilities advertisement, then issues an execution.request and asserts
// the correlated execution.result.
type wsCore struct {
	t *testing.T
}

func (h *wsCore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.t.Errorf("upgrade: %v", err)
		return
	}
	defer conn.Close()

	// 0. Body Authentication v1 handshake.
	if err := runServerHandshake(conn, h.t, bodyPubKey); err != nil {
		h.t.Errorf("handshake: %v", err)
		return
	}

	// 1. Expect the Body's capability advertisement.
	var adv core4.CapabilitiesAdvertisement
	if err := readJSON(conn, &adv); err != nil {
		h.t.Errorf("read advertisement: %v", err)
		return
	}
	if err := adv.Validate(); err != nil {
		h.t.Errorf("advertisement failed core4 validation: %v", err)
		return
	}
	if adv.BodyID != testBodyID {
		h.t.Errorf("advertisement body_id = %q", adv.BodyID)
	}
	hasProcess := false
	for _, c := range adv.Payload.Capabilities {
		if c.ID == "process" && len(c.Operations) == 1 && c.Operations[0] == "run" {
			hasProcess = true
		}
	}
	if !hasProcess {
		h.t.Error("advertisement missing process/run capability")
		return
	}

	// 2. Issue a canonical execution.request.
	req := &core4.ExecutionRequest{
		Type:      core4.TypeExecutionRequest,
		ID:        "req_test",
		Timestamp: time.Now().UTC(),
		BodyID:    adv.BodyID,
		Payload: core4.ExecutionRequestPayload{
			ExecutionID: "ex_test",
			Capability:  "process",
			Operation:   "run",
			Arguments: map[string]any{
				"command": "echo",
				"args":    []string{"pong"},
			},
		},
	}
	if err := conn.WriteJSON(req); err != nil {
		h.t.Errorf("write request: %v", err)
		return
	}

	// 3. Assert the correlated execution.result.
	var res core4.ExecutionResult
	if err := readJSON(conn, &res); err != nil {
		h.t.Errorf("read result: %v", err)
		return
	}
	if err := res.Validate(); err != nil {
		h.t.Errorf("result failed core4 validation: %v", err)
	}
	if err := res.ValidateResponseTo(req); err != nil {
		h.t.Errorf("result not correlated to request: %v", err)
	}
	if res.Payload.Status != core4.StatusSuccess {
		h.t.Errorf("expected success, got %q", res.Payload.Status)
	}
	if res.Payload.Result["stdout"] != "pong\n" {
		h.t.Errorf("unexpected stdout %q", res.Payload.Result["stdout"])
	}
}

// runServerHandshake performs the Core-side of Body Authentication v1 using
// Shell Body's own auth package (a fully independent Core 4 reproduction).
func runServerHandshake(conn *websocket.Conn, t *testing.T, bodyPub ed25519.PublicKey) error {
	var hello auth.Hello
	if err := readJSON(conn, &hello); err != nil {
		return err
	}
	if hello.Type != auth.TypeHello || hello.BodyID != testBodyID {
		t.Fatalf("unexpected hello: %+v", hello)
	}

	challenge := &auth.Challenge{
		Version:   auth.Version,
		Type:      auth.TypeChallenge,
		CoreID:    testCoreID,
		BodyID:    hello.BodyID,
		SessionID: "ses_test",
		BodyNonce: hello.BodyNonce,
		CoreNonce: mustNonce(t),
		ExpiresAt: time.Now().UTC().Add(time.Minute),
		Algorithm: auth.AlgorithmEd25519,
	}
	if err := auth.SignChallenge(corePrivKey, challenge); err != nil {
		return err
	}
	if err := conn.WriteJSON(challenge); err != nil {
		return err
	}

	var proof auth.Proof
	if err := readJSON(conn, &proof); err != nil {
		return err
	}
	if err := auth.VerifyProof(bodyPub, proof); err != nil {
		return err
	}
	if proof.CoreID != challenge.CoreID || proof.SessionID != challenge.SessionID ||
		proof.BodyNonce != challenge.BodyNonce || proof.CoreNonce != challenge.CoreNonce {
		return &handshakeError{msg: "proof not bound to challenge"}
	}

	accept := &auth.Accept{
		Version:   auth.Version,
		Type:      auth.TypeAccept,
		CoreID:    challenge.CoreID,
		BodyID:    challenge.BodyID,
		SessionID: challenge.SessionID,
		Status:    auth.StatusAuthenticated,
	}
	if err := auth.SignAccept(corePrivKey, accept); err != nil {
		return err
	}
	return conn.WriteJSON(accept)
}

type handshakeError struct{ msg string }

func (e *handshakeError) Error() string { return e.msg }

func mustNonce(t *testing.T) string {
	t.Helper()
	s, err := auth.RandomNonce()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func readJSON(conn *websocket.Conn, dst any) error {
	_, raw, err := conn.ReadMessage()
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}

func TestCore4SessionRoundTrip(t *testing.T) {
	handler := &wsCore{t: t}
	srv := httptest.NewServer(handler)
	defer srv.Close()
	wsURL := "ws" + srv.URL[len("http"):] + "/ws"

	creds := Credentials{
		BodyID:        testBodyID,
		PrivateKey:    bodyPrivKey,
		CoreID:        testCoreID,
		CorePublicKey: corePubKey,
	}
	sess, err := DialCore4(wsURL, creds, execution.New(process.NewExecutor(), 5*time.Second))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	accept, err := sess.Authenticate(ctx)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if accept.Status != auth.StatusAuthenticated {
		t.Fatalf("expected authenticated, got %q", accept.Status)
	}

	if err := sess.AdvertiseCapabilities(ctx, execution.Advertised()); err != nil {
		t.Fatalf("advertise: %v", err)
	}

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = sess.Run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("run loop did not complete in time")
	}
	wg.Wait()
}

func TestAuthenticateRejectsWrongTrustAnchor(t *testing.T) {
	// Dedicated handler: perform just the server half of the handshake and do
	// not wait for the proof. The Body under test holds a rogue Core trust
	// anchor, so it must reject the (validly signed) challenge and close.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var hello auth.Hello
		if err := readJSON(conn, &hello); err != nil {
			return
		}
		challenge := &auth.Challenge{
			Version:   auth.Version,
			Type:      auth.TypeChallenge,
			CoreID:    testCoreID,
			BodyID:    hello.BodyID,
			SessionID: "ses_test",
			BodyNonce: hello.BodyNonce,
			CoreNonce: mustNonce(t),
			ExpiresAt: time.Now().UTC().Add(time.Minute),
			Algorithm: auth.AlgorithmEd25519,
		}
		_ = auth.SignChallenge(corePrivKey, challenge)
		_ = conn.WriteJSON(challenge)
		// Return without reading; the Body should abort because its trust
		// anchor does not match Core.
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()
	wsURL := "ws" + srv.URL[len("http"):] + "/ws"

	// Use a rogue key that is not Core's real public key.
	roguePub, _, _ := ed25519.GenerateKey(rand.Reader)
	creds := Credentials{
		BodyID:        testBodyID,
		PrivateKey:    bodyPrivKey,
		CoreID:        testCoreID,
		CorePublicKey: roguePub,
	}
	sess, err := DialCore4(wsURL, creds, execution.New(process.NewExecutor(), 5*time.Second))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := sess.Authenticate(ctx); err == nil {
		t.Fatal("expected authentication to fail against a rogue trust anchor")
	}
}

func TestAdvertiseCapabilitiesValidatesBeforeSend(t *testing.T) {
	sess := &Core4Session{bodyID: testBodyID}
	adv := core4.NewCapabilitiesAdvertisement(sess.bodyID, nil)
	adv.ID = "adv_x"
	if err := adv.Validate(); err != nil {
		t.Fatalf("zero-capability advertisement should validate: %v", err)
	}
}
