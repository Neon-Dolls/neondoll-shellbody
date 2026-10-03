// Package body provides tests for the Doll Link client.
package body

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/link"
)

// coreHelloBytes builds a core.hello envelope whose payload selects the given
// Doll Link and Body Contract versions. A nil version argument means the JSON
// field is omitted entirely (i.e. the version is "not present").
func coreHelloBytes(t *testing.T, dollLink, bodyContract *int) []byte {
	t.Helper()
	payload := map[string]interface{}{}
	if dollLink != nil {
		payload["doll_link_version"] = *dollLink
	}
	if bodyContract != nil {
		payload["body_contract_version"] = *bodyContract
	}
	marshaled, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to marshal core.hello payload: %v", err)
	}
	core := link.Envelope{
		Type:      link.TypeCoreHello,
		ID:        "evt_core",
		BodyID:    "test-body-id",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   marshaled,
	}
	data, err := json.Marshal(core)
	if err != nil {
		t.Fatalf("failed to marshal core.hello: %v", err)
	}
	return data
}

// intPtr is a small helper for pointer-to-int arguments.
func intPtr(v int) *int { return &v }

// runNegotiationSel drives client.negotiateVersions against a fake peer that
// replies to body.hello with a core.hello carrying the given selected versions.
// It returns the error returned by negotiateVersions.
func runNegotiationSel(t *testing.T, dollLink, bodyContract *int) error {
	t.Helper()
	ft := NewFakeTransport()
	client := NewClient(ft, "test-body-id")

	peerErr := make(chan error, 1)
	go func() {
		// The client sends body.hello first.
		data := <-ft.sendCh
		var hello link.Envelope
		if err := json.Unmarshal(data, &hello); err != nil {
			peerErr <- err
			return
		}
		if hello.Type != link.TypeBodyHello {
			peerErr <- fmt.Errorf("expected body.hello, got %s", hello.Type)
			return
		}
		core := coreHelloBytes(t, dollLink, bodyContract)
		ft.recvCh <- core
		peerErr <- nil
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := client.negotiateVersions(ctx)

	if perr := <-peerErr; perr != nil {
		t.Fatalf("peer error: %v", perr)
	}
	return err
}

func TestNegotiateVersionsValidSelection(t *testing.T) {
	err := runNegotiationSel(t, intPtr(1), intPtr(1))
	if err != nil {
		t.Fatalf("negotiation with in-range versions should succeed, got %v", err)
	}
}

func TestNegotiateVersionsRejectsMissingDollLink(t *testing.T) {
	if err := runNegotiationSel(t, nil, intPtr(1)); err == nil {
		t.Fatal("expected negotiation to fail when Doll Link version is absent")
	}
}

func TestNegotiateVersionsRejectsMissingBodyContract(t *testing.T) {
	if err := runNegotiationSel(t, intPtr(1), nil); err == nil {
		t.Fatal("expected negotiation to fail when Body Contract version is absent")
	}
}

func TestNegotiateVersionsRejectsZeroDollLink(t *testing.T) {
	if err := runNegotiationSel(t, intPtr(0), intPtr(1)); err == nil {
		t.Fatal("expected negotiation to fail when Doll Link version is zero")
	}
}

func TestNegotiateVersionsRejectsZeroBodyContract(t *testing.T) {
	if err := runNegotiationSel(t, intPtr(1), intPtr(0)); err == nil {
		t.Fatal("expected negotiation to fail when Body Contract version is zero")
	}
}

func TestNegotiateVersionsRejectsOutOfRangeDollLink(t *testing.T) {
	if err := runNegotiationSel(t, intPtr(2), intPtr(1)); err == nil {
		t.Fatal("expected negotiation to fail when Doll Link version is out of range")
	}
}

func TestNegotiateVersionsRejectsOutOfRangeBodyContract(t *testing.T) {
	if err := runNegotiationSel(t, intPtr(1), intPtr(99)); err == nil {
		t.Fatal("expected negotiation to fail when Body Contract version is out of range")
	}
}

// TestNegotiateVersionsNoReadyAfterFailure verifies that when negotiation fails,
// a subsequent Run does not advertise capabilities nor emit body.ready.
func TestNegotiateVersionsNoReadyAfterFailure(t *testing.T) {
	ft := NewFakeTransport()
	client := NewClient(ft, "test-body-id")

	peerErr := make(chan error, 1)
	peerDone := make(chan struct{})
	go func() {
		defer close(peerDone)
		// body.hello
		data := <-ft.sendCh
		var hello link.Envelope
		if err := json.Unmarshal(data, &hello); err != nil {
			peerErr <- err
			return
		}
		if hello.Type != link.TypeBodyHello {
			peerErr <- fmt.Errorf("expected body.hello, got %s", hello.Type)
			return
		}
		// core.hello selects an out-of-range version.
		ft.recvCh <- coreHelloBytes(t, intPtr(2), intPtr(1))
		// After failed negotiation the client must not send anything further
		// (no body.capabilities, no body.ready).
		select {
		case extra := <-ft.sendCh:
			peerErr <- fmt.Errorf("sent further message after failed negotiation: %v", extra)
		case <-time.After(time.Second * 1):
			peerErr <- nil
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := client.Run(ctx)
	if err == nil {
		t.Fatal("expected Run to return an error after failed negotiation")
	}

	if perr := <-peerErr; perr != nil {
		t.Fatalf("peer error: %v", perr)
	}
	<-peerDone
}

// TestClientRuntimeMetadata verifies the client uses the actual runtime GOOS and
// GOARCH rather than hardcoded platform/architecture values, while the Body ID is
// preserved exactly as supplied.
func TestClientRuntimeMetadata(t *testing.T) {
	ft := NewFakeTransport()
	client := NewClient(ft, "my-stable-body-id")

	if client.Platform != runtime.GOOS {
		t.Fatalf("expected platform %q from runtime.GOOS, got %q", runtime.GOOS, client.Platform)
	}
	if client.Architecture != runtime.GOARCH {
		t.Fatalf("expected architecture %q from runtime.GOARCH, got %q", runtime.GOARCH, client.Architecture)
	}
	if client.BodyID != "my-stable-body-id" {
		t.Fatalf("expected Body ID to remain unchanged, got %q", client.BodyID)
	}
}
