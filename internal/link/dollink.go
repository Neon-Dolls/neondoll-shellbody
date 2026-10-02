// Package link provides the Doll Link transport framing and messaging.
// It implements the wire protocol defined in doll-link.md without
// depending on Core internals.
package link

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ErrUnknownMessageType is returned when a Doll Link message has an unknown type.
var ErrUnknownMessageType = errors.New("unknown Doll Link message type")

// MessageType represents the Doll Link message type field.
type MessageType string

// Known Doll Link message types from the Body Protocol.
const (
	TypeBodyHello         MessageType = "body.hello"
	TypeCoreHello         MessageType = "core.hello"
	TypeBodyCapabilities  MessageType = "body.capabilities"
	TypeBodyReady         MessageType = "body.ready"
	TypeBodyEvent         MessageType = "body.event"
	TypeBodyReconcile     MessageType = "body.reconcile"
	TypeCoreReconcile     MessageType = "core.reconcile"
	TypeExecutionRequest  MessageType = "execution.request"
	TypeExecutionResult   MessageType = "execution.result"
	TypeExecutionProgress MessageType = "execution.progress"
	TypeExecutionCancel   MessageType = "execution.cancel"
	TypeSessionOpen       MessageType = "session.open"
	TypeSessionOpened     MessageType = "session.opened"
	TypeSessionEvent      MessageType = "session.event"
	TypeSessionClose      MessageType = "session.close"
	TypeDelegationStart   MessageType = "delegation.start"
	TypeDelegationCancel  MessageType = "delegation.cancel"
	TypeDelegationStatus  MessageType = "delegation.status"
	TypeDelegationResult  MessageType = "delegation.result"
	TypeBodyHealth        MessageType = "body.health"
)

// Envelope is the common Doll Link message envelope.
// Payload is raw JSON bytes at the wire boundary.
type Envelope struct {
	Type          MessageType     `json:"type"`
	ID            string          `json:"id,omitempty"`
	Timestamp     string          `json:"timestamp,omitempty"`
	DollID        string          `json:"doll_id,omitempty"`
	BodyID        string          `json:"body_id,omitempty"`
	CorrelationID string          `json:"correlation_id,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"`
}

// UnmarshalJSON validates the message type and then uses the default struct unmarshaling.
func (e *Envelope) UnmarshalJSON(data []byte) error {
	// First, unmarshal into a map to get the type.
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	// Check the type.
	if v, ok := m["type"]; !ok {
		return errors.New("missing type field")
	} else {
		typeStr, ok := v.(string)
		if !ok {
			return errors.New("type field is not a string")
		}
		msgType := MessageType(typeStr)
		switch msgType {
		case TypeBodyHello, TypeCoreHello, TypeBodyCapabilities, TypeBodyReady,
			TypeBodyEvent, TypeBodyReconcile, TypeCoreReconcile, TypeExecutionRequest,
			TypeExecutionResult, TypeExecutionProgress, TypeExecutionCancel,
			TypeSessionOpen, TypeSessionOpened, TypeSessionEvent, TypeSessionClose,
			TypeDelegationStart, TypeDelegationCancel, TypeDelegationStatus,
			TypeDelegationResult, TypeBodyHealth:
			// Valid type: fall through to unmarshal into the struct.
		default:
			return fmt.Errorf("%w: %s", ErrUnknownMessageType, msgType)
		}
	}
	// Now unmarshal into the Envelope struct using a type alias to avoid recursion.
	type envelope Envelope
	var tmp envelope
	if err := json.Unmarshal(data, &tmp); err != nil {
		return err
	}
	*e = Envelope(tmp)
	return nil
}

// HelloPayload contains the version negotiation information.
type HelloPayload struct {
	BodyType       string      `json:"body_type,omitempty"`
	Implementation string      `json:"implementation,omitempty"`
	Platform       string      `json:"platform,omitempty"`
	Architecture   string      `json:"architecture,omitempty"`
	DollLink       VersionSpec `json:"doll_link,omitempty"`
	BodyContract   VersionSpec `json:"body_contract,omitempty"`
	Build          int         `json:"build,omitempty"`
}

// CoreHelloPayload contains the version negotiation information from Core to Body.
type CoreHelloPayload struct {
	DollLink     VersionSpec `json:"doll_link,omitempty"`
	CoreContract VersionSpec `json:"core_contract,omitempty"`
	Build        int         `json:"build,omitempty"`
}

// VersionSpec describes version ranges for protocol negotiation.
type VersionSpec struct {
	MinVersion int `json:"min_version,omitempty"`
	MaxVersion int `json:"max_version,omitempty"`
}

// CapabilitiesPayload advertises what the Body can do.
type CapabilitiesPayload struct {
	Capabilities []Capability `json:"capabilities,omitempty"`
}

// Capability describes a single capability the Body can execute.
type Capability struct {
	ID         string            `json:"id,omitempty"`
	Operations []string          `json:"operations,omitempty"`
	Available  bool              `json:"available,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// ReadyPayload indicates the Body is ready for normal operation.
type ReadyPayload struct {
	// No additional fields needed for v1
}

// EventPayload represents an observation from the Body.
type EventPayload struct {
	EventID    string                 `json:"event_id,omitempty"`
	Capability string                 `json:"capability,omitempty"`
	Event      string                 `json:"event,omitempty"`
	OccurredAt string                 `json:"occurred_at,omitempty"`
	Data       map[string]interface{} `json:"data,omitempty"`
}

// ReconcilePayload reports retained semantic state after reconnection.
type ReconcilePayload struct {
	Sessions    []string `json:"sessions,omitempty"`
	Executions  []string `json:"executions,omitempty"`
	Delegations []string `json:"delegations,omitempty"`
}

// HealthPayload reports the Body's connected state.
type HealthPayload struct {
	State   string                 `json:"state,omitempty"`
	Details map[string]interface{} `json:"details,omitempty"`
}

// ExecutionRequestPayload represents a request to execute a command.
type ExecutionRequestPayload struct {
	ExecutionID string                 `json:"execution_id,omitempty"`
	Capability  string                 `json:"capability,omitempty"`
	Operation   string                 `json:"operation,omitempty"`
	Arguments   map[string]interface{} `json:"arguments,omitempty"`
	Env         map[string]string      `json:"env,omitempty"`
	Dir         string                 `json:"dir,omitempty"`
	Timeout     int64                  `json:"timeout,omitempty"` // milliseconds, 0 means no timeout
	Stdin       string                 `json:"stdin,omitempty"`
}

// ExecutionResultPayload represents the result of an execution.
type ExecutionResultPayload struct {
	ExecutionID string                 `json:"execution_id,omitempty"`
	Status      string                 `json:"status,omitempty"` // "success", "failed", "timeout", "cancelled", "unsupported"
	ExitCode    int                    `json:"exit_code,omitempty"`
	Stdout      string                 `json:"stdout,omitempty"`
	Stderr      string                 `json:"stderr,omitempty"`
	Error       string                 `json:"error,omitempty"`
	Result      map[string]interface{} `json:"result,omitempty"`
}

// ExecutionProgressPayload represents progress of an execution.
type ExecutionProgressPayload struct {
	ExecutionID string  `json:"execution_id,omitempty"`
	Progress    float64 `json:"progress,omitempty"` // 0.0 to 1.0
}

// ExecutionCancelPayload represents a request to cancel an execution.
type ExecutionCancelPayload struct {
	ExecutionID string `json:"execution_id,omitempty"`
}

// SessionOpenPayload represents a request to open a session.
type SessionOpenPayload struct {
	LocalSessionID string            `json:"local_session_id,omitempty"`
	Kind           string            `json:"kind,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

// SessionOpenedPayload represents a session that has been opened.
type SessionOpenedPayload struct {
	SessionID  string            `json:"session_id,omitempty"`
	Capability string            `json:"capability,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// SessionEventPayload represents an event from a session.
type SessionEventPayload struct {
	SessionID  string                 `json:"session_id,omitempty"`
	Event      string                 `json:"event,omitempty"`
	OccurredAt string                 `json:"occurred_at,omitempty"`
	Data       map[string]interface{} `json:"data,omitempty"`
}

// SessionClosePayload represents a request to close a session.
type SessionClosePayload struct {
	SessionID string `json:"session_id,omitempty"`
}

// DelegationStartPayload represents the start of a delegation.
type DelegationStartPayload struct {
	DelegationID string                 `json:"delegation_id,omitempty"`
	Capability   string                 `json:"capability,omitempty"`
	Input        map[string]interface{} `json:"input,omitempty"`
}

// DelegationCancelPayload represents a request to cancel a delegation.
type DelegationCancelPayload struct {
	DelegationID string `json:"delegation_id,omitempty"`
}

// DelegationStatusPayload represents the status of a delegation.
type DelegationStatusPayload struct {
	DelegationID string `json:"delegation_id,omitempty"`
	Status       string `json:"status,omitempty"` // "pending", "in_progress", "completed", "failed", "cancelled"
}

// DelegationResultPayload represents the result of a delegation.
type DelegationResultPayload struct {
	DelegationID string                 `json:"delegation_id,omitempty"`
	Status       string                 `json:"status,omitempty"` // "success", "denied", etc.
	Result       map[string]interface{} `json:"result,omitempty"`
}

// generateID creates a simple monotonically increasing ID for demo/testing.
// In production, we should use a proper unique identifier (e.g., UUID).
var idCounter int64

func generateID() string {
	idCounter++
	return fmt.Sprintf("evt_%d", idCounter)
}
