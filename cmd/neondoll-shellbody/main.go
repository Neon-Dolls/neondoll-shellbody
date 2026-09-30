// Command neondoll-shellbody is the NeonDoll Shell Body reference that lets a
// Doll inhabit a terminal.
//
// M1 — A Doll Has a Terminal. The Shell Body runs standalone (no Core
// required), holds a stable identity under a configurable state directory,
// and provides a line-oriented terminal embodiment with a deterministic
// reflex. No pairing, WireGuard, Doll Link, Interaction Sessions, or shell
// execution are implemented yet.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/identity"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/terminal"
)

// Version is the build/version stamp. It is overridable at link time via
// -ldflags "-X main.version=...".
var version = "dev"

// defaultImplementation is the required implementation identifier from the
// M1 build plan.
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
	statusMode := fs.Bool("status", false, "print the persisted Body identity and exit")
	initMode := fs.Bool("init", false, "initialize a fresh state directory with a new Body identity and exit")

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

	st, err := identity.NewStore(*stateDir)
	if err != nil {
		return err
	}

	meta := identity.BodyMetadata{
		Implementation: *implementation,
		Platform:       *platform,
		Architecture:   *arch,
		Build:          *build,
	}

	switch {
	case *statusMode:
		return status(st)
	case *initMode:
		return initStandalone(st, *name, meta)
	default:
		return interactive(st, *name, meta)
	}
}

// status prints the persisted identity (proving restart stability) or, for a
// fresh directory, signals that none exists yet.
func status(st *identity.Store) error {
	ident, err := st.LoadIdentity()
	if err != nil {
		if errors.Is(err, identity.ErrStateNotFound) {
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
	fmt.Printf("state-dir: %s\n", st.Dir())
	return nil
}

// initStandalone creates a fresh durable identity in the state directory and
// prints it. It fails if an identity already exists.
func initStandalone(st *identity.Store, name string, meta identity.BodyMetadata) error {
	ident, err := st.CreateFresh(name, meta)
	if err != nil {
		return err
	}
	fmt.Printf("initialized fresh Body identity in %s\n", st.Dir())
	fmt.Printf("body_id: %s\n", ident.Identity.BodyID)
	return nil
}

// interactive loads (or on first run creates) the Body identity, prints a
// startup diagnostic, and runs the terminal embodiment until clean EOF or
// Ctrl-C. Durably created state is never modified by a run, so it cannot be
// corrupted by shutdown.
func interactive(st *identity.Store, name string, meta identity.BodyMetadata) error {
	ident, err := st.LoadIdentity()
	if err != nil {
		if errors.Is(err, identity.ErrStateNotFound) {
			ident, err = st.CreateFresh(name, meta)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "created new Body identity %s\n", ident.Identity.BodyID)
		} else {
			return err
		}
	}

	printBanner(ident, st.Dir())

	// Ctrl-C (SIGINT) and termination requests cancel the terminal loop so it
	// shuts down cleanly rather than being killed mid-write.
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
