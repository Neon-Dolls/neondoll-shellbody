// Package link provides Doll Link transport framing and messaging tests.
package link

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestEnvelopeMarshalUnmarshal(t *testing.T) {
	// Test body.hello
	hello := &Envelope{
		Type:      TypeBodyHello,
		ID:        "evt_123",
		BodyID:    "body_abc",
		Timestamp: "2026-09-23T00:00:00Z",
		Payload: mustMarshal(HelloPayload{
			BodyType:       "desktop",
			Implementation: "neondoll-shellbody",
			Platform:       "linux",
			Architecture:   "amd64",
			DollLink: VersionSpec{
				MinVersion: 1,
				MaxVersion: 1,
			},
			BodyContract: VersionSpec{
				MinVersion: 1,
				MaxVersion: 1,
			},
			Build: 1,
		}),
	}

	data, err := json.Marshal(hello)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var got Envelope
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	// Check essential fields
	if got.Type != TypeBodyHello {
		t.Fatalf("Type mismatch: got %v, want %v", got.Type, TypeBodyHello)
	}
	if got.ID != "evt_123" {
		t.Fatalf("ID mismatch: got %v, want %v", got.ID, "evt_123")
	}
	if got.BodyID != "body_abc" {
		t.Fatalf("BodyID mismatch: got %v, want %v", got.BodyID, "body_abc")
	}
	if got.Timestamp != "2026-09-23T00:00:00Z" {
		t.Fatalf("Timestamp mismatch: got %v, want %v", got.Timestamp, "2026-09-23T00:00:00Z")
	}

	// Check payload contents by unmarshaling into the expected type
	var helloPayload HelloPayload
	if err := json.Unmarshal(got.Payload, &helloPayload); err != nil {
		t.Fatalf("failed to unmarshal payload into HelloPayload: %v", err)
	}
	if helloPayload.BodyType != "desktop" {
		t.Fatalf("body_type mismatch: got %v, want desktop", helloPayload.BodyType)
	}
	if helloPayload.Implementation != "neondoll-shellbody" {
		t.Fatalf("implementation mismatch: got %v, want neondoll-shellbody", helloPayload.Implementation)
	}
	if helloPayload.Platform != "linux" {
		t.Fatalf("platform mismatch: got %v, want linux", helloPayload.Platform)
	}
	if helloPayload.Architecture != "amd64" {
		t.Fatalf("architecture mismatch: got %v, want amd64", helloPayload.Architecture)
	}
	if helloPayload.Build != 1 {
		t.Fatalf("build mismatch: got %v, want 1", helloPayload.Build)
	}

	// Check nested objects
	if helloPayload.DollLink.MinVersion != 1 {
		t.Fatalf("doll_link.min_version mismatch: got %v, want 1", helloPayload.DollLink.MinVersion)
	}
	if helloPayload.DollLink.MaxVersion != 1 {
		t.Fatalf("doll_link.max_version mismatch: got %v, want 1", helloPayload.DollLink.MaxVersion)
	}
	if helloPayload.BodyContract.MinVersion != 1 {
		t.Fatalf("body_contract.min_version mismatch: got %v, want 1", helloPayload.BodyContract.MinVersion)
	}
	if helloPayload.BodyContract.MaxVersion != 1 {
		t.Fatalf("body_contract.max_version mismatch: got %v, want 1", helloPayload.BodyContract.MaxVersion)
	}
}

// mustMarshal marshals v and panics on error.
func mustMarshal(v interface{}) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("marshal failed: %v", err))
	}
	return data
}

func TestUnknownMessageType(t *testing.T) {
	data := []byte(`{"type":"unknown.message","id":"evt_1"}`)
	var env Envelope
	if err := json.Unmarshal(data, &env); err == nil {
		t.Fatalf("expected error for unknown message type, got nil")
	}
	// When UnmarshalJSON returns an error, the Envelope should be zero-valued
	if env.Type != "" {
		t.Fatalf("expected empty type after error, got %s", env.Type)
	}
	if env.ID != "" {
		t.Fatalf("expected empty ID after error, got %s", env.ID)
	}
}
