// Package core5 defines Shell Body's independent reimplementation of the
// NeonDoll Core 5 Doll Link wire messages for Interaction Sessions and chat
// events.
//
// NeonDoll Core defines the protocol; Shell Body adapts. The structs and
// validation rules in this package mirror byte-for-byte the canonical Core 5
// types (DollLink/SessionControl and DollLink/Events in the neondoll
// repository at Core 5) so that Shell Body can open an Interaction Session,
// send terminal input, receive the Doll's response, and close the session
// without importing the neondoll repository into production code. Conformance
// is proven against the real Core 5 structures by JSON round-trip tests here,
// an end-to-end real-binary harness (test/deploy/run_m11_1_harness.sh), and —
// where supported — test-only imports of the real Core 5 types in the
// integration suite.
package core5

import "time"

// ---- SessionControl (DollLink/SessionControl) ----

// SessionControlVersion is the canonical protocol version.
const SessionControlVersion = 1

type MessageType string
type Status string
type ErrorCode string

const (
	TypeCreate MessageType = "session.create"
	TypeList   MessageType = "session.list"
	TypeSelect MessageType = "session.select"
	TypeClose  MessageType = "session.close"
	TypeResult MessageType = "session.result"
	// TypeInteraction routes one otherwise-unmodified legacy event through an
	// explicit per-interaction session selector.
	TypeInteraction MessageType = "interaction"

	StatusOK    Status = "ok"
	StatusError Status = "error"

	ErrorInvalidRequest     ErrorCode = "invalid_request"
	ErrorUnsupportedVersion ErrorCode = "unsupported_version"
	ErrorRequestIDConflict  ErrorCode = "request_id_conflict"
	ErrorUnauthenticated    ErrorCode = "unauthenticated"
	ErrorHandlerUnavailable ErrorCode = "handler_unavailable"
	ErrorSessionUnavailable ErrorCode = "session_unavailable"
	ErrorInternal           ErrorCode = "internal_error"
)

// SessionRequest is the common Body-to-Core request envelope. Payload fields
// not used by an operation must be omitted. ID is supplied by the Body and is
// echoed by the Core as the result correlation_id.
type SessionRequest struct {
	Version int                   `json:"version"`
	Type    MessageType           `json:"type"`
	ID      string                `json:"id"`
	Payload SessionRequestPayload `json:"payload"`
}

type SessionRequestPayload struct {
	ExternalConversationRef string `json:"external_conversation_ref,omitempty"`
	SessionID               string `json:"session_id,omitempty"`
}

// SessionResponse is the single Core-to-Body response envelope for session
// control operations. Foreign, unknown, and closed sessions intentionally use
// the same error code so authorization failures disclose no ownership data.
type SessionResponse struct {
	Version       int                    `json:"version"`
	Type          MessageType            `json:"type"`
	ID            string                 `json:"id"`
	CorrelationID string                 `json:"correlation_id"`
	Payload       SessionResponsePayload `json:"payload"`
}

type SessionResponsePayload struct {
	Operation MessageType   `json:"operation"`
	Status    Status        `json:"status"`
	Session   *Session      `json:"session,omitempty"`
	Sessions  []Session     `json:"sessions,omitempty"`
	Error     *SessionError `json:"error,omitempty"`
}

type SessionError struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

// Session is the Body-visible subset of a Core Interaction Session. Owner is
// deliberately omitted because every response is scoped to the authenticated
// connection identity.
type Session struct {
	ID                      string    `json:"session_id"`
	ExternalConversationRef string    `json:"external_conversation_ref,omitempty"`
	Status                  string    `json:"status"`
	CreatedAt               time.Time `json:"created_at"`
	UpdatedAt               time.Time `json:"updated_at"`
}

// Interaction routes one event through an explicit Core Interaction Session
// without modifying the connection-wide selection.
type Interaction struct {
	Version   int         `json:"version"`
	Type      MessageType `json:"type"`
	SessionID string      `json:"session_id"`
	Event     Event       `json:"event"`
}

// ---- Events (DollLink/Events) ----

type EventKind string

const (
	KindMessage  EventKind = "message"
	KindCommand  EventKind = "command"
	KindSystem   EventKind = "system"
	KindPresence EventKind = "presence"
)

// Event is the generic event envelope.
type Event struct {
	ID            string    `json:"id"`
	Type          EventKind `json:"type"`
	Timestamp     time.Time `json:"timestamp"`
	Source        string    `json:"source"`
	DollID        string    `json:"doll_id,omitempty"`
	CorrelationID string    `json:"correlation_id,omitempty"`
	Payload       any       `json:"payload"`
}

// MessagePayload is the standard chat-message payload.
type MessagePayload struct {
	Text      string `json:"text"`
	ChannelID string `json:"channel_id,omitempty"`
	UserID    string `json:"user_id,omitempty"`
	UserName  string `json:"user_name,omitempty"`
}

// SystemPayload is the lifecycle/system payload carrying an optional data
// value (Core reports errors here with Event:"error" and Data:message).
type SystemPayload struct {
	Event string `json:"event"`
	Data  any    `json:"data,omitempty"`
}

// NewDollMessage creates the message Event a client sends to a Doll. It is
// the canonical terminal-input wire event.
func NewDollMessage(id, dollID, text string) Event {
	return Event{
		ID:        id,
		Type:      KindMessage,
		Source:    "client",
		DollID:    dollID,
		Timestamp: time.Now().UTC(),
		Payload: MessagePayload{
			Text: text,
		},
	}
}

// sessionControlTypeValid reports whether t is a Body-addressable session
// control request.
func sessionControlTypeValid(t string) bool {
	switch t {
	case "session.create", "session.list", "session.select", "session.close":
		return true
	}
	return false
}

// eventKindValid reports whether k is a known event kind.
func eventKindValid(k string) bool {
	switch k {
	case "message", "command", "system", "presence":
		return true
	}
	return false
}

// parseKind converts a string to a EventKind.
func parseKind(s string) EventKind {
	switch s {
	case "message":
		return KindMessage
	case "command":
		return KindCommand
	case "system":
		return KindSystem
	case "presence":
		return KindPresence
	default:
		return "unknown"
	}
}
