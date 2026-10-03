// Package body provides tests for the Doll Link client.
package body

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/link"
)

// fakeTransport implements Transport for testing.
// It uses unbuffered channels to synchronize sends and receives, and
// marshals/unmarshals Envelopes to/from JSON to simulate the wire format.
type fakeTransport struct {
	mu       sync.Mutex
	sendCh   chan []byte // channel for JSON bytes sent by the client
	recvCh   chan []byte // channel for JSON bytes to be received by the client
	sendErr  error
	recvErr  error
	closeErr error
	closed   bool
}

// NewFakeTransport creates a fake transport for testing.
func NewFakeTransport() *fakeTransport {
	return &fakeTransport{
		sendCh: make(chan []byte),
		recvCh: make(chan []byte),
	}
}

// Send marshals the Envelope to JSON and sends it on sendCh.
func (t *fakeTransport) Send(ctx context.Context, msg link.Envelope) error {
	t.mu.Lock()
	if t.sendErr != nil {
		t.mu.Unlock()
		return t.sendErr
	}
	t.mu.Unlock()
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal envelope: %w", err)
	}
	select {
	case t.sendCh <- data:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Receive waits for JSON bytes on recvCh, unmarshals them into an Envelope, and returns it.
func (t *fakeTransport) Receive(ctx context.Context) (*link.Envelope, error) {
	t.mu.Lock()
	if t.recvErr != nil {
		t.mu.Unlock()
		return nil, t.recvErr
	}
	t.mu.Unlock()
	select {
	case data := <-t.recvCh:
		var msg link.Envelope
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal envelope: %w", err)
		}
		return &msg, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close marks the transport as closed and returns a close error.
func (t *fakeTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	return t.closeErr
}
