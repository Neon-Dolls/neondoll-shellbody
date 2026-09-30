// Package body — persistent Body relationship state.
//
// M2 introduces durable network membership: the record of this Body's
// relationship to a Doll Network, established by pairing and persisted after
// the complete Core response has validated. Before pairing this is empty.
//
// Separation invariant (Doll Network): persistent identity/membership state is
// kept separate from transient connection/endpoint state. Membership is what
// survives restart and is portable. Endpoints are runtime topology, never
// stored as identity.
package body

import (
	"encoding/json"
	"errors"
)

// MembershipStatus is the lifecycle state of this Body's network membership.
type MembershipStatus string

const (
	// MembershipPending means the Body has a local record but no confirmed
	// network membership yet (i.e. before successful pairing).
	MembershipPending MembershipStatus = "pending"
	// MembershipActive means Core has enrolled this Body in a Doll Network.
	MembershipActive MembershipStatus = "active"
	// MembershipRevoked means Core has revoked this Body's membership.
	MembershipRevoked MembershipStatus = "revoked"
)

// Membership is this Body's durable record of its relationship to a Doll
// Network, once established. It is portable and survives restart. Before
// pairing it is simply pending/empty.
//
// The field set is the Shell Body's own durable record of a successful
// canonical pairing: Network ID, the Body's peer ID and overlay IPv6, the
// Core's peer ID, WireGuard public key, overlay IPv6 address(es), and the
// Core endpoint(s) returned by Core. Endpoints are stored only because Core
// returned them in the pairing response as required by the current protocol;
// they remain runtime-meaningful and are not treated as identity.
type Membership struct {
	NetworkID string           `json:"network_id,omitempty"`
	PeerID    string           `json:"peer_id,omitempty"`
	Status    MembershipStatus `json:"status"`

	// Body peer: assigned by Core during pairing.
	BodyPeerID    string   `json:"body_peer_id,omitempty"`
	BodyIPv6      string   `json:"body_ipv6,omitempty"`
	BodyAddresses []string `json:"body_addresses,omitempty"`

	// Core peer: the remote side of our relationship.
	CorePeerID    string   `json:"core_peer_id,omitempty"`
	CoreWGKeyB64  string   `json:"core_wg_key_b64,omitempty"`
	CoreAddresses []string `json:"core_addresses,omitempty"`
	CoreEndpoints []string `json:"core_endpoints,omitempty"`
}

// NewPendingMembership returns an empty, pre-pairing membership record.
func NewPendingMembership() Membership {
	return Membership{Status: MembershipPending}
}

// CurrentMembershipVersion is the format version of the persistable member
// envelope. Bump it (and teach UnmarshalMembership) before changing shape.
const CurrentMembershipVersion = 1

// MembershipState is the persistable form of the Body's network membership.
type MembershipState struct {
	Version    int        `json:"version"`
	Membership Membership `json:"membership"`
}

// MarshalMembership returns the canonical JSON encoding of the membership
// envelope containing `m`.
func MarshalMembership(m *Membership) (string, error) {
	env := MembershipState{
		Version:    CurrentMembershipVersion,
		Membership: *m,
	}
	data, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// UnmarshalMembership parses and validates a persisted membership envelope.
func UnmarshalMembership(raw string) (*Membership, error) {
	var env MembershipState
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return nil, err
	}
	if env.Version != CurrentMembershipVersion {
		return nil, errors.New("unsupported membership version")
	}
	return &env.Membership, nil
}

// Active reports whether the membership record denotes a confirmed, live
// network relationship.
func (m *Membership) Active() bool {
	return m != nil && m.Status == MembershipActive
}

// Valid checks that an active membership carries the fields required for the
// Body to consider itself durably paired. This is the post-validation gate
// before a membership may be persisted or reported.
func (m *Membership) Valid() bool {
	if m == nil {
		return false
	}
	return m.NetworkID != "" &&
		m.BodyPeerID != "" &&
		m.BodyIPv6 != "" &&
		m.CorePeerID != "" &&
		m.CoreWGKeyB64 != ""
}
