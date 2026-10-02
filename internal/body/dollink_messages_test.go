// Tests for inbound Doll Link message handling after negotiation.
package body

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/link"
)

// TestClientExecutionRequest tests handling of execution.request messages.
func TestClientExecutionRequest(t *testing.T) {
	ft := NewFakeTransport()
	client := NewClient(ft, "test-body-id")

	// Set up a handler for execution.request
	handlerCalled := make(chan struct{}, 1)
	expectedReqID := "exec_123"
	client.SetExecutionRequestHandler(func(ctx context.Context, req link.ExecutionRequestPayload) (link.ExecutionResultPayload, error) {
		// Verify the request
		if req.ExecutionID != expectedReqID {
			t.Fatalf("expected execution ID %s, got %s", expectedReqID, req.ExecutionID)
		}
		if req.Capability != "terminal" {
			t.Fatalf("expected capability terminal, got %s", req.Capability)
		}
		if req.Operation != "input" {
			t.Fatalf("expected operation input, got %s", req.Operation)
		}
		handlerCalled <- struct{}{}
		return link.ExecutionResultPayload{
			ExecutionID: req.ExecutionID,
			Status:      "success",
			Result:      map[string]interface{}{"output": "hello"},
		}, nil
	})

	errc := make(chan error, 1)
	go func() {
		// 1. body.hello
		data := <-ft.sendCh
		var bodyHello link.Envelope
		if err := json.Unmarshal(data, &bodyHello); err != nil {
			errc <- fmt.Errorf("failed to unmarshal body.hello: %v", err)
			return
		}
		if bodyHello.Type != link.TypeBodyHello {
			errc <- fmt.Errorf("expected body.hello, got %s", bodyHello.Type)
			return
		}
		// 2. core.hello
		marshaled, err := json.Marshal(map[string]interface{}{"doll_link_version": 1, "body_contract_version": 1})
		if err != nil {
			errc <- fmt.Errorf("failed to marshal core.hello: %v", err)
			return
		}
		coreHello := link.Envelope{
			Type:      link.TypeCoreHello,
			ID:        "evt_core",
			BodyID:    "test-body-id",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Payload:   marshaled,
		}
		coreHelloData, err := json.Marshal(coreHello)
		if err != nil {
			errc <- fmt.Errorf("failed to marshal core.hello: %v", err)
			return
		}
		ft.recvCh <- coreHelloData

		// 3. body.capabilities
		data = <-ft.sendCh
		var bodyCap link.Envelope
		if err := json.Unmarshal(data, &bodyCap); err != nil {
			errc <- fmt.Errorf("failed to unmarshal body.capabilities: %v", err)
			return
		}
		if bodyCap.Type != link.TypeBodyCapabilities {
			errc <- fmt.Errorf("expected body.capabilities, got %s", bodyCap.Type)
			return
		}
		// 4. body.ready
		data = <-ft.sendCh
		var bodyReady link.Envelope
		if err := json.Unmarshal(data, &bodyReady); err != nil {
			errc <- fmt.Errorf("failed to unmarshal body.ready: %v", err)
			return
		}
		if bodyReady.Type != link.TypeBodyReady {
			errc <- fmt.Errorf("expected body.ready, got %s", bodyReady.Type)
			return
		}
		// 5. Send execution.request to the client (client reads it via recvCh).
		execReqPayload, err := json.Marshal(link.ExecutionRequestPayload{
			ExecutionID: expectedReqID,
			Capability:  "terminal",
			Operation:   "input",
		})
		if err != nil {
			errc <- fmt.Errorf("failed to marshal execution.request: %v", err)
			return
		}
		execReq := link.Envelope{
			Type:      link.TypeExecutionRequest,
			ID:        "evt_req",
			BodyID:    "test-body-id",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Payload:   execReqPayload,
		}
		execReqData, err := json.Marshal(execReq)
		if err != nil {
			errc <- fmt.Errorf("failed to marshal execution.request: %v", err)
			return
		}
		ft.recvCh <- execReqData

		// 6. Receive the client's execution.result reply (client sends it via sendCh).
		data = <-ft.sendCh
		var execResult link.Envelope
		if err := json.Unmarshal(data, &execResult); err != nil {
			errc <- fmt.Errorf("failed to unmarshal execution.result: %v", err)
			return
		}
		if execResult.Type != link.TypeExecutionResult {
			errc <- fmt.Errorf("expected execution.result, got %s", execResult.Type)
			return
		}
		if execResult.CorrelationID != execReq.ID {
			errc <- fmt.Errorf("expected correlation ID %s, got %s", execReq.ID, execResult.CorrelationID)
			return
		}
		var res link.ExecutionResultPayload
		if err := json.Unmarshal(execResult.Payload, &res); err != nil {
			errc <- fmt.Errorf("failed to unmarshal execution.result payload: %v", err)
			return
		}
		if res.ExecutionID != expectedReqID {
			errc <- fmt.Errorf("expected execution ID %s, got %s", expectedReqID, res.ExecutionID)
			return
		}
		errc <- nil
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clientDone := make(chan error, 1)
	go func() {
		clientDone <- client.Run(ctx)
	}()

	// Wait for the peer to observe the full request/reply exchange.
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("%v", err)
		}
	case <-time.After(time.Second * 5):
		t.Fatalf("timed out waiting for peer")
	}

	// Verify the handler was called.
	select {
	case <-handlerCalled:
		// good
	case <-time.After(time.Second * 1):
		t.Fatal("execution request handler was not called")
	}

	// Cancel the context to stop the client.
	cancel()
	select {
	case err := <-clientDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("client run failed after cancel: %v", err)
		}
	case <-time.After(time.Second * 1):
		t.Fatal("client run did not return after cancel")
	}
}

// TestClientSessionOpenRemoved verifies the M4 client no longer performs active
// session.open handling: receiving session.open must not produce a session.opened
// reply. The M4 client is not session-aware, so an incoming session.open is an
// unhandled message and the client reports it as such instead of opening a session.
func TestClientSessionOpenRemoved(t *testing.T) {
	ft := NewFakeTransport()
	client := NewClient(ft, "test-body-id")

	errc := make(chan error, 1)
	go func() {
		// 1. body.hello
		data := <-ft.sendCh
		var bodyHello link.Envelope
		if err := json.Unmarshal(data, &bodyHello); err != nil {
			errc <- fmt.Errorf("failed to unmarshal body.hello: %v", err)
			return
		}
		if bodyHello.Type != link.TypeBodyHello {
			errc <- fmt.Errorf("expected body.hello, got %s", bodyHello.Type)
			return
		}
		// 2. core.hello
		marshaled, err := json.Marshal(map[string]interface{}{"doll_link_version": 1, "body_contract_version": 1})
		if err != nil {
			errc <- fmt.Errorf("failed to marshal core.hello: %v", err)
			return
		}
		coreHello := link.Envelope{
			Type:      link.TypeCoreHello,
			ID:        "evt_core",
			BodyID:    "test-body-id",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Payload:   marshaled,
		}
		coreHelloData, err := json.Marshal(coreHello)
		if err != nil {
			errc <- fmt.Errorf("failed to marshal core.hello: %v", err)
			return
		}
		ft.recvCh <- coreHelloData

		// 3. body.capabilities
		data = <-ft.sendCh
		var bodyCap link.Envelope
		if err := json.Unmarshal(data, &bodyCap); err != nil {
			errc <- fmt.Errorf("failed to unmarshal body.capabilities: %v", err)
			return
		}
		if bodyCap.Type != link.TypeBodyCapabilities {
			errc <- fmt.Errorf("expected body.capabilities, got %s", bodyCap.Type)
			return
		}
		// 4. body.ready
		data = <-ft.sendCh
		var bodyReady link.Envelope
		if err := json.Unmarshal(data, &bodyReady); err != nil {
			errc <- fmt.Errorf("failed to unmarshal body.ready: %v", err)
			return
		}
		if bodyReady.Type != link.TypeBodyReady {
			errc <- fmt.Errorf("expected body.ready, got %s", bodyReady.Type)
			return
		}
		// 5. Send session.open. No active handler exists, so no session.opened reply
		// is generated; the client treats it as an unhandled message and fails.
		sessionOpen := link.Envelope{
			Type:      link.TypeSessionOpen,
			ID:        "evt_session_open",
			BodyID:    "test-body-id",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Payload:   json.RawMessage(`{"local_session_id":"local_sess_123","kind":"test-kind"}`),
		}
		sessionOpenData, err := json.Marshal(sessionOpen)
		if err != nil {
			errc <- fmt.Errorf("failed to marshal session.open: %v", err)
			return
		}
		ft.recvCh <- sessionOpenData
		errc <- nil
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clientDone := make(chan error, 1)
	go func() {
		clientDone <- client.Run(ctx)
	}()

	// Peer completes its script without error.
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("%v", err)
		}
	case <-time.After(time.Second * 5):
		t.Fatalf("timed out waiting for peer")
	}

	// Client must fail on the unhandled session.open (it does not open a session
	// nor emit session.opened).
	select {
	case err := <-clientDone:
		if err == nil {
			t.Fatal("expected client run to fail on unhandled session.open")
		}
		if !strings.Contains(err.Error(), "session.open") {
			t.Fatalf("expected session.open to be unhandled, got %v", err)
		}
	case <-time.After(time.Second * 5):
		t.Fatal("client run did not return after unhandled session.open")
	}
	cancel()
}

// TestClientBodyEvent tests handling of body.event messages.
func TestClientBodyEvent(t *testing.T) {
	ft := NewFakeTransport()
	client := NewClient(ft, "test-body-id")

	// Set up a handler for body.event
	handlerCalled := make(chan struct{}, 1)
	expectedEvent := "changed"
	expectedCap := "filesystem"
	client.SetBodyEventHandler(func(ctx context.Context, ev link.EventPayload) error {
		// Verify the event
		if ev.Event != expectedEvent {
			t.Fatalf("expected event %s, got %s", expectedEvent, ev.Event)
		}
		if ev.Capability != expectedCap {
			t.Fatalf("expected capability %s, got %s", expectedCap, ev.Capability)
		}
		handlerCalled <- struct{}{}
		return nil
	})

	errc := make(chan error, 1)
	go func() {
		// 1. body.hello
		data := <-ft.sendCh
		var bodyHello link.Envelope
		if err := json.Unmarshal(data, &bodyHello); err != nil {
			errc <- fmt.Errorf("failed to unmarshal body.hello: %v", err)
			return
		}
		if bodyHello.Type != link.TypeBodyHello {
			errc <- fmt.Errorf("expected body.hello, got %s", bodyHello.Type)
			return
		}
		// 2. core.hello
		marshaled, err := json.Marshal(map[string]interface{}{"doll_link_version": 1, "body_contract_version": 1})
		if err != nil {
			errc <- fmt.Errorf("failed to marshal core.hello: %v", err)
			return
		}
		coreHello := link.Envelope{
			Type:      link.TypeCoreHello,
			ID:        "evt_core",
			BodyID:    "test-body-id",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Payload:   marshaled,
		}
		coreHelloData, err := json.Marshal(coreHello)
		if err != nil {
			errc <- fmt.Errorf("failed to marshal core.hello: %v", err)
			return
		}
		ft.recvCh <- coreHelloData

		// 3. body.capabilities
		data = <-ft.sendCh
		var bodyCap link.Envelope
		if err := json.Unmarshal(data, &bodyCap); err != nil {
			errc <- fmt.Errorf("failed to unmarshal body.capabilities: %v", err)
			return
		}
		if bodyCap.Type != link.TypeBodyCapabilities {
			errc <- fmt.Errorf("expected body.capabilities, got %s", bodyCap.Type)
			return
		}
		// 4. body.ready
		data = <-ft.sendCh
		var bodyReady link.Envelope
		if err := json.Unmarshal(data, &bodyReady); err != nil {
			errc <- fmt.Errorf("failed to unmarshal body.ready: %v", err)
			return
		}
		if bodyReady.Type != link.TypeBodyReady {
			errc <- fmt.Errorf("expected body.ready, got %s", bodyReady.Type)
			return
		}
		// 5. Send body.event to the client (observation, no reply expected).
		eventPayload, err := json.Marshal(link.EventPayload{
			Event:      expectedEvent,
			Capability: expectedCap,
		})
		if err != nil {
			errc <- fmt.Errorf("failed to marshal body.event: %v", err)
			return
		}
		bodyEvent := link.Envelope{
			Type:      link.TypeBodyEvent,
			ID:        "evt_event",
			BodyID:    "test-body-id",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Payload:   eventPayload,
		}
		bodyEventData, err := json.Marshal(bodyEvent)
		if err != nil {
			errc <- fmt.Errorf("failed to marshal body.event: %v", err)
			return
		}
		ft.recvCh <- bodyEventData
		errc <- nil
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clientDone := make(chan error, 1)
	go func() {
		clientDone <- client.Run(ctx)
	}()

	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("%v", err)
		}
	case <-time.After(time.Second * 5):
		t.Fatalf("timed out waiting for peer")
	}
	select {
	case <-handlerCalled:
		// good
	case <-time.After(time.Second * 1):
		t.Fatal("body event handler was not called")
	}
	cancel()
	select {
	case err := <-clientDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("client run failed after cancel: %v", err)
		}
	case <-time.After(time.Second * 1):
		t.Fatal("client run did not return after cancel")
	}
}

// TestClientUnknownMessageType tests that unknown message types return an error.
func TestClientUnknownMessageType(t *testing.T) {
	ft := NewFakeTransport()
	client := NewClient(ft, "test-body-id")

	errc := make(chan error, 1)
	go func() {
		// 1. body.hello
		data := <-ft.sendCh
		var bodyHello link.Envelope
		if err := json.Unmarshal(data, &bodyHello); err != nil {
			errc <- fmt.Errorf("failed to unmarshal body.hello: %v", err)
			return
		}
		if bodyHello.Type != link.TypeBodyHello {
			errc <- fmt.Errorf("expected body.hello, got %s", bodyHello.Type)
			return
		}
		// 2. core.hello
		marshaled, err := json.Marshal(map[string]interface{}{"doll_link_version": 1, "body_contract_version": 1})
		if err != nil {
			errc <- fmt.Errorf("failed to marshal core.hello: %v", err)
			return
		}
		coreHello := link.Envelope{
			Type:      link.TypeCoreHello,
			ID:        "evt_core",
			BodyID:    "test-body-id",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Payload:   marshaled,
		}
		coreHelloData, err := json.Marshal(coreHello)
		if err != nil {
			errc <- fmt.Errorf("failed to marshal core.hello: %v", err)
			return
		}
		ft.recvCh <- coreHelloData
		// 3. body.capabilities
		data = <-ft.sendCh
		var bodyCap link.Envelope
		if err := json.Unmarshal(data, &bodyCap); err != nil {
			errc <- fmt.Errorf("failed to unmarshal body.capabilities: %v", err)
			return
		}
		if bodyCap.Type != link.TypeBodyCapabilities {
			errc <- fmt.Errorf("expected body.capabilities, got %s", bodyCap.Type)
			return
		}
		// 4. body.ready
		data = <-ft.sendCh
		var bodyReady link.Envelope
		if err := json.Unmarshal(data, &bodyReady); err != nil {
			errc <- fmt.Errorf("failed to unmarshal body.ready: %v", err)
			return
		}
		if bodyReady.Type != link.TypeBodyReady {
			errc <- fmt.Errorf("expected body.ready, got %s", bodyReady.Type)
			return
		}
		// 5. Send an unknown message type to the client; the client should error.
		unknown := link.Envelope{
			Type:      "unknown.message",
			ID:        "evt_unknown",
			BodyID:    "test-body-id",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Payload:   json.RawMessage(`{}`),
		}
		unknownData, err := json.Marshal(unknown)
		if err != nil {
			errc <- fmt.Errorf("failed to marshal unknown.message: %v", err)
			return
		}
		ft.recvCh <- unknownData
		errc <- nil
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clientDone := make(chan error, 1)
	go func() {
		clientDone <- client.Run(ctx)
	}()

	// The client should fail on the unknown message type and return an error
	// (it does so before the context is ever cancelled).
	select {
	case err := <-clientDone:
		if err == nil {
			t.Fatal("expected client run to fail on unknown message type")
		}
		if !strings.Contains(err.Error(), "unknown.message") {
			t.Fatalf("expected unknown message type error, got %v", err)
		}
	case <-time.After(time.Second * 5):
		t.Fatal("client run timed out")
	}
	cancel()
}
