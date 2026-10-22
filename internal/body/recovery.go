package body

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/auth"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/execution"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/link/core4"
)

// PermanentError marks a connection failure that retrying cannot fix: a Core
// that is reachable but rejects the Body's identity during the authentication
// handshake (revoked credentials, unrecognized body, invalid proof, or a
// tampered trust anchor), or an invalid local identity. The recovery loop
// fails closed on these rather than retrying forever.
type PermanentError struct {
	// Op names the failing stage, e.g. "local-credentials", "authenticate",
	// or "accepted".
	Op  string
	Err error
}

func (e *PermanentError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("body: permanent failure during %s: %v", e.Op, e.Err)
	}
	return fmt.Sprintf("body: permanent failure during %s", e.Op)
}

func (e *PermanentError) Unwrap() error { return e.Err }

// IsPermanent reports whether err is a PermanentError the recovery loop must
// fail closed on rather than retry.
func IsPermanent(err error) bool {
	var pe *PermanentError
	return errors.As(err, &pe)
}

const (
	defaultReconnectDelay    = 500 * time.Millisecond
	defaultMaxReconnectDelay = 30 * time.Second
	defaultJitter            = 500 * time.Millisecond
)

// RecoveryConfig configures the persistent Core connection recovery loop.
type RecoveryConfig struct {
	// URL is Core's /ws endpoint.
	URL string
	// Creds is the enrolled Ed25519 identity and Core trust anchor. It is
	// loaded once from durable state and preserved unchanged across every
	// reconnect cycle.
	Creds Credentials
	// Adapter handles inbound execution requests.
	Adapter *execution.Adapter
	// Advertised lists the capabilities re-advertised on every fresh
	// connection.
	Advertised []core4.Capability
	// OnAuthenticated, if non-nil, is invoked after Core accepts each fresh
	// connection's authentication. It is optional and used for logging.
	OnAuthenticated func(accept *auth.Accept)
	// ReconnectDelay is the base exponential backoff delay (default 500ms).
	ReconnectDelay time.Duration
	// MaxReconnectDelay caps the exponential backoff (default 30s).
	MaxReconnectDelay time.Duration
	// Jitter is the maximum random extra added to each backoff delay (default
	// 500ms). Set negative to disable jitter.
	Jitter time.Duration
}

// RunRecovery drives a single persistent Core connection for the Body's
// lifetime. It connects, performs fresh Body Authentication v1 and re-advertises
// capabilities on every connection, then blocks in the authenticated execution
// session until the connection drops. Transient failures (Core unreachable at
// dial time, or an authenticated session dropping) trigger a reconnect after
// bounded exponential backoff with jitter; context cancellation interrupts any
// backoff immediately. Permanent failures (authentication rejection by a
// reachable Core, or an invalid local identity) fail closed and return without
// retrying. At most one session is ever active, and a finished session is
// closed before a replacement is dialed.
func RunRecovery(ctx context.Context, cfg RecoveryConfig) error {
	// An invalid local identity can never become valid by retrying, so fail
	// closed before attempting any connection.
	if err := cfg.Creds.validate(); err != nil {
		return &PermanentError{Op: "local-credentials", Err: err}
	}
	if cfg.Adapter == nil {
		return &PermanentError{Op: "local-credentials", Err: errors.New("execution adapter must not be nil")}
	}

	base := cfg.ReconnectDelay
	if base <= 0 {
		base = defaultReconnectDelay
	}
	max := cfg.MaxReconnectDelay
	if max <= 0 {
		max = defaultMaxReconnectDelay
	}
	jitter := cfg.Jitter
	switch {
	case jitter < 0:
		jitter = 0 // explicitly disabled
	case jitter == 0:
		jitter = defaultJitter
	}

	backoff := base
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		err := connectOnce(ctx, cfg)
		if err == nil {
			return nil
		}
		if IsPermanent(err) {
			// Fail closed: revoked/invalid identity will never succeed by
			// retrying, so do not retry indefinitely.
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err() // cancellation wins over any pending reconnect
		}

		// Transient failure: back off before trying again. Cancellation
		// interrupts the sleep immediately, giving prompt shutdown.
		if err := waitBackoff(ctx, jittered(backoff, jitter)); err != nil {
			return err
		}
		backoff = nextBackoff(backoff, max)
	}
}

// connectOnce performs one full Core session: dial, authenticate (Body Auth
// v1), advertise capabilities, then serve executions until the connection
// drops. Authentication rejection by a reachable Core is surfaced as
// *PermanentError; every other failure — dial failure (Core unavailable), a
// post-authentication transport hiccup, or an authenticated session dropping —
// is transient and retried by RunRecovery.
func connectOnce(ctx context.Context, cfg RecoveryConfig) error {
	sess, err := DialCore4(cfg.URL, cfg.Creds, cfg.Adapter)
	if err != nil {
		// Dial failed: Core is unreachable or the wire request was refused.
		// Transient — retry with backoff until Core comes back.
		return err
	}
	// The session is always closed before RunRecovery dials a replacement, so
	// old connections never linger and at most one session is ever active.
	defer func() { _ = sess.Close() }()

	accept, err := sess.Authenticate(ctx)
	if err != nil {
		// We connected to Core but the handshake was rejected or abandoned:
		// retrying the same identity cannot fix it, so fail closed.
		return &PermanentError{Op: "authenticate", Err: err}
	}
	if accept.Status != auth.StatusAuthenticated {
		return &PermanentError{Op: "accepted",
			Err: fmt.Errorf("authentication not accepted by Core (status=%q)", accept.Status)}
	}
	if cfg.OnAuthenticated != nil {
		cfg.OnAuthenticated(accept)
	}

	// Every fresh connection re-advertises the Body's capabilities.
	if err := sess.AdvertiseCapabilities(ctx, cfg.Advertised); err != nil {
		// The connection was authenticated but capability advertisement failed;
		// transient — the next attempt re-authenticates and re-advertises.
		return fmt.Errorf("body: advertise capabilities: %w", err)
	}

	// Authenticated execution session; returns only when the connection closes
	// (transient) or ctx is cancelled.
	return sess.Run(ctx)
}

// jittered adds a uniformly random offset in [0, maxJitter] to base using a
// cryptographic random source. On RNG failure it returns base unchanged so the
// loop still makes progress deterministically.
func jittered(base, maxJitter time.Duration) time.Duration {
	if maxJitter <= 0 {
		return base
	}
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return base
	}
	n := binary.BigEndian.Uint64(raw[:])
	offset := time.Duration(n % uint64(maxJitter+1))
	return base + offset
}

// nextBackoff doubles cur, saturating at max and guarding against int64
// overflow.
func nextBackoff(cur, max time.Duration) time.Duration {
	next := cur * 2
	if next <= cur || next > max {
		return max
	}
	return next
}

// waitBackoff sleeps for d while remaining cancellation-aware: when ctx is
// cancelled it returns ctx.Err() immediately instead of finishing the delay.
func waitBackoff(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// validate checks that the local enrolled identity and trust anchor are well
// formed, so an invalid local identity fails closed before any connection is
// attempted.
func (c Credentials) validate() error {
	if len(c.BodyID) == 0 {
		return errors.New("BodyID must not be empty")
	}
	if len(c.CorePublicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("CorePublicKey has %d bytes, want %d", len(c.CorePublicKey), ed25519.PublicKeySize)
	}
	if len(c.PrivateKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("PrivateKey has %d bytes, want %d", len(c.PrivateKey), ed25519.PrivateKeySize)
	}
	return nil
}
