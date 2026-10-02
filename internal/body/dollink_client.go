// Package body provides the Shell Body's implementation of the Doll Link and Body Protocol.
// It handles communication with a NeonDoll Core over an established Doll Network path.
package body

import (
	"github.com/mitchellh/mapstructure"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/link"
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
	// onSessionOpen is called when a session.open is received.
	onSessionOpen func(ctx context.Context, req link.SessionOpenPayload) (link.SessionOpenedPayload, error)
	// onBodyEvent is called when a body.event is received (for observation).
	onBodyEvent func(ctx context.Context, ev link.EventPayload) error
}

// NewClient creates a new Body client with the given transport and Body ID.
func NewClient(t Transport, bodyID string) *Client {
	return &Client{
		Transport:     t,
		BodyID:        bodyID,
		BodyType:      "shell",
		Implementation: "neondoll-shellbody",
		Platform:      "linux",
		Architecture:  "amd64",
		done:          make(chan struct{}),
	}
}

// SetExecutionRequestHandler sets the handler for execution.request messages.
func (c *Client) SetExecutionRequestHandler(h func(ctx context.Context, req link.ExecutionRequestPayload) (link.ExecutionResultPayload, error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onExecutionRequest = h
}

// SetSessionOpenHandler sets the handler for session.open messages.
func (c *Client) SetSessionOpenHandler(h func(ctx context.Context, req link.SessionOpenPayload) (link.SessionOpenedPayload, error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onSessionOpen = h
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

// negotiateVersions performs the version negotiation handshake.
func (c *Client) negotiateVersions(ctx context.Context) error {
	// 1. Send body.hello
	bodyHello := link.Envelope{
		Type:    link.TypeBodyHello,
		ID:      generateID(),
		BodyID:  c.BodyID,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload: link.HelloPayload{
			BodyType:       c.BodyType,
			Implementation: c.Implementation,
			Platform:       c.Platform,
			Architecture:   c.Architecture,
			DollLink: link.VersionSpec{
				MinVersion: 1,
				MaxVersion: 1,
			},
			BodyContract: link.VersionSpec{
				MinVersion: 1,
				MaxVersion: 1,
			},
			Build: 0,
		},
	}
	if err := c.Transport.Send(ctx, bodyHello); err != nil {
		return fmt.Errorf("failed to send body.hello: %w", err)
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
		DollLinkVersion     int `json:"doll_link_version"`
		BodyContractVersion int `json:"body_contract_version"`
	}
	switch v := resp.Payload.(type) {
	case map[string]interface{}:
		if vv, ok := v["doll_link_version"].(float64); ok {
			coreHelloPayload.DollLinkVersion = int(vv)
		}
		if vv, ok := v["body_contract_version"].(float64); ok {
			coreHelloPayload.BodyContractVersion = int(vv)
		}
	case []byte:
		if err := json.Unmarshal(v, &coreHelloPayload); err != nil {
			return fmt.Errorf("failed to unmarshal core.hello payload: %w", err)
		}
	default:
		return fmt.Errorf("unexpected core.hello payload type: %T", resp.Payload)
	}
	c.mu.Lock()
	c.negotiatedVersions.DollLink = coreHelloPayload.DollLinkVersion
	c.negotiatedVersions.BodyContract = coreHelloPayload.BodyContractVersion
	c.mu.Unlock()

	return nil
}
func (c *Client) advertiseCapabilities(ctx context.Context) error {
	cap := link.Envelope{
		Type:    link.TypeBodyCapabilities,
		ID:      generateID(),
		BodyID:  c.BodyID,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload: link.CapabilitiesPayload{
			Capabilities: []link.Capability{
				{
					ID:         "terminal",
					Operations: []string{"input", "output"},
					Available:  true,
				},
			},
		},
	}
	if err := c.Transport.Send(ctx, cap); err != nil {
		return fmt.Errorf("failed to send body.capabilities: %w", err)
	}
	return nil
}

// sendReady sends body.ready.
func (c *Client) sendReady(ctx context.Context) error {
	ready := link.Envelope{
		Type:    link.TypeBodyReady,
		ID:      generateID(),
		BodyID:  c.BodyID,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload: link.ReadyPayload{},
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
		if err := json.Unmarshal(msg.Payload.([]byte), &req); err != nil {
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
				Payload:       res,
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
			Payload:       res,
		}
		if err := c.Transport.Send(ctx, out); err != nil {
			return fmt.Errorf("failed to send execution.result: %w", err)
		}
		return nil

	case link.TypeSessionOpen:
		var req link.SessionOpenPayload
		switch v := msg.Payload.(type) {
		case []byte:
			if err := json.Unmarshal(v, &req); err != nil {
				return fmt.Errorf("failed to unmarshal session.open: %w", err)
			}
		case map[string]interface{}:
			if err := mapstructure.Decode(v, &req); err != nil {
				return fmt.Errorf("failed to decode session.open: %w", err)
			}
		default:
			return fmt.Errorf("unexpected session.open payload type: %T", msg.Payload)
		}
		c.mu.RLock()
		h := c.onSessionOpen
		c.mu.RUnlock()
		if h == nil {
			// No handler: we could return an error, but for now we just ignore.
			return nil
		}
		res, err := h(ctx, req)
		if err != nil {
			return fmt.Errorf("session.open handler failed: %w", err)
		}
		out := link.Envelope{
			Type:          link.TypeSessionOpened,
			ID:            generateID(),
			CorrelationID: msg.ID,
			BodyID:        c.BodyID,
			Timestamp:     time.Now().UTC().Format(time.RFC3339),
			Payload:       res,
		}
		if err := c.Transport.Send(ctx, out); err != nil {
			return fmt.Errorf("failed to send session.opened: %w", err)
		}
		return nil

	case link.TypeBodyEvent:
		var ev link.EventPayload
		if err := json.Unmarshal(msg.Payload.([]byte), &ev); err != nil {
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

// generateID creates a simple monotonically increasing ID for demo/testing.
// In production, we should use a proper unique identifier (e.g., UUID).
var idCounter int64

func generateID() string {
	idCounter++
	return fmt.Sprintf("evt_%d", idCounter)
}