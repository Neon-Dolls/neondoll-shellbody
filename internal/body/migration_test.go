package body

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/identity"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/link"
)

// TestMobilityManagerMigration tests that the durable relationship (identity,
// WG keypair, and membership) survives restart and can be used to reconstruct
// the Body side of its connection via the MobilityManager.
func TestMobilityManagerMigration(t *testing.T) {
	t.Parallel()

	// Set up initial state directory
	initialDir := t.TempDir()
	initialStore, err := NewStore(initialDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// Create fresh identity and WG keypair (simulates first-time setup)
	meta := identity.BodyMetadata{
		Implementation: "neondoll-shellbody",
		Platform:       "test",
		Architecture:   "test",
		Build:          "test",
	}
	res, err := initialStore.CreateFresh("test-body", meta)
	if err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}
	initialBodyID := string(res.State.Identity.BodyID)
	initialWGPub := res.Key.PublicKeyBase64()

	// Create and persist a membership (simulates successful pairing)
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
	if err := initialStore.SaveMembership(membership); err != nil {
		t.Fatalf("SaveMembership: %v", err)
	}

	// Verify initial state is correctly persisted
	ident, kp, err := initialStore.LoadOrError()
	if err != nil {
		t.Fatalf("LoadOrError: %v", err)
	}
	if string(ident.Identity.BodyID) != initialBodyID {
		t.Fatalf("BodyID mismatch: expected %q, got %q", initialBodyID, string(ident.Identity.BodyID))
	}
	if kp.PublicKeyBase64() != initialWGPub {
		t.Fatalf("WG public key mismatch: expected %q, got %q", initialWGPub, kp.PublicKeyBase64())
	}
	loadedMem, err := initialStore.LoadMembership()
	if err != nil {
		t.Fatalf("LoadMembership: %v", err)
	}
	if loadedMem.NetworkID != membership.NetworkID ||
		loadedMem.BodyPeerID != membership.BodyPeerID ||
		loadedMem.BodyIPv6 != membership.BodyIPv6 ||
		loadedMem.CorePeerID != membership.CorePeerID ||
		loadedMem.CoreWGKeyB64 != membership.CoreWGKeyB64 {
		t.Fatalf("Membership mismatch: expected %+v, got %+v", membership, loadedMem)
	}

	// Simulate restart: new store pointing to same directory
	restartStore, err := NewStore(initialDir)
	if err != nil {
		t.Fatalf("NewStore (restart): %v", err)
	}

	// Verify restarted state matches initial state
	restartIdent, restartKP, err := restartStore.LoadOrError()
	if err != nil {
		t.Fatalf("LoadOrError (restart): %v", err)
	}
	if string(restartIdent.Identity.BodyID) != initialBodyID {
		t.Fatalf("BodyID changed after restart: expected %q, got %q", initialBodyID, string(restartIdent.Identity.BodyID))
	}
	if restartKP.PublicKeyBase64() != initialWGPub {
		t.Fatalf("WG public key changed after restart: expected %q, got %q", initialWGPub, restartKP.PublicKeyBase64())
	}
	restartMem, err := restartStore.LoadMembership()
	if err != nil {
		t.Fatalf("LoadMembership (restart): %v", err)
	}
	if restartMem.NetworkID != membership.NetworkID ||
		restartMem.BodyPeerID != membership.BodyPeerID ||
		restartMem.BodyIPv6 != membership.BodyIPv6 ||
		restartMem.CorePeerID != membership.CorePeerID ||
		restartMem.CoreWGKeyB64 != membership.CoreWGKeyB64 {
		t.Fatalf("Membership changed after restart: expected %+v, got %+v", membership, restartMem)
	}

	// Set up test harness to observe MobilityManager behavior
	var transportMu sync.Mutex
	var transportCreatedCh = make(chan *testTransportMig, 1)
	var clientMu sync.Mutex
	var clientStartedCh = make(chan *testClientRunnerMig, 1)

	transportFactory := func() (Transport, error) {
		tr := newTestTransportMig()
		// Make transport fail immediately to trigger reconnect logic
		tr.recvErr = errors.New("simulated transport failure")

		transportMu.Lock()
		transportMu.Unlock()
		select {
		case transportCreatedCh <- tr:
		default:
		}
		return tr, nil
	}

	clientFactory := func(t Transport) clientRunner {
		cr := newTestClientRunnerMig(t)

		clientMu.Lock()
		clientMu.Unlock()
		select {
		case clientStartedCh <- cr:
		default:
		}
		return cr
	}

	// Test 1: MobilityManager with initial state works
	initialCtx, initialCancel := context.WithCancel(context.Background())
	initialDone := make(chan struct{})
	initialErrs := make(chan error, 1)
	go func() {
		defer close(initialDone)
		initialErrs <- NewMobilityManager(restartMem, initialBodyID, transportFactory, clientFactory).Run(initialCtx)
	}()

	// Wait for first transport and client to be created
	select {
	case tr := <-transportCreatedCh:
		<-tr.Created // wait for Created signal
		select {
		case cr := <-clientStartedCh:
			<-cr.Started // wait for Started signal
			// Client will exit quickly due to recvErr
			select {
			case <-cr.Exited:
			case <-time.After(500 * time.Millisecond):
				t.Fatalf("timeout waiting for client Exited signal")
			}
			// Wait for transport to be closed after client exit
			select {
			case <-tr.Closed:
			case <-time.After(500 * time.Millisecond):
				t.Fatalf("timeout waiting for transport Closed signal")
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("timeout waiting for client Started signal")
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("timeout waiting for transport creation")
	case <-initialCtx.Done():
		t.Fatalf("context cancelled before manager started")
	}

	// Cancel and verify clean shutdown
	initialCancel()
	select {
	case err := <-initialErrs:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("mobility manager did not exit in time")
	}
	<-initialDone

	// Test 2: MobilityManager with restarted state works identically
	restartCtx, restartCancel := context.WithCancel(context.Background())
	restartDone := make(chan struct{})
	restartErrs := make(chan error, 1)
	go func() {
		defer close(restartDone)
		restartErrs <- NewMobilityManager(restartMem, initialBodyID, transportFactory, clientFactory).Run(restartCtx)
	}()

	// Wait for first transport and client to be created (should work same as before)
	select {
	case tr := <-transportCreatedCh:
		<-tr.Created // wait for Created signal
		select {
		case cr := <-clientStartedCh:
			<-cr.Started // wait for Started signal
			// Client will exit quickly due to recvErr
			select {
			case <-cr.Exited:
			case <-time.After(500 * time.Millisecond):
				t.Fatalf("timeout waiting for client Exited signal")
			}
			// Wait for transport to be closed after client exit
			select {
			case <-tr.Closed:
			case <-time.After(500 * time.Millisecond):
				t.Fatalf("timeout waiting for transport Closed signal")
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("timeout waiting for client Started signal")
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("timeout waiting for transport creation")
	case <-restartCtx.Done():
		t.Fatalf("context cancelled before manager started")
	}

	// Cancel and verify clean shutdown
	restartCancel()
	select {
	case err := <-restartErrs:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("mobility manager did not exit in time")
	}
	<-restartDone

	// Final verification: state is unchanged after both tests
	finalIdent, finalKP, err := restartStore.LoadOrError()
	if err != nil {
		t.Fatalf("final LoadOrError: %v", err)
	}
	if string(finalIdent.Identity.BodyID) != initialBodyID {
		t.Fatalf("BodyID changed after tests: expected %q, got %q", initialBodyID, string(finalIdent.Identity.BodyID))
	}
	if finalKP.PublicKeyBase64() != initialWGPub {
		t.Fatalf("WG public key changed after tests: expected %q, got %q", initialWGPub, finalKP.PublicKeyBase64())
	}
	finalMem, err := restartStore.LoadMembership()
	if err != nil {
		t.Fatalf("final LoadMembership: %v", err)
	}
	if finalMem.NetworkID != membership.NetworkID ||
		finalMem.BodyPeerID != membership.BodyPeerID ||
		finalMem.BodyIPv6 != membership.BodyIPv6 ||
		finalMem.CorePeerID != membership.CorePeerID ||
		finalMem.CoreWGKeyB64 != membership.CoreWGKeyB64 {
		t.Fatalf("Membership changed after tests: expected %+v, got %+v", membership, finalMem)
	}
}

// Helper types for mobility test observation (names suffixed to avoid duplication with mobility_test.go)
type testTransportMig struct {
	mu       sync.Mutex
	sendCh   chan []byte
	recvCh   chan []byte
	sendErr  error
	recvErr  error
	closeErr error
	closed   bool

	// Channels for test observation.
	Created  chan struct{}
	Closed   chan struct{}
	Received chan struct{}
	Sent     chan struct{}
}

func newTestTransportMig() *testTransportMig {
	t := &testTransportMig{
		sendCh:   make(chan []byte),
		recvCh:   make(chan []byte),
		Created:  make(chan struct{}),
		Closed:   make(chan struct{}, 1),
		Received: make(chan struct{}),
		Sent:     make(chan struct{}),
	}
	close(t.Created)
	return t
}

func (t *testTransportMig) Send(ctx context.Context, msg link.Envelope) error {
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

func (t *testTransportMig) Receive(ctx context.Context) (*link.Envelope, error) {
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

func (t *testTransportMig) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	select {
	case t.Closed <- struct{}{}:
	default:
	}
	return t.closeErr
}

type testClientRunnerMig struct {
	Transport Transport
	Started   chan struct{}
	Exited    chan error
}

func newTestClientRunnerMig(t Transport) *testClientRunnerMig {
	return &testClientRunnerMig{
		Transport: t,
		Started:   make(chan struct{}),
		Exited:    make(chan error, 1),
	}
}

func (cr *testClientRunnerMig) Run(ctx context.Context) error {
	close(cr.Started)
	_, err := cr.Transport.Receive(ctx)
	cr.Exited <- err
	return err
}
