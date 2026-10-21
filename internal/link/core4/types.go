// Package core4 defines Shell Body's independent reimplementation of the
// NeonDoll Core 4 Doll Link wire messages for a remote Body connection.
//
// NeonDoll Core defines the protocol; Shell Body adapts. The structs and
// validation rules in this package mirror byte-for-byte the canonical Core 4
// types (DollLink/Capabilities and DollLink/Execution in the neondoll
// repository) so that Shell Body's connection, capability advertisement, and
// execution handling conform to Core without importing the neondoll repo into
// production code. Conformance is proven against the real Core 4 structures in
// test/m10_2_core4_conformance_test.go.
package core4

import "time"

// ---- Capabilities advertisement (DollLink/Capabilities) ----

// TypeCapabilitiesAdvertisement is the canonical discriminator.
const TypeCapabilitiesAdvertisement = "body.capabilities"

// Capability is one advertised Body capability.
type Capability struct {
	ID          string            `json:"id"`
	Operations  []string          `json:"operations"`
	Available   bool              `json:"available"`
	Constraints map[string]string `json:"constraints,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// CapabilitiesPayload wraps the advertised capability list.
type CapabilitiesPayload struct {
	Capabilities []Capability `json:"capabilities"`
}

// CapabilitiesAdvertisement is the canonical body.capabilities envelope.
type CapabilitiesAdvertisement struct {
	Type      string              `json:"type"`
	ID        string              `json:"id"`
	Timestamp time.Time           `json:"timestamp,omitempty"`
	BodyID    string              `json:"body_id"`
	Payload   CapabilitiesPayload `json:"payload"`
}

// ---- Execution (DollLink/Execution) ----

// Execution type discriminators.
const (
	TypeExecutionRequest = "execution.request"
	TypeExecutionResult  = "execution.result"
)

// SendSender is the canonical sender discriminator.
const (
	SenderCore = "core"
	SenderBody = "body"
)

// Status is the canonical execution status.
type Status string

// Canonical execution statuses. These match Core 4 exactly.
const (
	StatusSuccess     Status = "success"
	StatusDenied      Status = "denied"
	StatusUnsupported Status = "unsupported"
	StatusInvalid     Status = "invalid"
	StatusUnavailable Status = "unavailable"
	StatusFailed      Status = "failed"
	StatusCancelled   Status = "cancelled"
	StatusExpired     Status = "expired"
)

func statusValid(s Status) bool {
	switch s {
	case StatusSuccess, StatusDenied, StatusUnsupported, StatusInvalid,
		StatusUnavailable, StatusFailed, StatusCancelled, StatusExpired:
		return true
	}
	return false
}

// ExecutionRequest is the canonical execution.request envelope (Core → Body).
type ExecutionRequest struct {
	Type          string                  `json:"type"`
	ID            string                  `json:"id"`
	Timestamp     time.Time               `json:"timestamp"`
	DollID        string                  `json:"doll_id,omitempty"`
	BodyID        string                  `json:"body_id"`
	CorrelationID string                  `json:"correlation_id,omitempty"`
	Payload       ExecutionRequestPayload `json:"payload"`
}

// ExecutionRequestPayload carries the capability/operation to execute.
type ExecutionRequestPayload struct {
	ExecutionID string         `json:"execution_id"`
	Capability  string         `json:"capability"`
	Operation   string         `json:"operation"`
	Arguments   map[string]any `json:"arguments"`
}

// ExecutionResult is the canonical execution.result envelope (Body → Core).
type ExecutionResult struct {
	Type          string                 `json:"type"`
	ID            string                 `json:"id"`
	Timestamp     time.Time              `json:"timestamp"`
	DollID        string                 `json:"doll_id,omitempty"`
	BodyID        string                 `json:"body_id"`
	CorrelationID string                 `json:"correlation_id"`
	Payload       ExecutionResultPayload `json:"payload"`
}

// ExecutionResultPayload carries the outcome.
type ExecutionResultPayload struct {
	ExecutionID string         `json:"execution_id"`
	Status      Status         `json:"status"`
	Result      map[string]any `json:"result,omitempty"`
}
