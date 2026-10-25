// Package interaction gives the Shell Body an interactive conversation mode
// (Milestone M11.1 — Real Terminal Conversation).
//
// A human types lines into the terminal; each line travels as a real Core 5
// Interaction Session message over the authenticated Doll Link WebSocket
// (session.create → interaction → session.close) and the Core's Doll response
// is displayed. The wire structs are mirrored from the real Core 5 types in
// internal/link/core5; no neondoll production import is made.
//
// The conversation drives any transport that satisfies the narrow Session
// surface (write a JSON frame, read the next frame interruptibly, close). The
// CLI adapts an authenticated body.Core4Session; tests use a scripted fake.
package interaction

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/link/core5"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/terminal"
	"github.com/google/uuid"
)

// SendErrorKind distinguishes why a SendLine did not get a normal reply.
type SendErrorKind string

const (
	ErrNone      SendErrorKind = ""
	ErrCore      SendErrorKind = "core"      // Core rejected the interaction; transport is alive
	ErrTransport SendErrorKind = "transport" // the connection failed; the conversation must end
)

// Session is the minimal transport surface the conversation needs. It is
// satisfied by an authenticated body.Core4Session and by test fakes.
type Session struct {
	Write func(ctx context.Context, value any) error
	Read  func(ctx context.Context) ([]byte, error)
	Close func() error
}

// OpenResult names the fields of a completed session open.
type OpenResult struct {
	SessionID string
}

// OpenSession creates a fresh Core 5 Interaction Session and returns its
// session_id. Failures (transport or an explicit Core rejection) are surfaced
// as an error, so a revoked/unavailable session control fails closed.
func OpenSession(ctx context.Context, that Session, requestID string) (string, error) {
	req := core5.SessionRequest{
		Version: core5.SessionControlVersion,
		Type:    core5.TypeCreate,
		ID:      requestID,
		Payload: core5.SessionRequestPayload{},
	}
	if err := that.Write(ctx, req); err != nil {
		return "", fmt.Errorf("body: interaction: session.create: %w", err)
	}
	for {
		msg, err := that.Read(ctx)
		if err != nil {
			return "", fmt.Errorf("body: interaction: await session.result: %w", err)
		}
		fr := decodeInbound(msg)
		switch fr.Kind {
		case "result":
			if fr.Result.CorrelationID != requestID {
				continue
			}
			if fr.Result.Payload.Status != core5.StatusOK {
				return "", sessionControlError(fr.Result.Payload.Error)
			}
			if fr.Result.Payload.Session == nil {
				return "", fmt.Errorf("body: interaction: session.create returned no session")
			}
			return fr.Result.Payload.Session.ID, nil
		default:
			continue // events and unrelated frames are not this response
		}
	}
}

// SendLine sends one terminal line as a message Event through the Interaction
// Session and returns the Doll's reply. A Core-reported error yields
// kind=ErrCore with a displayable error; a failed connection yields
// kind=ErrTransport with the underlying error.
func SendLine(ctx context.Context, that Session, sessionID, dollID, line string) (string, SendErrorKind, error) {
	msgID := "evt_" + uuid.NewString()
	message := core5.NewDollMessage(msgID, dollID, line)
	it := core5.Interaction{
		Version:   core5.SessionControlVersion,
		Type:      core5.TypeInteraction,
		SessionID: sessionID,
		Event:     message,
	}
	if err := that.Write(ctx, it); err != nil {
		return "", ErrTransport, err
	}
	return awaitReply(ctx, that, msgID)
}

// CloseSession closes the Interaction Session and awaits the close result.
// A Close on an already-closed or unreachable session is surfaced as an error
// (the caller decides whether that is fatal).
func CloseSession(ctx context.Context, that Session, sessionID string) error {
	requestID := "sc_" + uuid.NewString()
	req := core5.SessionRequest{
		Version: core5.SessionControlVersion,
		Type:    core5.TypeClose,
		ID:      requestID,
		Payload: core5.SessionRequestPayload{
			SessionID: sessionID,
		},
	}
	if err := that.Write(ctx, req); err != nil {
		return fmt.Errorf("body: interaction: session.close: %w", err)
	}
	for {
		msg, err := that.Read(ctx)
		if err != nil {
			return fmt.Errorf("body: interaction: await session.close result: %w", err)
		}
		fr := decodeInbound(msg)
		switch fr.Kind {
		case "result":
			if fr.Result.CorrelationID != requestID {
				continue
			}
			if fr.Result.Payload.Status != core5.StatusOK {
				return sessionControlError(fr.Result.Payload.Error)
			}
			return nil
		default:
			continue
		}
	}
}

// RunConversation opens a real Interaction Session, runs the interactive
// terminal against it (each line sent as an interaction, each Doll response
// displayed), and then closes the session cleanly on a normal end. A transport
// failure mid-conversation cancels the terminal and is surfaced as the result
// error so the caller can report it (and serve-style recovery can adapt).
func RunConversation(ctx context.Context, that Session, dollID string, in io.Reader, out io.Writer) (*terminal.Result, error) {
	requestID := "sc_" + uuid.NewString()
	sid, err := OpenSession(ctx, that, requestID)
	if err != nil {
		return nil, err
	}

	childCtx, childCancel := context.WithCancel(ctx)
	var fatal *error

	fn := func(line string, n int) string {
		reply, kind, err := SendLine(ctx, that, sid, dollID, line)
		switch kind {
		case ErrCore:
			return "! " + errorText(err)
		case ErrTransport:
			*fatal = err
			childCancel()
			return "! " + errorText(err)
		default:
			return reply
		}
	}

	tm := terminal.New(in, out, fn)
	res, rerr := tm.Run(childCtx)
	if fatal != nil {
		return res, *fatal
	}
	if rerr != nil {
		return res, rerr
	}
	// Close the session cleanly only on a genuine end of input; cancelled or
	// failed conversations are already beyond a clean close.
	if res != nil && res.EndedBy == terminal.EndEOF {
		if err := CloseSession(ctx, that, sid); err != nil {
			return res, fmt.Errorf("body: interaction: close session: %w", err)
		}
	}
	return res, nil
}

// awaitReply reads frames until the response correlated with msgID arrives.
func awaitReply(ctx context.Context, that Session, msgID string) (string, SendErrorKind, error) {
	for {
		msg, err := that.Read(ctx)
		if err != nil {
			return "", ErrTransport, err
		}
		fr := decodeInbound(msg)
		switch fr.Kind {
		case "event":
			e := fr.Event
			if e.CorrelationID != "" && e.CorrelationID != msgID {
				continue // another correlation's event (unexpected in a serial conversation)
			}
			switch e.Type {
			case "message":
				text, merr := eventMessageText(e.Payload)
				if merr != nil {
					return "", ErrCore, fmt.Errorf("body: interaction: %w", merr)
				}
				return text, ErrNone, nil
			case "system":
				stext, serr := eventSystemText(e.Payload)
				if serr != nil {
					return "", ErrCore, fmt.Errorf("body: interaction: %w", serr)
				}
				return "", ErrCore, fmt.Errorf("%s", stext)
			default:
				continue
			}
		case "result":
			// An unexpected session-control result mid-interaction is ignored.
			continue
		default:
			continue
		}
	}
}

// eventMessageText extracts the chat text from a message Event payload,
// mirroring Core's interactionMessageText (typed MessagePayload, raw-map
// fallback).
func eventMessageText(payload any) (string, error) {
	switch p := payload.(type) {
	case core5.MessagePayload:
		return p.Text, nil
	case map[string]any:
		if v, ok := p["text"].(string); ok {
			return v, nil
		}
		return "", fmt.Errorf("response payload has no text")
	default:
		return "", fmt.Errorf("unexpected response payload")
	}
}

// eventSystemText extracts the message text from a system Event payload (Core
// reports errors as Event:"error", Data:message).
func eventSystemText(payload any) (string, error) {
	switch p := payload.(type) {
	case core5.SystemPayload:
		if v, ok := p.Data.(string); ok {
			return v, nil
		}
		return "", fmt.Errorf("system payload has no data")
	case map[string]any:
		if v, ok := p["data"].(string); ok {
			return v, nil
		}
		return "", fmt.Errorf("system payload has no data")
	default:
		return "", fmt.Errorf("unexpected system payload")
	}
}

// ---- framing helpers ----

// inbound is a discriminated view of a decoded inbound frame.
type inbound struct {
	Kind   string
	Result *core5.SessionResponse
	Event  *core5.Event
}

// decodeInbound parses an inbound JSON frame enough to identify whether it is
// a session control result or an event. Malformed frames decode as
// Kind="unknown" and are ignored by callers, mirroring Core's resilience on
// the wire.
func decodeInbound(msg []byte) inbound {
	var env struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		return inbound{Kind: "unknown"}
	}
	switch env.Type {
	case string(core5.TypeResult):
		var r core5.SessionResponse
		if err := json.Unmarshal(msg, &r); err != nil {
			return inbound{Kind: "unknown"}
		}
		return inbound{Kind: "result", Result: &r}
	default:
		var e core5.Event
		if err := json.Unmarshal(msg, &e); err != nil {
			return inbound{Kind: "unknown"}
		}
		return inbound{Kind: "event", Event: &e}
	}
}

// sessionControlError renders a Core session control Error detail.
func sessionControlError(e *core5.SessionError) error {
	if e == nil {
		return fmt.Errorf("body: interaction: session control failed (no detail)")
	}
	return fmt.Errorf("body: interaction: %s: %s", e.Code, e.Message)
}

// errorText formats an error as a displayable string.
func errorText(err error) string {
	return fmt.Sprintf("%v", err)
}
