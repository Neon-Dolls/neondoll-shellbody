// Package interaction gives the Shell Body an interactive conversation mode
// (Milestones M11.1 — Real Terminal Conversation, and M11.2 — Persistent
// Terminal Sessions).
//
// A human types lines into the terminal; each line travels as a real Core 5
// Interaction Session message over the authenticated Doll Link WebSocket
// (session.create → interaction → session.close) and the Core's Doll response
// is displayed. The wire structs are mirrored from the real Core 5 types in
// internal/link/core5; no neondoll production import is made.
//
// M11.2 preserves the Core Interaction Session across temporary transport
// disconnects. The conversation holds the active session id in memory (it is
// durable Core state, owned by this Body, never written to disk by the Body).
// On a transport drop it re-dials and re-authenticates through the same
// recovery machinery (DialFrameSession + bounded backoff), then re-selects the
// existing session by id and verifies it is still active and owned by this
// Body before sending further input. If Core reports the session
// closed/revoked/unavailable, or a reconnection fails permanently, the
// conversation fails explicitly and never silently creates a replacement
// session. An interrupted request is never resent and never assumed to have
// succeeded.
//
// The conversation drives any transport that satisfies the narrow Session
// surface (write a JSON frame, read the next frame interruptibly, close). The
// CLI adapts an authenticated body.Core4Session via a Dialer; tests use a
// scripted fake.
package interaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/body"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/link/core5"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/terminal"
	"github.com/google/uuid"
)

// SendErrorKind distinguishes why a SendLine did not get a normal reply.
type SendErrorKind string

const (
	ErrNone      SendErrorKind = ""
	ErrCore      SendErrorKind = "core"      // Core rejected the interaction; transport is alive
	ErrTransport SendErrorKind = "transport" // the connection failed; the conversation drops it and reconnects
)

// Session is the minimal transport surface the conversation needs. It is
// satisfied by an authenticated body.Core4Session and by test fakes.
type Session struct {
	Write func(ctx context.Context, value any) error
	Read  func(ctx context.Context) ([]byte, error)
	Close func() error
}

// Dialer produces a fresh authenticated Core transport for a (re)connect. Each
// call re-dials the /ws endpoint, performs a fresh Body Authentication v1
// handshake, and re-advertises capabilities — it is the existing recovery
// machinery (body.Core4Session via DialFrameSession in the CLI; scripted fakes
// in tests). The CLI wraps this so a reconnect re-authenticates rather than
// reusing a stale, possibly-revoked credential on a dead socket.
type Dialer func(ctx context.Context) (Session, error)

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

// SelectSession re-activates an existing Core 5 Interaction Session by its id
// (session.select) and returns the session id on success (currently the same
// id). Core is the single authority on whether the session may be resumed: it
// only selects a session that is owned by the authenticated Body AND still
// active. A missing, foreign (owned by another Body identity), closed, or
// revoked session returns Status=error with code session_unavailable — and,
// because Core discloses no detail for foreign sessions, the caller can never
// distinguish why a resume was refused, so it must fail explicitly rather than
// guess. Selecting never creates a replacement.
func SelectSession(ctx context.Context, that Session, requestID, sessionID string) (string, error) {
	req := core5.SessionRequest{
		Version: core5.SessionControlVersion,
		Type:    core5.TypeSelect,
		ID:      requestID,
		Payload: core5.SessionRequestPayload{
			SessionID: sessionID,
		},
	}
	if err := that.Write(ctx, req); err != nil {
		return "", fmt.Errorf("body: interaction: session.select: %w", err)
	}
	for {
		msg, err := that.Read(ctx)
		if err != nil {
			return "", fmt.Errorf("body: interaction: await session.select result: %w", err)
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
				return "", fmt.Errorf("body: interaction: session.select returned no session")
			}
			if fr.Result.Payload.Session.Status != "active" {
				return "", fmt.Errorf("body: interaction: session %q is not active (status=%q): cannot resume safely", sessionID, fr.Result.Payload.Session.Status)
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

// chat carries the state of one persistent Core Interaction conversation
// across transport reconnects. sessionID (sid) is durable Core state owned by
// this Body; it is preserved here in memory for the lifetime of the process so
// a reconnect resumes the SAME session rather than creating a replacement. It
// is never written to disk by the Body.
type chat struct {
	dialer       Dialer
	dollID       string
	reconnect    time.Duration // base backoff for transient dial failures
	maxReconnect time.Duration // backoff ceiling
	live         Session       // current authenticated transport
	connected    bool          // whether live is a valid, resumed transport
	sid          string        // Core Interaction Session id; "" until a create succeeds
}

// Reconnect backoff mirrors body's recovery parameters (recovery.go). They are
// declared here rather than imported from body because those internal helpers
// are not exported across packages; the connection/auth machinery itself is
// still reused via the Dialer, and permanent-failure classification via
// body.IsPermanent.
const (
	reconnectInitialDelay = 500 * time.Millisecond
	reconnectMaxDelay     = 30 * time.Second
	// closeAfterDisconnectDeadline is the overall time budget for the
	// disconnected-EOF cleanup attempt. If Core stays unreachable beyond
	// this deadline, the cleanup returns an explicit error identifying the
	// session that could not be closed.
	closeAfterDisconnectDeadline = 5 * time.Second
)

// nextDelay reproduces body's nextBackoff: double the delay, saturating at max
// and guarding against duration overflow.
func nextDelay(cur, max time.Duration) time.Duration {
	next := cur * 2
	if next <= cur || next > max {
		return max
	}
	return next
}

// waitFor is a cancellation-aware sleep: a cancelled ctx returns immediately
// with ctx.Err() instead of finishing the delay (mirrors body.waitBackoff).
func waitFor(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// ensureResumed returns the conversation to a live, authenticated, resumed
// transport after a transport drop. It uses SelectSessionWithRetry to
// re-dial via the Dialer (which performs a fresh Body Authentication v1
// and re-advertises capabilities), retrying transient dial failures with
// bounded exponential backoff. Once connected it re-selects the existing
// Core Interaction Session by id and verifies it is still active and owned
// by this Body before returning. A missing, foreign, closed, or revoked
// session, or a permanent authentication failure, returns an error so the
// conversation fails explicitly and never silently creates a replacement.
//
// Transport failures during session.select are retried with fresh authentication
// and bounded backoff. Explicit Core rejections (session_unavailable, closed,
// revoked, non-active status) fail closed immediately — never retried.
func ensureResumed(ctx context.Context, c *chat) error {
	if c.connected {
		return nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// SelectSessionWithRetry handles dialing, select, and retry logic
		resumed, sess, serr := SelectSessionWithRetry(ctx, c.dialer, c.sid, c.reconnect, c.maxReconnect)
		if serr != nil {
			return fmt.Errorf("body: interaction: cannot resume session: %w", serr)
		}
		if resumed != c.sid {
			_ = sess.Close()
			return fmt.Errorf("body: interaction: session.select returned %q, want %q", resumed, c.sid)
		}
		c.live = sess
		c.connected = true
		return nil
	}
}

// SelectSessionWithRetry performs session.select with retry logic that
// distinguishes transient transport failures from explicit Core rejections.
//   - Transient transport errors (dial/read/write failures) are retried with
//     fresh authentication and bounded exponential backoff.
//   - Explicit Core rejections (session_unavailable, session not active,
//     foreign/closed/revoked) fail closed immediately and are never retried.
//   - Permanent authentication failures (IsPermanent) fail closed immediately.
//
// Returns the selected session ID and the authenticated Session (caller owns it).
func SelectSessionWithRetry(ctx context.Context, dialer Dialer, sessionID string, initialBackoff, maxBackoff time.Duration) (string, Session, error) {
	backoff := initialBackoff
	for {
		if err := ctx.Err(); err != nil {
			return "", Session{}, err
		}
		sess, err := dialer(ctx)
		if err != nil {
			// Dial failure: classify as permanent vs transient
			if body.IsPermanent(err) {
				return "", Session{}, err
			}
			// Transient dial failure - retry with backoff
			if ctx.Err() != nil {
				return "", Session{}, ctx.Err()
			}
			if werr := waitFor(ctx, backoff); werr != nil {
				return "", Session{}, werr
			}
			backoff = nextDelay(backoff, maxBackoff)
			continue
		}
		// Dial succeeded, now attempt session.select
		resumed, serr := SelectSession(ctx, sess, "sc_"+uuid.NewString(), sessionID)
		if serr == nil {
			// Success - session resumed
			return resumed, sess, nil
		}
		_ = sess.Close()
		// Check if this is a permanent Core rejection (never retry)
		var sessionErr *SessionError
		if errors.As(serr, &sessionErr) {
			switch sessionErr.Code {
			case core5.ErrorSessionUnavailable:
				// Explicit Core rejection - session unavailable, closed, revoked, foreign
				return "", Session{}, fmt.Errorf("body: interaction: session unavailable: %w", serr)
			case core5.ErrorInternal, core5.ErrorInvalidRequest:
				// Other explicit Core errors - fail closed
				return "", Session{}, fmt.Errorf("body: interaction: session.select rejected: %w", serr)
			}
		}
		// Check for non-active status in the error message
		if strings.Contains(serr.Error(), "not active") {
			return "", Session{}, fmt.Errorf("body: interaction: session not active: %w", serr)
		}
		// Transport failure during session.select - retry with fresh auth
		if ctx.Err() != nil {
			return "", Session{}, ctx.Err()
		}
		if werr := waitFor(ctx, backoff); werr != nil {
			return "", Session{}, werr
		}
		backoff = nextDelay(backoff, maxBackoff)
	}
}

// SessionError wraps a Core session control error code for typed checking.
type SessionError struct {
	Code    core5.ErrorCode
	Message string
}

func (e *SessionError) Error() string {
	return fmt.Sprintf("session error: code=%s message=%s", e.Code, e.Message)
}

// sessionControlError converts a Core session control error payload into a
// typed SessionError for programmatic checking.
func sessionControlError(err *core5.SessionError) error {
	if err == nil {
		return fmt.Errorf("body: interaction: session control error (no detail)")
	}
	return &SessionError{Code: err.Code, Message: err.Message}
}

// RunConversation opens a real Interaction Session, runs the interactive
// terminal against it (each line sent as an interaction, each Doll response
// displayed), and closes the session cleanly on a normal end. It preserves the
// session across temporary transport disconnects: on a drop it reconnects and
// re-authenticates, re-selects the same session id, and resumes — so the human
// continues the same conversation. If the session is reported closed/revoked/
// unavailable or a reconnect fails permanently, the conversation ends with an
// explicit error and never creates a replacement session.
func RunConversation(ctx context.Context, dialer Dialer, dollID string, in io.Reader, out io.Writer) (*terminal.Result, error) {
	c := &chat{
		dialer:       dialer,
		dollID:       dollID,
		reconnect:    reconnectInitialDelay,
		maxReconnect: reconnectMaxDelay,
	}

	// First connection establishes the Interaction Session. A failure here is
	// surfaced immediately (there is no prior session to resume yet), matching
	// the pre-M11.2 single-attempt connect behavior.
	sess, err := dialer(ctx)
	if err != nil {
		return nil, fmt.Errorf("body: interaction: connect: %w", err)
	}
	sid, oerr := OpenSession(ctx, sess, "sc_"+uuid.NewString())
	if oerr != nil {
		_ = sess.Close()
		return nil, oerr
	}
	c.live = sess
	c.connected = true
	c.sid = sid

	childCtx, childCancel := context.WithCancel(ctx)
	// fatalSlot owns the error storage that the per-line closure writes
	// through fatal. It is read only after Run returns, so it stays within
	// RunConversation's frame for the whole synchronous Run.
	var fatalSlot error
	var fatal *error = &fatalSlot

	fn := func(line string, n int) string {
		// A dropped conversation is reconnected and its session resumed before
		// the next input is sent; a failure here is fatal (fail closed).
		if rerr := ensureResumed(childCtx, c); rerr != nil {
			*fatal = rerr
			childCancel()
			return "! " + errorText(rerr)
		}
		reply, kind, err := SendLine(childCtx, c.live, c.sid, dollID, line)
		switch kind {
		case ErrCore:
			return "! " + errorText(err)
		case ErrTransport:
			// The request may or may not have reached Core; its outcome is
			// unknown. Do not resend it (duplication) and do not assume it
			// succeeded. Drop the transport; the next input reconnects and
			// resumes the same session.
			_ = c.live.Close()
			c.connected = false
			return "! connection interrupted; outcome unknown — reconnect and re-send if it did not land"
		default:
			return reply
		}
	}

	tm := terminal.New(in, out, fn)
	res, rerr := tm.Run(childCtx)
	if *fatal != nil {
		return res, *fatal
	}
	if rerr != nil {
		return res, rerr
	}
	// Close the session cleanly only on a genuine end of input while still
	// connected; cancelled/failed conversations are already beyond a clean
	// close, and a disconnected EOF leaves the durable session active (it can
	// be resumed or closed by a later client). This matches Core's semantics:
	// a transport disconnect never closes the Interaction Session.
	if res != nil && res.EndedBy == terminal.EndEOF {
		if c.connected {
			// We're still connected — clean close is straightforward.
			if err := CloseSession(ctx, c.live, c.sid); err != nil {
				return res, fmt.Errorf("body: interaction: close session: %w", err)
			}
		} else {
			// EOF arrived after a transport disconnect. The Core session is
			// still active; we must attempt a bounded reconnect to close it
			// properly rather than silently leaving it open. If reconnect
			// fails permanently, return an explicit error indicating the
			// session remains open on Core.
			deadline := time.Now().Add(closeAfterDisconnectDeadline)
			if cerr := closeSessionAfterDisconnect(ctx, c.dialer, c.sid, c.reconnect, c.maxReconnect, deadline); cerr != nil {
				return res, fmt.Errorf("body: interaction: session %q still active on Core (clean close failed): %w", c.sid, cerr)
			}
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
			// Require the event to be the response correlated with this send.
			// Events with a different or absent correlation_id (unsolicited
			// pushes, another session's events) are not this reply and are
			// skipped rather than mistaken for the Doll's response.
			if e.CorrelationID != msgID {
				continue
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

// closeSessionAfterDisconnect attempts to close a Core Interaction Session
// after a transport disconnect caused the conversation to end with EOF while
// disconnected. It performs a bounded reconnect with fresh authentication and
// retries transient transport failures up to the given deadline, but fails
// closed on explicit Core rejections (session_unavailable, closed, revoked) or
// permanent authentication failures. Returns nil if the session was closed
// successfully, or an error if the session remains active on Core or the
// deadline was exceeded.
func closeSessionAfterDisconnect(ctx context.Context, dialer Dialer, sessionID string, initialBackoff, maxBackoff time.Duration, deadline time.Time) error {
	// Create a context that respects both the original context and our deadline
	var dialCtx context.Context
	var cancel context.CancelFunc
	if !deadline.IsZero() {
		dialCtx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	} else {
		dialCtx = ctx
	}

	backoff := initialBackoff
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Check overall deadline
		if !deadline.IsZero() && time.Now().After(deadline) {
			return fmt.Errorf("body: interaction: close session deadline exceeded (session %q still active on Core)", sessionID)
		}
		sess, err := dialer(dialCtx)
		if err != nil {
			if body.IsPermanent(err) {
				return fmt.Errorf("body: interaction: permanent dial failure while closing session: %w", err)
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if werr := waitFor(ctx, backoff); werr != nil {
				return werr
			}
			backoff = nextDelay(backoff, maxBackoff)
			continue
		}
		// Dial succeeded. First, select the session to verify it's still
		// active and owned by this Body before attempting to close it.
		// This matches the pattern used in ensureResumed/SelectSessionWithRetry.
		resumed, serr := SelectSession(ctx, sess, "sc_"+uuid.NewString(), sessionID)
		if serr != nil {
			_ = sess.Close()
			// Check if this is a permanent Core rejection (never retry)
			var sessionErr *SessionError
			if errors.As(serr, &sessionErr) {
				switch sessionErr.Code {
				case core5.ErrorSessionUnavailable:
					return fmt.Errorf("body: interaction: session unavailable on close: %w", serr)
				case core5.ErrorInternal, core5.ErrorInvalidRequest:
					return fmt.Errorf("body: interaction: session.select rejected on close: %w", serr)
				}
			}
			if strings.Contains(serr.Error(), "not active") {
				return fmt.Errorf("body: interaction: session not active on close: %w", serr)
			}
			// Transport failure - retry with fresh auth
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if werr := waitFor(ctx, backoff); werr != nil {
				return werr
			}
			backoff = nextDelay(backoff, maxBackoff)
			continue
		}
		if resumed != sessionID {
			_ = sess.Close()
			return fmt.Errorf("body: interaction: session.select returned %q, want %q", resumed, sessionID)
		}
		// Session selected successfully, now attempt session.close
		if err := CloseSession(ctx, sess, sessionID); err != nil {
			_ = sess.Close()
			// Check if this is a permanent Core rejection
			var sessionErr *SessionError
			if errors.As(err, &sessionErr) {
				switch sessionErr.Code {
				case core5.ErrorSessionUnavailable:
					return fmt.Errorf("body: interaction: session unavailable on close: %w", err)
				case core5.ErrorInternal, core5.ErrorInvalidRequest:
					return fmt.Errorf("body: interaction: session.close rejected: %w", err)
				}
			}
			if strings.Contains(err.Error(), "not active") {
				return fmt.Errorf("body: interaction: session not active on close: %w", err)
			}
			// Transport failure - retry
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if werr := waitFor(ctx, backoff); werr != nil {
				return werr
			}
			backoff = nextDelay(backoff, maxBackoff)
			continue
		}
		// Successfully closed
		_ = sess.Close()
		return nil
	}
}

// errorText formats an error as a displayable string.
func errorText(err error) string {
	return fmt.Sprintf("%v", err)
}
