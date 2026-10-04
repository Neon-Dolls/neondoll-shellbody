package relay_test

import (
	"bytes"
	"encoding/binary"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/relay"
	"golang.org/x/net/websocket"
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

// TestWSSTransportWrite tests that WSSTransport.Write sends a valid frame to a WebSocket server.
func TestWSSTransportWrite(t *testing.T) {
	// Define test data
	routeID := [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	payloadA := []byte{0xde, 0xad, 0xbe, 0xef, 0xca, 0xfe, 0xba, 0xbe}

	// Channel to receive the payload from the WebSocket server
	received := make(chan []byte, 1)
	// Set up WebSocket server using httptest
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "websocket" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		ws := websocket.Server{
			Handler: func(wsConn *websocket.Conn) {
				var buf = make([]byte, 4096)
				n, err := wsConn.Read(buf)
				if err != nil || n == 0 {
					return
				}
				buf = buf[:n]
				if len(buf) < 4+16+2 {
					return
				}
				if buf[0] != 0x00 || buf[1] != 0x00 || buf[2] != 0x00 || buf[3] != 0x01 {
					return
				}
				var receivedRouteID [16]byte
				copy(receivedRouteID[:], buf[4:4+16])
				if receivedRouteID != routeID {
					return
				}
				packetLen := binary.BigEndian.Uint16(buf[4+16 : 4+16+2])
				expectedTotal := 4 + 16 + 2 + int(packetLen)
				if len(buf) != expectedTotal {
					return
				}
				receivedPayload := buf[4+16+2 : 4+16+2+packetLen]
				received <- receivedPayload
			},
			Handshake: func(c *websocket.Config, req *http.Request) error { return nil },
		}
		ws.ServeHTTP(w, r)
	}))
	ts.Start()
	defer ts.Close()

	// Create the WSSTransport
	wsURL := ts.URL
	if len(wsURL) >= 5 && wsURL[:5] == "http:" {
		wsURL = "ws:" + wsURL[5:]
	}
	transport, err := relay.NewWSSTransport(wsURL, routeID, "http://localhost")
	if err != nil {
		t.Fatalf("failed to create WSS transport: %v", err)
	}
	defer transport.Close()

	// Write the payload
	n, err := transport.Write(payloadA)
	if err != nil {
		t.Fatalf("transport.Write failed: %v", err)
	}
	if n != len(payloadA) {
		t.Fatalf("transport.Write wrote %d bytes, expected %d", n, len(payloadA))
	}

	// Wait for the server to receive and validate
	select {
	case receivedPayload, ok := <-received:
		if !ok {
			t.Fatalf("receiver channel closed unexpectedly")
		}
		if !bytes.Equal(receivedPayload, payloadA) {
			t.Fatalf("server received wrong payload: expected %x, got %x", payloadA, receivedPayload)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for server to receive frame")
	}
}

// TestWSSTransportRead tests that WSSTransport.Read can read a valid frame from a WebSocket server.
func TestWSSTransportRead(t *testing.T) {
	// Define test data
	routeID := [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	payloadB := []byte{0x11, 0x22, 0x33, 0x44}

	// Channel to know when the server has sent the frame
	sent := make(chan struct{}, 1)
	// Set up WebSocket server using httptest
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "websocket" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		ws := websocket.Server{
			Handler: func(wsConn *websocket.Conn) {
				// Create a frame to send
				testFrame := &relay.PacketFrame{
					Version:   [4]byte{0, 0, 0, 1},
					RouteID:   routeID,
					PacketLen: uint16(len(payloadB)),
					WireGuard: payloadB,
				}
				testBinary, err := testFrame.MarshalBinary()
				if err != nil {
					return
				}
				// Send the frame
				if _, err = wsConn.Write(testBinary); err != nil {
					return
				}
				// Signal that we've sent the frame
				close(sent)
			},
			Handshake: func(c *websocket.Config, req *http.Request) error { return nil },
		}
		ws.ServeHTTP(w, r)
	}))
	ts.Start()
	defer ts.Close()

	// Create the WSSTransport
	wsURL := ts.URL
	if len(wsURL) >= 5 && wsURL[:5] == "http:" {
		wsURL = "ws:" + wsURL[5:]
	}
	transport, err := relay.NewWSSTransport(wsURL, routeID, "http://localhost")
	if err != nil {
		t.Fatalf("failed to create WSS transport: %v", err)
	}
	defer transport.Close()

	// Wait for the server to send the frame
	select {
	case <-sent:
		// Server has sent the frame, now we can read it
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for server to send frame")
	}

	// Read via transport
	readBuf := make([]byte, len(payloadB))
	n, err := transport.Read(readBuf)
	if err != nil {
		t.Fatalf("transport.Read failed: %v", err)
	}
	if n != len(payloadB) {
		t.Fatalf("transport.Read read %d bytes, expected %d", n, len(payloadB))
	}
	if !bytes.Equal(readBuf[:n], payloadB) {
		t.Fatalf("transport.Read data mismatch: expected %x, got %x", payloadB, readBuf[:n])
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