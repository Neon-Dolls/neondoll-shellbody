// Package body provides tests for the Doll Link client.
package body

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

	// Peer goroutine: acts as the Core, reading client's sends and writing responses.
	errc := make(chan error, 1)
	go func() {
		// 1. Receive body.hello from client (as JSON bytes)
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
		// 2. Send core.hello
		coreHello := link.Envelope{
			Type:      link.TypeCoreHello,
			ID:        "evt_core",
			BodyID:    "test-body-id",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Payload:   json.RawMessage{},
		}
		if marshaled, err := json.Marshal(map[string]interface{}{"doll_link_version": 1, "body_contract_version": 1}); err != nil {
			errc <- fmt.Errorf("failed to marshal core.hello: %v", err)
			return
		} else {
			coreHello.Payload = marshaled
		}
		if marshaled, err := json.Marshal(map[string]interface{}{"doll_link_version": 1, "body_contract_version": 1}); err != nil {
			errc <- fmt.Errorf("failed to marshal core.hello: %v", err)
			return
		} else {
			coreHello.Payload = marshaled
		}
		coreHelloData, err := json.Marshal(coreHello)
		if err != nil {
			errc <- fmt.Errorf("failed to marshal core.hello: %v", err)
			return
		}
		ft.recvCh <- coreHelloData
	}()

	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("%v", err)
		}
	case <-time.After(time.Second * 5):
		t.Fatalf("timed out waiting for peer")
	}

	// Run client negotiation in a separate goroutine with a cancellable context.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := client.negotiateVersions(ctx); err != nil {
		t.Fatalf("version negotiation failed: %v", err)
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
	var hasInput, hasOutput bool

	// Peer goroutine: acts as the Core.
	errc := make(chan error, 1)
	go func() {
		// 1. Receive body.hello from client
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
		// 2. Send core.hello
		coreHello := link.Envelope{
			Type:      link.TypeCoreHello,
			ID:        "evt_core",
			BodyID:    "test-body-id",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Payload:   json.RawMessage{},
		}
		if marshaled, err := json.Marshal(map[string]interface{}{"doll_link_version": 1, "body_contract_version": 1}); err != nil {
			errc <- fmt.Errorf("failed to marshal core.hello: %v", err)
			return
		} else {
			coreHello.Payload = marshaled
		}
		if marshaled, err := json.Marshal(map[string]interface{}{"doll_link_version": 1, "body_contract_version": 1}); err != nil {
			errc <- fmt.Errorf("failed to marshal core.hello: %v", err)
			return
		} else {
			coreHello.Payload = marshaled
		}
		coreHelloData, err := json.Marshal(coreHello)
		if err != nil {
			errc <- fmt.Errorf("failed to marshal core.hello: %v", err)
			return
		}
		ft.recvCh <- coreHelloData

		// 3. Receive body.capabilities from client
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
		// Decode the payload to verify it's valid (optional, but we can)
		payloadBytes, err := json.Marshal(bodyCap.Payload)
		if err != nil {
			errc <- fmt.Errorf("failed to marshal capabilities payload: %v", err)
			return
		}
		var capResp link.CapabilitiesPayload
		if err := json.Unmarshal(payloadBytes, &capResp); err != nil {
			errc <- fmt.Errorf("failed to unmarshal capabilities payload: %v", err)
			return
		}
		if len(capResp.Capabilities) == 0 {
			errc <- fmt.Errorf("expected at least one capability")
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
		if len(capResp.Capabilities[0].Operations) != 2 {
			errc <- fmt.Errorf("expected 2 operations, got %d", len(capResp.Capabilities[0].Operations))
			return
		}
		for _, op := range capResp.Capabilities[0].Operations {
			if op == "input" {
				hasInput = true
			}
			if op == "output" {
				hasOutput = true
			}
		}
		if !hasInput || !hasOutput {
			errc <- fmt.Errorf("expected operations to include \\\"input\\\" and \\\"output\\\", got %v", capResp.Capabilities[0].Operations)
			return
		}
		if !capResp.Capabilities[0].Available {
			errc <- fmt.Errorf("expected capability to be available")
			return
		}
	}()

	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("%v", err)
		}
	case <-time.After(time.Second * 5):
		t.Fatalf("timed out waiting for peer")
	}

	// Run client negotiation and capabilities advertisement in a separate goroutine with a cancellable context.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- client.Run(ctx)
	}()

	// Wait for the client to finish or timeout.
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("client run failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client run timed out")
	}
}

// TestClientExecutionRequest tests handling of execution.request messages.
func TestClientExecutionRequest(t *testing.T) {
	ft := NewFakeTransport()
	client := NewClient(ft, "test-body-id")

	// Set up a handler for execution.request
	handlerCalled := make(chan struct{}, 1)
	expectedReqID := "exec_123"
	expectedCorrelation := "evt_req"
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

	// Peer goroutine
	errc := make(chan error, 1)
	go func() {
		// 1. Receive body.hello from client
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
		// 2. Send core.hello
		coreHello := link.Envelope{
			Type:      link.TypeCoreHello,
			ID:        "evt_core",
			BodyID:    "test-body-id",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Payload:   json.RawMessage{},
		}
		if marshaled, err := json.Marshal(map[string]interface{}{"doll_link_version": 1, "body_contract_version": 1}); err != nil {
			errc <- fmt.Errorf("failed to marshal core.hello: %v", err)
			return
		} else {
			coreHello.Payload = marshaled
		}
		if marshaled, err := json.Marshal(map[string]interface{}{"doll_link_version": 1, "body_contract_version": 1}); err != nil {
			errc <- fmt.Errorf("failed to marshal core.hello: %v", err)
			return
		} else {
			coreHello.Payload = marshaled
		}
		coreHelloData, err := json.Marshal(coreHello)
		if err != nil {
			errc <- fmt.Errorf("failed to marshal core.hello: %v", err)
			return
		}
		ft.recvCh <- coreHelloData
		// 3. Receive body.capabilities from client
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
		// 4. Receive body.ready from client
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
		// 5. Receive execution.request from client
		data = <-ft.sendCh
		var execReq link.Envelope
		if err := json.Unmarshal(data, &execReq); err != nil {
			errc <- fmt.Errorf("failed to unmarshal execution.request: %v", err)
			return
		}
		if execReq.Type != link.TypeExecutionRequest {
			errc <- fmt.Errorf("expected execution.request, got %s", execReq.Type)
			return
		}
		if execReq.ID != expectedCorrelation {
			errc <- fmt.Errorf("expected correlation ID %s, got %s", expectedCorrelation, execReq.ID)
			return
		}
		// Decode the request to verify (optional)
		payloadBytes, err := json.Marshal(execReq.Payload)
		if err != nil {
			errc <- fmt.Errorf("failed to marshal execution.request payload: %v", err)
			return
		}
		var req link.ExecutionRequestPayload
		if err := json.Unmarshal(payloadBytes, &req); err != nil {
			errc <- fmt.Errorf("failed to unmarshal execution.request: %v", err)
			return
		}
		if req.ExecutionID != expectedReqID {
			errc <- fmt.Errorf("expected execution ID %s, got %s", expectedReqID, req.ExecutionID)
			return
		}
		if req.Capability != "terminal" {
			errc <- fmt.Errorf("expected capability terminal, got %s", req.Capability)
			return
		}
		if req.Operation != "input" {
			errc <- fmt.Errorf("expected operation input, got %s", req.Operation)
			return
		}
		// 6. Send execution.result
		execResult := link.Envelope{
			Type:          link.TypeExecutionResult,
			ID:            "evt_result",
			BodyID:        "test-body-id",
			Timestamp:     time.Now().UTC().Format(time.RFC3339),
			CorrelationID: execReq.ID,
			Payload:       []byte(`{"execution_id":"` + expectedReqID + `","status":"success","result":{"output":"hello"}}`),
		}
		execResultData, err := json.Marshal(execResult)
		if err != nil {
			errc <- fmt.Errorf("failed to marshal execution.result: %v", err)
			return
		}
		ft.recvCh <- execResultData
	}()

	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("%v", err)
		}
	case <-time.After(time.Second * 5):
		t.Fatalf("timed out waiting for peer")
	}

	// Run client
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- client.Run(ctx)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("client run failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client run timed out")
	}

	// Verify handler was called
	select {
	case <-handlerCalled:
		// good
	case <-time.After(1 * time.Second):
		t.Fatal("execution request handler was not called")
	}

	// Cancel the context to stop the client.
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("client run failed after cancel: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("client run did not return after cancel")
	}
}

// TestClientSessionOpen tests handling of session.open messages.
func TestClientSessionOpen(t *testing.T) {
	ft := NewFakeTransport()
	client := NewClient(ft, "test-body-id")

	peerDone := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Peer goroutine: acts as the Core.
	errc := make(chan error, 1)
	go func() {
		// 1. Receive body.hello from client
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
		// 2. Send core.hello
		coreHello := link.Envelope{
			Type:      link.TypeCoreHello,
			ID:        "evt_core",
			BodyID:    "test-body-id",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Payload:   json.RawMessage{},
		}
		if marshaled, err := json.Marshal(map[string]interface{}{"doll_link_version": 1, "body_contract_version": 1}); err != nil {
			errc <- fmt.Errorf("failed to marshal core.hello: %v", err)
			return
		} else {
			coreHello.Payload = marshaled
		}
		if marshaled, err := json.Marshal(map[string]interface{}{"doll_link_version": 1, "body_contract_version": 1}); err != nil {
			errc <- fmt.Errorf("failed to marshal core.hello: %v", err)
			return
		} else {
			coreHello.Payload = marshaled
		}
		coreHelloData, err := json.Marshal(coreHello)
		if err != nil {
			errc <- fmt.Errorf("failed to marshal core.hello: %v", err)
			return
		}
		ft.recvCh <- coreHelloData

		// 3. Receive body.capabilities from client
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
		// 4. Receive body.ready from client
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
		// 5. Send session.open
		sessionOpen := link.Envelope{
			Type:      link.TypeSessionOpen,
			ID:        "evt_session_open",
			BodyID:    "test-body-id",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Payload:   json.RawMessage{},
		}
		if marshaled, err := json.Marshal(link.SessionOpenPayload{
			LocalSessionID: "local_sess_123",
			Kind:           "test-kind",
			Metadata:       map[string]string{"foo": "bar"},
		}); err != nil {
			errc <- fmt.Errorf("failed to marshal session.open: %v", err)
			return
		} else {
			sessionOpen.Payload = marshaled
		}
		sessionOpenData, err := json.Marshal(sessionOpen)
		if err != nil {
			errc <- fmt.Errorf("failed to marshal session.open: %v", err)
			return
		}
		ft.recvCh <- sessionOpenData

		// 6. Receive session.opened from client
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

		// Signal that the peer is done
		close(peerDone)
	}()

	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("%v", err)
		}
	case <-time.After(time.Second * 5):
		t.Fatalf("timed out waiting for peer")
	}

	// Run client in a separate goroutine with a cancellable context.
	clientDone := make(chan error, 1)
	go func() {
		clientDone <- client.Run(ctx)
	}()

	// Wait for the peer to finish its sequence
	<-peerDone

	// Now cancel the context to stop the client's message loop
	cancel()

	// Wait for the client to finish (it should return when the next Send or Receive gets the cancelled context)
	select {
	case err := <-clientDone:
		if err != nil {
			// We expect the client to return context.Canceled or similar due to the cancelled context.
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("client run failed: %v", err)
			}
		}
	case <-time.After(1 * time.Second):
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

	// Peer goroutine
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
		coreHello := link.Envelope{
			Type:      link.TypeCoreHello,
			ID:        "evt_core",
			BodyID:    "test-body-id",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Payload:   json.RawMessage{},
		}
		if marshaled, err := json.Marshal(map[string]interface{}{"doll_link_version": 1, "body_contract_version": 1}); err != nil {
			errc <- fmt.Errorf("failed to marshal core.hello: %v", err)
			return
		} else {
			coreHello.Payload = marshaled
		}
		if marshaled, err := json.Marshal(map[string]interface{}{"doll_link_version": 1, "body_contract_version": 1}); err != nil {
			errc <- fmt.Errorf("failed to marshal core.hello: %v", err)
			return
		} else {
			coreHello.Payload = marshaled
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
		// 5. body.event
		data = <-ft.sendCh
		var bodyEvent link.Envelope
		if err := json.Unmarshal(data, &bodyEvent); err != nil {
			errc <- fmt.Errorf("failed to unmarshal body.event: %v", err)
			return
		}
		if bodyEvent.Type != link.TypeBodyEvent {
			errc <- fmt.Errorf("expected body.event, got %s", bodyEvent.Type)
			return
		}
		if bodyEvent.ID != "evt_event" {
			errc <- fmt.Errorf("expected ID evt_event, got %s", bodyEvent.ID)
			return
		}
		// Decode the event
		payloadBytes, err := json.Marshal(bodyEvent.Payload)
		if err != nil {
			errc <- fmt.Errorf("failed to marshal body.event payload: %v", err)
			return
		}
		var ev link.EventPayload
		if err := json.Unmarshal(payloadBytes, &ev); err != nil {
			errc <- fmt.Errorf("failed to unmarshal body.event: %v", err)
			return
		}
		if ev.Event != expectedEvent {
			errc <- fmt.Errorf("expected event %s, got %s", expectedEvent, ev.Event)
			return
		}
		if ev.Capability != expectedCap {
			errc <- fmt.Errorf("expected capability %s, got %s", expectedCap, ev.Capability)
			return
		}
		// No response expected for body.event (it's an observation)
	}()

	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("%v", err)
		}
	case <-time.After(time.Second * 5):
		t.Fatalf("timed out waiting for peer")
	}

	// Run client
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- client.Run(ctx)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("client run failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client run timed out")
	}

	// Verify handler was called
	select {
	case <-handlerCalled:
		// good
	case <-time.After(1 * time.Second):
		t.Fatal("body event handler was not called")
	}

	// Cancel the context to stop the client.
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("client run failed after cancel: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("client run did not return after cancel")
	}
}

// TestClientUnknownMessageType tests that unknown message types return an error.
func TestClientUnknownMessageType(t *testing.T) {
	ft := NewFakeTransport()
	client := NewClient(ft, "test-body-id")

	// Peer goroutine
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
		coreHello := link.Envelope{
			Type:      link.TypeCoreHello,
			ID:        "evt_core",
			BodyID:    "test-body-id",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Payload:   json.RawMessage{},
		}
		if marshaled, err := json.Marshal(map[string]interface{}{"doll_link_version": 1, "body_contract_version": 1}); err != nil {
			errc <- fmt.Errorf("failed to marshal core.hello: %v", err)
			return
		} else {
			coreHello.Payload = marshaled
		}
		if marshaled, err := json.Marshal(map[string]interface{}{"doll_link_version": 1, "body_contract_version": 1}); err != nil {
			errc <- fmt.Errorf("failed to marshal core.hello: %v", err)
			return
		} else {
			coreHello.Payload = marshaled
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
		// 5. unknown.message
		data = <-ft.sendCh
		var unknown link.Envelope
		if err := json.Unmarshal(data, &unknown); err != nil {
			errc <- fmt.Errorf("failed to unmarshal unknown.message: %v", err)
			return
		}
		if unknown.Type != "unknown.message" {
			errc <- fmt.Errorf("expected unknown.message, got %s", unknown.Type)
			return
		}
		// We don't send a response; the client should error.
	}()

	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("%v", err)
		}
	case <-time.After(time.Second * 5):
		t.Fatalf("timed out waiting for peer")
	}

	// Run client - should fail on unknown message
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- client.Run(ctx)
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected client run to fail on unknown message type")
		}
		if !errors.Is(err, link.ErrUnknownMessageType) {
			t.Fatalf("expected unknown message type error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client run timed out")
	}

	// Cancel the context to stop the client (if it hasn't already returned).
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("client run failed after cancel: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("client run did not return after cancel")
	}
}

// TestClientReconnectionSameBodyID tests that reconnecting with the same Body ID works.
func TestClientReconnectionSameBodyID(t *testing.T) {
	// First connection
	ft1 := NewFakeTransport()
	client1 := NewClient(ft1, "reconnect-body-id")

	// Peer for first connection
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
		coreHello := link.Envelope{
			Type:      link.TypeCoreHello,
			ID:        "evt_core1",
			BodyID:    "reconnect-body-id",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Payload:   json.RawMessage{},
		}
		if marshaled, err := json.Marshal(map[string]interface{}{"doll_link_version": 1, "body_contract_version": 1}); err != nil {
			errc <- fmt.Errorf("failed to marshal core.hello: %v", err)
			return
		} else {
			coreHello.Payload = marshaled
		}
		if marshaled, err := json.Marshal(map[string]interface{}{"doll_link_version": 1, "body_contract_version": 1}); err != nil {
			errc <- fmt.Errorf("failed to marshal core.hello: %v", err)
			return
		} else {
			coreHello.Payload = marshaled
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
	}()

	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("%v", err)
		}
	case <-time.After(time.Second * 5):
		t.Fatalf("timed out waiting for peer")
	}

	// Run first client
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	done1 := make(chan error, 1)
	go func() {
		done1 <- client1.Run(ctx1)
	}()

	select {
	case err := <-done1:
		if err != nil {
			t.Fatalf("first client run failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first client run timed out")
	}

	if !client1.isReady() {
		t.Fatal("expected first client to be ready")
	}

	// Second connection (reconnect) with same Body ID
	ft2 := NewFakeTransport()
	client2 := NewClient(ft2, "reconnect-body-id") // Same Body ID

	// Peer for second connection
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
		coreHello := link.Envelope{
			Type:      link.TypeCoreHello,
			ID:        "evt_core2",
			BodyID:    "reconnect-body-id",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Payload:   json.RawMessage{},
		}
		if marshaled, err := json.Marshal(map[string]interface{}{"doll_link_version": 1, "body_contract_version": 1}); err != nil {
			errc <- fmt.Errorf("failed to marshal core.hello: %v", err)
			return
		} else {
			coreHello.Payload = marshaled
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
	}()

	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("%v", err)
		}
	case <-time.After(time.Second * 5):
		t.Fatalf("timed out waiting for peer")
	}

	// Run second client
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	done2 := make(chan error, 1)
	go func() {
		done2 <- client2.Run(ctx2)
	}()

	select {
	case err := <-done2:
		if err != nil {
			t.Fatalf("second client run failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second client run timed out")
	}

	if !client2.isReady() {
		t.Fatal("expected second client to be ready")
	}

	// Both clients should be ready and have the same Body ID
	if client1.BodyID != client2.BodyID {
		t.Fatalf("expected same Body ID, got %s and %s", client1.BodyID, client2.BodyID)
	}

	// Cancel the contexts to stop the clients.
	cancel1()
	cancel2()
	select {
	case err := <-done1:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("first client run failed after cancel: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("first client run did not return after cancel")
	}
	select {
	case err := <-done2:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("second client run failed after cancel: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("second client run did not return after cancel")
	}
}
