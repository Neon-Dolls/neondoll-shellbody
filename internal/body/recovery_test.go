package body

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/auth"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/execution"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/execution/process"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/link/core4"
	"github.com/gorilla/websocket"
)

// assertf reports a failing condition without stopping the test.
func assertf(t *testing.T, cond bool, format string, args ...any) {
	t.Helper()
	if !cond {
		t.Errorf(format, args...)
	}
}

var recovUpgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

// post-auth policy for each upgraded connection.
type recovPolicy string

const (
	pDrop    recovPolicy = "drop"    // close right after advertise (session drop)
	pExecute recovPolicy = "execute" // full execution round-trip then close
)

// recovCore is a controllable Core 4 /ws stand-in for recovery tests. It runs
// the canonical Body Authentication v1 handshake, expects the capability
// advertisement, and then applies a per-connection policy. Control flags make
// Core "unavailable" (HTTP 503, a dial failure), "reject" the Body's proof
// (revoked/unknown identity -> permanent), and switches between dropping vs
// executing on each connection. Counters let tests assert exactly how many
// connections, authentications, advertisements and executions happened, plus
// the peak number of concurrently-open sessions (which the recovery loop must
// never exceed 1 of).
type recovCore struct {
	t      *testing.T
	mu     sync.Mutex
	down   bool
	reject bool
	plan   []recovPolicy
	deflt  recovPolicy

	requests   int // every HTTP request (incl. 503 while down)
	conns      int // upgraded ws connections
	authed     int // handshakes completed (accept sent)
	advertised int // capability advertisements received
	executions int // execution round-trips completed
	openConns  int
	peakConns  int
	lastAuthID string
}

func (c *recovCore) setDown(b bool)   { c.mu.Lock(); c.down = b; c.mu.Unlock() }
func (c *recovCore) setReject(b bool) { c.mu.Lock(); c.reject = b; c.mu.Unlock() }

func (c *recovCore) snapshot(lockOk bool) (requests, conns, authed, advertised, executions, peakConns int, lastAuthID string) {
	if lockOk {
		c.mu.Lock()
		defer c.mu.Unlock()
	}
	return c.requests, c.conns, c.authed, c.advertised, c.executions, c.peakConns, c.lastAuthID
}

func (c *recovCore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.requests++
	down := c.down
	reject := c.reject
	c.mu.Unlock()

	if down {
		// Core is up enough to answer HTTP but refuses ws: the Body's dial
		// fails, which the recovery loop treats as a transient failure.
		http.Error(w, "core unavailable", http.StatusServiceUnavailable)
		return
	}

	conn, err := recovUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	c.mu.Lock()
	idx := c.conns
	c.conns++
	c.openConns++
	if c.openConns > c.peakConns {
		c.peakConns = c.openConns
	}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.openConns--
		c.mu.Unlock()
	}()

	// Body Authentication v1 handshake. The challenge is always sent (matching
	// real Core): the reject flag makes Core close after the proof — the exact
	// behavior of Core's enrollment guard on a revoked/unknown identity, which
	// the Body observes as a read-error while awaiting the accept (permanent).
	var hello auth.Hello
	if err := readJSON(conn, &hello); err != nil {
		return
	}
	challenge := &auth.Challenge{
		Version:   auth.Version,
		Type:      auth.TypeChallenge,
		CoreID:    testCoreID,
		BodyID:    hello.BodyID,
		SessionID: "ses_" + int64ToStr(c.requests) + "_" + int64ToStr(idx),
		BodyNonce: hello.BodyNonce,
		CoreNonce: mustNonce(c.t),
		ExpiresAt: time.Now().UTC().Add(time.Minute),
		Algorithm: auth.AlgorithmEd25519,
	}
	if err := auth.SignChallenge(corePrivKey, challenge); err != nil {
		return
	}
	if err := conn.WriteJSON(challenge); err != nil {
		return
	}
	var proof auth.Proof
	if err := readJSON(conn, &proof); err != nil {
		return
	}
	if reject {
		// Revoked/unknown identity: close after the proof without an accept.
		return
	}
	if err := auth.VerifyProof(bodyPubKey, proof); err != nil {
		return
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
		return
	}
	if err := conn.WriteJSON(accept); err != nil {
		return
	}
	c.mu.Lock()
	c.authed++
	c.lastAuthID = hello.BodyID
	c.mu.Unlock()

	// Expect the capability advertisement.
	var adv core4.CapabilitiesAdvertisement
	if err := readJSON(conn, &adv); err != nil {
		return
	}
	if err := adv.Validate(); err != nil {
		return
	}
	c.mu.Lock()
	c.advertised++
	c.mu.Unlock()

	c.mu.Lock()
	p := c.deflt
	if idx < len(c.plan) {
		p = c.plan[idx]
	}
	c.mu.Unlock()

	if p == pExecute {
		if err := c.runExecutionRoundTrip(conn, adv.BodyID, idx); err != nil {
			return
		}
		c.mu.Lock()
		c.executions++
		c.mu.Unlock()
	}
	// pDrop (and, after an execution, the return below) close the connection,
	// which the Body observes as a session drop and reconnects from.
}

func (c *recovCore) runExecutionRoundTrip(conn *websocket.Conn, bodyID string, idx int) error {
	req := &core4.ExecutionRequest{
		Type:      core4.TypeExecutionRequest,
		ID:        "req_recov_" + int64ToStr(c.requests) + "_" + int64ToStr(idx),
		Timestamp: time.Now().UTC(),
		BodyID:    bodyID,
		Payload: core4.ExecutionRequestPayload{
			ExecutionID: "ex_recov_" + int64ToStr(idx),
			Capability:  "process",
			Operation:   "run",
			Arguments: map[string]any{
				"command": "echo",
				"args":    []string{"recov"},
			},
		},
	}
	if err := conn.WriteJSON(req); err != nil {
		return err
	}
	var res core4.ExecutionResult
	if err := readJSON(conn, &res); err != nil {
		return err
	}
	if err := res.Validate(); err != nil {
		return err
	}
	if err := res.ValidateResponseTo(req); err != nil {
		return err
	}
	if res.Payload.Status != core4.StatusSuccess {
		return errors.New("execution not success")
	}
	return nil
}

func int64ToStr(v int) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

func testRecoveryCreds() Credentials {
	return Credentials{
		BodyID:        testBodyID,
		PrivateKey:    bodyPrivKey,
		CoreID:        testCoreID,
		CorePublicKey: corePubKey,
	}
}

func testRecoveryAdapter() *execution.Adapter {
	return execution.New(process.NewExecutor(), 5*time.Second)
}

// launchRecovery runs RunRecovery in a goroutine and returns its error channel
// plus the cancel func used to shut it down.
func launchRecovery(cfg RecoveryConfig) (chan error, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- RunRecovery(ctx, cfg) }()
	return errCh, cancel
}

// recoveryResult waits for RunRecovery to return or times out.
func recoveryResult(t *testing.T, errCh chan error, timeout time.Duration) (error, bool) {
	t.Helper()
	select {
	case err := <-errCh:
		return err, true
	case <-time.After(timeout):
		return nil, false
	}
}

// TestRecoveryReconnectsAfterSessionDrop proves the core M10.3 path: the first
// connection is dropped by Core after auth, the recovery loop reconnects (fresh
// auth + re-advertise) and a subsequent connection serves an authenticated
// execution. Identity is preserved across the reconnect.
func TestRecoveryReconnectsAfterSessionDrop(t *testing.T) {
	core := &recovCore{t: t, plan: []recovPolicy{pDrop, pExecute}, deflt: pDrop}
	srv := httptest.NewServer(core)
	defer srv.Close()
	wsURL := "ws" + srv.URL[len("http"):] + "/ws"

	errCh, cancel := launchRecovery(RecoveryConfig{
		URL:               wsURL,
		Creds:             testRecoveryCreds(),
		Adapter:           testRecoveryAdapter(),
		Advertised:        execution.AdvertisedCore4(),
		ReconnectDelay:    30 * time.Millisecond,
		MaxReconnectDelay: 200 * time.Millisecond,
		Jitter:            -1, // deterministic timing for the test
	})

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && core.executionsSnapshot() < 1 {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	err, ok := recoveryResult(t, errCh, 5*time.Second)
	if !ok {
		t.Fatal("RunRecovery did not exit promptly after cancellation")
	}
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}

	req, conns, authed, adv, ex, peak, lastAuthID := core.snapshot(true)
	assertf(t, conns >= 2, "expected >=2 connections after a drop, got %d", conns)
	assertf(t, authed >= 2, "expected re-authentication on reconnect, got %d", authed)
	assertf(t, adv >= 2, "expected re-advertisement on reconnect, got %d", adv)
	assertf(t, ex == 1, "expected exactly 1 execution, got %d", ex)
	assertf(t, lastAuthID == testBodyID, "identity not preserved across reconnect: %q", lastAuthID)
	assertf(t, peak == 1, "duplicate active sessions: peak %d", peak)
	_ = req
}

// TestRecoveryRecoversAfterCoreUnavailable proves Core going down then coming
// back is recovered via bounded retries (dial failure -> transient), ending in
// an authenticated execution.
func TestRecoveryRecoversAfterCoreUnavailable(t *testing.T) {
	core := &recovCore{t: t, deflt: pExecute}
	srv := httptest.NewServer(core)
	defer srv.Close()
	wsURL := "ws" + srv.URL[len("http"):] + "/ws"
	core.setDown(true)

	errCh, cancel := launchRecovery(RecoveryConfig{
		URL:               wsURL,
		Creds:             testRecoveryCreds(),
		Adapter:           testRecoveryAdapter(),
		Advertised:        execution.AdvertisedCore4(),
		ReconnectDelay:    30 * time.Millisecond,
		MaxReconnectDelay: 200 * time.Millisecond,
		Jitter:            -1,
	})

	// Let it fail its first dial(s) and enter backoff.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		req, _, _, _, _, _, _ := core.snapshot(true)
		if req >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Core comes back.
	core.setDown(false)

	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && core.executionsSnapshot() < 1 {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if _, ok := recoveryResult(t, errCh, 5*time.Second); !ok {
		t.Fatal("RunRecovery did not exit promptly after cancellation")
	}

	req, _, authed, adv, ex, peak, lastAuthID := core.snapshot(true)
	assertf(t, req >= 2, "expected retries while Core was down, got %d requests", req)
	assertf(t, authed >= 1, "no authentication after recovery")
	assertf(t, adv >= 1, "no capability advertisement after recovery")
	assertf(t, ex >= 1, "no authenticated execution after recovery")
	assertf(t, lastAuthID == testBodyID, "identity not preserved after recovery: %q", lastAuthID)
	assertf(t, peak == 1, "duplicate active sessions: peak %d", peak)
}

// TestRecoveryFailsClosedOnInvalidLocalIdentity proves invalid local
// credentials never dial and never retry.
func TestRecoveryFailsClosedOnInvalidLocalIdentity(t *testing.T) {
	core := &recovCore{t: t}
	srv := httptest.NewServer(core)
	defer srv.Close()
	wsURL := "ws" + srv.URL[len("http"):] + "/ws"

	creds := testRecoveryCreds()
	creds.BodyID = "" // corrupt local identity

	errCh, _ := launchRecovery(RecoveryConfig{
		URL:        wsURL,
		Creds:      creds,
		Adapter:    testRecoveryAdapter(),
		Advertised: execution.AdvertisedCore4(),
		Jitter:     -1,
	})
	err, ok := recoveryResult(t, errCh, 2*time.Second)
	if !ok {
		t.Fatal("RunRecovery did not fail closed on invalid local identity")
	}
	if !IsPermanent(err) {
		t.Errorf("expected PermanentError for invalid local identity, got %T: %v", err, err)
	}
	_, conns, authed, _, _, _, _ := core.snapshot(true)
	assertf(t, conns == 0, "invalid identity must not dial, dialed %d times", conns)
	assertf(t, authed == 0, "invalid identity must not authenticate")
}

// TestRecoveryFailsClosedOnRevokedProof proves Core rejecting the Body's proof
// (revoked/unknown identity) stops in ONE attempt -- no endless retries.
func TestRecoveryFailsClosedOnRevokedProof(t *testing.T) {
	core := &recovCore{t: t}
	core.setReject(true)
	srv := httptest.NewServer(core)
	defer srv.Close()
	wsURL := "ws" + srv.URL[len("http"):] + "/ws"

	start := time.Now()
	errCh, _ := launchRecovery(RecoveryConfig{
		URL:        wsURL,
		Creds:      testRecoveryCreds(),
		Adapter:    testRecoveryAdapter(),
		Advertised: execution.AdvertisedCore4(),
		Jitter:     -1,
	})
	err, ok := recoveryResult(t, errCh, 3*time.Second)
	if !ok {
		t.Fatal("RunRecovery did not fail closed after Core rejected the proof")
	}
	if !IsPermanent(err) {
		t.Errorf("expected PermanentError for rejected proof, got %T: %v", err, err)
	}
	if time.Since(start) > 2500*time.Millisecond {
		t.Errorf("permanent failure took too long to surface: %v", time.Since(start))
	}
	req, conns, authed, _, _, _, _ := core.snapshot(true)
	assertf(t, conns == 1, "revoked identity must not retry: made %d connections", conns)
	assertf(t, authed == 0, "revoked identity must not be authenticated")
	_ = req
}

// TestRecoveryCancellationInterruptsBackoff proves a long backoff is interrupted
// immediately when the process is asked to shut down.
func TestRecoveryCancellationInterruptsBackoff(t *testing.T) {
	core := &recovCore{t: t}
	core.setDown(true)
	srv := httptest.NewServer(core)
	defer srv.Close()
	wsURL := "ws" + srv.URL[len("http"):] + "/ws"

	errCh, cancel := launchRecovery(RecoveryConfig{
		URL:               wsURL,
		Creds:             testRecoveryCreds(),
		Adapter:           testRecoveryAdapter(),
		Advertised:        execution.AdvertisedCore4(),
		ReconnectDelay:    30 * time.Second, // long window
		MaxReconnectDelay: 30 * time.Second,
		Jitter:            -1,
	})

	// Wait for the first failed dial so we know we are inside a backoff sleep.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		req, _, _, _, _, _, _ := core.snapshot(true)
		if req >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	start := time.Now()
	cancel()
	err, ok := recoveryResult(t, errCh, 2*time.Second)
	if !ok {
		t.Fatal("cancellation did not interrupt the backoff sleep")
	}
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if since := time.Since(start); since > 1500*time.Millisecond {
		t.Errorf("cancellation during backoff too slow: %v", since)
	}
	req, conns, _, _, _, _, _ := core.snapshot(true)
	assertf(t, conns == 0, "should not have connected while down, made %d connections", conns)
	_ = req
}

// TestRecoveryNoDuplicateSessionsOrLeaks proves the recovery loop never holds
// two active sessions at once across many reconnect cycles, and does not leak
// goroutines after shutdown.
func TestRecoveryNoDuplicateSessionsOrLeaks(t *testing.T) {
	before := runtime.NumGoroutine()

	core := &recovCore{t: t, deflt: pDrop}
	srv := httptest.NewServer(core)
	defer srv.Close()
	wsURL := "ws" + srv.URL[len("http"):] + "/ws"

	errCh, cancel := launchRecovery(RecoveryConfig{
		URL:               wsURL,
		Creds:             testRecoveryCreds(),
		Adapter:           testRecoveryAdapter(),
		Advertised:        execution.AdvertisedCore4(),
		ReconnectDelay:    5 * time.Millisecond,
		MaxReconnectDelay: 10 * time.Millisecond,
		Jitter:            -1,
	})

	// Drive ~20 reconnect cycles.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		req, conns, _, _, _, _, _ := core.snapshot(true)
		if conns >= 20 {
			break
		}
		_ = req
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if _, ok := recoveryResult(t, errCh, 5*time.Second); !ok {
		t.Fatal("RunRecovery did not exit after 20 reconnect cycles")
	}
	srv.Close() // stop the test server so its goroutines unwind
	time.Sleep(200 * time.Millisecond)

	_, _, _, _, _, peak, _ := core.snapshot(true)
	assertf(t, peak == 1, "duplicate active sessions across reconnects: peak %d", peak)

	// No unbounded goroutine growth after a clean shutdown (allow a small
	// settle margin for the runtime and http server teardown).
	if after := runtime.NumGoroutine(); after > before+10 {
		t.Errorf("possible goroutine leak: before=%d after=%d", before, after)
	}
}

// helper accessors to keep snapshot() call sites terse.
func (c *recovCore) executionsSnapshot() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.executions
}

var _ = ed25519.PrivateKeySize // keep crypto/ed25519 import if unused elsewhere
var _ = rand.Reader
