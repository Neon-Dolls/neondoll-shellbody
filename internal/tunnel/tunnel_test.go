package tunnel

import "testing"

// TestNewWireGuardTunnel tests the tunnel constructor.
func TestNewWireGuardTunnel(t *testing.T) {
	bodyKey := make([]byte, 32)
	coreKey := make([]byte, 32)
	_, err := NewWireGuardTunnel("wg0", bodyKey, coreKey, "fd00::1/64", "example.com", 51820, "")
	if err != nil {
		t.Errorf("NewWireGuardTunnel returned error: %v", err)
	}

	// Test with invalid bodyKey length
	_, err = NewWireGuardTunnel("wg0", []byte{1, 2, 3}, coreKey, "fd00::1/64", "example.com", 51820, "")
	if err == nil {
		t.Error("Expected error for short bodyKey")
	}
	// Test with invalid coreKey length
	_, err = NewWireGuardTunnel("wg0", bodyKey, []byte{1, 2, 3}, "fd00::1/64", "example.com", 51820, "")
	if err == nil {
		t.Error("Expected error for short coreKey")
	}
}

// TestWireGuardTunnel_Configure tests that the Configure method can be called.
func TestWireGuardTunnel_Configure(t *testing.T) {
	bodyKey := make([]byte, 32)
	coreKey := make([]byte, 32)
	wt := &WireGuardTunnel{
		ifaceName: "wg0",
		bodyKey:   bodyKey,
		coreKey:   coreKey,
		bodyIPv6:  "fd00::1/64",
		coreHost:  "example.com",
		corePort:  51820,
		netns:     "",
	}
	// This test would require mocking or integration testing with actual wg/ip commands.
	// For now, we skip it because it requires root and wireguard tools.
	t.Skip("Integration test requiring root and wireguard tools")
	_ = wt.Configure
}

// TestWireGuardTunnel_Establish tests that the Establish method can be called.
// Note: There is no Establish method; the closest is Ping6Core.
func TestWireGuardTunnel_Establish(t *testing.T) {
	bodyKey := make([]byte, 32)
	coreKey := make([]byte, 32)
	wt := &WireGuardTunnel{
		ifaceName: "wg0",
		bodyKey:   bodyKey,
		coreKey:   coreKey,
		bodyIPv6:  "fd00::1/64",
		coreHost:  "example.com",
		corePort:  51820,
		netns:     "",
	}
	// This test would require mocking or integration testing with actual wg/ip commands.
	t.Skip("Integration test requiring root and wireguard tools")
	_ = wt.Ping6Core
}

// TestWireGuardTunnel_Stop tests that the Stop method can be called.
func TestWireGuardTunnel_Stop(t *testing.T) {
	bodyKey := make([]byte, 32)
	coreKey := make([]byte, 32)
	wt := &WireGuardTunnel{
		ifaceName: "wg0",
		bodyKey:   bodyKey,
		coreKey:   coreKey,
		bodyIPv6:  "fd00::1/64",
		coreHost:  "example.com",
		corePort:  51820,
		netns:     "",
	}
	// This test would require mocking or integration testing with actual wg/ip commands.
	t.Skip("Integration test requiring root and wireguard tools")
	_ = wt.Stop
}