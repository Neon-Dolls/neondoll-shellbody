package dollnetwork

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestValidDirect tests the validation logic for direct endpoints.
func TestValidDirect(t *testing.T) {
	tests := []struct {
		name    string
		e       DirectEndpoint
		wantErr bool
	}{
		{
			name: "valid IPv4 endpoint",
			e: DirectEndpoint{
				Type:      "direct",
				Host:      "203.0.113.20",
				Port:      51820,
				Transport: "udp",
			},
			wantErr: false,
		},
		{
			name: "valid IPv6 endpoint",
			e: DirectEndpoint{
				Type:      "direct",
				Host:      "fd00::1",
				Port:      51820,
				Transport: "udp",
			},
			wantErr: false,
		},
		{
			name: "valid hostname endpoint",
			e: DirectEndpoint{
				Type:      "direct",
				Host:      "core.example.com",
				Port:      51820,
				Transport: "udp",
			},
			wantErr: false,
		},
		{
			name: "invalid type",
			e: DirectEndpoint{
				Type:      "indirect",
				Host:      "203.0.113.20",
				Port:      51820,
				Transport: "udp",
			},
			wantErr: true,
		},
		{
			name: "invalid transport",
			e: DirectEndpoint{
				Type:      "direct",
				Host:      "203.0.113.20",
				Port:      51820,
				Transport: "tcp",
			},
			wantErr: true,
		},
		{
			name: "empty host",
			e: DirectEndpoint{
				Type:      "direct",
				Host:      "",
				Port:      51820,
				Transport: "udp",
			},
			wantErr: true,
		},
		{
			name: "port out of range low",
			e: DirectEndpoint{
				Type:      "direct",
				Host:      "203.0.113.20",
				Port:      0,
				Transport: "udp",
			},
			wantErr: true,
		},
		{
			name: "port out of range high",
			e: DirectEndpoint{
				Type:      "direct",
				Host:      "203.0.113.20",
				Port:      65536,
				Transport: "udp",
			},
			wantErr: true,
		},
		{
			name: "host with space",
			e: DirectEndpoint{
				Type:      "direct",
				Host:      "core example.com",
				Port:      51820,
				Transport: "udp",
			},
			wantErr: true,
		},
		{
			name: "host with tab",
			e: DirectEndpoint{
				Type:      "direct",
				Host:      "core\texample.com",
				Port:      51820,
				Transport: "udp",
			},
			wantErr: true,
		},
		{
			name: "host with slash",
			e: DirectEndpoint{
				Type:      "direct",
				Host:      "core/example.com",
				Port:      51820,
				Transport: "udp",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.e.ValidDirect(); (err != nil) != tt.wantErr {
				t.Errorf("ValidDirect() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestParseDirectEndpoint tests parsing of direct endpoint descriptors.
func TestParseDirectEndpoint(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    *DirectEndpoint
		wantErr bool
	}{
		{
			name: "valid direct endpoint JSON",
			input: `{
				"type": "direct",
				"host": "203.0.113.20",
				"port": 51820,
				"transport": "udp"
			}`,
			want: &DirectEndpoint{
				Type:      "direct",
				Host:      "203.0.113.20",
				Port:      51820,
				Transport: "udp",
			},
			wantErr: false,
		},
		{
			name:    "invalid JSON",
			input:   `{"type": "direct", "host": "203.0.113.20", "port": 51820, "transport": "udp"`,
			want:    nil,
			wantErr: true,
		},
		{
			name: "unsupported endpoint type",
			input: `{
				"type": "relay",
				"relay_url": "https://relay.example.net",
				"route_id": "opaque-route-id"
			}`,
			want:    nil,
			wantErr: true, // ErrUnsupportedEndpoint
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseDirectEndpoint(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseDirectEndpoint() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != nil {
				if got.Type != tt.want.Type || got.Host != tt.want.Host || got.Port != tt.want.Port || got.Transport != tt.want.Transport {
					t.Errorf("ParseDirectEndpoint() = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

// TestResolveDirectEndpointRejectsBootstrapURLs proves HTTP(S)/relay bootstrap
// URLs are NOT converted into WireGuard UDP endpoints. An HTTP(S) bootstrap URL
// identifies the pairing/bootstrap service; its port does not imply a WireGuard
// UDP listener. Reinterpreting it would fabricate transport topology, so
// ResolveDirectEndpoint must fail closed. This documents the public-protocol
// hole: M2 pairing advertises only bootstrap URLs, which carry no unambiguous
// direct WireGuard UDP endpoint.
func TestResolveDirectEndpointRejectsBootstrapURLs(t *testing.T) {
	httpURLWithPort := "https://core.example.com:51820"
	tests := []struct {
		name      string
		endpoints []string
	}{
		{
			name:      "HTTPS bootstrap URL with port is not a WG endpoint",
			endpoints: []string{httpURLWithPort},
		},
		{
			name:      "HTTP bootstrap URL with IPv6 web port is not a WG endpoint",
			endpoints: []string{"http://[fd00::1]:51820"},
		},
		{
			name:      "relay URL is not a direct WG endpoint",
			endpoints: []string{"relay://relay.example.net:9999"},
		},
		{
			name:      "mixed bootstrap URLs all fail closed",
			endpoints: []string{"https://a.example.com:443", "https://b.example.com:8443", "relay://r.example.net:51820"},
		},
		{
			name:      "bootstrap URL next to malformed descriptor fails closed",
			endpoints: []string{httpURLWithPort, "not a url or json"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveDirectEndpoint(tt.endpoints)
			if err == nil {
				t.Fatalf("ResolveDirectEndpoint(%v) = %+v, want error: bootstrap URL must never fabricate a WG UDP endpoint", tt.endpoints, got)
			}
			// The error must name the protocol hole so operators can act.
			if !strings.Contains(err.Error(), "protocol hole") {
				t.Errorf("error does not report protocol hole: %v", err)
			}
		})
	}
}

// TestResolveDirectEndpointStructuredOnly proves only an explicit structured
// direct descriptor yields an endpoint, and it is still validated.
func TestResolveDirectEndpointStructuredOnly(t *testing.T) {
	structEP := `{"type":"direct","host":"203.0.113.50","port":51820,"transport":"udp"}`
	relayEP := `{"type":"relay","relay_url":"https://relay.example.net/download","route_id":"opaque-route-id"}`
	badUDP := `{"type":"direct","host":"203.0.113.50","port":70000,"transport":"udp"}`

	tests := []struct {
		name      string
		endpoints []string
		wantHost  string
		wantPort  int
		wantErr   bool
	}{
		{
			name:      "structured direct descriptor works",
			endpoints: []string{structEP},
			wantHost:  "203.0.113.50",
			wantPort:  51820,
		},
		{
			name:      "structured descriptor preferred over HTTP bootstrap URL",
			endpoints: []string{"https://legacy.example.com:1111", structEP},
			wantHost:  "203.0.113.50",
			wantPort:  51820,
		},
		{
			name:      "relay descriptor fails closed",
			endpoints: []string{relayEP},
			wantErr:   true,
		},
		{
			name:      "non-direct transport descriptor fails closed",
			endpoints: []string{badUDP},
			wantErr:   true,
		},
		{
			name:      "empty endpoint list fails closed",
			endpoints: []string{},
			wantErr:   true,
		},
		{
			name:      "malformed endpoint string fails closed",
			endpoints: []string{"not a url or json"},
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveDirectEndpoint(tt.endpoints)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ResolveDirectEndpoint(%v) succeeded = %+v, want error (fail closed)", tt.endpoints, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveDirectEndpoint(%v) unexpected error: %v", tt.endpoints, err)
			}
			if got.Host != tt.wantHost || got.Port != tt.wantPort {
				t.Errorf("ResolveDirectEndpoint(%v) = %s:%d, want %s:%d", tt.endpoints, got.Host, got.Port, tt.wantHost, tt.wantPort)
			}
		})
	}
}

// TestEncodeDirectEndpoint tests encoding validated endpoints to JSON.
func TestEncodeDirectEndpoint(t *testing.T) {
	e := &DirectEndpoint{
		Type:      "direct",
		Host:      "203.0.113.20",
		Port:      51820,
		Transport: "udp",
	}
	if err := e.ValidDirect(); err != nil {
		t.Fatalf("Endpoint should be valid: %v", err)
	}

	got, err := EncodeDirectEndpoint(e)
	if err != nil {
		t.Errorf("EncodeDirectEndpoint() error = %v", err)
		return
	}

	var parsed DirectEndpoint
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Errorf("Failed to unmarshal encoded endpoint: %v", err)
		return
	}

	if parsed.Type != e.Type || parsed.Host != e.Host || parsed.Port != e.Port || parsed.Transport != e.Transport {
		t.Errorf("EncodeDirectEndpoint() = %v, want %v", got, e)
	}
}

// TestHostPort tests the HostPort method.
func TestHostPort(t *testing.T) {
	tests := []struct {
		name string
		e    DirectEndpoint
		want string
	}{
		{
			name: "IPv4 host",
			e: DirectEndpoint{
				Type:      "direct",
				Host:      "203.0.113.20",
				Port:      51820,
				Transport: "udp",
			},
			want: "203.0.113.20:51820",
		},
		{
			name: "IPv6 host",
			e: DirectEndpoint{
				Type:      "direct",
				Host:      "fd00::1",
				Port:      51820,
				Transport: "udp",
			},
			want: "[fd00::1]:51820",
		},
		{
			name: "hostname",
			e: DirectEndpoint{
				Type:      "direct",
				Host:      "core.example.com",
				Port:      51820,
				Transport: "udp",
			},
			want: "core.example.com:51820",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.e.HostPort(); got != tt.want {
				t.Errorf("HostPort() = %v, want %v", got, tt.want)
			}
		})
	}
}
