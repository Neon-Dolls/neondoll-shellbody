// Package body provides tests for the Doll Link client.
package body

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/link"
)

// fakeTransport implements Transport for testing.
// It uses unbuffered channels to synchronize sends and receives, and
// marshals/unmarshals Envelopes to/from JSON to simulate the wire format.
type fakeTransport struct {
	mu       sync.Mutex
	sendCh   chan []byte // channel for JSON bytes sent by the client
	recvCh   chan []byte // channel for JSON bytes to be received by the client
	sendErr  error
	recvErr  error
	closeErr error
	closed   bool
}

// NewFakeTransport creates a fake transport for testing.
func NewFakeTransport() *fakeTransport {
	return &fakeTransport{
		sendCh: make(chan []byte),
		recvCh: make(chan []byte),
	}
}

// Send marshals the Envelope to JSON and sends it on sendCh.
func (t *fakeTransport) Send(ctx context.Context, msg link.Envelope) error {
	t.mu.Lock()
	if t.sendErr != nil {
		t.mu.Unlock()
		return t.sendErr
	}
	t.mu.Unlock()
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal envelope: %w", err)
	}
	select {
	case t.sendCh <- data:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Receive waits for JSON bytes on recvCh, unmarshals them into an Envelope, and returns it.
func (t *fakeTransport) Receive(ctx context.Context) (*link.Envelope, error) {
	t.mu.Lock()
	if t.recvErr != nil {
		t.mu.Unlock()
		return nil, t.recvErr
	}
	t.mu.Unlock()
	select {
	case data := <-t.recvCh:
		var msg link.Envelope
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal envelope: %w", err)
		}
		return &msg, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close marks the transport as closed and returns a close error.
func (t *fakeTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	return t.closeErr
}

// TestClientNegotiation tests that version negotiation works correctly.
func TestClientNegotiation(t *testing.T) {
	ft := NewFakeTransport()
	client := NewClient(ft, "test-body-id")

	errc := make(chan error, 1)
	go func() {
		// 1. Receive body.hello from client (as JSON bytes). The client sends it first.
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
		// 2. Send core.hello back to the client (the client Receive reads recvCh).
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
		errc <- nil
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clientDone := make(chan error, 1)
	go func() {
		clientDone <- client.negotiateVersions(ctx)
	}()

	// The peer and the client run concurrently and synchronize through the
	// unbuffered channels. Wait for the peer to complete the handshake.
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("%v", err)
		}
	case <-time.After(time.Second * 5):
		t.Fatalf("timed out waiting for peer")
	}

	select {
	case err := <-clientDone:
		if err != nil {
			t.Fatalf("version negotiation failed: %v", err)
		}
	case <-time.After(time.Second * 5):
		t.Fatalf("client negotiation timed out")
	}

	// Check that versions were negotiated correctly.
	if client.negotiatedDollLinkVersion() != 1 {
		t.Fatalf("expected doll link version 1, got %d", client.negotiatedDollLinkVersion())
	}
	if client.negotiatedBodyContractVersion() != 1 {
		t.Fatalf("expected body contract version 1, got %d", client.negotiatedBodyContractVersion())
	}
}

// TestClientCapabilities tests that capabilities are advertised.
func TestClientCapabilities(t *testing.T) {
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
		var capResp link.CapabilitiesPayload
		if err := json.Unmarshal(bodyCap.Payload, &capResp); err != nil {
			errc <- fmt.Errorf("failed to unmarshal capabilities payload: %v", err)
			return
		}
		if len(capResp.Capabilities) == 0 {
			errc <- fmt.Errorf("expected at least one capability")
			return
		}
		if capResp.Capabilities[0].ID != "terminal" {
			errc <- fmt.Errorf("expected capability ID \"terminal\", got %s", capResp.Capabilities[0].ID)
			return
		}
		var hasInput, hasOutput bool
		for _, op := range capResp.Capabilities[0].Operations {
			switch op {
			case "input":
				hasInput = true
			case "output":
				hasOutput = true
			}
		}
		if !hasInput || !hasOutput {
			errc <- fmt.Errorf("expected operations to include \"input\" and \"output\", got %v", capResp.Capabilities[0].Operations)
			return
		}
		if !capResp.Capabilities[0].Available {
			errc <- fmt.Errorf("expected capability to be available")
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

		errc <- nil
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clientDone := make(chan error, 1)
	go func() {
		clientDone <- client.Run(ctx)
	}()

	// The peer consumes the negotiation/capabilities/ready sequence. Once the
	// peer is satisfied, cancel the context to stop the client's receive loop.
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("%v", err)
		}
	case <-time.After(time.Second * 5):
		t.Fatalf("timed out waiting for peer")
	}
	cancel()
	select {
	case err := <-clientDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("client run failed: %v", err)
		}
	case <-time.After(time.Second * 3):
		t.Fatalf("client run did not return after cancel")
	}
}

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

// TestClientSessionOpen tests handling of session.open messages.
func TestClientSessionOpen(t *testing.T) {
	ft := NewFakeTransport()
	client := NewClient(ft, "test-body-id")

	// Set up a session.open handler so the client can reply with session.opened.
	handlerCalled := make(chan struct{}, 1)
	client.SetSessionOpenHandler(func(ctx context.Context, req link.SessionOpenPayload) (link.SessionOpenedPayload, error) {
		handlerCalled <- struct{}{}
		return link.SessionOpenedPayload{
			SessionID:  "sess_123",
			Capability: "test-capability",
			Metadata:   map[string]string{"foo": "bar"},
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
		// 5. Send session.open to the client.
		sessionOpenPayload, err := json.Marshal(link.SessionOpenPayload{
			LocalSessionID: "local_sess_123",
			Kind:           "test-kind",
			Metadata:       map[string]string{"foo": "bar"},
		})
		if err != nil {
			errc <- fmt.Errorf("failed to marshal session.open: %v", err)
			return
		}
		sessionOpen := link.Envelope{
			Type:      link.TypeSessionOpen,
			ID:        "evt_session_open",
			BodyID:    "test-body-id",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Payload:   sessionOpenPayload,
		}
		sessionOpenData, err := json.Marshal(sessionOpen)
		if err != nil {
			errc <- fmt.Errorf("failed to marshal session.open: %v", err)
			return
		}
		ft.recvCh <- sessionOpenData

		// 6. Receive the client's session.opened reply.
		data = <-ft.sendCh
		var sessionOpened link.Envelope
		if err := json.Unmarshal(data, &sessionOpened); err != nil {
			errc <- fmt.Errorf("failed to unmarshal session.opened: %v", err)
			return
		}
		if sessionOpened.Type != link.TypeSessionOpened {
			errc <- fmt.Errorf("expected session.opened, got %s", sessionOpened.Type)
			return
		}
		var sessionOpenedPayload link.SessionOpenedPayload
		if err := json.Unmarshal(sessionOpened.Payload, &sessionOpenedPayload); err != nil {
			errc <- fmt.Errorf("failed to unmarshal session.opened payload: %v", err)
			return
		}
		if sessionOpenedPayload.SessionID != "sess_123" {
			errc <- fmt.Errorf("expected session ID \"sess_123\", got %s", sessionOpenedPayload.SessionID)
			return
		}
		if sessionOpenedPayload.Capability != "test-capability" {
			errc <- fmt.Errorf("expected capability \"test-capability\", got %s", sessionOpenedPayload.Capability)
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
		t.Fatal("session.open handler was not called")
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

// TestClientReconnectionSameBodyID tests that reconnecting with the same Body ID works.
func TestClientReconnectionSameBodyID(t *testing.T) {
	// First connection
	ft1 := NewFakeTransport()
	client1 := NewClient(ft1, "reconnect-body-id")

	errc := make(chan error, 1)
	go func() {
		// 1. body.hello
		data := <-ft1.sendCh
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
			ID:        "evt_core1",
			BodyID:    "reconnect-body-id",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Payload:   marshaled,
		}
		coreHelloData, err := json.Marshal(coreHello)
		if err != nil {
			errc <- fmt.Errorf("failed to marshal core.hello: %v", err)
			return
		}
		ft1.recvCh <- coreHelloData
		// 3. body.capabilities
		data = <-ft1.sendCh
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
		data = <-ft1.sendCh
		var bodyReady link.Envelope
		if err := json.Unmarshal(data, &bodyReady); err != nil {
			errc <- fmt.Errorf("failed to unmarshal body.ready: %v", err)
			return
		}
		if bodyReady.Type != link.TypeBodyReady {
			errc <- fmt.Errorf("expected body.ready, got %s", bodyReady.Type)
			return
		}
		errc <- nil
	}()

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	done1 := make(chan error, 1)
	go func() {
		done1 <- client1.Run(ctx1)
	}()

	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("%v", err)
		}
	case <-time.After(time.Second * 5):
		t.Fatalf("timed out waiting for peer")
	}
	cancel1()
	select {
	case err := <-done1:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("first client run failed: %v", err)
		}
	case <-time.After(time.Second * 3):
		t.Fatal("first client run did not return after cancel")
	}

	if !client1.isReady() {
		t.Fatal("expected first client to be ready")
	}

	// Second connection (reconnect) with same Body ID
	ft2 := NewFakeTransport()
	client2 := NewClient(ft2, "reconnect-body-id") // Same Body ID

	errc = make(chan error, 1)
	go func() {
		// 1. body.hello
		data := <-ft2.sendCh
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
			ID:        "evt_core2",
			BodyID:    "reconnect-body-id",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Payload:   marshaled,
		}
		coreHelloData, err := json.Marshal(coreHello)
		if err != nil {
			errc <- fmt.Errorf("failed to marshal core.hello: %v", err)
			return
		}
		ft2.recvCh <- coreHelloData
		// 3. body.capabilities
		data = <-ft2.sendCh
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
		data = <-ft2.sendCh
		var bodyReady link.Envelope
		if err := json.Unmarshal(data, &bodyReady); err != nil {
			errc <- fmt.Errorf("failed to unmarshal body.ready: %v", err)
			return
		}
		if bodyReady.Type != link.TypeBodyReady {
			errc <- fmt.Errorf("expected body.ready, got %s", bodyReady.Type)
			return
		}
		errc <- nil
	}()

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	done2 := make(chan error, 1)
	go func() {
		done2 <- client2.Run(ctx2)
	}()

	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("%v", err)
		}
	case <-time.After(time.Second * 5):
		t.Fatalf("timed out waiting for peer")
	}
	cancel2()
	select {
	case err := <-done2:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("second client run failed: %v", err)
		}
	case <-time.After(time.Second * 3):
		t.Fatal("second client run did not return after cancel")
	}

	if !client2.isReady() {
		t.Fatal("expected second client to be ready")
	}
}
