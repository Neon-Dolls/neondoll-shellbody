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
	mu            sync.RWMutex
	membership    *Membership
	bodyID        string
	transportFactory func() (Transport, error)
	clientFactory   func(Transport) clientRunner
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
		membership:    membership,
		bodyID:        bodyID,
		transportFactory: tf,
		clientFactory:  cf,
		reconnectDelay: time.Second,
		maxReconnectDelay: time.Minute,
	}
}

// Run starts the mobility manager. It loops until ctx is cancelled,
// creating transports and clients as needed. On transport failure,
// it closes the current transport and attempts to create a new one
// after a delay. It preserves the membership and bodyID across
// reconnect cycles.
func (m *MobilityManager) Run(ctx context.Context) error {
	var (
		currentTransport Transport
		currentClient    clientRunner
		clientDone       chan struct{}
		clientErr        chan error
		backoff          = m.reconnectDelay
	)

	for {
		// Check for cancellation before starting a new attempt.
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// If we have a current client, wait for it to exit.
		if currentClient != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case err := <-clientErr:
				// Client exited. Close the transport and reset.
				if currentTransport != nil {
					_ = currentTransport.Close()
					currentTransport = nil
				}
				currentClient = nil
				clientDone = nil
				clientErr = nil

				// If the error is due to context cancellation, we should exit.
				if errors.Is(err, context.Canceled) {
					return err
				}
				// Otherwise, treat as transport failure and retry.
				// We will continue to the reconnect logic below.
			case <-clientDone:
				// Client exited without error (unlikely, but handle).
				if currentTransport != nil {
					_ = currentTransport.Close()
					currentTransport = nil
				}
				currentClient = nil
				clientDone = nil
				clientErr = nil
				// Continue to reconnect.
			}
		}

		// If we don't have a current client, try to create a new transport.
		if currentTransport == nil {
			transport, err := m.transportFactory()
			if err != nil {
				// Factory failed (e.g., no endpoint info). Wait and retry.
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(backoff):
				}
				// Exponential backoff with jitter to avoid thundering herd.
				if backoff < m.maxReconnectDelay {
					backoff = backoff * 2
					if backoff > m.maxReconnectDelay {
						backoff = m.maxReconnectDelay
					}
				}
				continue
			}
			currentTransport = transport
			// Reset backoff on successful transport creation.
			backoff = m.reconnectDelay
		}

		// Now we have a transport, create the client.
		if currentClient == nil {
			currentClient = m.clientFactory(currentTransport)
			clientDone = make(chan struct{})
			clientErr = make(chan error, 1)
			go func() {
				defer close(clientDone)
				clientErr <- currentClient.Run(ctx)
			}()
		}
	}
}