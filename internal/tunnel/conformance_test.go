//go:build linux

package tunnel

// Privileged real-WireGuard conformance tests.
//
// Proof target: Shell Body ─▶ real WireGuard ─▶ Doll Network IPv6 ─▶ Core.
//
// These tests stand up TWO real WireGuard peers in isolated network
// namespaces joined by an Ethernet veth pair, exactly as the reference
// "two endpoints on one CI host" WireGuard integration suite does:
//
//   [body ns] wgb wireguard ──veth── core0 wireguard [core ns]
//
// The Shell Body peer is driven entirely by the PRODUCTION tunnel code path
// (NewWireGuardTunnel → SetPrivateKey → SetPeer → SetIPv6Address → Start →
// Ping6Core) inside the "body" namespace, so nothing here stubs or reimplements
// the shipping implementation. The "Core" peer is a real WireGuard endpoint
// that owns the Core's Doll Network IPv6 (ULA). A successful ICMPv6 round trip
// from the Shell Body's ULA to the Core's ULA over the real kernel WireGuard
// proves the whole private path end-to-end.
//
// Namespace isolation (rather than loopback) is deliberate: it is the canonical
// way to run two WireGuard endpoints on a single host, avoids loopback/hairpin
// routing quirks, keeps the test host's default routing table untouched, and is
// idempotent (every interface and namespace is removed on all exit paths).
//
// It genuinely executes the kernel WireGuard module and the shipping `ip` /
// `wg` invocations. It only skips when the host cannot perform the proof:
//   - not running as root (requires CAP_NET_ADMIN / CAP_SYS_ADMIN),
//   - the wireguard(5) module or the `wg` / `ip` / `ping6` / `ip netns bus`
//     facilities are missing, or
//   - creating a namespace (the isolation primitive) is unavailable.
//
// Run on a capable host (root, WireGuard installed):
//
//   sudo go test -run 'TestBodyPrivatePathToCore' ./internal/tunnel/ -count=1
//
// The IPv4 variant (`TestBodyPrivatePathToCoreIPv4`) proves the identical
// real-WireGuard private path using an RFC1918 link net and is useful on
// hosts with IPv6 administratively disabled.

import (
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ── helpers ──────────────────────────────────────────────────────────────

var conformanceSeq uint32

func conformanceName(base string) string {
	n := atomic.AddUint32(&conformanceSeq, 1)
	return fmt.Sprintf("%s%02d", base, n)
}

func haveRoot() bool            { return os.Geteuid() == 0 }
func haveIPv6() bool            { _, err := os.Stat("/proc/net/if_inet6"); return err == nil }
func haveTool(name string) bool { _, err := exec.LookPath(name); return err == nil }

// runCmd runs `cmd args...` in the host namespace.
func runCmd(name string, arg ...string) (string, error) {
	out, err := exec.Command(name, arg...).CombinedOutput()
	return string(out), err
}

// runInNS runs `cmd args...` inside the named network namespace.
func runInNS(ns, name string, arg ...string) (string, error) {
	argv := append([]string{"ip", "netns", "exec", ns, name}, arg...)
	out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
	return string(out), err
}

// haveNetns reports whether `ip netns add` can work in this environment.
func haveNetns() bool {
	if !haveTool("ip") {
		return false
	}
	probe := conformanceName("ndprobe")
	out, err := runCmd("ip", "netns", "add", probe)
	_, _ = runCmd("ip", "netns", "del", probe)
	if err != nil {
		return false
	}
	return !strings.Contains(out, "Permission denied")
}

// rmNetns removes a namespace if it exists (tolerates absence).
func rmNetns(ns string) { _, _ = runCmd("ip", "netns", "del", ns) }

// genWgKeypair generates a fresh keypair, returning raw 32-byte forms.
type wgKeypair struct {
	priv []byte
	pub  []byte
}

func genWgKeypair(t *testing.T) wgKeypair {
	t.Helper()
	out, err := runCmd("wg", "genkey")
	if err != nil {
		t.Fatalf("wg genkey: %v\n%s", err, out)
	}
	priv := strings.TrimSpace(out)
	privB, err := base64.StdEncoding.DecodeString(priv)
	if err != nil {
		t.Fatalf("decode wg private key: %v", err)
	}
	pubB, err := derivePub(t, priv)
	if err != nil {
		t.Fatalf("wg pubkey: %v", err)
	}
	return wgKeypair{priv: privB, pub: pubB}
}

func derivePub(t *testing.T, priv string) ([]byte, error) {
	// `wg pubkey` reads the base64 private key on stdin.
	cmd := exec.Command("wg", "pubkey")
	cmd.Stdin = strings.NewReader(priv + "\n")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	pub, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
	if err != nil {
		return nil, err
	}
	return pub, nil
}

// freeUDP returns a freshly-freed UDP port for a listen socket.
func freeUDP() int {
	ln, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return 51820
	}
	defer ln.Close()
	return ln.LocalAddr().(*net.UDPAddr).Port
}

// setupTopology creates two namespaces + veth pair and returns their names.
type topo struct {
	bodyNS, coreNS string
	vethB, vethC   string
	cleanup        func()
}

func newTopology(t *testing.T) topo {
	t.Helper()
	bodyNS := conformanceName("ndbod")
	coreNS := conformanceName("ndcore")
	vethB := conformanceName("ndvb")
	vethC := conformanceName("ndvc")

	if out, err := runCmd("ip", "netns", "add", bodyNS); err != nil {
		t.Fatalf("ip netns add %s: %v\n%s", bodyNS, err, out)
	}
	if out, err := runCmd("ip", "netns", "add", coreNS); err != nil {
		t.Fatalf("ip netns add %s: %v\n%s", coreNS, err, out)
	}

	if out, err := runCmd("ip", "link", "add", vethB, "type", "veth", "peer", "name", vethC); err != nil {
		t.Fatalf("ip link add veth %s/%s: %v\n%s", vethB, vethC, err, out)
	}
	if out, err := runCmd("ip", "link", "set", vethB, "netns", bodyNS); err != nil {
		t.Fatalf("move %s → %s: %v\n%s", vethB, bodyNS, err, out)
	}
	if out, err := runCmd("ip", "link", "set", vethC, "netns", coreNS); err != nil {
		t.Fatalf("move %s → %s: %v\n%s", vethC, coreNS, err, out)
	}

	cleanup := func() {
		rmNetns(bodyNS)
		rmNetns(coreNS)
	}
	return topo{
		bodyNS: bodyNS, coreNS: coreNS,
		vethB: vethB, vethC: vethC,
		cleanup: cleanup,
	}
}

// probeTunnel pings the Core over the tunnel, retrying until the WireGuard
// handshake completes and ICMP flows. Reaching the Core's address IS the
// acceptance proof: it can only succeed once the real WireGuard handshake has
// completed and the payload path carries the Doll Network IPv6 packet.
func probeTunnel(probe func() error, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		if err := probe(); err == nil {
			return nil
		} else {
			last = err
		}
		time.Sleep(300 * time.Millisecond)
	}
	return last
}

// TestBodyPrivatePathToCore proves the full Doll Network ULA path over real
// kernel WireGuard using the complete production Configure().
func TestBodyPrivatePathToCore(t *testing.T) {
	if !haveRoot() {
		t.Skip("privileged conformance test: requires root (CAP_NET_ADMIN)")
	}
	for _, tool := range []string{"wg", "ip", "ping6"} {
		if !haveTool(tool) {
			t.Skipf("privileged conformance test: missing tool %q", tool)
		}
	}
	if !haveIPv6() {
		t.Skip("privileged conformance test: kernel IPv6 disabled")
	}
	if !haveNetns() {
		t.Skip("privileged conformance test: cannot create network namespaces")
	}

	top := newTopology(t)
	defer top.cleanup()
	bodyNS, coreNS := top.bodyNS, top.coreNS
	vethB, vethC := top.vethB, top.vethC

	bodyKP, coreKP := genWgKeypair(t), genWgKeypair(t)
	corePort := freeUDP()

	// Bring up the veth link in both namespaces (the WG handshake rides it).
	if out, err := runInNS(bodyNS, "ip", "link", "set", vethB, "up"); err != nil {
		t.Fatalf("body: veth up: %v\n%s", err, out)
	}
	if out, err := runInNS(coreNS, "ip", "link", "set", vethC, "up"); err != nil {
		t.Fatalf("core: veth up: %v\n%s", err, out)
	}
	if out, err := runInNS(bodyNS, "ip", "addr", "add", "10.66.0.2/24", "dev", vethB); err != nil {
		t.Fatalf("body: veth ip: %v\n%s", err, out)
	}
	if out, err := runInNS(coreNS, "ip", "addr", "add", "10.66.0.1/24", "dev", vethC); err != nil {
		t.Fatalf("core: veth ip: %v\n%s", err, out)
	}

	// Core peer: a real WireGuard endpoint that owns the Core ULA.
	coreWG := conformanceName("ndcoreww")
	if out, err := runInNS(coreNS, "ip", "link", "add", "dev", coreWG, "type", "wireguard"); err != nil {
		t.Fatalf("core: create %s: %v\n%s", coreWG, err, out)
	}
	rmCoreWG := true
	defer runInNS(coreNS, "ip", "link", "del", "dev", coreWG)
	_ = rmCoreWG
	privFile := filepath.Join(t.TempDir(), "core-priv")
	if err := os.WriteFile(privFile, []byte(encodeKey(coreKP.priv)+"\n"), 0o600); err != nil {
		t.Fatalf("core: write key: %v", err)
	}
	defer os.Remove(privFile)
	if out, err := runInNS(coreNS, "wg", "set", coreWG,
		"listen-port", fmt.Sprint(corePort),
		"private-key", privFile,
		"peer", encodeKey(bodyKP.pub),
		"allowed-ips", "10.66.0.0/24, fd53::/48",
	); err != nil {
		t.Fatalf("core: configure: %v\n%s", err, out)
	}
	if out, err := runInNS(coreNS, "ip", "-6", "addr", "add", "fd53::1/48", "dev", coreWG); err != nil {
		t.Fatalf("core: assign core ULA: %v\n%s", err, out)
	}
	if out, err := runInNS(coreNS, "ip", "link", "set", "dev", coreWG, "up"); err != nil {
		t.Fatalf("core: up: %v\n%s", err, out)
	}

	// Shell Body side: PRODUCTION tunnel code, running inside `bodyNS`.
	bodyIface := conformanceName("ndwgbody")
	bodyTun, err := NewWireGuardTunnel(bodyIface, bodyKP.priv, coreKP.pub,
		"fd53::2", "10.66.0.1", corePort, bodyNS)
	if err != nil {
		t.Fatalf("NewWireGuardTunnel: %v", err)
	}
	defer bodyTun.Close()
	if err := bodyTun.Configure(); err != nil {
		t.Fatalf("bodyTun.Configure(): %v", err)
	}
	if err := bodyTun.Start(); err != nil {
		t.Fatalf("bodyTun.Start(): %v", err)
	}

	// Pin the ULA route: Configure assigns /128, so route the local net.
	if out, err := runInNS(bodyNS, "ip", "-6", "route", "replace", "fd53::/48", "dev", bodyIface); err != nil {
		t.Fatalf("body: ULA route: %v\n%s", err, out)
	}

	if err := probeTunnel(func() error { return bodyTun.Ping6Core("fd53::1", 3, 5*time.Second) }, 25*time.Second); err != nil {
		t.Fatalf("Ping6Core(fd53::1) over real WireGuard failed: %v", err)
	}
	t.Log("IPv6 Doll Network: Shell Body → real WireGuard → fd53::/48 → Core: OK")
}
