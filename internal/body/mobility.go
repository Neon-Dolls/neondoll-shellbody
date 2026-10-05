package body

import (
	"context"
	"errors"
	"sync"
	"time"
)

// clientRunner is the subset of *Client needed to run it.
type clientRunner interface {
	Run(context.Context) error
}

// MobilityManager manages the lifecycle of a Doll Link client across
// transport failures. It preserves Body ID, membership, and related
// state while tearing down and recreating only ephemeral transport
// and client state.
type MobilityManager struct {
	mu               sync.RWMutex
	membership       *Membership
	bodyID           string
	transportFactory func() (Transport, error)
	clientFactory    func(Transport) clientRunner
	// reconnectDelay is the base delay between reconnect attempts.
	reconnectDelay time.Duration
	// maxReconnectDelay is the maximum delay between reconnect attempts.
	maxReconnectDelay time.Duration
}

// NewMobilityManager returns a new MobilityManager. The transportFactory
// must be provided by the caller; in tests it can return a working
// transport (e.g., a fake transport). In production, if the factory
// cannot create a transport due to missing endpoint information,
// it should return an error, which will be treated as a transient
// failure and trigger a reconnect attempt after a delay.
func NewMobilityManager(membership *Membership, bodyID string,
	tf func() (Transport, error), cf func(Transport) clientRunner) *MobilityManager {
	if tf == nil {
		panic("transport factory must not be nil")
	}
	if cf == nil {
		panic("client factory must not be nil")
	}
	return &MobilityManager{
		membership:        membership,
		bodyID:            bodyID,
		transportFactory:  tf,
		clientFactory:     cf,
		reconnectDelay:    time.Second,
		maxReconnectDelay: time.Minute,
	}
}

// Run starts the mobility manager. It loops until ctx is cancelled,
// creating transports and clients as needed. On transport failure,
// it closes the current transport and attempts to create a new one
// after a delay. It preserves the membership and bodyID across
// reconnect cycles.
func (m *MobilityManager) Run(ctx context.Context) error {
	var backoff time.Duration = m.reconnectDelay
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Obtain a transport, retrying with backoff on failure.
		transport, err := m.transportFactory()
		if err != nil {
			// Factory failed (e.g., no endpoint info). Wait and retry.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			// Exponential backoff with jitter avoided for determinism.
			if backoff < m.maxReconnectDelay {
				backoff *= 2
				if backoff > m.maxReconnectDelay {
					backoff = m.maxReconnectDelay
				}
			}
			continue
		}
		// Transport created successfully; reset backoff.
		backoff = m.reconnectDelay

		// Create client for this transport.
		client := m.clientFactory(transport)

		// Channel to receive the result of client.Run.
		clientErrChan := make(chan error, 1)
		go func() {
			clientErrChan <- client.Run(ctx)
		}()

		// Wait for client result or cancellation.
		select {
		case <-ctx.Done():
			// Cancellation: close transport and exit.
			_ = transport.Close()
			return ctx.Err()
		case err := <-clientErrChan:
			// Client exited. Close its transport.
			_ = transport.Close()
			if errors.Is(err, context.Canceled) {
				// Should not happen because we already checked ctx.Done, but handle.
				return err
			}
			// Client failed with non-cancellation error: treat as transport
			// failure and retry after backoff.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			if backoff < m.maxReconnectDelay {
				backoff *= 2
				if backoff > m.maxReconnectDelay {
					backoff = m.maxReconnectDelay
				}
			}
			// Loop to obtain a new transport.
		}
	}
}
