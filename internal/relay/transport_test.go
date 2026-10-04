package relay_test

import (
	"net"
	"testing"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/relay"
)

// TestUDPTransportRoundtrip tests UDP transport Write and Read with real UDP sockets.
func TestUDPTransportRoundtrip(t *testing.T) {
	// Create two UDP endpoints to simulate a full round trip
	listenerA, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("Failed to create listener A: %v", err)
	}
	defer listenerA.Close()

	listenerB, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("Failed to create listener B: %v", err)
	}
	defer listenerB.Close()

	addrA := listenerA.LocalAddr().String()
	addrB := listenerB.LocalAddr().String()
	routeID := [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}

	// Create transport A that sends to listener B
	transportA, err := relay.NewUDPTransport(addrB, routeID)
	if err != nil {
		t.Fatalf("Failed to create UDP transport A: %v", err)
	}
	defer transportA.Close()

	// Create transport B that sends to listener A
	transportB, err := relay.NewUDPTransport(addrA, routeID)
	if err != nil {
		t.Fatalf("Failed to create UDP transport B: %v", err)
	}
	defer transportB.Close()

	// Test Write path A -> B
	testPacket := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
	n, err := transportA.Write(testPacket)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if n != len(testPacket) {
		t.Errorf("Write returned wrong byte count: got %d, want %d", n, len(testPacket))
	}

	// Transport B should receive the frame via its connection
	frameBuf := make([]byte, 65535)
	frameN, _, err := listenerB.ReadFromUDP(frameBuf)
	if err != nil {
		t.Fatalf("Failed to read frame from UDP: %v", err)
	}

	// Unmarshal the frame
	pf, err := relay.UnmarshalBinary(frameBuf[:frameN])
	if err != nil {
		t.Fatalf("Failed to unmarshal packet frame: %v", err)
	}

	// Validate the frame contents
	if pf.Version[0] != 0 || pf.Version[1] != 0 || pf.Version[2] != 0 || pf.Version[3] != 1 {
		t.Errorf("Wrong version in received frame: got %v, want [0 0 0 1]", pf.Version)
	}
	if pf.RouteID != routeID {
		t.Errorf("Wrong route ID in received frame: got %v, want %v", pf.RouteID, routeID)
	}
	if pf.PacketLen != uint16(len(testPacket)) {
		t.Errorf("Wrong packet length in received frame: got %d, want %d", pf.PacketLen, len(testPacket))
	}
	if len(pf.WireGuard) != len(testPacket) {
		t.Errorf("Wrong WireGuard packet length in received frame: got %d, want %d", len(pf.WireGuard), len(testPacket))
	}
	for i := 0; i < len(testPacket); i++ {
		if pf.WireGuard[i] != testPacket[i] {
			t.Errorf("Wrong WireGuard packet byte %d: got 0x%x, want 0x%x", i, pf.WireGuard[i], testPacket[i])
		}
	}

	// Test Read path B -> A
	testPacket2 := []byte{0x10, 0x20, 0x30, 0x40}
	pf2, err := relay.NewPacketFrame(routeID, testPacket2)
	if err != nil {
		t.Fatalf("Failed to create packet frame for read test: %v", err)
	}
	binaryData, err := pf2.MarshalBinary()
	if err != nil {
		t.Fatalf("Failed to marshal packet frame: %v", err)
	}

	// Send frame from listener A to transport B's local address
	localAddrB := transportB.LocalAddr().(*net.UDPAddr)
	if _, err := listenerA.WriteToUDP(binaryData, localAddrB); err != nil {
		t.Fatalf("Failed to write test frame to transport B: %v", err)
	}

	// Transport B should read the frame
	readBuf := make([]byte, len(testPacket2)+100) // Extra space for safety
	readN, err := transportB.Read(readBuf)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if readN != len(testPacket2) {
		t.Errorf("Read returned wrong byte count: got %d, want %d", readN, len(testPacket2))
	}
	if !equalBytes(readBuf[:readN], testPacket2) {
		t.Errorf("Read returned wrong data: got %x, want %x", readBuf[:readN], testPacket2)
	}
}

// Helper function to compare byte slices
func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestPacketFrameTrailingBytes tests that frames with trailing bytes are rejected.
func TestPacketFrameTrailingBytes(t *testing.T) {
	routeID := [16]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	wgPacket := []byte{0xde, 0xad, 0xbe, 0xef}

	// Create a valid frame
	pf, err := relay.NewPacketFrame(routeID, wgPacket)
	if err != nil {
		t.Fatalf("Failed to create packet frame: %v", err)
	}
	binaryData, err := pf.MarshalBinary()
	if err != nil {
		t.Fatalf("Failed to marshal packet frame: %v", err)
	}

	// Append trailing bytes
	trailingData := append(binaryData, 0x00, 0x01, 0x02)

	// Attempt to unmarshal - should fail because of extra bytes
	_, err = relay.UnmarshalBinary(trailingData)
	if err == nil {
		t.Error("Expected error for packet frame with trailing bytes, got nil")
	} else {
		// Check that we got some kind of error (the exact message may vary based on implementation)
		// The important thing is that it doesn't succeed
		t.Logf("Correctly got error for trailing bytes: %v", err)
	}
}

// TestUDPTransportBasic tests basic UDP transport functionality (kept for backward compatibility).
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
