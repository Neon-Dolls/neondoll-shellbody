package tunnel

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// WireGuardTunnel manages a WireGuard interface for connecting to a Core peer.
type WireGuardTunnel struct {
	ifaceName string
	bodyKey   []byte // 32-byte WireGuard private key (NEVER exposed in logs)
	bodyIPv6  string
	coreKey   []byte // 32-byte WireGuard public key
	coreHost  string
	corePort  int
	netns     string // network namespace, empty for default
}

// NewWireGuardTunnel creates a new tunnel manager.
// bodyKey must be 32 bytes and is zeroed on Close.
// coreKey must be 32 bytes.
func NewWireGuardTunnel(ifaceName string, bodyKey, coreKey []byte, bodyIPv6 string, coreHost string, corePort int, netns string) (*WireGuardTunnel, error) {
	if len(bodyKey) != 32 || len(coreKey) != 32 {
		return nil, errors.New("wireguard: keys must be 32 bytes")
	}
	if ifaceName == "" {
		ifaceName = "wg0"
	}
	if coreHost == "" {
		return nil, errors.New("wireguard: core host must not be empty")
	}
	if corePort < 1 || corePort > 65535 {
		return nil, fmt.Errorf("wireguard: core port %d out of range", corePort)
	}
	return &WireGuardTunnel{
		ifaceName: ifaceName,
		bodyKey:   bytes.Clone(bodyKey),
		bodyIPv6:  bodyIPv6,
		coreKey:   bytes.Clone(coreKey),
		coreHost:  coreHost,
		corePort:  corePort,
		netns:     netns,
	}, nil
}

// Close zeros the private key and removes the interface.
func (t *WireGuardTunnel) Close() error {
	if t.bodyKey != nil {
		for i := range t.bodyKey {
			t.bodyKey[i] = 0
		}
		t.bodyKey = nil
	}
	if t.coreKey != nil {
		for i := range t.coreKey {
			t.coreKey[i] = 0
		}
		t.coreKey = nil
	}
	return t.Delete()
}

// Configure sets up the WireGuard interface with keys and IPs.
// Does not bring the interface up.
func (t *WireGuardTunnel) Configure() error {
	if err := t.Create(); err != nil {
		return fmt.Errorf("create interface: %w", err)
	}
	if err := t.SetPrivateKey(); err != nil {
		return fmt.Errorf("set private key: %w", err)
	}
	if err := t.SetIPv6Address(); err != nil {
		return fmt.Errorf("set IPv6 address: %w", err)
	}
	if err := t.SetPeer(); err != nil {
		return fmt.Errorf("set peer: %w", err)
	}
	return nil
}

// Start brings the interface up.
// Call Configure() first.
func (t *WireGuardTunnel) Start() error {
	return t.SetUp()
}

// Stop brings the interface down.
func (t *WireGuardTunnel) Stop() error {
	return t.SetDown()
}

// Delete removes the interface.
func (t *WireGuardTunnel) Delete() error {
	_, err := t.exec("ip", "link", "delete", "dev", t.ifaceName)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete interface: %w", err)
	}
	return nil
}

// exec runs a command inside the tunnel's network namespace, if one is set.
// When t.netns is empty it runs in the default namespace. Namespace execution
// is scoped through `ip netns exec <ns> ...` — the one form that works for
// every subtool (`ip`, `wg`, `ping6`), since `-netns` is only a global flag of
// `ip` and not understood by `wg` or `ping6`.
func (t *WireGuardTunnel) exec(name string, arg ...string) ([]byte, error) {
	if t.netns != "" {
		argv := append([]string{"ip", "netns", "exec", t.netns, name}, arg...)
		cmd := exec.Command(argv[0], argv[1:]...)
		return cmd.CombinedOutput()
	}
	cmd := exec.Command(name, arg...)
	return cmd.CombinedOutput()
}

// Create creates the WireGuard interface.
func (t *WireGuardTunnel) Create() error {
	_, err := t.exec("ip", "link", "add", "dev", t.ifaceName, "type", "wireguard")
	if err != nil {
		return fmt.Errorf("ip link add: %w", err)
	}
	return nil
}

// SetPrivateKey sets the WireGuard private key.
// wg(8) reads the key from a file that must contain the base64-encoded raw
// 32 bytes (the same form wg-quick uses for /etc/wireguard/*.conf). The raw
// bytes are never written to the file; only their base64 form is.
func (t *WireGuardTunnel) SetPrivateKey() error {
	// Write the base64 key to a temporary file and use `wg set`.
	tmpfile, err := os.CreateTemp("", "wg-body-key-*")
	if err != nil {
		return fmt.Errorf("temp file: %w", err)
	}
	defer os.Remove(tmpfile.Name())

	if _, err = tmpfile.WriteString(encodeKey(t.bodyKey) + "\n"); err != nil {
		return fmt.Errorf("write key: %w", err)
	}
	if err = tmpfile.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	_, err = t.exec("wg", "set", t.ifaceName, "private-key", tmpfile.Name())
	if err != nil {
		return fmt.Errorf("wg set private-key: %w", err)
	}
	return nil
}

// SetIPv6Address assigns the Body's Doll Network IPv6 to the interface.
func (t *WireGuardTunnel) SetIPv6Address() error {
	// The Body IPv6 is typically a /128 address
	_, err := t.exec("ip", "-6", "address", "add", t.bodyIPv6+"/128", "dev", t.ifaceName)
	if err != nil {
		return fmt.Errorf("ip addr add: %w", err)
	}
	return nil
}

// SetPeer configures the Core peer.
func (t *WireGuardTunnel) SetPeer() error {
	// wg set wg0 peer <core-pubkey> allowed-ips ::/0 endpoint <host>:<port>
	_, err := t.exec(
		"wg", "set", t.ifaceName,
		"peer", encodeKey(t.coreKey),
		"allowed-ips", "::/0",
		"endpoint", net.JoinHostPort(t.coreHost, strconv.Itoa(t.corePort)),
	)
	if err != nil {
		return fmt.Errorf("wg set peer: %w", err)
	}
	return nil
}

// SetUp brings the interface up.
func (t *WireGuardTunnel) SetUp() error {
	_, err := t.exec("ip", "link", "set", "dev", t.ifaceName, "up")
	if err != nil {
		return fmt.Errorf("ip link set up: %w", err)
	}
	return nil
}

// SetDown brings the interface down.
func (t *WireGuardTunnel) SetDown() error {
	_, err := t.exec("ip", "link", "set", "dev", t.ifaceName, "down")
	if err != nil {
		return fmt.Errorf("ip link set down: %w", err)
	}
	return nil
}

// encodeKey converts a 32-byte key to base64 for wg(8).
func encodeKey(key []byte) string {
	return base64.StdEncoding.EncodeToString(key)
}

// Ping6Core pings the Core's IPv6 over the WireGuard interface to verify connectivity.
// coreIPv6 should be the Core's Doll Network IPv6 (from membership.CoreIPv6).
func (t *WireGuardTunnel) Ping6Core(coreIPv6 string, count int, timeout time.Duration) error {
	args := []string{"-I", t.ifaceName, "-c", strconv.Itoa(count), "-W", fmt.Sprintf("%d", int(timeout.Seconds())), coreIPv6}
	out, err := t.exec("ping6", args...)
	if err != nil {
		return fmt.Errorf("ping6 failed: %w, output: %s", err, string(out))
	}
	return nil
}

// Show prints the current interface configuration (without keys).
func (t *WireGuardTunnel) Show() (string, error) {
	out, err := t.exec("wg", "show", t.ifaceName)
	if err != nil {
		return "", fmt.Errorf("wg show: %w", err)
	}
	return string(out), nil
}
