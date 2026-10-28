package integration

// M10.3 — Persistent Core connection and recovery against a live Core 4.
//
// These tests reuse the M10.2 in-process real-Core fixture (core4Fixture) to
// prove the recovery loop actually survives a real Core-mediated session drop
// and re-authenticates with the SAME persisted identity, and that an unknown /
// never-enrolled identity fails closed instead of retrying forever.
//
// Pure loop mechanics (exponential backoff under temporary Core unavailability,
// cancellation during backoff, no duplicate active sessions, goroutine hygiene)
// are tested deterministically in the mock recovery suite
// (internal/body/recovery_test.go) against a controllable /ws stand-in; here we
// pin the loop to the real Core 4 wire protocol.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	corebody "github.com/Neon-Dolls/neondoll/Core/Body"

	auth "github.com/Neon-Dolls/neondoll-shellbody/internal/auth"
	body "github.com/Neon-Dolls/neondoll-shellbody/internal/body"
	mkenroll "github.com/Neon-Dolls/neondoll-shellbody/internal/enrollment"
	exec4 "github.com/Neon-Dolls/neondoll-shellbody/internal/execution"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/execution/process"
)

// authCounter counts successful authentications reported via the recovery
// loop's OnAuthenticated hook, so tests can assert reconnects happened.
type authCounter struct {
	mu  sync.Mutex
	n   int
	ids []string
}

func (a *authCounter) add(bodyID, sessionID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.n++
	a.ids = append(a.ids, bodyID)
}

func (a *authCounter) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.n
}

// recoveryCreds builds Shell Body Credentials from an enrolled identity +
// Core trust anchor, matching what cmd/neondoll-shellbody --serve builds.
func recoveryCreds(fx *core4Fixture, identity *mkenroll.Identity, bodyID string) body.Credentials {
	return body.Credentials{
		BodyID:        bodyID,
		PrivateKey:    identity.PrivateKey(),
		CoreID:        fx.coreID,
		CorePublicKey: fx.corePub,
	}
}

func (fx *core4Fixture) startRecovery(t *testing.T, creds body.Credentials, counter *authCounter) (chan error, context.CancelFunc) {
	t.Helper()
	errCh := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		errCh <- body.RunRecovery(ctx, body.RecoveryConfig{
			URL:        fx.wsURL,
			Creds:      creds,
			Adapter:    exec4.New(process.NewExecutor(), 30*time.Second),
			Advertised: exec4.AdvertisedCore4(),
			OnAuthenticated: func(accept *auth.Accept) {
				counter.add(accept.BodyID, accept.SessionID)
			},
		})
	}()
	return errCh, cancel
}

// assertRecoveryHealthy verifies the loop is still connected (has not exited)
// and Core has the advertised capability registered for the given body.
func assertRecoveryHealthy(t *testing.T, errCh chan error, fx *core4Fixture, bodyID string) {
	t.Helper()
	awaitCapability(t, fx.registry, bodyID)
	select {
	case runErr := <-errCh:
		t.Fatalf("recovery loop exited while it should be connected: %v", runErr)
	default:
	}
}

// TestRecoveryReauthenticatesWithSamePersistedIdentity is the realistic
// process-restart scenario: the same enrolled identity (the persisted Body's
// Ed25519 key + trust anchor) is used for a fresh recovery run and authenticates
// again against the same Core, then serves an authorized execution.
func TestRecoveryReauthenticatesWithSamePersistedIdentity(t *testing.T) {
	fx := newCore4Fixture(t)
	identity, bodyID, _ := enrollViaOwnerControl(t, fx)
	creds := recoveryCreds(fx, identity, bodyID)

	// --- "run one": connect, advertise, execute, then stop the process. ---
	counter := &authCounter{}
	errCh, cancel := fx.startRecovery(t, creds, counter)
	assertRecoveryHealthy(t, errCh, fx, bodyID)

	result := executeProcess(t, fx, bodyID, "process", "run", "sh", []string{"-c", "printf persistence-1"})
	if result.Status != corebody.StatusSuccess {
		t.Fatalf("run one execution status = %q, want success", result.Status)
	}
	cancel()
	_ = waitRecoveryExit(t, errCh)

	// --- "run two": fresh recovery run with the SAME persisted identity. ---
	counter2 := &authCounter{}
	errCh2, cancel2 := fx.startRecovery(t, creds, counter2)
	// Await the fresh authentication: run one's capability registration is
	// stale after that session ended, so awaitCapability alone is not proof the
	// new session is live. A fresh auth means the restart actually reconnected.
	awaitAuthCount(t, counter2, 1)
	assertRecoveryHealthy(t, errCh2, fx, bodyID)

	result2 := executeProcess(t, fx, bodyID, "process", "run", "sh", []string{"-c", "printf persistence-2"})
	if result2.Status != corebody.StatusSuccess {
		t.Fatalf("run two execution status = %q, want success", result2.Status)
	}
	cancel2()
	_ = waitRecoveryExit(t, errCh2)

	if counter2.count() != 1 {
		t.Errorf("expected exactly one fresh authentication on restart, got %d", counter2.count())
	}
	if counter2.ids[0] != bodyID {
		t.Errorf("restart authenticated as %q, want %q (identity not preserved)", counter2.ids[0], bodyID)
	}
}

// waitRecoveryExit waits for a recovery loop goroutine to return.
func waitRecoveryExit(t *testing.T, errCh chan error) error {
	t.Helper()
	select {
	case err := <-errCh:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("recovery loop did not exit after cancellation")
		return nil
	}
}

// awaitAuthCount waits until the recovery loop has performed at least minAuth
// successful authentications (a fresh authentication proves reconnection).
func awaitAuthCount(t *testing.T, counter *authCounter, minAuth int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && counter.count() < minAuth {
		time.Sleep(20 * time.Millisecond)
	}
	if counter.count() < minAuth {
		t.Fatalf("expected >=%d authentications, got %d", minAuth, counter.count())
	}
}

// TestRecoveryReconnectsAfterCoreDisconnect proves the core reconnection path
// against real Core: Core force-disconnects the Body's live WebSocket session,
// and the recovery loop reconnects (re-authenticating the same identity,
// re-advertising capabilities) and serves another authorized execution.
func TestRecoveryReconnectsAfterCoreDisconnect(t *testing.T) {
	fx := newCore4Fixture(t)
	identity, bodyID, _ := enrollViaOwnerControl(t, fx)

	counter := &authCounter{}
	errCh, cancel := fx.startRecovery(t, recoveryCreds(fx, identity, bodyID), counter)

	assertRecoveryHealthy(t, errCh, fx, bodyID)
	if counter.count() != 1 {
		t.Fatalf("expected first authentication, got %d", counter.count())
	}

	result := executeProcess(t, fx, bodyID, "process", "run", "sh", []string{"-c", "printf before-drop"})
	if result.Status != corebody.StatusSuccess {
		t.Fatalf("pre-drop execution status = %q, want success", result.Status)
	}

	// Core severs the Body's session. The recovery loop must reconnect on its
	// own (no operator action), re-authenticate, and re-advertise.
	fx.srv.DisconnectBody(bodyID)

	awaitCapability(t, fx.registry, bodyID)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && counter.count() < 2 {
		time.Sleep(20 * time.Millisecond)
	}
	if counter.count() < 2 {
		t.Fatalf("expected a re-authentication after Core disconnect, got %d", counter.count())
	}

	assertRecoveryHealthy(t, errCh, fx, bodyID)
	result2 := executeProcess(t, fx, bodyID, "process", "run", "sh", []string{"-c", "printf after-drop"})
	if result2.Status != corebody.StatusSuccess {
		t.Fatalf("post-reconnect execution status = %q, want success", result2.Status)
	}
	var out struct {
		Stdout string `json:"stdout"`
	}
	if err := json.Unmarshal([]byte(result2.Output), &out); err != nil {
		t.Fatalf("decode post-reconnect output %q: %v", result2.Output, err)
	}
	if !strings.Contains(out.Stdout, "after-drop") {
		t.Fatalf("stdout %q does not contain after-drop", out.Stdout)
	}

	cancel()
	_ = waitRecoveryExit(t, errCh)
}

// TestRecoveryFailsClosedOnUnknownIdentity proves a Body identity real Core
// does not recognize is rejected at the handshake and the loop fails closed in
// ONE attempt -- never retrying an identity that can never be accepted.
func TestRecoveryFailsClosedOnUnknownIdentity(t *testing.T) {
	fx := newCore4Fixture(t)
	// A body that was never enrolled has no membership record; real Core's
	// enrollment guard rejects its handshake.
	unknown, _ := mkenroll.NewIdentity()

	start := time.Now()
	errCh, cancel := fx.startRecovery(t, recoveryCreds(fx, unknown, "body_never_enrolled"), &authCounter{})
	defer cancel()

	select {
	case err := <-errCh:
		if !body.IsPermanent(err) {
			t.Fatalf("expected a permanent failure for unknown identity, got %T: %v", err, err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("unknown identity did not fail closed -- recovery loop kept retrying")
	}
	if since := time.Since(start); since > 2500*time.Millisecond {
		t.Errorf("unknown-identity failure surfaced too slowly: %v", since)
	}
}
