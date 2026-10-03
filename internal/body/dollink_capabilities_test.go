// Tests for Doll Link capability advertisement and body.ready.
package body

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/link"
)

// TestClientCapabilities tests that capabilities are advertised.
func TestClientCapabilities(t *testing.T) {
	ft := NewFakeTransport()
	client := NewClient(ft, "test-body-id")

	errc := make(chan error, 1)
	go func() {
		// 1. body.hello
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
		// 2. core.hello
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

		// 3. body.capabilities
		data = <-ft.sendCh
		var bodyCap link.Envelope
		if err := json.Unmarshal(data, &bodyCap); err != nil {
			errc <- fmt.Errorf("failed to unmarshal body.capabilities: %v", err)
			return
		}
		if bodyCap.Type != link.TypeBodyCapabilities {
			errc <- fmt.Errorf("expected body.capabilities, got %s", bodyCap.Type)
			return
		}
		var capResp link.CapabilitiesPayload
		if err := json.Unmarshal(bodyCap.Payload, &capResp); err != nil {
			errc <- fmt.Errorf("failed to unmarshal capabilities payload: %v", err)
			return
		}
		if len(capResp.Capabilities) == 0 {
			errc <- fmt.Errorf("expected at least one capability")
			return
		}
		if capResp.Capabilities[0].ID != "terminal" {
			errc <- fmt.Errorf("expected capability ID \"terminal\", got %s", capResp.Capabilities[0].ID)
			return
		}
		var hasInput, hasOutput bool
		for _, op := range capResp.Capabilities[0].Operations {
			switch op {
			case "input":
				hasInput = true
			case "output":
				hasOutput = true
			}
		}
		if !hasInput || !hasOutput {
			errc <- fmt.Errorf("expected operations to include \"input\" and \"output\", got %v", capResp.Capabilities[0].Operations)
			return
		}
		if !capResp.Capabilities[0].Available {
			errc <- fmt.Errorf("expected capability to be available")
			return
		}

		// 4. body.ready
		data = <-ft.sendCh
		var bodyReady link.Envelope
		if err := json.Unmarshal(data, &bodyReady); err != nil {
			errc <- fmt.Errorf("failed to unmarshal body.ready: %v", err)
			return
		}
		if bodyReady.Type != link.TypeBodyReady {
			errc <- fmt.Errorf("expected body.ready, got %s", bodyReady.Type)
			return
		}

		errc <- nil
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clientDone := make(chan error, 1)
	go func() {
		clientDone <- client.Run(ctx)
	}()

	// The peer consumes the negotiation/capabilities/ready sequence. Once the
	// peer is satisfied, cancel the context to stop the client's receive loop.
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("%v", err)
		}
	case <-time.After(time.Second * 5):
		t.Fatalf("timed out waiting for peer")
	}
	cancel()
	select {
	case err := <-clientDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("client run failed: %v", err)
		}
	case <-time.After(time.Second * 3):
		t.Fatalf("client run did not return after cancel")
	}
}
