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
		Payload: HelloPayload{
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
		},
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

	// Check payload contents (since Payload is interface{}, it unmarshaled as map[string]interface{})
	payloadMap, ok := got.Payload.(map[string]interface{})
	if !ok {
		t.Fatalf("Payload is not a map: got %T", got.Payload)
	}
	if payloadMap["body_type"] != "desktop" {
		t.Fatalf("body_type mismatch: got %v, want desktop", payloadMap["body_type"])
	}
	if payloadMap["implementation"] != "neondoll-shellbody" {
		t.Fatalf("implementation mismatch: got %v, want neondoll-shellbody", payloadMap["implementation"])
	}
	if payloadMap["platform"] != "linux" {
		t.Fatalf("platform mismatch: got %v, want linux", payloadMap["platform"])
	}
	if payloadMap["architecture"] != "amd64" {
		t.Fatalf("architecture mismatch: got %v, want amd64", payloadMap["architecture"])
	}
	if payloadMap["build"] != float64(1) { // JSON numbers unmarshal as float64
		t.Fatalf("build mismatch: got %v, want 1", payloadMap["build"])
	}

	// Check nested objects
	dollLink, ok := payloadMap["doll_link"].(map[string]interface{})
	if !ok {
		t.Fatalf("doll_link is not a map: got %T", payloadMap["doll_link"])
	}
	if dollLink["min_version"] != float64(1) {
		t.Fatalf("doll_link.min_version mismatch: got %v, want 1", dollLink["min_version"])
	}
	if dollLink["max_version"] != float64(1) {
		t.Fatalf("doll_link.max_version mismatch: got %v, want 1", dollLink["max_version"])
	}

	bodyContract, ok := payloadMap["body_contract"].(map[string]interface{})
	if !ok {
		t.Fatalf("body_contract is not a map: got %T", payloadMap["body_contract"])
	}
	if bodyContract["min_version"] != float64(1) {
		t.Fatalf("body_contract.min_version mismatch: got %v, want 1", bodyContract["min_version"])
	}
	if bodyContract["max_version"] != float64(1) {
		t.Fatalf("body_contract.max_version mismatch: got %v, want 1", bodyContract["max_version"])
	}
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

func TestGenerateIDFormat(t *testing.T) {
	id := generateID()
	if id == "" {
		t.Fatalf("generateID returned empty string")
	}
	// Should start with "evt_"
	if len(id) < 5 || id[:4] != "evt_" {
		t.Fatalf("generateID does not start with 'evt_': %s", id)
	}
	// Should have numeric suffix
	if _, err := parseIDSuffix(id); err != nil {
		t.Fatalf("generateID suffix not numeric: %v", err)
	}
}

func parseIDSuffix(id string) (int64, error) {
	var n int64
	_, err := fmt.Sscanf(id, "evt_%d", &n)
	return n, err
}
