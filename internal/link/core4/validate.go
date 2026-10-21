package core4

import (
	"fmt"
	"time"
)

// NewCapabilitiesAdvertisement builds a canonical body.capabilities
// advertisement for a Body.
func NewCapabilitiesAdvertisement(bodyID string, advertised []Capability) *CapabilitiesAdvertisement {
	return &CapabilitiesAdvertisement{
		Type:      TypeCapabilitiesAdvertisement,
		Timestamp: time.Now().UTC(),
		BodyID:    bodyID,
		Payload:   CapabilitiesPayload{Capabilities: advertised},
	}
}

// ID is set by the caller (uuid); kept out of the constructor so the caller
// controls id generation.

// Validate checks the advertisement against Core's canonical rules:
// non-empty ID and BodyID, correct type, and each capability requires a unique
// non-empty ID and at least one operation.
func (a *CapabilitiesAdvertisement) Validate() error {
	if a == nil {
		return fmt.Errorf("core4: nil advertisement")
	}
	if a.ID == "" {
		return fmt.Errorf("core4: advertisement requires id")
	}
	if a.BodyID == "" {
		return fmt.Errorf("core4: advertisement requires body_id")
	}
	if a.Type != TypeCapabilitiesAdvertisement {
		return fmt.Errorf("core4: advertisement type must be %q", TypeCapabilitiesAdvertisement)
	}
	seenCaps := map[string]bool{}
	for _, c := range a.Payload.Capabilities {
		if c.ID == "" {
			return fmt.Errorf("core4: capability requires id")
		}
		if len(c.Operations) == 0 {
			return fmt.Errorf("core4: capability %q requires at least one operation", c.ID)
		}
		if seenCaps[c.ID] {
			return fmt.Errorf("core4: duplicate capability id %q", c.ID)
		}
		seenCaps[c.ID] = true
		seenOps := map[string]bool{}
		for _, op := range c.Operations {
			if op == "" {
				return fmt.Errorf("core4: capability %q has empty operation", c.ID)
			}
			if seenOps[op] {
				return fmt.Errorf("core4: capability %q repeats operation %q", c.ID, op)
			}
			seenOps[op] = true
		}
	}
	return nil
}

func isUTC(t time.Time) bool {
	_, offset := t.Zone()
	return offset == 0
}

// ValidateRequest checks an inbound execution.request envelope (a Body
// receiving from Core must ensure the request is well-formed and correlated).
func (r *ExecutionRequest) Validate() error {
	if r == nil {
		return fmt.Errorf("core4: nil request")
	}
	if r.Type != TypeExecutionRequest {
		return fmt.Errorf("core4: request type must be %q", TypeExecutionRequest)
	}
	if r.ID == "" {
		return fmt.Errorf("core4: request requires id")
	}
	if r.Timestamp.IsZero() {
		return fmt.Errorf("core4: request requires timestamp")
	}
	if !isUTC(r.Timestamp) {
		return fmt.Errorf("core4: request timestamp must be UTC")
	}
	if r.BodyID == "" {
		return fmt.Errorf("core4: request requires body_id")
	}
	if r.CorrelationID != "" {
		return fmt.Errorf("core4: a request from Core must not carry correlation_id")
	}
	if r.Payload.ExecutionID == "" {
		return fmt.Errorf("core4: request payload requires execution_id")
	}
	if r.Payload.Capability == "" {
		return fmt.Errorf("core4: request payload requires capability")
	}
	if r.Payload.Operation == "" {
		return fmt.Errorf("core4: request payload requires operation")
	}
	if r.Payload.Arguments == nil {
		return fmt.Errorf("core4: request payload requires non-nil arguments")
	}
	return nil
}

// Validate checks a Body-built execution.result envelope against Core's
// canonical SenderBody rules: correlated, canonical status, UTC timestamp.
func (r *ExecutionResult) Validate() error {
	if r == nil {
		return fmt.Errorf("core4: nil result")
	}
	if r.Type != TypeExecutionResult {
		return fmt.Errorf("core4: result type must be %q", TypeExecutionResult)
	}
	if r.ID == "" {
		return fmt.Errorf("core4: result requires id")
	}
	if r.Timestamp.IsZero() {
		return fmt.Errorf("core4: result requires timestamp")
	}
	if !isUTC(r.Timestamp) {
		return fmt.Errorf("core4: result timestamp must be UTC")
	}
	if r.BodyID == "" {
		return fmt.Errorf("core4: result requires body_id")
	}
	if r.CorrelationID == "" {
		return fmt.Errorf("core4: result from Body requires correlation_id")
	}
	if !statusValid(r.Payload.Status) {
		return fmt.Errorf("core4: invalid execution status %q", r.Payload.Status)
	}
	if r.Payload.ExecutionID == "" {
		return fmt.Errorf("core4: result payload requires execution_id")
	}
	return nil
}

// ValidateResponseTo checks a Body-built result correlates to the originating
// request: correlation_id == request.id, and matching execution_id, body_id,
// and (when set) doll_id.
func (r *ExecutionResult) ValidateResponseTo(request *ExecutionRequest) error {
	if request == nil {
		return fmt.Errorf("core4: nil request")
	}
	if r.CorrelationID != request.ID {
		return fmt.Errorf("core4: result correlation_id %q does not match request id %q", r.CorrelationID, request.ID)
	}
	if r.Payload.ExecutionID != request.Payload.ExecutionID {
		return fmt.Errorf("core4: result execution_id %q does not match request %q", r.Payload.ExecutionID, request.Payload.ExecutionID)
	}
	if r.BodyID != request.BodyID {
		return fmt.Errorf("core4: result body_id %q does not match request %q", r.BodyID, request.BodyID)
	}
	if request.DollID != "" && r.DollID != request.DollID {
		return fmt.Errorf("core4: result doll_id %q does not match request %q", r.DollID, request.DollID)
	}
	return nil
}
