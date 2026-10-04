package relay_test

import (
	"net"
	"testing"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/relay"
)

// TestUDPTransportBasic tests basic UDP transport functionality (Write only).
func TestUDPTransportBasic(t *testing.T) {
	// Create a UDP listener for testing (to avoid errors on write, but we don't verify the packet is received).
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("Failed to create listener: %v", err)
	}
	defer listener.Close()

	addr := listener.LocalAddr().String()
	routeID := [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}

	// Create UDP transport
	transport, err := relay.NewUDPTransport(addr, routeID)
	if err != nil {
		t.Fatalf("Failed to create UDP transport: %v", err)
	}
	defer transport.Close()

	// Test that we can get local and remote addresses
	if transport.LocalAddr() == nil {
		t.Error("LocalAddr() returned nil")
	}
	if transport.RemoteAddr() == nil {
		t.Error("RemoteAddr() returned nil")
	}

	// Test Write
	testPacket := []byte{0x01, 0x02, 0x03, 0x04}
	n, err := transport.Write(testPacket)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if n != len(testPacket) {
		t.Errorf("Write returned wrong byte count: got %d, want %d", n, len(testPacket))
	}
}

// TestPacketFrame tests the packet frame encoding/decoding
func TestPacketFrame(t *testing.T) {
	routeID := [16]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	wgPacket := []byte{0xde, 0xad, 0xbe, 0xef}

	// Create packet frame
	pf, err := relay.NewPacketFrame(routeID, wgPacket)
	if err != nil {
		t.Fatalf("Failed to create packet frame: %v", err)
	}

	// Check version
	expectedVersion := [4]byte{0, 0, 0, 1}
	if pf.Version != expectedVersion {
		t.Errorf("Wrong version: got %v, want %v", pf.Version, expectedVersion)
	}

	// Check route ID
	if pf.RouteID != routeID {
		t.Errorf("Wrong route ID: got %v, want %v", pf.RouteID, routeID)
	}

	// Check packet length
	if pf.PacketLen != uint16(len(wgPacket)) {
		t.Errorf("Wrong packet length: got %d, want %d", pf.PacketLen, len(wgPacket))
	}

	// Check WireGuard packet
	if len(pf.WireGuard) != len(wgPacket) {
		t.Errorf("Wrong WireGuard packet length: got %d, want %d", len(pf.WireGuard), len(wgPacket))
	}
	for i := 0; i < len(wgPacket); i++ {
		if pf.WireGuard[i] != wgPacket[i] {
			t.Errorf("Wrong WireGuard packet byte %d: got 0x%x, want 0x%x", i, pf.WireGuard[i], wgPacket[i])
		}
	}

	// Test marshaling
	binaryData, err := pf.MarshalBinary()
	if err != nil {
		t.Fatalf("Failed to marshal packet frame: %v", err)
	}

	// Test unmarshaling
	pf2, err := relay.UnmarshalBinary(binaryData)
	if err != nil {
		t.Fatalf("Failed to unmarshal packet frame: %v", err)
	}

	// Check that they match
	if pf2.Version != pf.Version {
		t.Errorf("Unmarshaled version mismatch: got %v, want %v", pf2.Version, pf.Version)
	}
	if pf2.RouteID != pf.RouteID {
		t.Errorf("Unmarshaled route ID mismatch: got %v, want %v", pf2.RouteID, pf.RouteID)
	}
	if pf2.PacketLen != pf.PacketLen {
		t.Errorf("Unmarshaled packet length mismatch: got %d, want %d", pf2.PacketLen, pf.PacketLen)
	}
	if len(pf2.WireGuard) != len(pf.WireGuard) {
		t.Errorf("Unmarshaled WireGuard length mismatch: got %d, want %d", len(pf2.WireGuard), len(pf.WireGuard))
	}
	for i := 0; i < len(pf.WireGuard); i++ {
		if pf2.WireGuard[i] != pf.WireGuard[i] {
			t.Errorf("Unmarshaled WireGuard byte mismatch %d: got 0x%x, want 0x%x", i, pf2.WireGuard[i], pf.WireGuard[i])
		}
	}
}

// TestPacketFrameErrors tests error conditions in packet frame creation
func TestPacketFrameErrors(t *testing.T) {
	routeID := [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}

	// Test zero-length packet
	_, err := relay.NewPacketFrame(routeID, []byte{})
	if err == nil {
		t.Error("Expected error for zero-length packet")
	} else {
		t.Logf("Correctly got error for zero-length packet: %v", err)
	}

	// Test oversized packet
	oversized := make([]byte, relay.MaxPacketSize+1)
	_, err = relay.NewPacketFrame(routeID, oversized)
	if err == nil {
		t.Error("Expected error for oversized packet")
	} else {
		t.Logf("Correctly got error for oversized packet: %v", err)
	}

	// Test valid packet size
	valid := make([]byte, relay.MaxPacketSize)
	pf, err := relay.NewPacketFrame(routeID, valid)
	if err != nil {
		t.Fatalf("Expected success for valid packet size: %v", err)
	}
	if len(pf.WireGuard) != relay.MaxPacketSize {
		t.Errorf("Wrong WireGuard packet length for max size: got %d, want %d", len(pf.WireGuard), relay.MaxPacketSize)
	}
}

// TestUnmarshalBinaryErrors tests error conditions in unmarshaling
func TestUnmarshalBinaryErrors(t *testing.T) {
	// Test too short data
	_, err := relay.UnmarshalBinary([]byte{0, 0, 0, 1}) // Only version bytes
	if err == nil {
		t.Error("Expected error for too short data")
	} else {
		t.Logf("Correctly got error for too short data: %v", err)
	}

	// Test wrong version
	data := make([]byte, 4+16+2+4) // version(4) + route(16) + len(2) + packet(4)
	// Set wrong version
	data[0] = 0
	data[1] = 0
	data[2] = 0
	data[3] = 2 // Wrong version
	_, err = relay.UnmarshalBinary(data)
	if err == nil {
		t.Error("Expected error for wrong version")
	} else {
		t.Logf("Correctly got error for wrong version: %v", err)
	}

	// Test insufficient data for declared length
	data = make([]byte, 4+16+2) // header only
	// Set length to 100 but only provide header
	data[20] = 0
	data[21] = 100 // Length 100
	_, err = relay.UnmarshalBinary(data)
	if err == nil {
		t.Error("Expected error for insufficient data")
	} else {
		t.Logf("Correctly got error for insufficient data: %v", err)
	}
}
