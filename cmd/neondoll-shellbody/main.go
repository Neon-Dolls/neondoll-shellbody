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
	"crypto/ed25519"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/auth"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/body"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/dollnetwork"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/enrollment"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/execution"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/execution/process"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/identity"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/interaction"
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
	enrollMode := fs.String("enroll", "", "enroll as a Body with a Core 4 instance by consuming an invitation JSON file (requires --core-url; Core identity is discovered via GET /v1/core/identity unless --core-id/--core-key are supplied)")
	enrollOwnerMode := fs.Bool("enroll-owner", false, "run the owner-approved Core 4 enrollment ceremony: begin a body_request, print the approval code for `neondollctl body approve CODE`, poll until the owner's decision is recorded, and persist the receipt (requires --core-url; discovers Core identity unless --core-id/--core-key are supplied)")
	serveMode := fs.String("serve", "", "run a Core 4 Doll Link execution session against a Core ws URL (e.g. ws://host:port/ws)")
	serveCore5Mode := fs.String("serve-core5", "", "run a Core 5 Doll Link execution session against a Core ws URL (e.g. ws://host:port/ws)")
	converseMode := fs.String("converse", "", "open a real Core 5 Interaction Session and run the terminal conversation against a Core ws URL (e.g. ws://host:port/ws)")
	dollID := fs.String("doll", "core", "which Doll to address in --converse (default: core, the Core's local Doll)")

	// Options.
	stateDir := fs.String("state-dir", ".neondoll-shellbody", "directory for durable Body state")
	name := fs.String("name", "", "display name recorded on the Body identity")
	implementation := fs.String("implementation", defaultImplementation, "implementation identifier")
	platform := fs.String("platform", runtime.GOOS, "platform identifier")
	arch := fs.String("arch", runtime.GOARCH, "architecture identifier")
	build := fs.String("build", version, "build/version stamp")

	coreURL := fs.String("core-url", "", "Core base HTTP URL for enrollment (e.g. http://host:port)")
	coreID := fs.String("core-id", "", "expected Core identity (core_id); cross-checked against discovery when both are present, required only for the offline --core-key path")
	coreKey := fs.String("core-key", "", "Core's Ed25519 public key (base64url), an explicit trust anchor for Body Authentication v1 (optional; when omitted, discovery is used)")

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
	case *enrollMode != "":
		return enroll(store, *enrollMode, *coreURL, *coreID, *coreKey)
	case *enrollOwnerMode:
		return enrollOwner(store, *coreURL, *coreID, *coreKey)
	case *serveMode != "":
		return serve(store, *serveMode)
	case *serveCore5Mode != "":
		return serveCore5(store, *serveCore5Mode)
	case *converseMode != "":
		return converse(store, *converseMode, *dollID)
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

	// Resolve an unambiguous direct WireGuard UDP endpoint for Core from the
	// persisted state. This supports both the canonical structured descriptor
	// (M3) and the M2 URL form actually persisted by real pairing; it fails
	// closed if no unambiguous direct endpoint is present.
	coreEP, err := dollnetwork.ResolveDirectEndpoint(mem.CoreEndpoints)
	if err != nil {
		return err
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

	// Select Core IPv6 from persisted membership and verify connectivity
	coreIPv6 := mem.SelectCoreIPv6()
	if coreIPv6 == "" {
		return errors.New("no valid Core IPv6 address found in membership")
	}

	// Verify connectivity by pinging the Core's IPv6 over the WG interface
	if err := tun.Ping6Core(coreIPv6, 1, 5*time.Second); err != nil {
		return fmt.Errorf("failed to reach Core over WG: %w", err)
	}

	fmt.Printf("WireGuard tunnel established successfully:\n")
	fmt.Printf("  Interface: wg0\n")
	fmt.Printf("  Body IPv6: %s\n", mem.BodyIPv6)
	fmt.Printf("  Core IPv6: %s\n", coreIPv6)
	fmt.Printf("  Core peer ID: %s\n", mem.CorePeerID)
	fmt.Printf("  Core endpoint: %s:%d\n", coreEP.Host, coreEP.Port)
	fmt.Printf("  Network ID: %s\n", mem.NetworkID)

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

// enrollStage bundles the durable identity, WireGuard key rendering, resolved
// Core trust anchor, and HTTP/context plumbing shared by both enrollment
// ceremonies. Extracting it keeps the trust-binding policy identical whether
// the Body enrolls by invitation redemption or by the owner-approved begin /
// approve / result ceremony.
type enrollStage struct {
	EnrSt  *enrollment.Store
	EnrID  *enrollment.Identity
	WGStd  string
	HC     *http.Client
	Ctx    context.Context
	Stop   func()
	Trust  *enrollment.CoreTrustResolution
	CorePK ed25519.PublicKey
	Cli    *enrollment.Client
}

// stageEnrollment loads (or creates) the durable enrollment identity, renders
// the Body's WireGuard public key, resolves Core's trust anchor, and builds an
// enrollment client. Callers MUST invoke st.Stop() (or defer it) once done.
func stageEnrollment(store *body.Store, coreURL, coreID, coreKeyRaw string) (*enrollStage, error) {
	enrStore := enrollment.NewStore(store.Dir())
	// Load (or create) the durable enrollment Ed25519 identity. Only genuinely
	// absent identity state — the identity file does not exist, as on a first
	// enrollment — may mint a fresh identity. Any other load failure (corrupt
	// seed, unreadable file, incomplete persisted state) fails closed: we never
	// silently discard existing identity material and re-key a Body that may
	// already be enrolled with a Core.
	var enrIdent *enrollment.Identity
	var err error
	enrIdent, err = enrStore.LoadIdentity()
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("enrollment: failed to read existing identity state: %w", err)
		}
		enrIdent, err = enrollment.NewIdentity()
		if err != nil {
			return nil, err
		}
		if err := enrStore.SaveIdentity(enrIdent); err != nil {
			return nil, err
		}
	}

	// Render the Body's WireGuard public key in the canonical Doll Network
	// wg_public_key form (STANDARD base64 of the 32-byte X25519 key).
	_, kp, err := store.LoadOrError()
	if err != nil {
		if errors.Is(err, body.ErrStateNotFound) {
			return nil, errors.New("run --init first to create a Body identity + WireGuard key")
		}
		return nil, err
	}
	wgStd := kp.PublicKeyBase64()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	hc := &http.Client{Timeout: 30 * time.Second}

	// Resolve Core's trust anchor. Discovery is the default bootstrap path and
	// any operator-supplied --core-key / --core-id is cross-checked against the
	// live document; the policy also refuses to silently replace an existing
	// pinned anchor. Discovery alone never establishes trust — the anchor is
	// bound later by receipt signature verification inside whichever ceremony
	// runs.
	var existing *enrollment.Record
	loaded, lerr := enrStore.LoadRecord()
	if lerr != nil {
		// Only ErrStateNotFound means "no existing enrollment" (fresh state).
		// Any other error — corrupt or unreadable state file — fails closed so
		// we never silently discard an unverifiable trust anchor and re-enroll.
		if !errors.Is(lerr, enrollment.ErrStateNotFound) {
			return nil, fmt.Errorf("enrollment: failed to read existing enrollment state: %w", lerr)
		}
	} else {
		existing = loaded
	}
	res, err := enrollment.ResolveCoreTrust(ctx, hc, coreURL, coreID, coreKeyRaw, existing)
	if err != nil {
		stop()
		return nil, err
	}
	corePub := ed25519.PublicKey(res.PublicKey)
	cli := enrollment.NewClient(coreURL, res.CoreID, enrIdent, wgStd, hc)
	return &enrollStage{
		EnrSt: enrStore, EnrID: enrIdent, WGStd: wgStd, HC: hc, Ctx: ctx, Stop: stop,
		Trust: res, CorePK: corePub, Cli: cli,
	}, nil
}

// persistEnrollment binds the resolved Core trust anchor to the Core-signed
// receipt, persists the record, and prints the enrolled identity. This is the
// single completion path for both enrollment ceremonies. It refuses a receipt
// whose signature does not verify under the resolved public key or that names
// a different Core ID (identity mismatch / invalid signature — both rejected).
func persistEnrollment(st *enrollStage, receipt *enrollment.EnrollmentReceipt) error {
	if err := enrollment.VerifyEnrollmentReceipt(st.CorePK, *receipt); err != nil {
		return fmt.Errorf("enrollment: Core receipt failed trust-anchor verification: %w", err)
	}
	if receipt.CoreID != st.Trust.CoreID {
		return fmt.Errorf("enrollment: Core identity mismatch: resolved %s, receipt %s", st.Trust.CoreID, receipt.CoreID)
	}
	record := &enrollment.Record{
		CoreID:                   receipt.CoreID,
		CorePublicKey:            enrollment.EncodeRawURL(st.Trust.PublicKey),
		BodyID:                   receipt.BodyID,
		BodyPublicKeyFingerprint: receipt.BodyPublicKeyFingerprint,
		MembershipReference:      receipt.MembershipReference,
		Permissions:              receipt.Permissions,
		Receipt:                  *receipt,
		EnrolledAt:               time.Now(),
	}
	if err := st.EnrSt.SaveRecord(record); err != nil {
		return err
	}
	fmt.Printf("enrolled body_id=%s\n", receipt.BodyID)
	fmt.Printf("membership_reference=%s\n", receipt.MembershipReference)
	fmt.Printf("core_id=%s\n", receipt.CoreID)
	fmt.Printf("permissions=%v\n", receipt.Permissions)
	return nil
}

// enroll consumes a Core 4 invitation and runs the canonical signed enrollment
// redemption flow (Core 4 Body enrollment v1). By default it resolves Core's
// trust anchor by identity discovery (GET /v1/core/identity); the operator may
// instead supply an explicit --core-key/--core-id trust anchor. Discovered or
// explicit, the anchor is bound to the owner-approved redemption by receipt
// signature verification (plus a Core ID cross-check) and then persisted. It
// never silently replaces an existing trust anchor.
func enroll(store *body.Store, invitationFile, coreURL, coreID, coreKeyRaw string) error {
	if coreURL == "" {
		return errors.New("--enroll requires --core-url")
	}

	raw, err := os.ReadFile(invitationFile)
	if err != nil {
		return fmt.Errorf("failed to read invitation %s: %w", invitationFile, err)
	}
	// Core 4 enrollment invitations (created by `neondollctl body invite`)
	// omit the legacy `version` field and advertise no bootstrap endpoint; load
	// them with the enrollment invitation contract rather than the pairing one.
	inv, err := body.LoadEnrollmentInvitation(string(raw))
	if err != nil {
		return fmt.Errorf("invalid invitation: %w", err)
	}

	st, err := stageEnrollment(store, coreURL, coreID, coreKeyRaw)
	if err != nil {
		return err
	}
	defer st.Stop()

	receipt, err := st.Cli.Redeem(st.Ctx, inv.InvitationID, inv.InvitationSecret)
	if err != nil {
		return fmt.Errorf("enrollment failed: %w", err)
	}
	return persistEnrollment(st, receipt)
}

// enrollOwner runs the real owner-approved enrollment ceremony. It begins a
// body_request-mode enrollment, prints the human approval code that Core
// issued — which the owner redeems against the Core admin socket with
// `neondollctl body approve CODE [-permissions ...]` — then polls the terminal
// status until the owner's decision is recorded. On approval the returned
// receipt is bound to the resolved trust anchor and persisted exactly as the
// invitation path. It fails closed if the owner denies or the pending expires,
// and never bypasses owner approval.
func enrollOwner(store *body.Store, coreURL, coreID, coreKeyRaw string) error {
	if coreURL == "" {
		return errors.New("--enroll-owner requires --core-url")
	}

	st, err := stageEnrollment(store, coreURL, coreID, coreKeyRaw)
	if err != nil {
		return err
	}
	defer st.Stop()

	pending, err := st.Cli.Begin(st.Ctx)
	if err != nil {
		return fmt.Errorf("enrollment failed: %w", err)
	}
	fmt.Printf("pending approval_code=%s request_id=%s core_id=%s\n",
		pending.ApprovalCode, pending.RequestID, pending.CoreID)

	// Bound, cancellation-aware poll of the terminal status. Core consumes a
	// fresh nonce per poll and limits attempts; stay within that budget.
	for i := 0; i < 8; i++ {
		if errors.Is(st.Ctx.Err(), context.Canceled) {
			return context.Canceled
		}
		time.Sleep(2 * time.Second)
		resp, err := st.Cli.Result(st.Ctx)
		if err != nil {
			return fmt.Errorf("enrollment: poll result: %w", err)
		}
		if resp.Status == enrollment.StatusEnrolled {
			if resp.Receipt == nil {
				return errors.New("enrollment: enrolled response missing receipt")
			}
			return persistEnrollment(st, resp.Receipt)
		}
		if resp.Status == enrollment.StatusDenied {
			return errors.New("enrollment: denied by owner")
		}
		if resp.Status == enrollment.StatusExpired {
			return errors.New("enrollment: expired before approval")
		}
		if resp.Status != enrollment.StatusPending {
			return fmt.Errorf("enrollment: unexpected status %q", resp.Status)
		}
	}
	return errors.New("enrollment: owner did not approve within the attempt budget")
}

// enrollmentMaterial is the persisted evidence an enrolled Body needs to
// authenticate to Core over the Doll Link transport: the enrollment record, the
// enrollment identity, and the decoded Core trust anchor (Core's Ed25519 public
// key).
type enrollmentMaterial struct {
	Record    *enrollment.Record
	Identity  *enrollment.Identity
	PublicKey []byte
}

// loadEnrollmentMaterial loads and validates the enrolled Body identity and
// Core trust anchor. It fails closed when the record is missing, malformed, or
// names no trust anchor — never silently minting a fresh identity.
func loadEnrollmentMaterial(store *body.Store) (m *enrollmentMaterial, err error) {
	enrStore := enrollment.NewStore(store.Dir())
	record, err := enrStore.LoadRecord()
	if err != nil {
		if errors.Is(err, enrollment.ErrStateNotFound) {
			return nil, errors.New("not enrolled yet (run --enroll first)")
		}
		return nil, err
	}
	if record.CoreID == "" {
		return nil, errors.New("record has no core_id; re-run --enroll")
	}
	if record.CorePublicKey == "" {
		return nil, errors.New("record has no core_public_key trust anchor; re-run --enroll")
	}
	corePubRaw, err := enrollment.DecodeRawURL(record.CorePublicKey)
	if err != nil {
		return nil, fmt.Errorf("record core_public_key is invalid: %w", err)
	}
	enrIdent, err := enrStore.LoadIdentity()
	if err != nil {
		return nil, fmt.Errorf("load identity for authentication: %w", err)
	}
	return &enrollmentMaterial{Record: record, Identity: enrIdent, PublicKey: corePubRaw}, nil
}

// converse runs the Real Terminal Conversation (M11.1): it connects to Core's
// /ws endpoint, authenticates as the enrolled Body (Body Auth v1), opens a real
// Core 5 Interaction Session, and lets the human type lines that stream through
// the session as message events — displaying each Doll response — then closes
// the session cleanly on end of input (Ctrl-D / Ctrl-C).
func converse(store *body.Store, wsURL, doll string) error {
	if wsURL == "" {
		return errors.New("--converse requires a Core ws URL (e.g. ws://host:port/ws)")
	}

	mat, err := loadEnrollmentMaterial(store)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	creds := body.Credentials{
		BodyID:        mat.Record.BodyID,
		PrivateKey:    mat.Identity.PrivateKey(),
		CoreID:        mat.Record.CoreID,
		CorePublicKey: ed25519.PublicKey(mat.PublicKey),
	}
	// Build a Dialer that reuses the existing authenticated connection +
	// recovery machinery: every call re-dials /ws, performs a fresh Body Auth
	// v1 handshake, and re-advertises capabilities. RunConversation uses it for
	// the first connect AND for each reconnect after a transport drop, so a
	// reconnect re-authenticates rather than reusing a stale, dead socket.
	var dials int
	dialer := func(dctx context.Context) (interaction.Session, error) {
		dials++
		fs, sessionID, derr := body.DialFrameSession(dctx, wsURL, creds, execution.AdvertisedCore4())
		if derr != nil {
			// Dial or auth failure: surfaced to RunConversation, which
			// reuses the recovery loop's classification — retry transient
			// failures, fail closed on rejected/revoked identity.
			return interaction.Session{}, fmt.Errorf("body: dial/authenticate %s: %w", wsURL, derr)
		}
		if dials == 1 {
			fmt.Printf("connected to Core %s as body_id=%s (session=%s)\n",
				mat.Record.CoreID, mat.Record.BodyID, sessionID)
		} else {
			fmt.Printf("reconnected to Core (auth session=%s); re-selecting the shell session\n", sessionID)
		}
		return interaction.Session{
			Write: fs.Write,
			Read:  fs.Read,
			Close: fs.Close,
		}, nil
	}

	// StatusReporter writes connection diagnostics to stderr, keeping stdout
	// clean for Doll conversation.
	reporter := interaction.StatusReporterFunc(func(status string) {
		fmt.Fprintln(os.Stderr, status)
	})

	fmt.Fprintln(os.Stdout, "type and press Enter to talk to your Doll; Ctrl-D / Ctrl-C to close")

	res, rerr := interaction.RunConversation(ctx, dialer, doll, os.Stdin, os.Stdout, reporter)
	if rerr == nil && res != nil {
		fmt.Fprintf(os.Stderr, "conversation ended (%s, %d line(s))\n", res.EndedBy, res.LinesIn)
	}
	return rerr
}

// serve runs a canonical Core 4 Doll Link execution session: it connects to
// Core's /ws endpoint, advertises the Body's capabilities, and serves inbound
// execution.request messages until the connection or the process is stopped.
func serve(store *body.Store, wsURL string) error {
	if wsURL == "" {
		return errors.New("--serve requires a Core ws URL (e.g. ws://host:port/ws)")
	}
	enrStore := enrollment.NewStore(store.Dir())
	record, err := enrStore.LoadRecord()
	if err != nil {
		if errors.Is(err, enrollment.ErrStateNotFound) {
			return errors.New("not enrolled yet (run --enroll first)")
		}
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if record.CoreID == "" {
		return errors.New("record has no core_id; re-run --enroll")
	}
	if record.CorePublicKey == "" {
		return errors.New("record has no core_public_key trust anchor; re-run --enroll")
	}
	corePubRaw, err := enrollment.DecodeRawURL(record.CorePublicKey)
	if err != nil {
		return fmt.Errorf("record core_public_key is invalid: %w", err)
	}
	enrIdent, err := enrStore.LoadIdentity()
	if err != nil {
		return fmt.Errorf("load identity for authentication: %w", err)
	}

	adapter := execution.New(process.NewExecutor(), 30*time.Second)
	creds := body.Credentials{
		BodyID:        record.BodyID,
		PrivateKey:    enrIdent.PrivateKey(),
		CoreID:        record.CoreID,
		CorePublicKey: ed25519.PublicKey(corePubRaw),
	}
	// Persistent connection: an enrolled Body reconnects automatically after
	// losing its Core connection, without re-enrollment or identity loss.
	return body.RunRecovery(ctx, body.RecoveryConfig{
		URL:        wsURL,
		Creds:      creds,
		Adapter:    adapter,
		Advertised: execution.AdvertisedCore4(),
		OnAuthenticated: func(accept *auth.Accept) {
			fmt.Printf("authenticated with Core as body_id=%s (session=%s)\n", record.BodyID, accept.SessionID)
		},
	})
}

// serveCore5 runs a canonical Core 5 Doll Link execution session: it connects to
// Core's /ws endpoint, authenticates using Body Auth v1, advertises the Body's
// capabilities, and serves inbound execution.request messages until the connection
// or the process is stopped.
func serveCore5(store *body.Store, wsURL string) error {
	if wsURL == "" {
		return errors.New("--serve-core5 requires a Core ws URL (e.g. ws://host:port/ws)")
	}
	enrStore := enrollment.NewStore(store.Dir())
	record, err := enrStore.LoadRecord()
	if err != nil {
		if errors.Is(err, enrollment.ErrStateNotFound) {
			return errors.New("not enrolled yet (run --enroll first)")
		}
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if record.CoreID == "" {
		return errors.New("record has no core_id; re-run --enroll")
	}
	if record.CorePublicKey == "" {
		return errors.New("record has no core_public_key trust anchor; re-run --enroll")
	}
	corePubRaw, err := enrollment.DecodeRawURL(record.CorePublicKey)
	if err != nil {
		return fmt.Errorf("record core_public_key is invalid: %w", err)
	}
	enrIdent, err := enrStore.LoadIdentity()
	if err != nil {
		return fmt.Errorf("load identity for authentication: %w", err)
	}

	adapter := execution.NewCore5(process.NewExecutor(), 30*time.Second)
	creds := body.Core5Credentials{
		BodyID:        record.BodyID,
		PrivateKey:    enrIdent.PrivateKey(),
		CoreID:        record.CoreID,
		CorePublicKey: ed25519.PublicKey(corePubRaw),
	}
	// Persistent connection: an enrolled Body reconnects automatically after
	// losing its Core connection, without re-enrollment or identity loss.
	return body.RunCore5Recovery(ctx, body.Core5RecoveryConfig{
		URL:        wsURL,
		Creds:      creds,
		Adapter:    adapter,
		Advertised: execution.AdvertisedCore5(),
		OnAuthenticated: func(sessionID string) {
			fmt.Printf("authenticated with Core as body_id=%s (session=%s)\n", record.BodyID, sessionID)
		},
	})
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
