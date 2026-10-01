package dollnetwork

import (
	"encoding/json"
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

// TestFromBootstrapURL tests deriving direct endpoints from bootstrap URLs.
func TestFromBootstrapURL(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    *DirectEndpoint
		wantErr bool
	}{
		{
			name:  "valid bootstrap URL with port",
			input: "https://core.example.com:51820",
			want: &DirectEndpoint{
				Type:      "direct",
				Host:      "core.example.com",
				Port:      51820,
				Transport: "udp",
			},
			wantErr: false,
		},
		{
			name:  "valid bootstrap URL with IPv6 and port",
			input: "http://[fd00::1]:51820",
			want: &DirectEndpoint{
				Type:      "direct",
				Host:      "fd00::1",
				Port:      51820,
				Transport: "udp",
			},
			wantErr: false,
		},
		{
			name:    "bootstrap URL without port",
			input:   "https://core.example.com/",
			want:    nil,
			wantErr: true, // ErrUnsupportedEndpoint with type "https:no-port"
		},
		{
			name:    "invalid URL",
			input:   "not a url",
			want:    nil,
			wantErr: true,
		},
		{
			name:    "URL with invalid port",
			input:   "https://core.example.com:notaport",
			want:    nil,
			wantErr: true, // ErrUnsupportedEndpoint with type "https:invalid-port"
		},
		{
			name:    "URL with port out of range",
			input:   "https://core.example.com:70000",
			want:    nil,
			wantErr: true, // ErrUnsupportedEndpoint with type "https:port-out-of-range"
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FromBootstrapURL(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("FromBootstrapURL() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != nil {
				if got.Type != tt.want.Type || got.Host != tt.want.Host || got.Port != tt.want.Port || got.Transport != tt.want.Transport {
					t.Errorf("FromBootstrapURL() = %v, want %v", got, tt.want)
				}
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

// TestResolveDirectEndpoint covers endpoint selection from the persisted Core
// endpoint strings that the connect path reads. It must resolve the M2 URL form
// persisted by real pairing, prefer a canonical structured direct descriptor
// when one is present, and fail closed when no unambiguous direct WG UDP
// endpoint exists (relay-only or port-less state).
func TestResolveDirectEndpoint(t *testing.T) {
	structEP := `{"type":"direct","host":"203.0.113.50","port":51820,"transport":"udp"}`
	relayEP := `{"type":"relay","relay_url":"https://relay.example.net","route_id":"opaque-route-id"}`
	badUDP := `{"type":"direct","host":"203.0.113.50","port":70000,"transport":"udp"}`

	tests := []struct {
		name      string
		endpoints []string
		wantHost  string
		wantPort  int
		wantErr   bool
	}{
		{
			name:      "M2 URL form resolves to a direct endpoint",
			endpoints: []string{"https://core.example.com:51820"},
			wantHost:  "core.example.com",
			wantPort:  51820,
		},
		{
			name:      "structured direct descriptor works",
			endpoints: []string{structEP},
			wantHost:  "203.0.113.50",
			wantPort:  51820,
		},
		{
			name:      "structured descriptor preferred over M2 URL",
			endpoints: []string{"https://legacy.example.com:1111", structEP},
			wantHost:  "203.0.113.50",
			wantPort:  51820,
		},
		{
			name:      "IPv6 M2 URL form resolves",
			endpoints: []string{"http://[fd00::1]:51820"},
			wantHost:  "fd00::1",
			wantPort:  51820,
		},
		{
			name:      "port-less bootstrap URL fails closed",
			endpoints: []string{"https://core.example.com/"},
			wantErr:   true,
		},
		{
			name:      "relay descriptor fails closed",
			endpoints: []string{relayEP},
			wantErr:   true,
		},
		{
			name:      "relay:// URL fails closed (not reinterpreted as direct)",
			endpoints: []string{"relay://relay.example.net:9999"},
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
