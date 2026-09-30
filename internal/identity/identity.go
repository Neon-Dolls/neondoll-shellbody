// Package identity defines the stable, persistent identity of a NeonDoll
// Shell Body.
//
// Body identity is the invariant that survives every transport, endpoint,
// network, and session change (see NeonDoll body-contract). For the M1
// milestone this package holds only the identity: a Body ID, an optional
// display name, and implementation metadata. It deliberately contains no
// pairing, WireGuard, or network semantics (those arrive in later
// milestones).
package identity

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// BodyID is a stable opaque identifier for a Body. It is produced once, then
// re-derived from persistent state on every restart.
type BodyID string

// NewBodyID generates a fresh random Body ID.
func NewBodyID() (BodyID, error) {
	var raw [16]byte
	if _, err := io.ReadFull(rand.Reader, raw[:]); err != nil {
		return "", fmt.Errorf("identity: generate body id: %w", err)
	}
	return BodyID("body_" + hex.EncodeToString(raw[:])), nil
}

// BodyMetadata is the implementation metadata advertised for a Body. It
// follows the fields used by the NeonDoll Body Protocol for identification
// without assuming the presence of a running Core.
type BodyMetadata struct {
	Implementation string `json:"implementation"`
	Platform       string `json:"platform"`
	Architecture   string `json:"architecture"`
	Build          string `json:"build,omitempty"`
}

// BodyIdentity is the stable identity persisted for a Body.
type BodyIdentity struct {
	BodyID BodyID       `json:"body_id"`
	Name   string       `json:"name,omitempty"`
	Meta   BodyMetadata `json:"meta"`
}

// CurrentIdentityVersion is the version of the identity envelope written to
// disk. Bump it and migrate when the on-disk shape changes.
const CurrentIdentityVersion = 1

// identityFileName is the single file that holds the Body identity in a
// state directory.
const identityFileName = "body.json"

// IdentityState is the versioned, persistable form of a Body identity.
type IdentityState struct {
	Version  int          `json:"version"`
	Identity BodyIdentity `json:"identity"`
}

// NewIdentityState builds a fresh identity state, generating a new Body ID.
func NewIdentityState(name string, meta BodyMetadata) (*IdentityState, error) {
	id, err := NewBodyID()
	if err != nil {
		return nil, err
	}
	return &IdentityState{
		Version: CurrentIdentityVersion,
		Identity: BodyIdentity{
			BodyID: id,
			Name:   name,
			Meta:   meta,
		},
	}, nil
}

// BodyID returns the Body ID held by the state.
func (s *IdentityState) BodyID() BodyID {
	if s == nil {
		return ""
	}
	return s.Identity.BodyID
}

// Marshal encodes the identity state as JSON.
func (s *IdentityState) Marshal() (string, error) {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return "", fmt.Errorf("identity: marshal: %w", err)
	}
	return string(raw), nil
}

// UnmarshalIdentity decodes and validates a JSON-encoded identity state.
func UnmarshalIdentity(raw string) (*IdentityState, error) {
	st := new(IdentityState)
	if err := json.Unmarshal([]byte(raw), st); err != nil {
		return nil, fmt.Errorf("identity: unmarshal: %w", err)
	}
	if st.Version != CurrentIdentityVersion {
		return nil, fmt.Errorf("identity: unsupported identity version %d (want %d)", st.Version, CurrentIdentityVersion)
	}
	if st.Identity.BodyID == "" {
		return nil, errors.New("identity: missing body id")
	}
	return st, nil
}
