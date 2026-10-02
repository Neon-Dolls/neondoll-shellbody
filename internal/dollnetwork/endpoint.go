// Package dollnetwork — canonical direct endpoint descriptor (Doll Network v1).
//
// Doll Network Protocol v1 carries endpoints in the membership document as
// structured descriptors, NOT as overloaded HTTP bootstrap URLs. A "direct"
// endpoint describes an Internet-reachable WireGuard UDP endpoint:
//
//	{
//	  "type": "direct",
//	  "host": "203.0.113.20",      // IP or hostname
//	  "port": 51820,
//	  "transport": "udp"
//	}
//
// This file is an INDEPENDENT implementation of that public descriptor shape.
// It does not import or depend on any other repository's code.
package dollnetwork

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// DirectTransport is the only transport the M3 direct path implements.
const DirectTransport = "udp"

// DirectEndpoint describes a direct WireGuard UDP endpoint advertised by a
// peer. Endpoints are runtime topology, never identity: changing one MUST NOT
// change Body identity, network ID, peer ID, WG keypair, or membership.
type DirectEndpoint struct {
	Type      string `json:"type"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Transport string `json:"transport"`
}

// ValidDirect validates that `e` is a well-formed direct UDP endpoint.
// The host may be a literal IP (v4 or v6) or a hostname; the port must be in
// the valid UDP port range. The descriptor "transport" must be the direct UDP
// transport. A descriptor that fails validation cannot be used to reach a peer.
func (e *DirectEndpoint) ValidDirect() error {
	if e == nil {
		return errors.New("dollnetwork: nil direct endpoint")
	}
	if e.Type != "direct" {
		return errors.New("dollnetwork: endpoint type must be \\\"direct\\\"")
	}
	if e.Transport != DirectTransport {
		return errors.New("dollnetwork: only direct udp transport is supported")
	}
	if strings.TrimSpace(e.Host) == "" {
		return errors.New("dollnetwork: direct endpoint host is empty")
	}
	if e.Port < 1 || e.Port > 65535 {
		return fmt.Errorf("dollnetwork: direct endpoint port %d out of range", e.Port)
	}
	// An IP literal (IPv6) must parse; anything else must be a hostname with
	// no whitespace. We intentionally do not require DNS resolution at
	// validation time — resolution is a runtime concern.
	if _, err := netip.ParseAddr(e.Host); err == nil {
		return nil
	}
	if strings.Contains(e.Host, " ") || strings.Contains(e.Host, "	") || strings.Contains(e.Host, "/") {
		return errors.New("dollnetwork: direct endpoint host is not a valid IPv6 literal or hostname")
	}
	return nil
}

// ParseDirectEndpoint parses the canonical JSON descriptor into a
// DirectEndpoint. It does not itself validate the value; call ValidDirect to do
// that. Returns ErrUnsupportedEndpoint for a descriptor with a type other than
// "direct" so callers can distinguish an unsupported (e.g. relay) descriptor
// from a malformed direct one.
func ParseDirectEndpoint(raw string) (*DirectEndpoint, error) {
	var e DirectEndpoint
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		return nil, fmt.Errorf("dollnetwork: invalid direct endpoint JSON: %w", err)
	}
	if e.Type != "direct" {
		return nil, &ErrUnsupportedEndpoint{Type: e.Type}
	}
	return &e, nil
}

// ErrUnsupportedEndpoint indicates a parsed endpoint that is not a supported
// direct descriptor (for example a future relay descriptor). Its presence lets
// callers attempt a supported path next instead of failing the whole route.
type ErrUnsupportedEndpoint struct {
	Type string
}

func (e *ErrUnsupportedEndpoint) Error() string {
	return fmt.Sprintf("dollnetwork: unsupported endpoint type %q", e.Type)
}

// EncodeDirectEndpoint renders a validated DirectEndpoint to its canonical JSON
// string. Callers should only encode endpoints that have passed ValidDirect.
func EncodeDirectEndpoint(e *DirectEndpoint) (string, error) {
	if err := e.ValidDirect(); err != nil {
		return "", err
	}
	data, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// HostPort renders the host:port address form of a validated endpoint,
// bracketing IPv6 literals (e.g. "[fd00::1]:51820").
func (e *DirectEndpoint) HostPort() string {
	if _, err := netip.ParseAddr(e.Host); err == nil && strings.Contains(e.Host, ":") {
		return fmt.Sprintf("[%s]:%d", e.Host, e.Port)
	}
	return fmt.Sprintf("%s:%d", e.Host, e.Port)
}

// NOTE: there is intentionally no FromBootstrapURL helper here anymore.
// Historically we derived a "direct WireGuard UDP endpoint" from an HTTP(S)
// bootstrap URL by copying the URL's port into a DirectEndpoint. That is wrong:
// an HTTP(S) bootstrap URL identifies the pairing/bootstrap service, and its
// port does NOT imply a WireGuard UDP listener on the same port. Reinterpreting
// it as a WG endpoint fabricates transport topology. This repository no longer
// does that — see ResolveDirectEndpoint.

// ResolveDirectEndpoint picks an unambiguous direct WireGuard UDP endpoint from
// the persisted Core endpoint strings carried by Shell Body membership. It
// accepts ONLY the canonical structured descriptor (a JSON DirectEndpoint with a
// direct UDP transport). HTTP(S) bootstrap URLs advertise the pairing/bootstrap
// service, not a WG UDP listener; relay URLs advertise transit via a relay. Both
// fail closed here — we never guess a WireGuard UDP endpoint from a URL. If the
// membership carries no structured direct descriptor (e.g. it was paired through
// an M2-era Core that persisted only bootstrap URLs), this returns a clear
// protocol-hole error: the current public Doll Network contract does not
// advertise an unambiguous direct WireGuard UDP endpoint.
func ResolveDirectEndpoint(coreEndpoints []string) (*DirectEndpoint, error) {
	// Preferred (only) pass: canonical structured descriptors.
	for _, s := range coreEndpoints {
		ep, err := ParseDirectEndpoint(s)
		if err == nil {
			if verr := ep.ValidDirect(); verr == nil {
				return ep, nil
			}
		}
	}
	// No structured direct descriptor persisted. If we were handed any endpoint
	// strings at all, they are the URL-form bootstrap endpoints M2 actually
	// persists (HTTP(S) pairing/bootstrap or relay). Report the protocol hole
	// plainly rather than inventing a WireGuard UDP endpoint.
	if len(coreEndpoints) == 0 {
		return nil, errors.New(
			"no Core endpoints persisted in membership; cannot resolve a WireGuard endpoint")
	}
	return nil, errors.New(
		"protocol hole: no direct WireGuard UDP endpoint was advertised for Core. " +
			"Persisted Core endpoints are pairing/bootstrap URLs (HTTP(S) and/or relay), " +
			"whose ports identify the bootstrap service, not a guaranteed WireGuard UDP " +
			"listener. The current public Doll Network contract (M2 pairing) does not " +
			"advertise an unambiguous direct WireGuard UDP endpoint. A Core that publishes " +
			"a structured direct descriptor ({\"type\":\"direct\",\"host\":...,\"port\":...," +
			"\"transport\":\"udp\"}) is required for --connect; the contract must be fixed " +
			"(see README M3 notes) to carry that descriptor.")
}
