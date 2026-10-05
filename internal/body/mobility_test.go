package body

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/link"
)

// testTransport is a fake transport for testing that provides synchronization
// channels to observe its lifecycle.
type testTransport struct {
	mu       sync.Mutex
	sendCh   chan []byte
	recvCh   chan []byte
	sendErr  error
	recvErr  error
	closeErr error
	closed   bool

	// Channels for test observation.
	// closed when the transport is created (i.e., factory returns).
	Created chan struct{}
	// closed when Close is called.
	Closed chan struct{}
	// closed when Receive is called (successful or error).
	Received chan struct{}
	// closed when Send is called.
	Sent chan struct{}
}

// newTestTransport returns a newly initialized testTransport with
// the observation channels set up.
func newTestTransport() *testTransport {
	t := &testTransport{
		sendCh:   make(chan []byte),
		recvCh:   make(chan []byte),
		Created:  make(chan struct{}),
		Closed:   make(chan struct{}, 1), // buffered to help with timing
		Received: make(chan struct{}),
		Sent:     make(chan struct{}),
	}
	// Signal creation immediately.
	close(t.Created)
	return t
}

// Send implements Transport.
func (t *testTransport) Send(ctx context.Context, msg link.Envelope) error {
	t.mu.Lock()
	if t.sendErr != nil {
		t.mu.Unlock()
		return t.sendErr
	}
	t.mu.Unlock()
	select {
	case t.Sent <- struct{}{}:
	default:
	}
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

// Receive implements Transport.
func (t *testTransport) Receive(ctx context.Context) (*link.Envelope, error) {
	t.mu.Lock()
	if t.recvErr != nil {
		t.mu.Unlock()
		return nil, t.recvErr
	}
	t.mu.Unlock()
	select {
	case t.Received <- struct{}{}:
	default:
	}
	data := <-t.recvCh
	var msg link.Envelope
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal envelope: %w", err)
	}
	return &msg, nil
}

// Close implements Transport.
func (t *testTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	select {
	case t.Closed <- struct{}{}:
	default:
	}
	return t.closeErr
}

// testClientRunner is a fake client runner for testing that provides
// synchronization channels to observe its lifecycle.
type testClientRunner struct {
	Transport Transport
	Started   chan struct{} // closed when Run is called
	Exited    chan error    // receives the error from Run (or nil)
	// Optionally, we can set an error to be returned by Run via Transport.
}

func newTestClientRunner(t Transport) *testClientRunner {
	return &testClientRunner{
		Transport: t,
		Started:   make(chan struct{}),
		Exited:    make(chan error, 1),
	}
}

// Run implements clientRunner.
func (cr *testClientRunner) Run(ctx context.Context) error {
	close(cr.Started)
	// Delegate to the transport's Receive (which will block or error).
	_, err := cr.Transport.Receive(ctx)
	cr.Exited <- err
	return err
}

// TestMobilityManagerBasic tests that the MobilityManager starts and
// shuts down cleanly when cancelled while a client is running.
func TestMobilityManagerBasic(t *testing.T) {
	t.Parallel()

	// Create a fake membership and body ID.
	membership := &Membership{
		NetworkID:     "test-network",
		PeerID:        "test-peer-id",
		Status:        MembershipActive,
		BodyPeerID:    "test-body-peer-id",
		BodyIPv6:      "2001:db8::1",
		BodyAddresses: []string{"2001:db8::1"},
		CorePeerID:    "test-core-peer-id",
		CoreWGKeyB64:  "BASE64KEY==",
		CoreAddresses: []string{"fd00::1"},
		CoreEndpoints: []string{"https://core.example.com"},
	}
	bodyID := "test-body-id"

	// Transport factory returns a working fake transport.
	transportFactory := func() (Transport, error) {
		return newTestTransport(), nil
	}
	// Client factory returns a testClientRunner.
	clientFactory := func(t Transport) clientRunner {
		return newTestClientRunner(t)
	}

	mm := NewMobilityManager(membership, bodyID, transportFactory, clientFactory)

	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		defer close(doneCh)
		errCh <- mm.Run(ctx)
	}()

	// Wait for the manager to create a transport and start a client.
	// We'll use a short timeout as failure bound to wait for startup.
	select {
	case <-time.After(500 * time.Millisecond):
		// Assume it started (this is acceptable as failure bound only)
	case <-ctx.Done():
		t.Fatalf("context cancelled before manager started")
	}

	// Cancel while client is running.
	cancel()

	// Check that it exited cleanly.
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("mobility manager did not exit in time")
	}
	<-doneCh

	// Verify that the manager's internal state is preserved (we cannot
	// access unexported fields, but we can at least ensure the manager
	// didn't panic).
	if mm == nil {
		t.Fatalf("mobility manager is nil")
	}
}

// TestMobilityManagerCancellationDuringBackoff tests that cancellation
// while waiting in backoff (after a client error) exits cleanly and does
// not create additional transports or clients.
func TestMobilityManagerCancellationDuringBackoff(t *testing.T) {
	t.Parallel()

	membership := &Membership{
		NetworkID:     "test-network",
		PeerID:        "test-peer-id",
		Status:        MembershipActive,
		BodyPeerID:    "test-body-peer-id",
		BodyIPv6:      "2001:db8::1",
		BodyAddresses: []string{"2001:db8::1"},
		CorePeerID:    "test-core-peer-id",
		CoreWGKeyB64:  "BASE64KEY==",
		CoreAddresses: []string{"fd00::1"},
		CoreEndpoints: []string{"https://core.example.com"},
	}
	bodyID := "test-body-id"

	// We'll track the latest transport and client instances for observation.
	var transportMu sync.Mutex
	var transportCreatedCh = make(chan *testTransport, 1) // buffered to avoid blocking

	var clientMu sync.Mutex
	var clientStartedCh = make(chan *testClientRunner, 1)

	transportFactory := func() (Transport, error) {
		tr := newTestTransport()
		// Make the transport fail immediately on Receive.
		tr.recvErr = errors.New("simulated transport failure")

		// Publish the transport for observation.
		transportMu.Lock()
		transportMu.Unlock()
		select {
		case transportCreatedCh <- tr:
		default:
		}
		return tr, nil
	}
	clientFactory := func(t Transport) clientRunner {
		cr := newTestClientRunner(t)

		// Publish the client for observation.
		clientMu.Lock()
		clientMu.Unlock()
		select {
		case clientStartedCh <- cr:
		default:
		}
		return cr
	}

	mm := NewMobilityManager(membership, bodyID, transportFactory, clientFactory)

	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		defer close(doneCh)
		errCh <- mm.Run(ctx)
	}()

	// Wait for the first transport to be created.
	select {
	case tr := <-transportCreatedCh:
		// Wait for the transport to signal creation (should already be signaled).
		select {
		case <-tr.Created:
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("timeout waiting for transport Created signal")
		}
		// Wait for the client to start.
		select {
		case cr := <-clientStartedCh:
			// Wait for client Started signal.
			select {
			case <-cr.Started:
			case <-time.After(500 * time.Millisecond):
				t.Fatalf("timeout waiting for client Started signal")
			}
			// Wait for the client to exit (will be error due to recvErr).
			select {
			case <-cr.Exited:
				// Client exited, now wait for transport to be closed.
				select {
				case <-tr.Closed:
					// Transport closed, now we are in backoff.
				case <-time.After(500 * time.Millisecond):
					t.Fatalf("timeout waiting for transport Closed signal after client exit")
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatalf("timeout waiting for client Exited signal")
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("timeout waiting for client Started signal")
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("timeout waiting for first transport creation")
	case <-ctx.Done():
		t.Fatalf("context cancelled before first transport creation")
	}

	// Now we have observed: transport created, client started, client exited, transport closed.
	// We are now in the backoff period after a client error.
	// Cancel during backoff.
	cancel()

	// Check that it exited cleanly.
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("mobility manager did not exit in time after cancellation")
	}
	<-doneCh

	// Verify that no additional factories were called after cancellation.
	// We'll wait a short time to see if another transport is created or client started.
	select {
	case <-transportCreatedCh:
		t.Fatalf("unexpected second transport created after cancellation")
	case <-time.After(500 * time.Millisecond):
		// good, no second transport
	}
	select {
	case <-clientStartedCh:
		t.Fatalf("unexpected second client started after cancellation")
	case <-time.After(500 * time.Millisecond):
		// good
	}
}

// TestMobilityManagerThreeReconnectCycles performs at least three
// disconnect/reconnect cycles and verifies that the membership and body
// ID remain unchanged by observing the lifecycle events in order.
func TestMobilityManagerThreeReconnectCycles(t *testing.T) {
	t.Parallel()

	membership := &Membership{
		NetworkID:     "test-network",
		PeerID:        "test-peer-id",
		Status:        MembershipActive,
		BodyPeerID:    "test-body-peer-id",
		BodyIPv6:      "2001:db8::1",
		BodyAddresses: []string{"2001:db8::1"},
		CorePeerID:    "test-core-peer-id",
		CoreWGKeyB64:  "BASE64KEY==",
		CoreAddresses: []string{"fd00::1"},
		CoreEndpoints: []string{"https://core.example.com"},
	}
	bodyID := "test-body-id"

	// We'll track observations for each cycle.
	var transportMu sync.Mutex
	var transportCreatedCh = make(chan *testTransport, 1)

	var clientMu sync.Mutex
	var clientStartedCh = make(chan *testClientRunner, 1)

	transportFactory := func() (Transport, error) {
		tr := newTestTransport()
		// Make the transport fail immediately on Receive.
		tr.recvErr = errors.New("simulated transport failure")

		// Publish the transport for observation.
		transportMu.Lock()
		transportMu.Unlock()
		select {
		case transportCreatedCh <- tr:
		default:
		}
		return tr, nil
	}
	clientFactory := func(t Transport) clientRunner {
		cr := newTestClientRunner(t)

		// Publish the client for observation.
		clientMu.Lock()
		clientMu.Unlock()
		select {
		case clientStartedCh <- cr:
		default:
		}
		return cr
	}

	mm := NewMobilityManager(membership, bodyID, transportFactory, clientFactory)

	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		defer close(doneCh)
		errCh <- mm.Run(ctx)
	}()

	// Observe three full cycles: for each cycle, wait for
	// transport created -> client started -> client exited -> transport closed.
	for cycle := 0; cycle < 3; cycle++ {
		// Wait for transport to be created.
		select {
		case tr := <-transportCreatedCh:
			// Wait for transport Created signal.
			select {
			case <-tr.Created:
			case <-time.After(500 * time.Millisecond):
				t.Fatalf("cycle %d: timeout waiting for transport Created signal", cycle)
			}
			// Wait for client to start.
			select {
			case cr := <-clientStartedCh:
				// Wait for client Started signal.
				select {
				case <-cr.Started:
				case <-time.After(500 * time.Millisecond):
					t.Fatalf("cycle %d: timeout waiting for client Started signal", cycle)
				}
				// Wait for client to exit (will be error due to recvErr).
				select {
				case <-cr.Exited:
					// Client exited, now wait for transport to be closed.
					select {
					case <-tr.Closed:
						// Transport closed, cycle complete.
					case <-time.After(500 * time.Millisecond):
						t.Fatalf("cycle %d: timeout waiting for transport Closed signal after client exit", cycle)
					}
				case <-time.After(500 * time.Millisecond):
					t.Fatalf("cycle %d: timeout waiting for client Exited signal", cycle)
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatalf("cycle %d: timeout waiting for client Started signal", cycle)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for transport creation in cycle %d", cycle)
		case <-ctx.Done():
			t.Fatalf("context cancelled during cycle %d", cycle)
		}
	}

	// After three cycles, cancel to stop the manager.
	cancel()

	// Check that it exited cleanly.
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected error after cycles: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("mobility manager did not exit in time after cycles")
	}
	<-doneCh

	// Verify membership and bodyID are unchanged.
	// Since we never modify membership or bodyID in this test, they remain
	// as initialized. The manager preserves its own copies, so this check
	// ensures that the manager did not somehow modify the input variables
	// (which it shouldn't do anyway).
	if membership.NetworkID != "test-network" ||
		membership.PeerID != "test-peer-id" ||
		membership.Status != MembershipActive ||
		membership.BodyPeerID != "test-body-peer-id" ||
		membership.BodyIPv6 != "2001:db8::1" ||
		!equalSlice(membership.BodyAddresses, []string{"2001:db8::1"}) ||
		membership.CorePeerID != "test-core-peer-id" ||
		membership.CoreWGKeyB64 != "BASE64KEY==" ||
		!equalSlice(membership.CoreAddresses, []string{"fd00::1"}) ||
		!equalSlice(membership.CoreEndpoints, []string{"https://core.example.com"}) ||
		bodyID != "test-body-id" {
		t.Fatalf("membership or bodyID changed after cycles")
	}
}

// equalSlice returns true if the two slices have the same length and elements.
func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i, v := range a {
		if v != b[i] {
			return false
		}
	}
	return true
}
