package identity

import (
	"strings"
	"testing"
)

func testMeta() BodyMetadata {
	return BodyMetadata{
		Implementation: "neondoll-shellbody",
		Platform:       "linux",
		Architecture:   "amd64",
		Build:          "test",
	}
}

func TestNewBodyID(t *testing.T) {
	a, err := NewBodyID()
	if err != nil {
		t.Fatalf("NewBodyID: %v", err)
	}
	b, err := NewBodyID()
	if err != nil {
		t.Fatalf("NewBodyID: %v", err)
	}
	if a == b {
		t.Fatalf("two NewBodyID produced identical IDs %q", a)
	}
	if !strings.HasPrefix(string(a), "body_") {
		t.Fatalf("BodyID %q missing body_ prefix", a)
	}
}

func TestNewIdentityState(t *testing.T) {
	st, err := NewIdentityState("Alice", testMeta())
	if err != nil {
		t.Fatalf("NewIdentityState: %v", err)
	}
	if st.Version != CurrentIdentityVersion {
		t.Fatalf("version = %d, want %d", st.Version, CurrentIdentityVersion)
	}
	if st.BodyID() != st.Identity.BodyID {
		t.Fatalf("BodyID() = %q, direct = %q", st.BodyID(), st.Identity.BodyID)
	}
	if st.Identity.Name != "Alice" {
		t.Fatalf("name = %q, want Alice", st.Identity.Name)
	}
	if st.Identity.Meta.Implementation != "neondoll-shellbody" {
		t.Fatalf("implementation = %q", st.Identity.Meta.Implementation)
	}
}

func TestMarshalUnmarshalRoundTrip(t *testing.T) {
	st, err := NewIdentityState("Alice", testMeta())
	if err != nil {
		t.Fatalf("NewIdentityState: %v", err)
	}
	raw, err := st.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := UnmarshalIdentity(raw)
	if err != nil {
		t.Fatalf("UnmarshalIdentity: %v", err)
	}
	if got.BodyID() != st.BodyID() {
		t.Fatalf("round-trip body id = %q, want %q", got.BodyID(), st.BodyID())
	}
	if got.Identity.Meta.Architecture != st.Identity.Meta.Architecture {
		t.Fatalf("round-trip arch = %q, want %q", got.Identity.Meta.Architecture, st.Identity.Meta.Architecture)
	}
	if got.Identity.Meta.Build != st.Identity.Meta.Build {
		t.Fatalf("round-trip build = %q, want %q", got.Identity.Meta.Build, st.Identity.Meta.Build)
	}
}

func TestUnmarshalIdentityRejectsBadVersion(t *testing.T) {
	raw := `{"version":99,"identity":{"body_id":"body_ab","meta":{}}}`
	if _, err := UnmarshalIdentity(raw); err == nil {
		t.Fatal("expected error for unsupported version")
	} else if !strings.Contains(err.Error(), "version") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUnmarshalIdentityRejectsMissingBodyID(t *testing.T) {
	raw := `{"version":1,"identity":{"meta":{}}}`
	if _, err := UnmarshalIdentity(raw); err == nil {
		t.Fatal("expected error for missing body id")
	}
}

func TestUnmarshalIdentityRejectsGarbage(t *testing.T) {
	if _, err := UnmarshalIdentity("not json"); err == nil {
		t.Fatal("expected error for garbage input")
	}
}
