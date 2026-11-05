// Package body provides the Shell Body's interaction with Core 5 over the Doll Link.
package body

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/auth"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/execution"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/execution/process"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/link/core5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

var testUpgrader = websocket.Upgrader{}

// blockingExecutor wraps a process.Executor and blocks on Execute until context is cancelled.
type blockingExecutor struct {
	*process.Executor
}

func (e *blockingExecutor) Execute(ctx context.Context, command string, args []string, env []string, dir string, stdin string, timeout time.Duration) (int, string, string, error) {
	close(execStarted)
	<-ctx.Done() // Wait for cancellation
	close(execCancelled)
	return -1, "", "", ctx.Err()
}

var (
	execStarted   chan struct{}
	execCancelled chan struct{}
)

func mustNonceForCore5(t *testing.T) string {
	t.Helper()
	s, err := auth.RandomNonce()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestExecutionResultSerialization verifies that execution.result frames
// are properly serialized without double-marshaling. It decodes the actual
// WebSocket frame and validates the canonical ExecutionResult structure.
func TestExecutionResultSerialization(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Generate keys
	corePub, corePriv, _ := ed25519.GenerateKey(rand.Reader)
	_, bodyPriv, _ := ed25519.GenerateKey(rand.Reader)

	bodyID := "test-body-" + uuid.NewString()
	coreID := "test-core"

	executor := process.NewExecutor()
	adapter := execution.NewCore5(executor, 30*time.Second)

	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := testUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// Read hello
		_, _, err = conn.ReadMessage()
		if err != nil {
			return
		}

		// Send challenge
		challenge := auth.Challenge{
			Version:   auth.Version,
			Type:      auth.TypeChallenge,
			CoreID:    coreID,
			BodyID:    bodyID,
			SessionID: "test-session",
			BodyNonce: mustNonceForCore5(t),
			CoreNonce: mustNonceForCore5(t),
			ExpiresAt: time.Now().Add(30 * time.Second),
			Algorithm: auth.AlgorithmEd25519,
		}
		_ = auth.SignChallenge(corePriv, &challenge)
		_ = conn.WriteJSON(challenge)

		// Read proof
		_, _, err = conn.ReadMessage()
		if err != nil {
			return
		}

		// Send accept
		accept := auth.Accept{
			Version:   auth.Version,
			Type:      auth.TypeAccept,
			CoreID:    coreID,
			BodyID:    bodyID,
			SessionID: "test-session",
			Status:    auth.StatusAuthenticated,
		}
		_ = auth.SignAccept(corePriv, &accept)
		_ = conn.WriteJSON(accept)

		// Read caps
		_, _, err = conn.ReadMessage()
		if err != nil {
			return
		}

		// Send execution request
		req := core5.ExecutionRequest{
			Type:      core5.TypeExecutionRequest,
			ID:        "exec-test",
			Timestamp: time.Now().UTC(),
			DollID:    "doll",
			BodyID:    bodyID,
			Payload: core5.ExecutionRequestPayload{
				Capability:  "process",
				Operation:   "run",
				ExecutionID: uuid.NewString(),
				Arguments:   map[string]any{"command": "echo", "args": []string{"hello"}},
			},
		}
		_ = conn.WriteJSON(req)

		// Read the result frame and verify it's properly structured
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}

		var result core5.ExecutionResult
		if err := json.Unmarshal(data, &result); err != nil {
			t.Errorf("Failed to unmarshal execution.result: %v", err)
			return
		}

		// Validate canonical structure
		if result.Type != core5.TypeExecutionResult {
			t.Errorf("Expected Type=%q, got %q", core5.TypeExecutionResult, result.Type)
		}
		if result.CorrelationID != req.ID {
			t.Errorf("Expected CorrelationID=%q, got %q", req.ID, result.CorrelationID)
		}
		if result.Payload.ExecutionID != req.Payload.ExecutionID {
			t.Errorf("Expected ExecutionID=%q, got %q", req.Payload.ExecutionID, result.Payload.ExecutionID)
		}
		if result.Payload.Status != core5.ExecutionStatusSuccess {
			t.Errorf("Expected Status=ExecutionStatusSuccess, got %v", result.Payload.Status)
		}
		// Verify result payload has exit_code, stdout, stderr
		if result.Payload.Result == nil {
			t.Error("Expected Result payload to be non-nil")
		} else {
			resultMap := result.Payload.Result
			if _, ok := resultMap["exit_code"]; !ok {
				t.Error("Expected exit_code in Result")
			}
			if _, ok := resultMap["stdout"]; !ok {
				t.Error("Expected stdout in Result")
			}
			if _, ok := resultMap["stderr"]; !ok {
				t.Error("Expected stderr in Result")
			}
			if resultMap["exit_code"] != float64(0) {
				t.Errorf("Expected exit_code 0, got %v", resultMap["exit_code"])
			}
			if resultMap["stdout"] != "hello\n" {
				t.Errorf("Expected stdout 'hello\\n', got %q", resultMap["stdout"])
			}
		}
	}))
	defer testServer.Close()

	wsURL := "ws" + testServer.URL[len("http"):] + "/ws"

	creds := Core5Credentials{
		BodyID:        bodyID,
		PrivateKey:    bodyPriv,
		CoreID:        coreID,
		CorePublicKey: corePub,
	}

	session, err := DialCore5(wsURL, creds, adapter)
	if err != nil {
		t.Fatalf("DialCore5 failed: %v", err)
	}

	sessionID, err := session.Authenticate(ctx)
	if err != nil {
		t.Fatalf("Authenticate failed: %v", err)
	}
	if sessionID == "" {
		t.Fatal("empty session ID")
	}

	// Advertise capabilities
	caps := execution.AdvertisedCore5()
	if err := session.AdvertiseCapabilities(ctx, caps); err != nil {
		t.Fatalf("AdvertiseCapabilities failed: %v", err)
	}

	// Serve and wait for completion
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- session.Serve(ctx)
	}()

	select {
	case err := <-serveErr:
		if err != nil && err != context.Canceled {
			t.Logf("serve ended: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not complete")
	}

	_ = session.Close()
}

func TestCore5SessionHandlesDisconnectDuringExecution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Generate keys
	corePub, corePriv, _ := ed25519.GenerateKey(rand.Reader)
	_, bodyPriv, _ := ed25519.GenerateKey(rand.Reader)

	bodyID := "test-body-" + uuid.NewString()
	coreID := "test-core"

	executor := process.NewExecutor()
	blockingExec := &blockingExecutor{executor}
	adapter := execution.NewCore5(blockingExec, 30*time.Second)

	execStarted = make(chan struct{})
	execCancelled = make(chan struct{})

	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := testUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// Read hello
		_, _, err = conn.ReadMessage()
		if err != nil {
			return
		}

		// Send challenge
		challenge := auth.Challenge{
			Version:   auth.Version,
			Type:      auth.TypeChallenge,
			CoreID:    coreID,
			BodyID:    bodyID,
			SessionID: "test-session",
			BodyNonce: mustNonceForCore5(t),
			CoreNonce: mustNonceForCore5(t),
			ExpiresAt: time.Now().Add(30 * time.Second),
			Algorithm: auth.AlgorithmEd25519,
		}
		_ = auth.SignChallenge(corePriv, &challenge)
		_ = conn.WriteJSON(challenge)

		// Read proof
		_, _, err = conn.ReadMessage()
		if err != nil {
			return
		}

		// Send accept
		accept := auth.Accept{
			Version:   auth.Version,
			Type:      auth.TypeAccept,
			CoreID:    coreID,
			BodyID:    bodyID,
			SessionID: "test-session",
			Status:    auth.StatusAuthenticated,
		}
		_ = auth.SignAccept(corePriv, &accept)
		_ = conn.WriteJSON(accept)

		// Read caps
		_, _, err = conn.ReadMessage()
		if err != nil {
			return
		}

		// Send first execution request
		req1 := core5.ExecutionRequest{
			Type:      core5.TypeExecutionRequest,
			ID:        "exec-1",
			Timestamp: time.Now().UTC(),
			DollID:    "doll",
			BodyID:    bodyID,
			Payload: core5.ExecutionRequestPayload{
				Capability:  "process",
				Operation:   "run",
				ExecutionID: uuid.NewString(),
				Arguments:   map[string]any{"command": "sleep", "args": []string{"10"}},
			},
		}
		_ = conn.WriteJSON(req1)

		<-execStarted // Wait for first exec to start

		// Send second execution request while first is running
		req2 := core5.ExecutionRequest{
			Type:      core5.TypeExecutionRequest,
			ID:        "exec-2",
			Timestamp: time.Now().UTC(),
			DollID:    "doll",
			BodyID:    bodyID,
			Payload: core5.ExecutionRequestPayload{
				Capability:  "process",
				Operation:   "run",
				ExecutionID: uuid.NewString(),
				Arguments:   map[string]any{"command": "echo", "args": []string{"hello"}},
			},
		}
		_ = conn.WriteJSON(req2)

		time.Sleep(100 * time.Millisecond)
		conn.Close()
	}))
	defer testServer.Close()

	wsURL := "ws" + testServer.URL[len("http"):] + "/ws"

	creds := Core5Credentials{
		BodyID:        bodyID,
		PrivateKey:    bodyPriv,
		CoreID:        coreID,
		CorePublicKey: corePub,
	}

	session, err := DialCore5(wsURL, creds, adapter)
	if err != nil {
		t.Fatalf("DialCore5 failed: %v", err)
	}

	sessionID, err := session.Authenticate(ctx)
	if err != nil {
		t.Fatalf("Authenticate failed: %v", err)
	}
	if sessionID == "" {
		t.Fatal("empty session ID")
	}

	// Advertise capabilities
	caps := execution.AdvertisedCore5()
	if err := session.AdvertiseCapabilities(ctx, caps); err != nil {
		t.Fatalf("AdvertiseCapabilities failed: %v", err)
	}

	// Start serving in background
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- session.Serve(ctx)
	}()

	// Wait for execution to start
	select {
	case <-execStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("execution did not start")
	case <-serveErr:
		t.Fatal("serve ended unexpectedly before disconnect")
	}

	// Now wait for cancellation due to disconnect
	select {
	case <-execCancelled:
		t.Log("execution was cancelled as expected")
	case <-time.After(3 * time.Second):
		t.Fatal("execution was not cancelled after disconnect")
	}

	// The serve loop should exit due to connection close
	select {
	case err := <-serveErr:
		if err != nil && err != context.Canceled {
			t.Logf("serve ended with error (expected): %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serve did not end after disconnect")
	}

	_ = session.Close()
}

func TestCore5SessionNoReplayAfterReconnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	corePub, corePriv, _ := ed25519.GenerateKey(rand.Reader)
	_, bodyPriv, _ := ed25519.GenerateKey(rand.Reader)

	bodyID := "test-body-" + uuid.NewString()
	coreID := "test-core"

	executor := process.NewExecutor()
	blockingExec := &blockingExecutor{executor}
	adapter := execution.NewCore5(blockingExec, 30*time.Second)

	execStarted = make(chan struct{})
	execCancelled = make(chan struct{})

	serverCount := 0
	var mu sync.Mutex

	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := testUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		mu.Lock()
		serverCount++
		connNum := serverCount
		mu.Unlock()

		// Read hello
		_, _, err = conn.ReadMessage()
		if err != nil {
			return
		}

		// Send challenge
		challenge := auth.Challenge{
			Version:   auth.Version,
			Type:      auth.TypeChallenge,
			CoreID:    coreID,
			BodyID:    bodyID,
			SessionID: fmt.Sprintf("session-%d", connNum),
			BodyNonce: mustNonceForCore5(t),
			CoreNonce: mustNonceForCore5(t),
			ExpiresAt: time.Now().Add(30 * time.Second),
			Algorithm: auth.AlgorithmEd25519,
		}
		_ = auth.SignChallenge(corePriv, &challenge)
		_ = conn.WriteJSON(challenge)

		// Read proof
		_, _, err = conn.ReadMessage()
		if err != nil {
			return
		}

		// Send accept
		accept := auth.Accept{
			Version:   auth.Version,
			Type:      auth.TypeAccept,
			CoreID:    coreID,
			BodyID:    bodyID,
			SessionID: fmt.Sprintf("session-%d", connNum),
			Status:    auth.StatusAuthenticated,
		}
		_ = auth.SignAccept(corePriv, &accept)
		_ = conn.WriteJSON(accept)

		// Read caps
		_, _, err = conn.ReadMessage()
		if err != nil {
			return
		}

		if connNum == 1 {
			// First connection: send execution request
			req1 := core5.ExecutionRequest{
				Type:      core5.TypeExecutionRequest,
				ID:        "exec-1",
				Timestamp: time.Now().UTC(),
				DollID:    "doll",
				BodyID:    bodyID,
				Payload: core5.ExecutionRequestPayload{
					Capability:  "process",
					Operation:   "run",
					ExecutionID: uuid.NewString(),
					Arguments:   map[string]any{"command": "sleep", "args": []string{"10"}},
				},
			}
			_ = conn.WriteJSON(req1)

			<-execStarted
			time.Sleep(100 * time.Millisecond)
			conn.Close()
		} else if connNum == 2 {
			// Second connection (reconnect): wait a bit then close
			time.Sleep(200 * time.Millisecond)
			conn.Close()
		}
	}))
	defer testServer.Close()

	wsURL := "ws" + testServer.URL[len("http"):] + "/ws"

	creds := Core5Credentials{
		BodyID:        bodyID,
		PrivateKey:    bodyPriv,
		CoreID:        coreID,
		CorePublicKey: corePub,
	}

	// First connection
	session1, err := DialCore5(wsURL, creds, adapter)
	if err != nil {
		t.Fatalf("DialCore5 (1) failed: %v", err)
	}

	sessionID1, err := session1.Authenticate(ctx)
	if err != nil {
		t.Fatalf("Authenticate (1) failed: %v", err)
	}
	if sessionID1 == "" {
		t.Fatal("empty session ID (1)")
	}

	caps := execution.AdvertisedCore5()
	if err := session1.AdvertiseCapabilities(ctx, caps); err != nil {
		t.Fatalf("AdvertiseCapabilities (1) failed: %v", err)
	}

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- session1.Serve(ctx)
	}()

	// Wait for execution to start
	select {
	case <-execStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("execution did not start on first connection")
	case <-serveErr:
		t.Fatal("serve ended unexpectedly before disconnect")
	}

	// Wait for cancellation
	select {
	case <-execCancelled:
		t.Log("execution cancelled on first connection")
	case <-time.After(3 * time.Second):
		t.Fatal("execution not cancelled after first disconnect")
	}

	// Wait for serve to end
	select {
	case <-serveErr:
	case <-time.After(2 * time.Second):
		t.Fatal("serve (1) did not end")
	}

	_ = session1.Close()

	// Now reconnect (second connection)
	session2, err := DialCore5(wsURL, creds, adapter)
	if err != nil {
		t.Fatalf("DialCore5 (2) failed: %v", err)
	}

	sessionID2, err := session2.Authenticate(ctx)
	if err != nil {
		t.Fatalf("Authenticate (2) failed: %v", err)
	}
	if sessionID2 == "" {
		t.Fatal("empty session ID (2)")
	}

	if err := session2.AdvertiseCapabilities(ctx, caps); err != nil {
		t.Fatalf("AdvertiseCapabilities (2) failed: %v", err)
	}

	serveErr2 := make(chan error, 1)
	go func() {
		serveErr2 <- session2.Serve(ctx)
	}()

	// Wait for second serve to end (server will close after 200ms)
	select {
	case <-serveErr2:
	case <-time.After(2 * time.Second):
		t.Fatal("serve (2) did not end")
	}

	_ = session2.Close()

	// Verify the execution was NOT replayed on reconnect
	// The execStarted channel was already consumed, so if it fires again it would panic
	// Instead, we verify the server only saw one execution request on the first connection
	mu.Lock()
	finalCount := serverCount
	mu.Unlock()

	if finalCount != 2 {
		t.Fatalf("expected 2 connections, got %d", finalCount)
	}

	// Test passes if we get here - no replay occurred
	t.Log("Successfully verified no replay after reconnect")
}
