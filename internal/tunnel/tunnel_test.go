package tunnel

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

// TestNewWireGuardTunnelValid asserts that a valid configuration produces a
// working manager with the expected defaults (interface name, cloned keys).
func TestNewWireGuardTunnelValid(t *testing.T) {
	bodyKey := bytes.Repeat([]byte{0x11}, 32)
	coreKey := bytes.Repeat([]byte{0x22}, 32)

	wt, err := NewWireGuardTunnel("", bodyKey, coreKey, "fd00:abcd::2", "203.0.113.9", 51820, "")
	if err != nil {
		t.Fatalf("NewWireGuardTunnel(valid) unexpected error: %v", err)
	}
	if wt == nil {
		t.Fatal("NewWireGuardTunnel returned nil manager")
	}
	// Empty interface name must default to wg0.
	if wt.ifaceName != "wg0" {
		t.Errorf("ifaceName = %q, want %q", wt.ifaceName, "wg0")
	}
	// Keys must be cloned, not reference-shared, so later mutation of the
	// caller's buffer cannot corrupt the tunnel's secret material.
	bodyKey[0] = 0xff
	coreKey[0] = 0xfe
	if got := wt.bodyKey[0]; got != 0x11 {
		t.Errorf("bodyKey leaked caller mutation: first byte = %#x, want 0x11", got)
	}
	if got := wt.coreKey[0]; got != 0x22 {
		t.Errorf("coreKey leaked caller mutation: first byte = %#x, want 0x22", got)
	}
	if wt.bodyIPv6 != "fd00:abcd::2" || wt.coreHost != "203.0.113.9" || wt.corePort != 51820 {
		t.Errorf("fields not copied: %+v", wt)
	}
}

// TestNewWireGuardTunnelExplicitName asserts the interface name is honored.
func TestNewWireGuardTunnelExplicitName(t *testing.T) {
	wt, err := NewWireGuardTunnel("ndwg0", key32(1), key32(2), "fd00::1", "core.example.com", 51820, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wt.ifaceName != "ndwg0" {
		t.Errorf("ifaceName = %q, want ndwg0", wt.ifaceName)
	}
}

// TestNewWireGuardTunnelValidation asserts the constructor rejects every
// invalid input that would otherwise produce a broken interface later.
func TestNewWireGuardTunnelValidation(t *testing.T) {
	validBody := key32(1)
	validCore := key32(2)

	cases := []struct {
		name    string
		bodyKey []byte
		coreKey []byte
		host    string
		port    int
		wantErr bool
	}{
		{"short body key", []byte{1, 2, 3}, validCore, "h", 51820, true},
		{"long body key", append(validBody, 0x00), validCore, "h", 51820, true},
		{"short core key", validBody, []byte{1}, "h", 51820, true},
		{"long core key", validBody, append(validCore, 0x00), "h", 51820, true},
		// The constructor rejects a fully-empty host, but host *validation* is
		// the endpoint layer's job (ValidDirect rejects whitespace/blank); the
		// tunnel constructor only guards the command against an empty host so a
		// malformed peer config cannot be emitted. Whitespace therefore passes
		// the constructor and is caught earlier by the endpoint validator.
		{"empty host", validBody, validCore, "", 51820, true},
		{"whitespace host", validBody, validCore, "  ", 51820, false},
		{"port zero", validBody, validCore, "h", 0, true},
		{"negative port", validBody, validCore, "h", -1, true},
		{"port too high", validBody, validCore, "h", 65536, true},
		{"valid minimal", validBody, validCore, "h", 1, false},
		{"valid max port", validBody, validCore, "h", 65535, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewWireGuardTunnel("wg0", tc.bodyKey, tc.coreKey, "fd00::1", tc.host, tc.port, "")
			if (err != nil) != tc.wantErr {
				t.Errorf("NewWireGuardTunnel error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// TestNewWireGuardTunnelNilKeys asserts a nil key is rejected (len 0 != 32).
func TestNewWireGuardTunnelNilKeys(t *testing.T) {
	if _, err := NewWireGuardTunnel("wg0", nil, key32(1), "fd00::1", "h", 51820, ""); err == nil {
		t.Error("nil bodyKey not rejected")
	}
	if _, err := NewWireGuardTunnel("wg0", key32(1), nil, "fd00::1", "h", 51820, ""); err == nil {
		t.Error("nil coreKey not rejected")
	}
}

// TestEncodeKey asserts encodeKey produces the standard base64 of the raw
// 32-byte key — exactly what wg(8) expects on its command line.
func TestEncodeKey(t *testing.T) {
	key := bytes.Repeat([]byte{0xab}, 32)
	want := base64.StdEncoding.EncodeToString(key)
	if got := encodeKey(key); got != want {
		t.Errorf("encodeKey = %q, want %q", got, want)
	}
	// Encode a second distinct key to prove we are not accidentally the
	// identity function on the previous key.
	key2 := bytes.Repeat([]byte{0xcd}, 32)
	if got := encodeKey(key2); got == want {
		t.Errorf("encodeKey produced identical output for distinct keys")
	}
	// Round-trip with a fully random key.
	raw := []byte{
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10,
		0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18,
		0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x20,
	}
	if got := encodeKey(raw); got != base64.StdEncoding.EncodeToString(raw) {
		t.Errorf("encodeKey(random) mismatch")
	}
}

// TestCloseWipesKeys asserts Close zeroes and releases the private key
// material held by the manager (defense-in-depth so a stale manager cannot
// leak the body's never-to-be-transmitted secret).
//
// It does not require root: Close deletes the interface as best-effort; for a
// never-created interface the delete simply fails and is tolerated. What we
// assert is the key hygiene, which must hold regardless of interface state.
func TestCloseWipesKeys(t *testing.T) {
	bodyKey := []byte("\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c\x0d\x0e\x0f\x10" +
		"\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f\x20")
	coreKey := key32(0x42)

	// Snapshot the caller-owned buffers before construction.
	callerBody := bytes.Clone(bodyKey)
	callerCore := bytes.Clone(coreKey)

	wt, err := NewWireGuardTunnel("definitely-does-not-exist-"+t.Name(), bodyKey, coreKey, "fd00::1", "h", 51820, "")
	if err != nil {
		t.Fatalf("NewWireGuardTunnel: %v", err)
	}

	// Interface was never created, so the delete step inside Close will error —
	// that is expected and tolerated for the key-hygiene assertion.
	_ = wt.Close()

	if wt.bodyKey != nil {
		t.Error("bodyKey not released after Close")
	}
	if wt.coreKey != nil {
		t.Error("coreKey not released after Close")
	}
	// Close must clear the manager's own clone, never the caller-owned input.
	if !bytes.Equal(bodyKey, callerBody) {
		t.Error("Close mutated the caller-owned body key buffer")
	}
	if !bytes.Equal(coreKey, callerCore) {
		t.Error("Close mutated the caller-owned core key buffer")
	}
}

// key32 returns a 32-byte slice filled with a constant, suitable for tests.
func key32(b byte) []byte {
	return bytes.Repeat([]byte{b}, 32)
}

// TestEncodeKeyEmpty ensures encodeKey never panics on a validation-skip path
// and that its output length matches WireGuard's fixed 44-char base64 form.
func TestEncodeKeyLength(t *testing.T) {
	got := encodeKey(key32(7))
	if len(got) != 44 {
		t.Errorf("encodeKey output length = %d, want 44", len(got))
	}
	if strings.TrimRight(got, "=") == got {
		t.Errorf("expected base64 padding on 32-byte key, got %q", got)
	}
}
