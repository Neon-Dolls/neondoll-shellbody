package core5

// JSON round-trip and shape tests for the Core 5 Doll Link wire mirror
// (SessionControl + Events). These prove the structures are self-consistent and
// emit the canonical JSON field names; byte-for-byte conformance against the
// real Core 5 types is proven by the integration suite and the real-binary
// harness (test/deploy/run_m11_1_harness.sh).

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func contains(t *testing.T, s, needle string) {
	if !strings.Contains(s, needle) {
		t.Fatalf("missing %q in %q", needle, s)
	}
}

func TestSessionControlTypeValid(t *testing.T) {
	for _, ok := range []string{"session.create", "session.list", "session.select", "session.close"} {
		if !sessionControlTypeValid(ok) {
			t.Fatalf("expected %q to be a valid session control request", ok)
		}
	}
	if sessionControlTypeValid("bogus") {
		t.Fatalf("bogus reported valid")
	}
	if sessionControlTypeValid("session.result") {
		t.Fatalf("session.result is a response, not a request")
	}
	if sessionControlTypeValid("interaction") {
		t.Fatalf("interaction is not a session control request")
	}
}

func TestEventKindValid(t *testing.T) {
	for _, ok := range []string{"message", "command", "system", "presence"} {
		if !eventKindValid(ok) {
			t.Fatalf("expected %q to be a valid event kind", ok)
		}
	}
	if eventKindValid("bogus") {
		t.Fatalf("bogus reported valid")
	}
}

func TestSessionCreateRequestShape(t *testing.T) {
	req := SessionRequest{
		Version: SessionControlVersion,
		Type:    TypeCreate,
		ID:      "req-1",
		Payload: SessionRequestPayload{},
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	s := string(b)
	contains(t, s, `"version":1`)
	contains(t, s, `"type":"session.create"`)
	contains(t, s, `"id":"req-1"`)
	contains(t, s, `"payload":{}`)
}

func TestSessionCloseRequestCarriesSessionID(t *testing.T) {
	req := SessionRequest{
		Version: SessionControlVersion,
		Type:    TypeClose,
		ID:      "req-2",
		Payload: SessionRequestPayload{SessionID: "sess-9"},
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	s := string(b)
	contains(t, s, `"type":"session.close"`)
	contains(t, s, `"session_id":"sess-9"`)
}

func TestSessionCreateResponseParse(t *testing.T) {
	raw := `{"version":1,"type":"session.result","id":"core-9","correlation_id":"req-1",` +
		`"payload":{"operation":"session.create","status":"ok",` +
		`"session":{"session_id":"sess-1","status":"open",` +
		`"created_at":"2026-10-25T00:00:00Z","updated_at":"2026-10-25T00:00:00Z"}}}`
	var resp SessionResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if resp.Type != TypeResult {
		t.Fatalf("Type = %q, want %q", resp.Type, TypeResult)
	}
	if resp.CorrelationID != "req-1" {
		t.Fatalf("CorrelationID = %q, want req-1", resp.CorrelationID)
	}
	if resp.Payload.Status != StatusOK {
		t.Fatalf("Status = %q, want ok", resp.Payload.Status)
	}
	if resp.Payload.Session == nil {
		t.Fatal("no session in response")
	}
	if resp.Payload.Session.ID != "sess-1" {
		t.Fatalf("Session.ID = %q, want sess-1", resp.Payload.Session.ID)
	}
}

func TestSessionErrorResponseParse(t *testing.T) {
	raw := `{"version":1,"type":"session.result","id":"core-9","correlation_id":"req-1",` +
		`"payload":{"operation":"session.create","status":"error",` +
		`"error":{"code":"session_unavailable","message":"no"}}}`
	var resp SessionResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if resp.Payload.Status != StatusError {
		t.Fatalf("Status = %q, want error", resp.Payload.Status)
	}
	if resp.Payload.Error == nil {
		t.Fatal("no error in response")
	}
	if resp.Payload.Error.Code != ErrorSessionUnavailable {
		t.Fatalf("Error.Code = %q, want %q", resp.Payload.Error.Code, ErrorSessionUnavailable)
	}
}

func TestInteractionShape(t *testing.T) {
	ev := NewDollMessage("evt-1", "", "hello")
	it := Interaction{
		Version:   SessionControlVersion,
		Type:      TypeInteraction,
		SessionID: "sess-1",
		Event:     ev,
	}
	b, err := json.Marshal(it)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	s := string(b)
	contains(t, s, `"version":1`)
	contains(t, s, `"type":"interaction"`)
	contains(t, s, `"session_id":"sess-1"`)
	contains(t, s, `"event":{"id":"evt-1","type":"message"`)
	contains(t, s, `"payload":{"text":"hello"}`)
}

func TestExecutionStatusValid(t *testing.T) {
	for _, ok := range []ExecutionStatus{
		ExecutionStatusSuccess,
		ExecutionStatusDenied,
		ExecutionStatusUnsupported,
		ExecutionStatusInvalid,
		ExecutionStatusUnavailable,
		ExecutionStatusFailed,
		ExecutionStatusCancelled,
		ExecutionStatusExpired,
	} {
		if !executionStatusValid(ok) {
			t.Fatalf("expected %q to be valid", ok)
		}
	}
	if executionStatusValid("bogus") {
		t.Fatalf("bogus reported valid")
	}
}

func TestExecutionRequestShape(t *testing.T) {
	req := ExecutionRequest{
		Type:      TypeExecutionRequest,
		ID:        "req-exec-1",
		Timestamp: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		DollID:    "doll-1",
		BodyID:    "body-1",
		Payload: ExecutionRequestPayload{
			ExecutionID: "exec-1",
			Capability:  "process",
			Operation:   "run",
			Arguments: map[string]any{
				"command":  "echo",
				"args":     []string{"hello"},
				"timeout_s": 30.0,
			},
		},
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	s := string(b)
	contains(t, s, `"type":"execution.request"`)
	contains(t, s, `"id":"req-exec-1"`)
	contains(t, s, `"doll_id":"doll-1"`)
	contains(t, s, `"body_id":"body-1"`)
	contains(t, s, `"execution_id":"exec-1"`)
	contains(t, s, `"capability":"process"`)
	contains(t, s, `"operation":"run"`)
}

func TestExecutionResultShape(t *testing.T) {
	res := ExecutionResult{
		Type:          TypeExecutionResult,
		ID:            "res-exec-1",
		Timestamp:     time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		DollID:        "doll-1",
		BodyID:        "body-1",
		CorrelationID: "req-exec-1",
		Payload: ExecutionResultPayload{
			ExecutionID: "exec-1",
			Status:      ExecutionStatusSuccess,
			Result: map[string]any{
				"exit_code": float64(0),
				"stdout":    "hello\n",
				"stderr":    "",
			},
		},
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	s := string(b)
	contains(t, s, `"type":"execution.result"`)
	contains(t, s, `"id":"res-exec-1"`)
	contains(t, s, `"correlation_id":"req-exec-1"`)
	contains(t, s, `"execution_id":"exec-1"`)
	contains(t, s, `"status":"success"`)
	contains(t, s, `"exit_code":0`)
}
