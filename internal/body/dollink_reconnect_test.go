// Tests for Doll Link reconnect behavior and Body identity preservation.
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

// TestClientReconnectionSameBodyID tests that reconnecting with the same Body ID works.
func TestClientReconnectionSameBodyID(t *testing.T) {
	// First connection
	ft1 := NewFakeTransport()
	client1 := NewClient(ft1, "reconnect-body-id")

	errc := make(chan error, 1)
	go func() {
		// 1. body.hello
		data := <-ft1.sendCh
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
			ID:        "evt_core1",
			BodyID:    "reconnect-body-id",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Payload:   marshaled,
		}
		coreHelloData, err := json.Marshal(coreHello)
		if err != nil {
			errc <- fmt.Errorf("failed to marshal core.hello: %v", err)
			return
		}
		ft1.recvCh <- coreHelloData
		// 3. body.capabilities
		data = <-ft1.sendCh
		var bodyCap link.Envelope
		if err := json.Unmarshal(data, &bodyCap); err != nil {
			errc <- fmt.Errorf("failed to unmarshal body.capabilities: %v", err)
			return
		}
		if bodyCap.Type != link.TypeBodyCapabilities {
			errc <- fmt.Errorf("expected body.capabilities, got %s", bodyCap.Type)
			return
		}
		// 4. body.ready
		data = <-ft1.sendCh
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

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	done1 := make(chan error, 1)
	go func() {
		done1 <- client1.Run(ctx1)
	}()

	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("%v", err)
		}
	case <-time.After(time.Second * 5):
		t.Fatalf("timed out waiting for peer")
	}
	cancel1()
	select {
	case err := <-done1:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("first client run failed: %v", err)
		}
	case <-time.After(time.Second * 3):
		t.Fatal("first client run did not return after cancel")
	}

	if !client1.isReady() {
		t.Fatal("expected first client to be ready")
	}

	// Second connection (reconnect) with same Body ID
	ft2 := NewFakeTransport()
	client2 := NewClient(ft2, "reconnect-body-id") // Same Body ID

	errc = make(chan error, 1)
	go func() {
		// 1. body.hello
		data := <-ft2.sendCh
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
			ID:        "evt_core2",
			BodyID:    "reconnect-body-id",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Payload:   marshaled,
		}
		coreHelloData, err := json.Marshal(coreHello)
		if err != nil {
			errc <- fmt.Errorf("failed to marshal core.hello: %v", err)
			return
		}
		ft2.recvCh <- coreHelloData
		// 3. body.capabilities
		data = <-ft2.sendCh
		var bodyCap link.Envelope
		if err := json.Unmarshal(data, &bodyCap); err != nil {
			errc <- fmt.Errorf("failed to unmarshal body.capabilities: %v", err)
			return
		}
		if bodyCap.Type != link.TypeBodyCapabilities {
			errc <- fmt.Errorf("expected body.capabilities, got %s", bodyCap.Type)
			return
		}
		// 4. body.ready
		data = <-ft2.sendCh
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

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	done2 := make(chan error, 1)
	go func() {
		done2 <- client2.Run(ctx2)
	}()

	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("%v", err)
		}
	case <-time.After(time.Second * 5):
		t.Fatalf("timed out waiting for peer")
	}
	cancel2()
	select {
	case err := <-done2:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("second client run failed: %v", err)
		}
	case <-time.After(time.Second * 3):
		t.Fatal("second client run did not return after cancel")
	}

	if !client2.isReady() {
		t.Fatal("expected second client to be ready")
	}
}
