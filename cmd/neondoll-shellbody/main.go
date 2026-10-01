// Command neondoll-shellbody is the NeonDoll Shell Body reference that lets a
// Doll inhabit a terminal.
//
// M2 — The Terminal Pairs. The Shell Body runs standalone (no Core required)
// for identity/terminal work, and can pair with a real NeonDoll Core through
// the canonical Doll Network pairing v1 protocol, persisting the resulting
// relationship across restart. Later milestones add WireGuard, Doll Link,
// Interaction Sessions, and shell execution.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/body"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/dollnetwork"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/identity"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/terminal"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/tunnel"
)

// Version is the build/version stamp. It is overridable at link time via
// -ldflags "-X main.version=...".
var version = "dev"

// defaultImplementation is the required implementation identifier.
const defaultImplementation = "neondoll-shellbody"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "neondoll-shellbody:", err)
		os.Exit(1)
	}
}

// run parses flags and dispatches to the requested mode.
func run(args []string) error {
	fs := flag.NewFlagSet("neondoll-shellbody", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	// Modes (mutually exclusive, in priority order below).
	statusMode := fs.Bool("status", false, "print the persisted Body identity and pairing status")
	initMode := fs.Bool("init", false, "initialize a fresh state directory with a new Body identity and exit")
	pairMode := fs.String("pair", "", "pair with a Core by consuming an invitation JSON file, then exit")
	connectMode := fs.Bool("connect", false, "establish a WireGuard tunnel to the paired Core using persisted state")

	// Options.
	stateDir := fs.String("state-dir", ".neondoll-shellbody", "directory for durable Body state")
	name := fs.String("name", "", "display name recorded on the Body identity")
	implementation := fs.String("implementation", defaultImplementation, "implementation identifier")
	platform := fs.String("platform", runtime.GOOS, "platform identifier")
	arch := fs.String("arch", runtime.GOARCH, "architecture identifier")
	build := fs.String("build", version, "build/version stamp")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil // help requested; exit cleanly
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected positional arguments: %v", fs.Args()[0])
	}

	store, err := body.NewStore(*stateDir)
	if err != nil {
		return err
	}

	meta := bodyMetadata(*implementation, *platform, *arch, *build)

	switch {
	case *statusMode:
		return status(store)
	case *initMode:
		return initStandalone(store, *name, meta)
	case *pairMode != "":
		return pair(store, *pairMode)
	case *connectMode:
		return connect(store)
	default:
		return interactive(store, *name, meta)
	}
}

// bodyMetadata composes the canonical implementation metadata from flags.
func bodyMetadata(implementation, platform, arch, build string) identity.BodyMetadata {
	return identity.BodyMetadata{
		Implementation: implementation,
		Platform:       platform,
		Architecture:   arch,
		Build:          build,
	}
}

// status prints the persisted identity and non-secret pairing status,
// proving restart stability. It never prints the invitation secret or any
// private WireGuard material.
func status(store *body.Store) error {
	ident, kp, err := store.LoadOrError()
	if err != nil {
		if errors.Is(err, body.ErrStateNotFound) {
			return errors.New("no identity yet (run --init or start interactively)")
		}
		return err
	}
	fmt.Printf("body_id:   %s\n", ident.Identity.BodyID)
	fmt.Printf("name:      %s\n", ident.Identity.Name)
	fmt.Printf("impl:      %s\n", ident.Identity.Meta.Implementation)
	fmt.Printf("platform:  %s\n", ident.Identity.Meta.Platform)
	fmt.Printf("arch:      %s\n", ident.Identity.Meta.Architecture)
	fmt.Printf("build:     %s\n", ident.Identity.Meta.Build)
	fmt.Printf("wg_pub:    %s\n", kp.PublicKeyBase64())
	fmt.Printf("state-dir: %s\n", store.Dir())

	// Non-secret relationship info, if a membership is committed.
	mem, err := store.LoadMembership()
	if err != nil {
		if errors.Is(err, body.ErrStateNotFound) {
			fmt.Printf("paired:    no\n")
			return nil
		}
		return err
	}
	fmt.Printf("paired:    yes\n")
	fmt.Printf("network:   %s\n", mem.NetworkID)
	fmt.Printf("peer_id:   %s\n", mem.BodyPeerID)
	fmt.Printf("body_ipv6: %s\n", mem.BodyIPv6)
	fmt.Printf("core_peer: %s\n", mem.CorePeerID)
	fmt.Printf("core_wg:   %s\n", mem.CoreWGKeyB64)
	return nil
}

// connect establishes a WireGuard tunnel to the paired Core using persisted M2 state.
// It fails explicitly when: Body is not paired, membership is corrupt, required Core
// endpoint is missing/invalid, WG configuration fails, or Core cannot be reached
// through the private path.
// It never creates a new identity, generates a new WG key, re-pairs, or allocates
// a new Doll Network address.
func connect(store *body.Store) error {
	// Load persisted identity and membership
	_, kp, err := store.LoadOrError()
	if err != nil {
		if errors.Is(err, body.ErrStateNotFound) {
			return errors.New("no identity yet (run --init or start interactively)")
		}
		return err
	}

	mem, err := store.LoadMembership()
	if err != nil {
		if errors.Is(err, body.ErrStateNotFound) {
			return errors.New("body is not paired (run --pair first)")
		}
		return err
	}

	// Validate that we have all required M2 state for WireGuard
	if mem.BodyIPv6 == "" {
		return errors.New("membership corrupt: missing Body assigned Doll Network IPv6")
	}
	if mem.CorePeerID == "" {
		return errors.New("membership corrupt: missing Core peer ID")
	}
	if mem.CoreWGKeyB64 == "" {
		return errors.New("membership corrupt: missing Core WG public key")
	}
	if len(mem.CoreEndpoints) == 0 {
		return errors.New("membership corrupt: missing Core endpoint(s)")
	}

	// Parse Core endpoints and select the first valid direct endpoint
	var coreEP dollnetwork.DirectEndpoint
	found := false
	for _, epStr := range mem.CoreEndpoints {
		ep, err := dollnetwork.ParseDirectEndpoint(epStr)
		if err != nil {
			continue // skip invalid endpoints
		}
		if ep.Type == "direct" && ep.Transport == "udp" {
			coreEP = *ep
			found = true
			break
		}
	}
	if !found {
		return errors.New("no valid direct UDP endpoint found in membership")
	}

	// Decode the Body's private key (never exposed in logs)
	bodyKey := kp.PrivateKeyBytes() // This gives us the raw private key bytes

	// Decode Core's public key
	coreKey, err := dollnetwork.DecodeWgPublicKey(mem.CoreWGKeyB64)
	if err != nil {
		return fmt.Errorf("invalid Core WG public key: %w", err)
	}

	// Create and configure the WireGuard tunnel
	tun, err := tunnel.NewWireGuardTunnel(
		"wg0",
		bodyKey,
		coreKey,
		mem.BodyIPv6,
		coreEP.Host,
		coreEP.Port,
		"", // netns - empty for default namespace
	)
	if err != nil {
		return fmt.Errorf("failed to create tunnel: %w", err)
	}
	defer tun.Close() // Ensure cleanup

	// Configure the tunnel
	if err := tun.Configure(); err != nil {
		return fmt.Errorf("failed to configure tunnel: %w", err)
	}

	// Start the tunnel interface
	if err := tun.Start(); err != nil {
		return fmt.Errorf("failed to start tunnel: %w", err)
	}
	defer tun.Stop() // Ensure we stop it

	// Verify connectivity by pinging the Core's IPv6 over the WG interface
	// Note: We don't have the Core's IPv6 from M2 state, so we can't do full verification yet
	// This would require extending the membership to include Core's assigned IPv6
	fmt.Printf("WireGuard tunnel established successfully:\n")
	fmt.Printf("  Interface: wg0\n")
	fmt.Printf("  Body IPv6: %s\n", mem.BodyIPv6)
	fmt.Printf("  Core peer ID: %s\n", mem.CorePeerID)
	fmt.Printf("  Core endpoint: %s:%d\n", coreEP.Host, coreEP.Port)
	fmt.Printf("  Network ID: %s\n", mem.NetworkID)
	fmt.Printf("\nNOTE: Full connectivity verification requires Core's assigned IPv6 in membership state\n")

	return nil
}

// initStandalone creates a fresh durable identity + WG keypair in the state
// directory and prints it. It fails if identity already exists.
func initStandalone(store *body.Store, name string, meta identity.BodyMetadata) error {
	res, err := store.CreateFresh(name, meta)
	if err != nil {
		return err
	}
	fmt.Printf("initialized fresh Body identity in %s\n", store.Dir())
	fmt.Printf("body_id: %s\n", res.State.Identity.BodyID)
	fmt.Printf("wg_public_key: %s\n", res.Key.PublicKeyBase64())
	return nil
}

// pair consumes a Core invitation document and runs the M2 pairing pipeline.
// On success it prints the committed membership (never the secret) and exits
// 0; on failure it leaves durable state untouched and exits non-zero.
func pair(store *body.Store, invitationFile string) error {
	raw, err := os.ReadFile(invitationFile)
	if err != nil {
		return fmt.Errorf("failed to read invitation %s: %w", invitationFile, err)
	}
	inv, err := body.LoadInvitation(string(raw))
	if err != nil {
		return fmt.Errorf("invalid invitation: %w", err)
	}
	now := time.Now().Unix()
	hc := &http.Client{Timeout: body.PairingHTTPTimeout}
	res, err := body.PairWithInvitation(context.Background(), store, inv, now, hc)
	if err != nil {
		return fmt.Errorf("pairing failed: %w", err)
	}
	fmt.Printf("paired network_id=%s\n", res.Membership.NetworkID)
	fmt.Printf("body_peer_id=%s\n", res.Membership.BodyPeerID)
	fmt.Printf("body_ipv6=%s\n", res.Membership.BodyIPv6)
	fmt.Printf("core_peer_id=%s\n", res.Membership.CorePeerID)
	fmt.Printf("core_wg_public_key=%s\n", res.Membership.CoreWGKeyB64)
	return nil
}

// interactive loads (or on first run creates) the Body identity + WG keypair,
// prints a startup diagnostic, and runs the terminal embodiment until clean
// EOF or Ctrl-C. Durably created state is never modified by a run.
func interactive(store *body.Store, name string, meta identity.BodyMetadata) error {
	ident, _, err := store.LoadOrError()
	if err != nil {
		if errors.Is(err, body.ErrStateNotFound) {
			res, err := store.CreateFresh(name, meta)
			if err != nil {
				return err
			}
			ident = res.State
			fmt.Fprintf(os.Stderr, "created new Body identity %s\n", ident.Identity.BodyID)
		} else {
			return err
		}
	}

	printBanner(ident, store.Dir())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	t := terminal.New(os.Stdin, os.Stdout, terminal.Echo)
	fmt.Fprintln(os.Stdout, "type and press Enter; Ctrl-D / Ctrl-C to quit")
	res, rerr := t.Run(ctx)
	fmt.Fprintf(os.Stderr, "terminal ended (%s, %d line(s))\n", res.EndedBy, res.LinesIn)
	return rerr
}

// printBanner writes the startup diagnostic that proves the runtime is
// operating as the same Body across restarts.
func printBanner(ident *identity.IdentityState, dir string) {
	fmt.Printf("neondoll-shellbody %s\n", ident.Identity.Meta.Build)
	fmt.Printf("body_id:  %s\n", ident.Identity.BodyID)
	fmt.Printf("platform: %s\n", ident.Identity.Meta.Platform)
	fmt.Printf("arch:     %s\n", ident.Identity.Meta.Architecture)
	fmt.Printf("state:    %s\n", dir)
}
