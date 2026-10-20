// Tests for terminal session flow: session.opened establishes an active
// session, SendTerminalInput emits a session.event (terminal_input), and a
// session.event (terminal_output) with a matching session ID is delivered back
// via ReceiveTerminalResponse.
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

// TestTerminalInteraction exercises the terminal I/O path end to end over the
// fake transport: handshake, session.open(session.opened), terminal input, and
// terminal response with active session ID validation.
func TestTerminalInteraction(t *testing.T) {
	ft := NewFakeTransport()
	client := NewClient(ft, "test-body-id")

	const wantSessionID = "sess_1"
	const wantInput = "echo hello"
	const wantOutput = "hello\n"

	// sessionOpenedCh signals the main goroutine once the peer has established
	// the active session so it can send terminal input. erc collects peer errors
	// during the rest of the exchange.
	sessionOpenedCh := make(chan struct{})
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
		helloPayload, err := json.Marshal(map[string]interface{}{
			"doll_link_version":     1,
			"body_contract_version": 1,
		})
		if err != nil {
			errc <- fmt.Errorf("failed to marshal core.hello payload: %v", err)
			return
		}
		coreHelloData, err := json.Marshal(link.Envelope{
			Type:      link.TypeCoreHello,
			ID:        "evt_core_hello",
			BodyID:    "test-body-id",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Payload:   helloPayload,
		})
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

		// 5. session.opened establishes the active session.
		openedData, err := json.Marshal(map[string]interface{}{
			"session_id": wantSessionID,
			"capability": "terminal",
		})
		if err != nil {
			errc <- fmt.Errorf("failed to marshal session.opened payload: %v", err)
			return
		}
		sessOpened, err := json.Marshal(link.Envelope{
			Type:      link.TypeSessionOpened,
			ID:        "evt_session_opened",
			BodyID:    "test-body-id",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Payload:   openedData,
		})
		if err != nil {
			errc <- fmt.Errorf("failed to marshal session.opened: %v", err)
			return
		}
		ft.recvCh <- sessOpened

		// Signal that the active session is established; the main goroutine
		// can now call SendTerminalInput.
		close(sessionOpenedCh)

		// 6. Read the terminal_input session.event sent by SendTerminalInput.
		select {
		case data = <-ft.sendCh:
		case <-time.After(5 * time.Second):
			errc <- fmt.Errorf("timed out waiting for terminal input")
			return
		}
		var inputEvent link.Envelope
		if err := json.Unmarshal(data, &inputEvent); err != nil {
			errc <- fmt.Errorf("failed to unmarshal terminal input session.event: %v", err)
			return
		}
		if inputEvent.Type != link.TypeSessionEvent {
			errc <- fmt.Errorf("expected session.event, got %s", inputEvent.Type)
			return
		}
		var inputPayload link.SessionEventPayload
		if err := json.Unmarshal(inputEvent.Payload, &inputPayload); err != nil {
			errc <- fmt.Errorf("failed to unmarshal terminal input payload: %v", err)
			return
		}
		if inputPayload.SessionID != wantSessionID {
			errc <- fmt.Errorf("terminal input carried wrong session ID: expected %s, got %s", wantSessionID, inputPayload.SessionID)
			return
		}
		if inputPayload.Event != "terminal_input" {
			errc <- fmt.Errorf("expected terminal_input event, got %s", inputPayload.Event)
			return
		}
		line, _ := inputPayload.Data["line"].(string)
		if line != wantInput {
			errc <- fmt.Errorf("terminal input line mismatch: expected %q, got %q", wantInput, line)
			return
		}

		// 7. Send back a terminal_output session.event with the matching session ID.
		outputData, err := json.Marshal(map[string]interface{}{
			"session_id": wantSessionID,
			"event":      "terminal_output",
			"data": map[string]interface{}{
				"line":     wantOutput,
				"sequence": 0,
			},
		})
		if err != nil {
			errc <- fmt.Errorf("failed to marshal terminal output payload: %v", err)
			return
		}
		outputEvent, err := json.Marshal(link.Envelope{
			Type:      link.TypeSessionEvent,
			ID:        "evt_terminal_output",
			BodyID:    "test-body-id",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Payload:   outputData,
		})
		if err != nil {
			errc <- fmt.Errorf("failed to marshal terminal output: %v", err)
			return
		}
		ft.recvCh <- outputEvent

		errc <- nil
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clientDone := make(chan error, 1)
	go func() {
		clientDone <- client.Run(ctx)
	}()

	// Wait for the peer to finish the handshake and open the session.
	select {
	case <-sessionOpenedCh:
	case err := <-errc:
		if err != nil {
			t.Fatalf("peer failed before session opened: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out establishing session")
	}

	// Send terminal input. The peer signals sessionOpenedCh as soon as the
	// session.opened message is handed to the transport, but the client's
	// handleMessage may not have set sessionOpened yet; retry briefly to allow
	// the run loop to process it.
	deadline := time.Now().Add(5 * time.Second)
	var sendErr error
	for {
		sendErr = client.SendTerminalInput(ctx, wantInput)
		if sendErr == nil {
			break
		}
		if !strings.Contains(sendErr.Error(), "session not opened") {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if sendErr != nil {
		t.Fatalf("SendTerminalInput returned error: %v", sendErr)
	}

	// Receive the terminal response (delivered via terminal_output).
	resp, err := client.ReceiveTerminalResponse(ctx)
	if err != nil {
		t.Fatalf("ReceiveTerminalResponse returned error: %v", err)
	}
	if resp != wantOutput {
		t.Fatalf("terminal output mismatch: expected %q, got %q", wantOutput, resp)
	}

	// Confirm the peer completed the exchange without error.
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("peer failed during terminal exchange: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("peer did not finish exchange")
	}

	// Cancel and confirm the run loop exits cleanly.
	cancel()
	select {
	case err := <-clientDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("client run failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("client run did not return after cancel")
	}
}

// TestSessionEventRejectsMismatchedSessionID confirms that a session.event whose
// session ID does not match the active session is rejected (never delivered to
// ReceiveTerminalResponse).
func TestSessionEventRejectsMismatchedSessionID(t *testing.T) {
	ft := NewFakeTransport()
	client := NewClient(ft, "test-body-id")
	const wantSessionID = "sess_active"

	errc := make(chan error, 1)
	go func() {
		data := <-ft.sendCh // body.hello
		_ = data
		helloPayload, err := json.Marshal(map[string]interface{}{
			"doll_link_version":     1,
			"body_contract_version": 1,
		})
		if err != nil {
			errc <- err
			return
		}
		coreHelloData, _ := json.Marshal(link.Envelope{
			Type:      link.TypeCoreHello,
			ID:        "evt_ch",
			BodyID:    "test-body-id",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Payload:   helloPayload,
		})
		ft.recvCh <- coreHelloData
		<-ft.sendCh // body.capabilities
		<-ft.sendCh // body.ready

		opened, _ := json.Marshal(map[string]interface{}{
			"session_id": wantSessionID,
			"capability": "terminal",
		})
		openedData, _ := json.Marshal(link.Envelope{
			Type:      link.TypeSessionOpened,
			ID:        "evt_opened",
			BodyID:    "test-body-id",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Payload:   opened,
		})
		ft.recvCh <- openedData

		// A session.event with a DIFFERENT session ID.
		badPayload, _ := json.Marshal(map[string]interface{}{
			"session_id": "sess_wrong",
			"event":      "terminal_output",
			"data":       map[string]interface{}{"line": "nope", "sequence": 0},
		})
		badData, _ := json.Marshal(link.Envelope{
			Type:      link.TypeSessionEvent,
			ID:        "evt_bad",
			BodyID:    "test-body-id",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Payload:   badPayload,
		})
		ft.recvCh <- badData

		// The client Run loop should surface the mismatched-session error.
		errc <- nil
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clientDone := make(chan error, 1)
	go func() {
		clientDone <- client.Run(ctx)
	}()

	select {
	case err := <-clientDone:
		if err == nil {
			t.Fatalf("expected client run to fail on mismatched session ID")
		}
		if !strings.Contains(err.Error(), "mismatched session ID") {
			t.Fatalf("expected mismatched session ID error, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for client run to reject mismatched session")
	}
	cancel()
}
