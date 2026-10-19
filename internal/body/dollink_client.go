// Package body provides the Shell Body's implementation of the Doll Link and Body Protocol.
// It handles communication with a NeonDoll Core over an established Doll Network path.
package body

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/link"
	"github.com/google/uuid"
)

// Transport is the interface for sending and receiving Doll Link messages.
// Implementations should handle the underlying connection (e.g., WebSocket over WireGuard).
type Transport interface {
	// Send sends a Doll Link envelope.
	Send(ctx context.Context, msg link.Envelope) error
	// Receive blocks until it receives an envelope or the context is cancelled.
	Receive(ctx context.Context) (*link.Envelope, error)
	// Close closes the transport.
	Close() error
}

// Client manages the Doll Link and Body Protocol interaction with a Core.
type Client struct {
	// Transport is the underlying Doll Link transport.
	Transport Transport
	// BodyID is the stable Body identity.
	BodyID string
	// BodyType is the type of body (e.g., "shell").
	BodyType string
	// Implementation is the implementation name (e.g., "neondoll-shellbody").
	Implementation string
	// Platform is the runtime platform (e.g., "linux").
	Platform string
	// Architecture is the CPU architecture (e.g., "amd64").
	Architecture string
	// mu protects the client state.
	mu sync.RWMutex
	// negotiatedVersions holds the negotiated Doll Link and Body Contract versions.
	negotiatedVersions struct {
		DollLink     int
		BodyContract int
	}
	// ready indicates whether the Body has completed negotiation and is ready for normal operation.
	ready bool
	// done is closed when the client's run loop exits.
	done chan struct{}
	// onExecutionRequest is called when an execution.request is received.
	onExecutionRequest func(ctx context.Context, req link.ExecutionRequestPayload) (link.ExecutionResultPayload, error)
	// onBodyEvent is called when a body.event is received (for observation).
	onBodyEvent func(ctx context.Context, ev link.EventPayload) error
	// sessionID is the current session identifier.
	sessionID string
	// sessionOpened indicates whether the session has been opened.
	sessionOpened bool
	// onSessionEvent is called when a session.event is received.
	onSessionEvent func(ctx context.Context, ev link.SessionEventPayload) error
	// ---- Terminal session I/O ----
	//
	// The remote terminal session uses a single-outstanding-request contract:
	// at most one input request may be in flight at a time, and a request is
	// outstanding until all of its responses have been consumed from the
	// buffer. The wire protocol carries a monotonic sequence number, but we do
	// NOT claim sequence-based response correlation — responses are delivered
	// in arrival order through a single bounded FIFO.

	// nextTerminalSequence is monotonically increased for each input request.
	// It is allocated atomically so concurrent senders never collide.
	nextTerminalSequence atomic.Uint64
	// terminalMu guards the terminal response buffer and in-flight state.
	terminalMu sync.Mutex
	// terminalInFlight is true while an input request is awaiting responses.
	// It enforces the single-outstanding-request contract.
	terminalInFlight bool
	// terminalOverflowed latches an explicit overflow failure: a
	// terminal_output arrived while the bounded buffer was full and was not
	// delivered. ReceiveTerminalResponse surfaces it exactly once instead of
	// silently dropping it.
	terminalOverflowed bool
	// terminalResponseCh delivers terminal response lines (FIFO) to caller
	// goroutines. It is fed, non-blocking, by handleMessage on the run loop
	// (the single transport receiver). Capacity is bounded by
	// terminalResponseBufferSize; when full, overflow is latched explicitly.
	terminalResponseCh chan string
	// terminalSessionDone is closed when the terminal session closes or
	// changes, waking any goroutine blocked in ReceiveTerminalResponse. It is
	// recreated for each new session.
	terminalSessionDone chan struct{}
}

// terminalResponseBufferSize bounds how many terminal output lines may be
// queued before ReceiveTerminalResponse must drain. A larger buffer smooths
// bursts of terminal output; when full, additional output is never silently
// dropped — overflow is latched and surfaced as an explicit error.
const terminalResponseBufferSize = 16

// NewClient creates a new Body client with the given transport and Body ID.
func NewClient(t Transport, bodyID string) *Client {
	return &Client{
		Transport:           t,
		BodyID:              bodyID,
		BodyType:            "shell",
		Implementation:      "neondoll-shellbody",
		Platform:            runtime.GOOS,
		Architecture:        runtime.GOARCH,
		done:                make(chan struct{}),
		terminalResponseCh:  make(chan string, terminalResponseBufferSize),
		terminalSessionDone: make(chan struct{}),
	}
}

// SetExecutionRequestHandler sets the handler for execution.request messages.
func (c *Client) SetExecutionRequestHandler(h func(ctx context.Context, req link.ExecutionRequestPayload) (link.ExecutionResultPayload, error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onExecutionRequest = h
}

// SetBodyEventHandler sets the handler for body.event messages.
func (c *Client) SetBodyEventHandler(h func(ctx context.Context, ev link.EventPayload) error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onBodyEvent = h
}

// negotiatedDollLinkVersion returns the negotiated Doll Link version.
func (c *Client) negotiatedDollLinkVersion() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.negotiatedVersions.DollLink
}

// negotiatedBodyContractVersion returns the negotiated Body Contract version.
func (c *Client) negotiatedBodyContractVersion() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.negotiatedVersions.BodyContract
}

// isReady returns whether the client is ready for normal operation.
func (c *Client) isReady() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ready
}

// ErrTerminalSessionNotOpened is returned by SendTerminalInput when no
// terminal session is currently open.
var ErrTerminalSessionNotOpened = errors.New("cannot send terminal input: session not opened")

// ErrTerminalRequestOutstanding is returned by SendTerminalInput when a second
// input is sent before the previous request's responses have been consumed.
// Terminal input enforces a single-outstanding-request contract.
var ErrTerminalRequestOutstanding = errors.New("terminal request already outstanding: receive the previous response before sending another input")

// ErrTerminalResponseOverflow is returned by ReceiveTerminalResponse when the
// bounded terminal output buffer filled and output was not delivered. The
// overflow is surfaced explicitly rather than dropped silently.
var ErrTerminalResponseOverflow = errors.New("terminal output buffer overflowed: output was not delivered")

// ErrTerminalSessionClosed is returned by ReceiveTerminalResponse when the
// terminal session closes or is replaced before a response arrives.
var ErrTerminalSessionClosed = errors.New("terminal session closed or changed before a response arrived")

// SendTerminalInput sends terminal input to the Core via session.event.
// It enforces a single-outstanding-request contract: a new input is rejected
// with ErrTerminalRequestOutstanding while a previous request's responses are
// still pending. The sequence number is allocated atomically, so concurrent
// callers can never observe or reuse the same number.
func (c *Client) SendTerminalInput(ctx context.Context, input string) error {
	// Reserve the single-in-flight slot before any I/O so concurrent sends are
	// rejected deterministically, and allocate the sequence atomically. On any
	// failure below the slot is released via releaseInFlight.
	c.terminalMu.Lock()
	if c.terminalInFlight {
		c.terminalMu.Unlock()
		return ErrTerminalRequestOutstanding
	}
	sequence := c.nextTerminalSequence.Add(1) - 1
	c.terminalInFlight = true
	c.terminalMu.Unlock()

	release := true
	defer func() {
		if release {
			c.terminalMu.Lock()
			c.terminalInFlight = false
			c.terminalMu.Unlock()
		}
	}()

	c.mu.RLock()
	sessionID := c.sessionID
	sessionOpened := c.sessionOpened
	c.mu.RUnlock()
	if !sessionOpened {
		return ErrTerminalSessionNotOpened
	}

	payload := link.SessionEventPayload{
		SessionID: sessionID,
		Event:     "terminal_input",
		Data: map[string]interface{}{
			"line":     input,
			"sequence": sequence,
		},
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal terminal input payload: %w", err)
	}

	event := link.Envelope{
		Type:      link.TypeSessionEvent,
		ID:        generateID(),
		BodyID:    c.BodyID,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   payloadBytes,
	}

	if err := c.Transport.Send(ctx, event); err != nil {
		return fmt.Errorf("failed to send terminal input: %w", err)
	}

	// The send succeeded; the request stays in flight until its responses are
	// consumed via ReceiveTerminalResponse.
	release = false
	return nil
}

// ReceiveTerminalResponse returns the next terminal output line, blocking until
// one arrives, the context is cancelled, or the terminal session closes or is
// replaced. Responses are delivered in arrival order through a bounded buffer;
// a saturated buffer surfaces an explicit ErrTerminalResponseOverflow rather
// than silently dropping output.
func (c *Client) ReceiveTerminalResponse(ctx context.Context) (string, error) {
	for {
		c.terminalMu.Lock()
		// Surface a latched overflow exactly once, before consuming buffered
		// lines, so a saturated buffer is reported rather than dropped.
		if c.terminalOverflowed {
			c.terminalOverflowed = false
			c.terminalMu.Unlock()
			return "", ErrTerminalResponseOverflow
		}
		done := c.terminalSessionDone
		c.terminalMu.Unlock()

		select {
		case line := <-c.terminalResponseCh:
			c.terminalMu.Lock()
			if len(c.terminalResponseCh) == 0 {
				c.terminalInFlight = false
			}
			c.terminalMu.Unlock()
			return line, nil
		case <-done:
			return "", ErrTerminalSessionClosed
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// resetTerminalSession clears all pending terminal state and wakes any
// goroutine blocked in ReceiveTerminalResponse. It is called when the terminal
// session closes or is replaced so that stale pending responses are
// invalidated rather than delivered to the wrong session.
func (c *Client) resetTerminalSession() {
	c.terminalMu.Lock()
	c.terminalInFlight = false
	c.terminalOverflowed = false
	// Drain any buffered responses belonging to the previous session.
	for {
		select {
		case <-c.terminalResponseCh:
		default:
			goto drained
		}
	}
drained:
	// Wake blocked receivers; install a fresh signal for the next session.
	close(c.terminalSessionDone)
	c.terminalSessionDone = make(chan struct{})
	c.terminalMu.Unlock()
}

// Run starts the client's main loop: performs hello negotiation, then processes messages.
// It blocks until the context is cancelled or an unrecoverable error occurs.
func (c *Client) Run(ctx context.Context) error {
	// Perform version negotiation.
	if err := c.negotiateVersions(ctx); err != nil {
		return fmt.Errorf("version negotiation failed: %w", err)
	}

	// Advertise capabilities.
	if err := c.advertiseCapabilities(ctx); err != nil {
		return fmt.Errorf("failed to advertise capabilities: %w", err)
	}

	// Signal ready state.
	if err := c.sendReady(ctx); err != nil {
		return fmt.Errorf("failed to send ready: %w", err)
	}

	// Main message loop.
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			// Receive a message.
			msg, err := c.Transport.Receive(ctx)
			if err != nil {
				if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
					return ctx.Err()
				}
				// Log and continue? For now, we treat receive errors as fatal.
				return fmt.Errorf("receive failed: %w", err)
			}

			// Handle the message based on its type.
			if err := c.handleMessage(ctx, msg); err != nil {
				// Depending on the error, we might want to disconnect.
				return fmt.Errorf("message handling failed: %w", err)
			}
		}
	}
}

// advertisedVersionRange is a single source of truth for the Doll Link and
// Body Contract version ranges this Body advertises during negotiation.
type advertisedVersionRange struct {
	DollLink     link.VersionSpec
	BodyContract link.VersionSpec
}

// advertisedRanges returns the version ranges this Body advertises.
func (c *Client) advertisedRanges() advertisedVersionRange {
	return advertisedVersionRange{
		DollLink:     link.VersionSpec{MinVersion: 1, MaxVersion: 1},
		BodyContract: link.VersionSpec{MinVersion: 1, MaxVersion: 1},
	}
}

// selectVersion validates that the selected version present in core.hello falls
// within the advertised spec range. It returns the version and true when the
// selection is present and in-range; missing, zero, or out-of-range selections
// are rejected.
func (r advertisedVersionRange) selectVersion(selected *int, spec link.VersionSpec) (int, bool) {
	if selected == nil || *selected == 0 || *selected < spec.MinVersion || *selected > spec.MaxVersion {
		return 0, false
	}
	return *selected, true
}

// negotiateVersions performs the version negotiation handshake.
func (c *Client) negotiateVersions(ctx context.Context) error {
	ranges := c.advertisedRanges()

	// 1. Send body.hello
	if err := c.sendHello(ctx, ranges); err != nil {
		return err
	}

	// 2. Receive core.hello.
	resp, err := c.Transport.Receive(ctx)
	if err != nil {
		return fmt.Errorf("failed to receive core.hello: %w", err)
	}
	if resp.Type != link.TypeCoreHello {
		return fmt.Errorf("expected core.hello, got %s", resp.Type)
	}
	var coreHelloPayload struct {
		DollLinkVersion     *int `json:"doll_link_version"`
		BodyContractVersion *int `json:"body_contract_version"`
	}
	if err := json.Unmarshal(resp.Payload, &coreHelloPayload); err != nil {
		return fmt.Errorf("failed to unmarshal core.hello payload: %w", err)
	}

	// 3. Validate Core's selected versions against our advertised ranges.
	dollLink, okDollLink := ranges.selectVersion(coreHelloPayload.DollLinkVersion, ranges.DollLink)
	bodyContract, okBody := ranges.selectVersion(coreHelloPayload.BodyContractVersion, ranges.BodyContract)
	if !okDollLink {
		return fmt.Errorf("negotiation failed: Core selected unsupported Doll Link version (advertised %d-%d)", ranges.DollLink.MinVersion, ranges.DollLink.MaxVersion)
	}
	if !okBody {
		return fmt.Errorf("negotiation failed: Core selected unsupported Body Contract version (advertised %d-%d)", ranges.BodyContract.MinVersion, ranges.BodyContract.MaxVersion)
	}

	c.mu.Lock()
	c.negotiatedVersions.DollLink = dollLink
	c.negotiatedVersions.BodyContract = bodyContract
	c.mu.Unlock()

	return nil
}

// sendHello sends the body.hello advertisement including the advertised ranges.
func (c *Client) sendHello(ctx context.Context, ranges advertisedVersionRange) error {
	payloadBytes, err := json.Marshal(link.HelloPayload{
		BodyType:       c.BodyType,
		Implementation: c.Implementation,
		Platform:       c.Platform,
		Architecture:   c.Architecture,
		DollLink:       ranges.DollLink,
		BodyContract:   ranges.BodyContract,
		Build:          0,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal body.hello payload: %w", err)
	}
	bodyHello := link.Envelope{
		Type:      link.TypeBodyHello,
		ID:        generateID(),
		BodyID:    c.BodyID,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   payloadBytes,
	}
	if err := c.Transport.Send(ctx, bodyHello); err != nil {
		return fmt.Errorf("failed to send body.hello: %w", err)
	}
	return nil
}
func (c *Client) advertiseCapabilities(ctx context.Context) error {
	payloadBytes, err := json.Marshal(link.CapabilitiesPayload{
		Capabilities: []link.Capability{
			{
				ID:         "terminal",
				Operations: []string{"input", "output"},
				Available:  true,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to marshal capabilities payload: %w", err)
	}
	cap := link.Envelope{
		Type:      link.TypeBodyCapabilities,
		ID:        generateID(),
		BodyID:    c.BodyID,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   payloadBytes,
	}
	if err := c.Transport.Send(ctx, cap); err != nil {
		return fmt.Errorf("failed to send body.capabilities: %w", err)
	}
	return nil
}

// sendReady sends body.ready.
func (c *Client) sendReady(ctx context.Context) error {
	payloadBytes, err := json.Marshal(link.ReadyPayload{})
	if err != nil {
		return fmt.Errorf("failed to marshal ready payload: %w", err)
	}
	ready := link.Envelope{
		Type:      link.TypeBodyReady,
		ID:        generateID(),
		BodyID:    c.BodyID,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   payloadBytes,
	}
	if err := c.Transport.Send(ctx, ready); err != nil {
		return fmt.Errorf("failed to send body.ready: %w", err)
	}
	c.mu.Lock()
	c.ready = true
	c.mu.Unlock()
	return nil
}

// handleMessage routes incoming messages to the appropriate handler.
func (c *Client) handleMessage(ctx context.Context, msg *link.Envelope) error {
	switch msg.Type {
	case link.TypeExecutionRequest:
		var req link.ExecutionRequestPayload
		if err := json.Unmarshal(msg.Payload, &req); err != nil {
			return fmt.Errorf("failed to unmarshal execution.request: %w", err)
		}
		c.mu.RLock()
		h := c.onExecutionRequest
		c.mu.RUnlock()
		if h == nil {
			// No handler: return unsupported.
			res := link.ExecutionResultPayload{
				ExecutionID: req.ExecutionID,
				Status:      "unsupported",
			}
			out := link.Envelope{
				Type:          link.TypeExecutionResult,
				ID:            generateID(),
				CorrelationID: msg.ID,
				BodyID:        c.BodyID,
				Timestamp:     time.Now().UTC().Format(time.RFC3339),
				Payload:       json.RawMessage{},
			}
			if marshaled, err := json.Marshal(res); err != nil {
				return fmt.Errorf("failed to marshal execution.result: %w", err)
			} else {
				out.Payload = marshaled
			}
			if err := c.Transport.Send(ctx, out); err != nil {
				return fmt.Errorf("failed to send execution.result: %w", err)
			}
			return nil
		}
		res, err := h(ctx, req)
		if err != nil {
			// If the handler returns an error, we treat it as a failure.
			res = link.ExecutionResultPayload{
				ExecutionID: req.ExecutionID,
				Status:      "failed",
			}
		}
		out := link.Envelope{
			Type:          link.TypeExecutionResult,
			ID:            generateID(),
			CorrelationID: msg.ID,
			BodyID:        c.BodyID,
			Timestamp:     time.Now().UTC().Format(time.RFC3339),
			Payload:       json.RawMessage{},
		}
		if marshaled, err := json.Marshal(res); err != nil {
			return fmt.Errorf("failed to marshal execution.result: %w", err)
		} else {
			out.Payload = marshaled
		}
		if err := c.Transport.Send(ctx, out); err != nil {
			return fmt.Errorf("failed to send execution.result: %w", err)
		}
		return nil

	case link.TypeBodyEvent:
		var ev link.EventPayload
		if err := json.Unmarshal(msg.Payload, &ev); err != nil {
			return fmt.Errorf("failed to unmarshal body.event: %w", err)
		}
		c.mu.RLock()
		h := c.onBodyEvent
		c.mu.RUnlock()
		if h == nil {
			return nil
		}
		if err := h(ctx, ev); err != nil {
			return fmt.Errorf("body.event handler failed: %w", err)
		}
		return nil

	case link.TypeSessionOpened:
		var payload link.SessionOpenedPayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			return fmt.Errorf("failed to unmarshal session.opened: %w", err)
		}
		c.mu.Lock()
		c.sessionID = payload.SessionID
		c.sessionOpened = true
		c.mu.Unlock()
		// A session open (including a replacement) invalidates any pending
		// responses from a previous session.
		c.resetTerminalSession()
		return nil

	case link.TypeSessionEvent:
		var payload link.SessionEventPayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			return fmt.Errorf("failed to unmarshal session.event: %w", err)
		}
		c.mu.RLock()
		if c.sessionID != payload.SessionID {
			c.mu.RUnlock()
			return fmt.Errorf("session.event with mismatched session ID: expected %s, got %s", c.sessionID, payload.SessionID)
		}
		c.mu.RUnlock()
		// Handle terminal_output events by delivering the response to
		// ReceiveTerminalResponse regardless of whether a session-event
		// handler is attached.
		if payload.Event == "terminal_output" {
			if line, lineOk := payload.Data["line"].(string); lineOk {
				c.terminalMu.Lock()
				select {
				case c.terminalResponseCh <- line:
				default:
					// Buffer full: never drop output silently. Latch an
					// explicit overflow failure that ReceiveTerminalResponse
					// surfaces deterministically. The run loop must not block,
					// so this is the bounded-overflow contract.
					c.terminalOverflowed = true
				}
				c.terminalMu.Unlock()
			}
		}
		h := c.onSessionEvent
		if h == nil {
			return nil
		}
		return h(ctx, payload)

	case link.TypeSessionClose:
		var payload link.SessionClosePayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			return fmt.Errorf("failed to unmarshal session.close: %w", err)
		}
		c.mu.Lock()
		c.sessionID = ""
		c.sessionOpened = false
		c.mu.Unlock()
		// Closing the session invalidates any pending terminal responses.
		c.resetTerminalSession()
		return nil

	default:
		// Ignore unknown message types? The spec says unknown required message types MUST fail explicitly.
		// We'll treat it as an error.
		return fmt.Errorf("unexpected message type: %s", msg.Type)
	}
}

// Close closes the client and its transport.
func (c *Client) Close() error {
	close(c.done)
	return c.Transport.Close()
}

// generateID creates a unique ID suitable for production messages.
// Uses UUID version 4 for uniqueness without coordination.
func generateID() string {
	return "evt_" + uuid.NewString()
}
