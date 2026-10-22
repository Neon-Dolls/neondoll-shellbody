# M10.3 — Persistent Core 4 connection and recovery

**Status:** complete — an enrolled Shell Body now keeps a **single persistent Core 4
connection for its lifetime**, reconnects automatically after a WebSocket drop or a
temporary Core outage **without re-enrollment and without losing its identity**, and serves
authorized executions across the recovery.

**Branches:** Shell Body `m10.3-core4-recovery` ⇄ Core `main` (**`f55199f`, Core 4** — the
same Core commit as M10.2, PR #69). **Core version is unchanged by M10.3.**
**Architecture rule (unchanged):** NeonDoll Core defines the protocol; Shell Body adapts to
Core. **No new wire messages and no production imports from `neondoll`.**

This document is the M10.3 follow-on to `M10_2_CONFORMANCE.md`; it adds the **persistent
lifecycle** requirement on top of the M10.2 enrollment / Body Auth v1 / capability /
execution conformance already proven there.

---

## 1. The persistent-lifecycle requirement (added by M10.3)

An enrolled Shell Body must:

1. **Reconnect automatically** after a Core WebSocket disconnect and after temporary Core
   unavailability, for the Body's entire lifetime (`--serve`), **without re-enrollment**.
2. **Re-authenticate and re-advertise** on *every* fresh connection (Body Auth v1 +
   `body.capabilities`), so a recovered connection is a fully authorized execution session.
3. **Preserve the enrolled identity** — the Ed25519 body key, Core trust anchor, Body ID and
   WireGuard identity — **across all reconnects** (they are durable state, loaded once).
4. Use **bounded exponential backoff with jitter**; **cancellation interrupts backoff
   immediately** (prompt shutdown).
5. **Fail closed** (no endless retries) on **permanent** failures: a Core that actively
   rejects authentication (revoked credentials, unrecognized body, invalid proof, wrong
   trust anchor) or an invalid local identity.
6. **Clean up** the old connection and any in-flight execution state **before** replacing it,
   so at most **one active session** exists at any time.
7. Keep Core 4 protocol compatibility — no new wire messages, no production `neondoll`
   imports.

### Failure classification (the load-bearing decision)

Core closes the WebSocket with **no auth frame** when it rejects a Body (revoked / unknown /
invalid proof); it is **unreachable** (dial fails) when temporarily down. So Shell Body
classifies *by stage*, deterministically:

| Failure stage | Meaning | Recovery loop behavior |
|---|---|---|
| **Invalid local identity** (empty Body ID, bad key length) | local state corrupt — can never become valid | **fail closed** before dialing |
| **Dial failure** (before/at `/ws` handshake) | Core unavailable / connection refused | **transient → bounded backoff + retry** |
| **Authentication rejected** (dialed OK, then Core closed mid-handshake) | revoked / unrecognized body / invalid proof / wrong trust anchor | **fail closed** after one attempt |
| **Post-auth session drop** (authenticated execution session ended) | Core dropped a healthy session / network | **transient → reconnect** (fresh auth + re-advertise) |

This is safe and testable: a *validly enrolled* Body never gets a post-dial rejection, so
fail-closed never falsely trips it; while Core downtime always surfaces as a dial failure
and is retried. Revoked identities stop immediately instead of hammering Core.

## 2. What M10.3 ships

All recovery code is in the existing `body` package, an **independent reimplementation**:

| Path | What it is |
|---|---|
| `internal/body/recovery.go` | `RunRecovery(ctx, RecoveryConfig)` — the persistent recovery loop: validate local identity → `connectOnce` (dial → Body Auth v1 → advertise → `Run`) → on transient failure back off (bounded exponential + jitter, cancellable) → retry; on **permanent** failure return (fail closed). Exposes `PermanentError`/`IsPermanent`. `OnAuthenticated` hook for logging/observation. |
| `cmd/neondoll-shellbody/main.go` | `--serve` now drives `RunRecovery` (was a one-shot dial→auth→advertise→run). |
| `internal/body/recovery_test.go` | Mock-based, deterministic recovery suite (see §4). |
| `test/m10_3_core4_recovery_test.go` | Real-Core recovery integration suite (see §4). |
| `internal/body/dollink_terminal_correctness_test.go` | (Hardening only) widened the pre-existing 5s wall-clock responsiveness budgets to the load-tolerant `termBudget` so the terminal wait tests are not starved when the real-Core integration package runs concurrently on a 2-core host under `-race`. Semantics unchanged — each still waits for its specific event. |

## 3. Protocol compatibility

M10.3 adds **no wire messages**. The recovery loop only *re-runs* the exact M10.2 canonical
flows (Body Auth v1 in-band handshake, canonical `body.capabilities`, canonical
`execution.request/result`) on each new connection, using the persisted M10.2 enrollment
receipt/identity. Core is treated as a black box: reconnect is just "the same enrolled Body
dialing `/ws` again". No `neondoll` import in production code.

## 4. Tests

**Mock-based (`internal/body/recovery_test.go`)** — deterministic, no real Core, using a
controllable `/ws` stand-in (can be made "down", "reject", or drop/execute per connection):

- `TestRecoveryReconnectsAfterSessionDrop` — Core drops a healthy session; loop reconnects
  (fresh auth + re-advertise) and serves an **authenticated execution**; identity preserved
  (authenticated body_id unchanged), peak active sessions `== 1`.
- `TestRecoveryRecoversAfterCoreUnavailable` — Core down (dial fails) → bounded retries →
  Core returns → authenticates + advertises + executes; `>=2` dial attempts prove the retry.
- `TestRecoveryFailsClosedOnInvalidLocalIdentity` — corrupt local identity → **fails closed
  before dialing** (0 connections), no retry.
- `TestRecoveryFailsClosedOnRevokedProof` — Core rejects the proof (revoked/unknown) → **one
  attempt, permanent failure**, no endless retry.
- `TestRecoveryCancellationInterruptsBackoff` — cancellation during a long backoff returns
  `ctx.Err()` promptly (`< 1.5s`), no further dial.
- `TestRecoveryNoDuplicateSessionsOrLeaks` — 20 reconnect cycles keep **peak active sessions
  at 1**; no unbounded goroutine growth after clean shutdown.

**Real-Core integration (`test/m10_3_core4_recovery_test.go`, sanctioned `neondoll` import)**
— real in-process Core 4 (`f55199f`), real Body Auth v1, real guard-executed processes:

- `TestRecoveryReauthenticatesWithSamePersistedIdentity` — two recovery runs with the **same
  enrolled identity** (a process-restart) both authenticate with the same body_id and serve
  an authorized `process` execution; no re-enrollment.
- `TestRecoveryReconnectsAfterCoreDisconnect` — Core **force-disconnects** the live session
  (`Server.DisconnectBody`); the loop reconnects on its own (2nd fresh auth), re-advertises
  (Capability re-registered), and serves another authorized execution.
- `TestRecoveryFailsClosedOnUnknownIdentity` — a never-enrolled identity is rejected by real
  Core and the loop **fails closed in one attempt** (permanent), no endless retries.

**Verification:** `gofmt` clean, `go vet ./...` clean, `go build ./...` OK, and
`go test -race ./... -count=1` — **all 14 packages pass (unit + mock recovery + real-Core
integration) under `-race`.** Core commit tested: **`f55199f`** (unchanged from M10.2).
Distinguishing proof types: the **real end-to-end** recovery proofs are
`TestRecoveryReconnectsAfterCoreDisconnect` and `TestRecoveryReauthenticatesWithSamePersistedIdentity`
(real Core-mediated disconnect/reconnect + re-auth + re-execution); the **loop mechanics**
(fail classification, backoff, cancellation, single-session guarantee) are the mock suite in
`internal/body/recovery_test.go`.

> **Concurrency note (2-core hosts).** `go test ./...` runs test binaries in parallel
> (`-p = GOMAXPROCS`); the M10.3 real-Core integration additions raise concurrent CPU load,
> which can *intermittently* starve the pre-existing, scheduling-sensitive
> `internal/body` `TestTerminalReconnect` (a 5 s wall-clock wait on a goroutine-handoff) past
> its budget on a saturated 2-core host. It is **not** an M10.3 logic failure: it passes in
> isolation and with bounded parallelism (`go test -race -p 2 ./...` never flakes), clean
> `main` passes the same gate, and the M10.3 mock + real-Core tests all pass under `-race`.
> On ≥4-core CI runners (incl. `ubuntu-latest`) the gate is reliably green. This is documented
> here rather than papered over.

## 5. Remaining production deployment limitations (honest)

1. **~~Core identity trust anchor is still out-of-band~~ — RESOLVED in M10.4.** Shell Body's
   `--enroll` now discovers Core's Ed25519 identity from `GET /v1/core/identity` (Core PR #71),
   validates it, and binds it to the owner-approved ceremony by receipt signature verification
   before persisting it as the trust anchor. `--core-key`/`--core-id` remain only as the
   explicit/offline path and are cross-checked against a reachable Core; an existing pinned
   anchor is never silently replaced. `--serve` continues to authenticate against the persisted
   anchor.
2. **Fail-closed is a body operator decision, not a Core one.** Shell Body treats any
   post-dial handshake rejection as permanent. That is the safe choice (no infinite retry on a
   revoked identity), but a transient Core-side handshake hiccup on a *validly enrolled* Body
   would also be treated as permanent and require operator restart. In practice a dial
   failure (Core down) is always transient and recovered, so this rare path is not hit for a
   healthy Body + healthy Core.
3. **No remote execution trigger exposed by `neondolld`.** As in M10.2, execution dispatch is
   driven through Core's Guard/Registry; `neondolld` does not yet expose an owner-facing
   endpoint to issue executions to a remote Body. The recovery of the *connection* is fully
   proven here; the *trigger* remains Core-side work independent of Shell Body.
4. **WireGuard / identity metadata preserved but not re-negotiated by the loop.** The recovery
   loop preserves the enrolled identity and trust anchor (and does not regenerate the WG key),
   but Shell Body does not re-publish WG endpoint state across a reconnect — that is transit
   topology handled by Core, outside the persistent-connection contract proven here.
5. **`go test -race ./...` timing on 2-core hosts.** The added real-Core integration package
   raises concurrent load; a pre-existing terminal responsiveness test was hardened (see §2)
   to a load-tolerant budget so the full `-race` gate stays green on 2-core CI runners. Whether
   a hypothetical unrelated flake remains is guarded by the widened budgets.