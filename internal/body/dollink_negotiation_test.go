// Tests for Doll Link version negotiation.
package body

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/link"
)

// TestClientNegotiation tests that version negotiation works correctly.
func TestClientNegotiation(t *testing.T) {
	ft := NewFakeTransport()
	client := NewClient(ft, "test-body-id")

	errc := make(chan error, 1)
	go func() {
		// 1. Receive body.hello from client (as JSON bytes). The client sends it first.
		data := <-ft.sendCh
		var bodyHello link.Envelope
		if err := json.Unmarshal(data, &bodyHello); err != nil {
			errc <- fmt.Errorf("failed to unmarshal body.hello: %v", err)
			return
		}
		if bodyHello.Type != link.TypeBodyHello {
			errc <- fmt.Errorf("expected body.hello, got %s", bodyHello.Type)
			return
		}
		// 2. Send core.hello back to the client (the client Receive reads recvCh).
		marshaled, err := json.Marshal(map[string]interface{}{"doll_link_version": 1, "body_contract_version": 1})
		if err != nil {
			errc <- fmt.Errorf("failed to marshal core.hello: %v", err)
			return
		}
		coreHello := link.Envelope{
			Type:      link.TypeCoreHello,
			ID:        "evt_core",
			BodyID:    "test-body-id",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Payload:   marshaled,
		}
		coreHelloData, err := json.Marshal(coreHello)
		if err != nil {
			errc <- fmt.Errorf("failed to marshal core.hello: %v", err)
			return
		}
		ft.recvCh <- coreHelloData
		errc <- nil
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clientDone := make(chan error, 1)
	go func() {
		clientDone <- client.negotiateVersions(ctx)
	}()

	// The peer and the client run concurrently and synchronize through the
	// unbuffered channels. Wait for the peer to complete the handshake.
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("%v", err)
		}
	case <-time.After(time.Second * 5):
		t.Fatalf("timed out waiting for peer")
	}

	select {
	case err := <-clientDone:
		if err != nil {
			t.Fatalf("version negotiation failed: %v", err)
		}
	case <-time.After(time.Second * 5):
		t.Fatalf("client negotiation timed out")
	}

	// Check that versions were negotiated correctly.
	if client.negotiatedDollLinkVersion() != 1 {
		t.Fatalf("expected doll link version 1, got %d", client.negotiatedDollLinkVersion())
	}
	if client.negotiatedBodyContractVersion() != 1 {
		t.Fatalf("expected body contract version 1, got %d", client.negotiatedBodyContractVersion())
	}
}
