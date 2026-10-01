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
	"net/url"
	"strconv"
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

// FromBootstrapURL derives a DirectEndpoint from an http(s) bootstrap URL when
// a port is present in that URL. This is a MIGRATION helper for memberships
// persisted by M2, which carried only URL-based bootstrap endpoints. Runtime
// M3 prefers an explicit canonical descriptor; this helper exists so a Body
// paired against an M2-era Core can still resolve a real direct endpoint rather
// than guessing. It returns ErrUnsupportedEndpoint when the URL has no usable
// port (so callers can tell "no direct endpoint yet" apart from parse errors).
func FromBootstrapURL(rawURL string) (*DirectEndpoint, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("dollnetwork: invalid bootstrap URL %q: %w", rawURL, err)
	}
	scheme := u.Scheme
	// Only the direct transports (http/https) can yield a direct WG endpoint.
	// A relay:// URL advertises transit via a relay, not a direct UDP endpoint;
	// silently reinterpreting it as direct would fabricate a route. We reject
	// it explicitly so callers can report the limitation instead of guessing.
	if scheme != "http" && scheme != "https" {
		return nil, &ErrUnsupportedEndpoint{Type: fmt.Sprintf("%s:not-direct-transport", schemeOr(scheme))}
	}
	host := u.Hostname()
	if host == "" {
		return nil, &ErrUnsupportedEndpoint{Type: "http:no-host"}
	}
	portStr := u.Port()
	if portStr == "" {
		return nil, &ErrUnsupportedEndpoint{Type: fmt.Sprintf("%s:no-port", scheme)}
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, &ErrUnsupportedEndpoint{Type: fmt.Sprintf("%s:invalid-port", scheme)}
	}
	if port < 1 || port > 65535 {
		return nil, &ErrUnsupportedEndpoint{Type: fmt.Sprintf("%s:port-out-of-range", scheme)}
	}
	ep := &DirectEndpoint{
		Type:      "direct",
		Host:      host,
		Port:      port,
		Transport: DirectTransport,
	}
	if err := ep.ValidDirect(); err != nil {
		return nil, err
	}
	return ep, nil
}

// schemeOr returns the scheme, or "(empty)" when it is missing (unreachable
// guard against a URL parse that yields an empty scheme).
func schemeOr(s string) string {
	if s == "" {
		return "(empty)"
	}
	return s
}

// ResolveDirectEndpoint picks an unambiguous direct WireGuard UDP endpoint from
// the persisted Core endpoint strings carried by Shell Body membership. M3
// prefers a canonical structured descriptor (JSON DirectEndpoint with a direct
// UDP transport); when only M2-form bootstrap URLs were persisted it falls back
// to FromBootstrapURL — the explicit migration helper — so a Body paired
// through the real M2 flow can resolve a genuine direct endpoint. Relay-only or
// otherwise ambiguous state (port-less URLs, non-direct transports, malformed
// descriptors) fails closed with a descriptive error so callers never guess at
// a WireGuard UDP endpoint.
func ResolveDirectEndpoint(coreEndpoints []string) (*DirectEndpoint, error) {
	// Preferred pass: canonical structured descriptors.
	for _, s := range coreEndpoints {
		ep, err := ParseDirectEndpoint(s)
		if err == nil {
			if verr := ep.ValidDirect(); verr == nil {
				return ep, nil
			}
		}
	}
	// Migration pass: M2 URL form, resolved only through the sanctioned helper.
	for _, s := range coreEndpoints {
		ep, err := FromBootstrapURL(s)
		if err == nil && ep.ValidDirect() == nil {
			return ep, nil
		}
	}
	return nil, errors.New(
		"no unambiguous direct WireGuard UDP endpoint in persisted Core endpoints: " +
			"the current public protocol persists pairing/bootstrap URLs; a Core that advertises " +
			"only relay or port-less endpoints cannot yield a direct WG UDP endpoint")
}
