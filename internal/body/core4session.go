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
	"github.com/Neon-Dolls/neondoll-shellbody/internal/link/core4"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// Credentials carries the enrolled identity and the Core trust anchor required
// to run Body Authentication v1 over the Doll Link WebSocket. CorePublicKey is
// Core's Ed25519 identity key (base64url), a trust anchor the Body holds from
// enrollment; Core does not publish it over HTTP, so the CLI persists it in the
// enrollment record at enrollment time (--core-key).
type Credentials struct {
	// BodyID is the body id Core issued at enrollment completion.
	BodyID string
	// PrivateKey is the Body's enrolled Ed25519 identity key used to sign the
	// body.auth.proof frame.
	PrivateKey ed25519.PrivateKey
	// CoreID is Core's public id presented in authentication frames.
	CoreID string
	// CorePublicKey is Core's Ed25519 public key, used to verify the
	// body.auth.challenge and body.auth.accept signatures.
	CorePublicKey ed25519.PublicKey
}

// Core4Session is Shell Body's canonical Core 4 Doll Link connection. It
// connects to Core at /ws, performs Body Authentication v1 in-band using its
// enrolled Ed25519 identity, advertises capabilities, and handles inbound
// execution.request messages, replying with correlated execution.result
// messages. It makes no use of body.hello / core.hello / session.* — those
// legacy wire concepts are not part of Core 4 and are intentionally absent.
type Core4Session struct {
	conn          *websocket.Conn
	bodyID        string
	privateKey    ed25519.PrivateKey
	coreID        string
	corePublicKey ed25519.PublicKey
	adapter       *execution.Adapter

	writeMu sync.Mutex
}

// DialCore4 connects to Core's /ws endpoint. Authentication is not started
// until Authenticate is called.
func DialCore4(url string, creds Credentials, adapter *execution.Adapter) (*Core4Session, error) {
	if len(creds.CorePublicKey) == 0 {
		return nil, fmt.Errorf("body: Core public key is required for authentication")
	}
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return nil, fmt.Errorf("body: dial %s: %w", url, err)
	}
	return &Core4Session{
		conn:          conn,
		bodyID:        creds.BodyID,
		privateKey:    creds.PrivateKey,
		coreID:        creds.CoreID,
		corePublicKey: creds.CorePublicKey,
		adapter:       adapter,
	}, nil
}

// Authenticate performs the canonical Body Authentication v1 handshake:
//
//	body.auth.hello  → (Core) body.auth.challenge
//	body.auth.proof  → (Core) body.auth.accept
//
// It verifies Core's challenge and accept signatures against the Core trust
// anchor and signs the proof with the enrolled identity key. After a
// successful handshake the connection is an authorized execution session.
func (s *Core4Session) Authenticate(ctx context.Context) (*auth.Accept, error) {
	hello, err := auth.BuildHello(s.bodyID)
	if err != nil {
		return nil, fmt.Errorf("body: build hello: %w", err)
	}
	if err := s.writeJSON(ctx, hello); err != nil {
		return nil, fmt.Errorf("body: send hello: %w", err)
	}

	msg, err := s.readFrame(ctx)
	if err != nil {
		return nil, fmt.Errorf("body: read challenge: %w", err)
	}
	var challenge auth.Challenge
	if err := json.Unmarshal(msg, &challenge); err != nil {
		return nil, fmt.Errorf("body: decode challenge: %w", err)
	}
	if challenge.Type != auth.TypeChallenge {
		return nil, fmt.Errorf("body: expected %q frame, got %q", auth.TypeChallenge, challenge.Type)
	}
	if challenge.BodyID != s.bodyID {
		return nil, fmt.Errorf("body: challenge body_id %q != enrolled %q", challenge.BodyID, s.bodyID)
	}
	if s.coreID != "" && challenge.CoreID != s.coreID {
		return nil, fmt.Errorf("body: challenge core_id %q != expected %q", challenge.CoreID, s.coreID)
	}
	if err := auth.VerifyChallenge(s.corePublicKey, challenge); err != nil {
		return nil, fmt.Errorf("body: verify challenge: %w", err)
	}

	proof := auth.BuildProof(challenge, s.coreID)
	if err := auth.SignProof(s.privateKey, proof); err != nil {
		return nil, fmt.Errorf("body: sign proof: %w", err)
	}
	if err := s.writeJSON(ctx, proof); err != nil {
		return nil, fmt.Errorf("body: send proof: %w", err)
	}

	msg, err = s.readFrame(ctx)
	if err != nil {
		return nil, fmt.Errorf("body: read accept: %w", err)
	}
	var accept auth.Accept
	if err := json.Unmarshal(msg, &accept); err != nil {
		return nil, fmt.Errorf("body: decode accept: %w", err)
	}
	if accept.Type != auth.TypeAccept {
		return nil, fmt.Errorf("body: expected %q frame, got %q", auth.TypeAccept, accept.Type)
	}
	if accept.BodyID != s.bodyID {
		return nil, fmt.Errorf("body: accept body_id %q != enrolled %q", accept.BodyID, s.bodyID)
	}
	if accept.SessionID != proof.SessionID {
		return nil, fmt.Errorf("body: accept session_id %q != proof %q", accept.SessionID, proof.SessionID)
	}
	if err := auth.VerifyAccept(s.corePublicKey, accept); err != nil {
		return nil, fmt.Errorf("body: verify accept: %w", err)
	}
	return &accept, nil
}

// AdvertiseCapabilities sends the canonical body.capabilities advertisement.
func (s *Core4Session) AdvertiseCapabilities(ctx context.Context, advertised []core4.Capability) error {
	adv := core4.NewCapabilitiesAdvertisement(s.bodyID, advertised)
	adv.ID = uuid.NewString()
	if err := adv.Validate(); err != nil {
		return fmt.Errorf("body: invalid advertisement: %w", err)
	}
	return s.writeJSON(ctx, adv)
}

// Run is the read loop. It blocks until the connection closes or ctx ends.
// Inbound execution.request messages are executed and answered; any other
// message type is ignored (matching Core's readLoop behavior of not hard-failing
// on unknown types).
func (s *Core4Session) Run(ctx context.Context) error {
	for {
		message, err := s.readFrame(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("body: read: %w", err)
		}
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(message, &envelope); err != nil {
			continue // malformed frame: ignore, mirroring Core's resilience
		}
		switch envelope.Type {
		case core4.TypeExecutionRequest:
			s.handleExecutionRequest(ctx, message)
		default:
			// Not an execution request (event/unknown): ignore.
		}
	}
}

// Close terminates the underlying WebSocket connection.
func (s *Core4Session) Close() error {
	return s.conn.Close()
}

func (s *Core4Session) handleExecutionRequest(ctx context.Context, message []byte) {
	var req core4.ExecutionRequest
	if err := json.Unmarshal(message, &req); err != nil {
		return
	}
	result := s.adapter.Handle(ctx, &req)
	// Adapter already stamped Type/ID/Timestamp/BodyID/CorrelationID; make
	// sure an envelope that arrives without a valid id still answers best-effort.
	_ = s.writeJSON(ctx, result)
}

func (s *Core4Session) readFrame(ctx context.Context) ([]byte, error) {
	_ = s.conn.SetReadDeadline(time.Now().Add(readDeadline))
	_, message, err := s.conn.ReadMessage()
	if err != nil {
		return nil, err
	}
	return message, nil
}

func (s *Core4Session) writeJSON(ctx context.Context, value any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(writeDeadline))
	return s.conn.WriteJSON(value)
}

const (
	readDeadline  = 90 * time.Second
	writeDeadline = 30 * time.Second
)
