package core4

import (
	"testing"
	"time"
)

func TestCapabilitiesAdvertisementValidate(t *testing.T) {
	adv := NewCapabilitiesAdvertisement("body_1", []Capability{
		{ID: "process", Operations: []string{"run"}, Available: true},
	})
	adv.ID = "adv_1" // ID is caller-provided (uuid); required for core4 validation
	if err := adv.Validate(); err != nil {
		t.Fatalf("valid advertisement rejected: %v", err)
	}

	// Wrong type.
	adv.Type = "body.hello"
	if err := adv.Validate(); err == nil {
		t.Fatal("wrong type accepted")
	}
	adv.Type = TypeCapabilitiesAdvertisement

	// Empty body_id.
	adv.BodyID = ""
	if err := adv.Validate(); err == nil {
		t.Fatal("empty body_id accepted")
	}
	adv.BodyID = "body_1"

	// Duplicate capability id.
	adv.Payload.Capabilities = append(adv.Payload.Capabilities,
		Capability{ID: "process", Operations: []string{"list"}})
	if err := adv.Validate(); err == nil {
		t.Fatal("duplicate capability id accepted")
	}
}

func TestExecutionRequestValidate(t *testing.T) {
	req := validRequest()
	if err := req.Validate(); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	req.CorrelationID = "x"
	if err := req.Validate(); err == nil {
		t.Fatal("correlated request from Core accepted")
	}
	req.CorrelationID = ""
	req.Payload.Arguments = nil
	if err := req.Validate(); err == nil {
		t.Fatal("nil arguments accepted")
	}
}

func TestExecutionResultValidateAndCorrelation(t *testing.T) {
	req := validRequest()
	result := validResultFor(t, req)
	if err := result.Validate(); err != nil {
		t.Fatalf("valid result rejected: %v", err)
	}
	if err := result.ValidateResponseTo(req); err != nil {
		t.Fatalf("correlated result rejected: %v", err)
	}

	// Mismatched correlation must be rejected.
	bad := result
	bad.CorrelationID = "other"
	if err := bad.ValidateResponseTo(req); err == nil {
		t.Fatal("mismatched correlation accepted")
	}

	// Non-canonical status rejected at build time.
	bad.Payload.Status = "timeout"
	if err := bad.Validate(); err == nil {
		t.Fatal("non-canonical status accepted")
	}
}

func validRequest() *ExecutionRequest {
	return &ExecutionRequest{
		Type:      TypeExecutionRequest,
		ID:        "exec_1",
		Timestamp: time.Now().UTC(),
		BodyID:    "body_1",
		Payload: ExecutionRequestPayload{
			ExecutionID: "ex_1",
			Capability:  "process",
			Operation:   "run",
			Arguments:   map[string]any{"command": "echo"},
		},
	}
}

func validResultFor(t *testing.T, req *ExecutionRequest) *ExecutionResult {
	t.Helper()
	return &ExecutionResult{
		Type:          TypeExecutionResult,
		ID:            "res_1",
		Timestamp:     time.Now().UTC(),
		BodyID:        req.BodyID,
		CorrelationID: req.ID,
		Payload: ExecutionResultPayload{
			ExecutionID: req.Payload.ExecutionID,
			Status:      StatusSuccess,
			Result:      map[string]any{"exit_code": 0},
		},
	}
}
