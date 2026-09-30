// Package dollnetwork defines the canonical wire-protocol types for the
// NeonDoll network as the Shell Body implements them.
//
// This is an INDEPENDENT implementation of the public Doll Network Protocol
// v1 pairing wire shape. It deliberately mirrors the field names/JSON of the
// shared protocol (so it interoperates with a real NeonDoll Core) but does not
// import or depend on any other repository's code: the Shell Body must remain
// implementable from the public specifications alone. No Core-internal types
// or secrets appear here.
package dollnetwork

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// ProtocolVersion is the Doll Network Protocol version.
const ProtocolVersion = 1

// WgPublicKeySize is the byte length of an X25519/WireGuard public key.
const WgPublicKeySize = 32

// EncodeWgPublicKey returns the Doll Network wire representation of a WG
// public key: standard (RFC 4648) base64 of the 32-byte X25519 key.
//
// Anything that is not standard base64 of exactly 32 bytes is not valid wire
// material; encode via this function instead of relying on local encodings.
func EncodeWgPublicKey(pk []byte) (string, error) {
	if len(pk) != WgPublicKeySize {
		return "", fmt.Errorf("dollnetwork: wg public key must be %d bytes, got %d", WgPublicKeySize, len(pk))
	}
	return base64.StdEncoding.EncodeToString(pk), nil
}

// DecodeWgPublicKey parses the Doll Network wire form (standard base64) of a
// WG public key back into the raw 32 bytes. Used to validate wire material and
// to map wire fields into Body representations.
func DecodeWgPublicKey(s string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("dollnetwork: wg public key is not valid base64: %w", err)
	}
	if len(raw) != WgPublicKeySize {
		return nil, fmt.Errorf("dollnetwork: wg public key must decode to %d bytes, got %d", WgPublicKeySize, len(raw))
	}
	return raw, nil
}

// PairRequestBody mirrors the "body" object of the canonical pairing request.
type PairRequestBody struct {
	BodyID         string `json:"body_id"`
	Name           string `json:"name,omitempty"`
	Implementation string `json:"implementation"`
	Platform       string `json:"platform"`
	Arch           string `json:"arch"`
}

// PairingNetwork mirrors the "network" object: the WG public key only. This is
// the only WireGuard material the runtime ever serializes. The public key uses
// the shared wire encoding (standard base64 of 32 bytes); other forms are
// invalid on the wire.
type PairingNetwork struct {
	WireGuardPublicKey string `json:"wireguard_public_key"`
}

// PairRequest is the pairing request carried to Core. invitation_id and secret
// are provided by Core's invitation at pairing time.
type PairRequest struct {
	Version      int             `json:"version"`
	InvitationID string          `json:"invitation_id"`
	Secret       string          `json:"secret"`
	Body         PairRequestBody `json:"body"`
	Network      PairingNetwork  `json:"network"`
}

// ToJSON renders the pairing request as JSON for submission.
func (r *PairRequest) ToJSON() (string, error) {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ValidationError carries the reason string for a malformed request/response.
type ValidationError struct {
	Reason string
	err    error
}

func (e *ValidationError) Error() string { return e.err.Error() }
func (e *ValidationError) Unwrap() error { return e.err }

// ValidatePairRequest defensively checks the request carries the public key in
// the shared wire encoding and never a private-key field. Requiring
// DecodeWgPublicKey here rejects malformed/non-wire representations at the
// protocol boundary.
func (r *PairRequest) ValidatePairRequest() error {
	if r.Version != ProtocolVersion {
		return &ValidationError{Reason: "unsupported_version", err: fmt.Errorf("dollnetwork: unsupported protocol version %d", r.Version)}
	}
	if r.Body.BodyID == "" {
		return &ValidationError{Reason: "missing_body_id", err: fmt.Errorf("dollnetwork: pairing request missing body_id")}
	}
	if r.Body.Implementation == "" {
		return &ValidationError{Reason: "missing_implementation", err: fmt.Errorf("dollnetwork: pairing request missing implementation")}
	}
	if r.Network.WireGuardPublicKey == "" {
		return &ValidationError{Reason: "missing_wireguard_key", err: fmt.Errorf("dollnetwork: pairing request missing wireguard public key")}
	}
	if _, err := DecodeWgPublicKey(r.Network.WireGuardPublicKey); err != nil {
		return &ValidationError{Reason: "invalid_wg_public_key", err: fmt.Errorf("dollnetwork: invalid wireguard public key: %w", err)}
	}
	return nil
}

// PairResponse is the canonical successful pairing response from Core. It
// carries the information a Body needs to establish WireGuard and Doll Link
// connectivity. CoreEndpoints may be empty; an empty slice is valid.
type PairResponse struct {
	Version         int       `json:"version"`
	NetworkID       string    `json:"network_id"`
	BodyPeerID      string    `json:"body_peer_id"`
	BodyAddresses   []string  `json:"body_addresses"`
	CorePeerID      string    `json:"core_peer_id"`
	CoreWGPublicKey string    `json:"core_wg_public_key"`
	CoreAddresses   []string  `json:"core_addresses"`
	CoreEndpoints   Endpoints `json:"core_endpoints"`
}

// PairErrorResponse is the canonical error/denial response from Core.
// Consumed indicates whether the invitation was consumed by the attempt.
type PairErrorResponse struct {
	Version  int    `json:"version"`
	Error    string `json:"error"`
	Reason   string `json:"reason"`
	Consumed bool   `json:"consumed"`
}

// Invitation is the out-of-band invitation document that Core creates and
// delivers to a Body through a side channel. The secret is only revealed once
// and MUST be kept confidential.
type Invitation struct {
	Version            int       `json:"version"`
	InvitationID       string    `json:"invitation_id"`
	InvitationSecret   string    `json:"invitation_secret"`
	ExpiresAt          string    `json:"expires_at"`
	BootstrapEndpoints Endpoints `json:"bootstrap_endpoints"`
}

// BootstrapEndpoint describes a reachable address where the Body can contact
// Core to begin pairing. Transport is inferred from the URL scheme (http/https
// = direct, relay:// = via a relay).
type BootstrapEndpoint struct {
	// URL is the full URL (e.g. "https://core.example.com:8443").
	URL string `json:"url"`
}

// Endpoints is a convenience alias for a slice of BootstrapEndpoint.
type Endpoints []BootstrapEndpoint
