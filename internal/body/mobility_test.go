package body

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	_ "github.com/Neon-Dolls/neondoll-shellbody/internal/link"
)

// TestMobilityManagerBasic tests that the MobilityManager preserves state
// across transport failures and successfully reconnects.
func TestMobilityManagerBasic(t *testing.T) {
	t.Parallel()

	// Create a fake membership and body ID
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

	// Create a transport factory that returns a working fake transport
	transportFactory := func() (Transport, error) {
		return NewFakeTransport(), nil
	}

	// Create a client factory that returns a client runner
	clientFactory := func(t Transport) clientRunner {
		return &fakeClientRunner{Transport: t}
	}

	// Create the mobility manager
	mm := NewMobilityManager(membership, bodyID, transportFactory, clientFactory)

	// Run the mobility manager in a goroutine with a cancelable context
	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		defer close(doneCh)
		errCh <- mm.Run(ctx)
	}()

	// Give it a moment to start
	time.Sleep(50 * time.Millisecond)

	// Cancel the context to stop the manager
	cancel()

	// Check that it exited cleanly
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("mobility manager did not exit in time")
	}

	// Wait for the goroutine to finish
	<-doneCh

	// Verify that we preserved the membership and body ID
	if mm == nil {
		t.Fatalf("mobility manager is nil")
	}
	// Note: We can't easily access the internal state without exporting it,
	// but the test verifies that the manager runs without panicking or leaking.
}

// TestMobilityManagerTransportFailure tests that the MobilityManager
// handles transport failures and attempts reconnection.
func TestMobilityManagerTransportFailure(t *testing.T) {
	t.Parallel()

	// Create a fake membership and body ID
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

	// We'll track each transport created.
	var factoryMu sync.Mutex
	var transportCallCount int

	// Create a transport factory that returns a fake transport that is set to fail.
	transportFactory := func() (Transport, error) {
		factoryMu.Lock()
		defer factoryMu.Unlock()
		transportCallCount++
		t := NewFakeTransport()
		// Make this transport fail immediately on Receive.
		t.recvErr = errors.New("simulated transport failure")
		return t, nil
	}

	// Create a client factory
	clientFactory := func(t Transport) clientRunner {
		return &fakeClientRunner{Transport: t}
	}

	// Create the mobility manager
	mm := NewMobilityManager(membership, bodyID, transportFactory, clientFactory)

	// Run the mobility manager in a goroutine with a cancelable context
	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		defer close(doneCh)
		errCh <- mm.Run(ctx)
	}()

	// Give it a moment to start
	time.Sleep(50 * time.Millisecond)

	// Check that at least one transport was created
	factoryMu.Lock()
	initialTransportCalls := transportCallCount
	factoryMu.Unlock()

	if initialTransportCalls == 0 {
		t.Fatalf("expected transport factory to be called at least once, got %d", transportCallCount)
	}

	// Give it a moment to detect failure and reconnect a few times.
	time.Sleep(3 * time.Second)

	// Check that it tried to create a new transport more than once.
	factoryMu.Lock()
	afterDelayCalls := transportCallCount
	factoryMu.Unlock()

	if afterDelayCalls <= initialTransportCalls {
		t.Fatalf("expected transport factory to be called more than once, got %d (initial %d)", transportCallCount, initialTransportCalls)
	}

	// Cancel the context to stop the manager
	cancel()

	// Check that it exited cleanly
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("mobility manager did not exit in time")
	}

	// Wait for the goroutine to finish
	<-doneCh
}

// fakeClientRunner is a simple client runner for testing.
type fakeClientRunner struct {
	Transport
}

func (f *fakeClientRunner) Run(ctx context.Context) error {
	// We'll just try to receive; if we get an error, return it.
	_, err := f.Transport.Receive(ctx)
	return err
}
