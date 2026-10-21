package body

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/execution"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/link/core4"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// Core4Session is Shell Body's canonical Core 4 Doll Link connection. It
// connects to Core at /ws, advertises capabilities, and handles inbound
// execution.request messages, replying with correlated execution.result
// messages. It makes no use of body.hello / core.hello / session.* — those
// legacy wire concepts are not part of Core 4 and are intentionally absent
// here.
//
// Acceptance of the connection as an execution session is decided by Core at
// upgrade time via its AuthenticateExecution trust boundary (outside this
// transport). This session honors the canonical sequence regardless.
type Core4Session struct {
	conn    *websocket.Conn
	bodyID  string
	adapter *execution.Adapter

	writeMu sync.Mutex
}

// DialCore4 connects to Core's /ws endpoint.
func DialCore4(url, bodyID string, adapter *execution.Adapter) (*Core4Session, error) {
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return nil, fmt.Errorf("body: dial %s: %w", url, err)
	}
	return &Core4Session{
		conn:    conn,
		bodyID:  bodyID,
		adapter: adapter,
	}, nil
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
		_ = s.conn.SetReadDeadline(time.Now().Add(readDeadline))
		_, message, err := s.conn.ReadMessage()
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
