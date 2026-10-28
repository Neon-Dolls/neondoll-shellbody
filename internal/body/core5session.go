// Package body provides the Shell Body's interaction with Core 5 over the Doll Link.
package body

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/auth"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/execution"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/link/core5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// Core5Session is Shell Body's canonical Core 5 Doll Link connection. It
// connects to Core at /ws, performs Body Authentication v1 in-band using its
// enrolled Ed25519 identity, advertises capabilities, and handles inbound
// execution.request messages, replying with correlated execution.result
// messages.
type Core5Session struct {
	conn          *websocket.Conn
	bodyID        string
	privateKey    ed25519.PrivateKey
	coreID        string
	corePublicKey ed25519.PublicKey
	adapter       *execution.Core5Adapter

	writeMu sync.Mutex
}

// DialCore5 connects to Core's /ws endpoint. Authentication is not started
// until Authenticate is called.
func DialCore5(url string, creds Core5Credentials, adapter *execution.Core5Adapter) (*Core5Session, error) {
	if len(creds.CorePublicKey) == 0 {
		return nil, fmt.Errorf("body: Core public key is required for authentication")
	}
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return nil, fmt.Errorf("body: dial %s: %w", url, err)
	}
	return &Core5Session{
		conn:          conn,
		bodyID:        creds.BodyID,
		privateKey:    creds.PrivateKey,
		coreID:        creds.CoreID,
		corePublicKey: creds.CorePublicKey,
		adapter:       adapter,
	}, nil
}

// Authenticate performs the Body Authentication v1 handshake.
// Returns the session id assigned by Core on success.
func (s *Core5Session) Authenticate(ctx context.Context) (string, error) {
	// Send body.auth.hello
	hello, err := auth.BuildHello(s.bodyID)
	if err != nil {
		return "", fmt.Errorf("body: build hello: %w", err)
	}

	helloFrame := map[string]any{
		"type":      auth.TypeHello,
		"id":        uuid.NewString(),
		"timestamp": time.Now().UTC(),
		"body_id":   s.bodyID,
		"payload":   hello,
	}

	if err := s.writeFrame(ctx, helloFrame); err != nil {
		return "", fmt.Errorf("body: send hello: %w", err)
	}

	// Read the challenge frame from Core
	challengeData, err := s.readFrame(ctx)
	if err != nil {
		return "", fmt.Errorf("body: read challenge: %w", err)
	}

	var challenge auth.Challenge
	if err := json.Unmarshal(challengeData, &challenge); err != nil {
		return "", fmt.Errorf("body: unmarshal challenge: %w", err)
	}

	// Verify Core's signature on the challenge
	if err := auth.VerifyChallenge(s.corePublicKey, challenge); err != nil {
		return "", fmt.Errorf("body: invalid challenge signature: %w", err)
	}

	// Send the proof frame
	proof := auth.BuildProof(challenge, s.coreID)
	if err := auth.SignProof(s.privateKey, proof); err != nil {
		return "", fmt.Errorf("body: sign proof: %w", err)
	}

	proofFrame := map[string]any{
		"type":      auth.TypeProof,
		"id":        uuid.NewString(),
		"timestamp": time.Now().UTC(),
		"body_id":   s.bodyID,
		"payload":   proof,
	}

	if err := s.writeFrame(ctx, proofFrame); err != nil {
		return "", fmt.Errorf("body: send proof: %w", err)
	}

	// Read the accept frame
	acceptData, err := s.readFrame(ctx)
	if err != nil {
		return "", fmt.Errorf("body: read accept: %w", err)
	}

	var accept auth.Accept
	if err := json.Unmarshal(acceptData, &accept); err != nil {
		return "", fmt.Errorf("body: unmarshal accept: %w", err)
	}

	// Verify Core's signature on the accept
	if err := auth.VerifyAccept(s.corePublicKey, accept); err != nil {
		return "", fmt.Errorf("body: invalid accept: %w", err)
	}

	if accept.Status != auth.StatusAuthenticated {
		return "", fmt.Errorf("body: authentication failed: %s", accept.Status)
	}

	return accept.SessionID, nil
}

// AdvertiseCapabilities sends the body.capabilities advertisement to Core.
func (s *Core5Session) AdvertiseCapabilities(ctx context.Context, caps []core5.Capability) error {
	advertisement := core5.CapabilitiesAdvertisement{
		Type:      core5.TypeCapabilitiesAdvertisement,
		ID:        uuid.NewString(),
		Timestamp: time.Now().UTC(),
		BodyID:    s.bodyID,
		Payload: core5.CapabilitiesPayload{
			Capabilities: caps,
		},
	}
	return s.writeFrame(ctx, advertisement)
}

// Serve starts the message handling loop. Returns when the connection closes.
func (s *Core5Session) Serve(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			data, err := s.readFrame(ctx)
			if err != nil {
				return err
			}
			if err := s.handleMessage(ctx, data); err != nil {
				return err
			}
		}
	}
}

func (s *Core5Session) handleMessage(ctx context.Context, data []byte) error {
	var envelope core5.Envelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("unmarshal envelope: %w", err)
	}

	switch envelope.Type {
	case core5.TypeExecutionRequest:
		return s.handleExecutionRequest(ctx, data)
	default:
		// Ignore unknown message types
		return nil
	}
}

func (s *Core5Session) handleExecutionRequest(ctx context.Context, data []byte) error {
	var req core5.ExecutionRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return fmt.Errorf("unmarshal execution.request: %w", err)
	}

	result := s.adapter.Handle(ctx, &req)

	resultData, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal execution.result: %w", err)
	}

	return s.writeFrame(ctx, resultData)
}

func (s *Core5Session) writeFrame(ctx context.Context, value any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	data, err := json.Marshal(value)
	if err != nil {
		return err
	}

	return s.conn.WriteMessage(websocket.TextMessage, data)
}

func (s *Core5Session) readFrame(ctx context.Context) ([]byte, error) {
	_, data, err := s.conn.ReadMessage()
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Close closes the session and underlying transport.
func (s *Core5Session) Close() error {
	return s.conn.Close()
}
