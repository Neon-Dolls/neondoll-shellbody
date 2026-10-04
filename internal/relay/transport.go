package relay

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"

	"golang.org/x/net/websocket"
)

// MaxPacketSize is the maximum accepted packet size for WireGuard datagrams.
// This should be at least sufficient for normal WireGuard UDP datagrams.
const MaxPacketSize = 65535

// PacketFrame represents the binary frame structure for relay transport.
type PacketFrame struct {
	Version   [4]byte
	RouteID   [16]byte
	PacketLen uint16
	WireGuard []byte
}

// NewPacketFrame creates a new packet frame with the given route ID and WireGuard packet.
func NewPacketFrame(routeID [16]byte, wgPacket []byte) (*PacketFrame, error) {
	if len(wgPacket) > MaxPacketSize {
		return nil, fmt.Errorf("wireguard packet too large: %d > %d", len(wgPacket), MaxPacketSize)
	}
	if len(wgPacket) == 0 {
		return nil, errors.New("zero-length wireguard packet")
	}

	// Version 1 as specified in the protocol
	version := [4]byte{0, 0, 0, 1}

	return &PacketFrame{
		Version:   version,
		RouteID:   routeID,
		PacketLen: uint16(len(wgPacket)),
		WireGuard: wgPacket,
	}, nil
}

// MarshalBinary encodes the packet frame into binary format.
func (pf *PacketFrame) MarshalBinary() ([]byte, error) {
	buf := make([]byte, 4+16+2+len(pf.WireGuard))
	// Version (4 bytes)
	copy(buf[0:4], pf.Version[:])
	// Route ID (16 bytes)
	copy(buf[4:20], pf.RouteID[:])
	// Packet Length (2 bytes, big-endian)
	binary.BigEndian.PutUint16(buf[20:22], pf.PacketLen)
	// WireGuard packet
	copy(buf[22:], pf.WireGuard)
	return buf, nil
}

// UnmarshalBinary decodes binary data into a packet frame.
func UnmarshalBinary(data []byte) (*PacketFrame, error) {
	if len(data) < 4+16+2 {
		return nil, errors.New("packet frame too short")
	}

	pf := &PacketFrame{}
	// Version (4 bytes)
	copy(pf.Version[:], data[0:4])
	// Route ID (16 bytes)
	copy(pf.RouteID[:], data[4:20])
	// Packet Length (2 bytes, big-endian)
	pf.PacketLen = binary.BigEndian.Uint16(data[20:22])
	// WireGuard packet
	twgStart := 22
	twgEnd := twgStart + int(pf.PacketLen)
	if len(data) < twgEnd {
		return nil, errors.New("packet frame data too short for declared length")
	}
	pf.WireGuard = make([]byte, pf.PacketLen)
	copy(pf.WireGuard, data[twgStart:twgEnd])

	// Validate version (we only support version 1)
	if pf.Version[0] != 0 || pf.Version[1] != 0 || pf.Version[2] != 0 || pf.Version[3] != 1 {
		return nil, errors.New("unsupported packet frame version")
	}

	return pf, nil
}

// UDPTransport handles UDP packet transport to/from a relay.
type UDPTransport struct {
	conn    *net.UDPConn
	remote  *net.UDPAddr
	routeID [16]byte
}

// NewUDPTransport creates a new UDP transport to the given relay address.
func NewUDPTransport(relayAddr string, routeID [16]byte) (*UDPTransport, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", relayAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve relay UDP address %s: %w", relayAddr, err)
	}

	conn, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to dial UDP relay %s: %w", relayAddr, err)
	}

	return &UDPTransport{
		conn:    conn,
		remote:  udpAddr,
		routeID: routeID,
	}, nil
}

// Write sends a WireGuard packet through the UDP transport.
// Returns the number of WireGuard packet bytes written.
func (t *UDPTransport) Write(wgPacket []byte) (int, error) {
	pf, err := NewPacketFrame(t.routeID, wgPacket)
	if err != nil {
		return 0, err
	}

	binaryData, err := pf.MarshalBinary()
	if err != nil {
		return 0, err
	}

	if _, err := t.conn.Write(binaryData); err != nil {
		return 0, err
	}

	return len(wgPacket), nil
}

// Read reads a WireGuard packet from the UDP transport into b.
// Returns the number of bytes copied into b.
func (t *UDPTransport) Read(b []byte) (int, error) {
	// Read with a reasonable buffer size
	buf := make([]byte, MaxPacketSize+4+16+2) // frame overhead
	n, err := t.conn.Read(buf)
	if err != nil {
		return 0, err
	}

	if n == 0 {
		return 0, io.EOF
	}

	pf, err := UnmarshalBinary(buf[:n])
	if err != nil {
		return 0, err
	}

	if len(pf.WireGuard) > len(b) {
		return 0, fmt.Errorf("wireguard packet too large for buffer: %d > %d", len(pf.WireGuard), len(b))
	}
	copy(b, pf.WireGuard)
	return len(pf.WireGuard), nil
}

// Close closes the UDP transport.
func (t *UDPTransport) Close() error {
	return t.conn.Close()
}

// LocalAddr returns the local network address.
func (t *UDPTransport) LocalAddr() net.Addr {
	return t.conn.LocalAddr()
}

// RemoteAddr returns the remote network address.
func (t *UDPTransport) RemoteAddr() net.Addr {
	return t.remote
}

// WSSTransport handles WSS (WebSocket Secure) packet transport to/from a relay.
type WSSTransport struct {
	ws      *websocket.Conn
	routeID [16]byte
}

// NewWSSTransport creates a new WSS transport to the given relay URL.
func NewWSSTransport(relayURL string, routeID [16]byte, origin string) (*WSSTransport, error) {
	// Use websocket.Dial for a blocking connection, or dial with context for timeout
	config, err := websocket.NewConfig(relayURL, origin)
	if err != nil {
		return nil, fmt.Errorf("failed to create websocket config: %w", err)
	}
	// Optionally configure TLS, headers, etc. here

	ws, err := websocket.DialConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to dial WSS relay %s: %w", relayURL, err)
	}

	return &WSSTransport{
		ws:      ws,
		routeID: routeID,
	}, nil
}

// Write sends a WireGuard packet through the WSS transport.
// Returns the number of WireGuard packet bytes written.
func (t *WSSTransport) Write(wgPacket []byte) (int, error) {
	pf, err := NewPacketFrame(t.routeID, wgPacket)
	if err != nil {
		return 0, err
	}

	binaryData, err := pf.MarshalBinary()
	if err != nil {
		return 0, err
	}

	// WebSocket connection expects []byte for binary messages
	if _, err = t.ws.Write(binaryData); err != nil {
		return 0, err
	}

	return len(wgPacket), nil
}

// Read reads a WireGuard packet from the WSS transport into b.
// Returns the number of bytes copied into b.
func (t *WSSTransport) Read(b []byte) (int, error) {
	// WebSocket message receipt
	var buf = make([]byte, MaxPacketSize+4+16+2) // frame overhead
	n, err := t.ws.Read(buf)
	if err != nil {
		return 0, err
	}

	if n == 0 {
		return 0, io.EOF
	}

	pf, err := UnmarshalBinary(buf[:n])
	if err != nil {
		return 0, err
	}

	if len(pf.WireGuard) > len(b) {
		return 0, fmt.Errorf("wireguard packet too large for buffer: %d > %d", len(pf.WireGuard), len(b))
	}
	copy(b, pf.WireGuard)
	return len(pf.WireGuard), nil
}

// Close closes the WSS transport.
func (t *WSSTransport) Close() error {
	return t.ws.Close()
}

// LocalAddr returns the local network address (not really applicable for WS, but implement for interface).
func (t *WSSTransport) LocalAddr() net.Addr {
	return nil
}

// RemoteAddr returns the remote network address.
func (t *WSSTransport) RemoteAddr() net.Addr {
	return nil
}
