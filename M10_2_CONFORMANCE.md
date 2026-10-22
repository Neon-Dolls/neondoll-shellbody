# M10.2 — Shell Body ⇄ Core 4 Conformance

**Status:** complete — Shell Body now *adapts to* Core 4's canonical protocol, including
in-band Body Authentication v1 and owner-controlled enrollment. The previous "Core has no
auth callback" blocker is **resolved** in current Core and the new wire contract is proven
end-to-end here.

**Branches:** Shell Body `m10-core4-conformance` ⇄ Core `main` (**`f55199f`, Core 4**, incl.
PR #67 Body Auth v1, PR #68 Owner enrollment control, PR #69 Reference Body harness).
**Architecture rule:** NeonDoll Core defines the protocol; Shell Body adapts to Core.

---

## 1. What changed vs. the M10.1 baseline

M10.1 (Core `d24571c`) concluded Core had *no* Body protocol. **That is obsolete.** Core 4
added a complete production-wired surface. M10.2 re-does the reconnaissance and lands
conformance code, updated for the post-`c1132d3` advances.

| Surface | M10.1 assumed | Core 4 actually has |
|---|---|---|
| Enrollment | none (legacy `/v1/pair` invitation pairing) | `POST /v1/body/enrollment/{begin,redeem,result}` — signed Ed25519 challenge/response; **owner-controlled** |
| Identity | self-minted `body_<hex>`, no keypair | **Core mints `body_id`** after the Body proves possession of an Ed25519 key |
| Session auth | `body.hello` → `core.hello` (blocks) | **Body Authentication v1:** in-band `body.auth.hello` → challenge → proof → accept over `/ws` |
| Capability advertisement | custom `body.capabilities` (terminal in/out) | canonical `body.capabilities` (ID/operations/available/constraints/metadata) |
| Execution | session.* `terminal_input/output` events | canonical `execution.request` / `execution.result` (8 statuses) |

`body.hello`, `core.hello`, `body.ready`, `session.*`, `terminal_input`, `terminal_output`
do **not** exist in Core 4. Shell Body no longer introduces them.

## 2. Core 4 canonical contract (what Shell Body now conforms to)

**Enrollment (HTTP, plain POST — the signature IS the proof):**
1. Body generates an **Ed25519** keypair (`body_public_key`) + owns an **X25519** WireGuard key.
2. `POST begin` (body_request mode) → Core returns a signed **pending challenge** carrying an
   **owner approval code**; the **owner** approves it (`OwnerControl`, permissions-gated).
   Or `POST redeem` (invitation mode) → Core returns a signed receipt directly.
3. Every request is signed over the **`NEONDOLL-SIGN-V1\x00`** length-prefixed transcript
   (`Core/DollNetwork/enrollment_signing.go`), anti-replay via per-flow `RequestID` + nonce.
4. `POST result` returns the terminal signed **`EnrollmentReceipt`** when the status is
   `enrolled`. Core mints `body_id`, a WG membership, and grants the approved permissions.

**Base64 asymmetry (load-bearing):** `body_public_key`, `body_nonce`, and `signature` are
**RawURL** base64; **`wg_public_key` is STANDARD base64** (RFC 4648)
(`Core/DollNetwork/pairing.go` `DecodeWgPublicKey` uses `base64.StdEncoding`). Also,
`core_nonce` is a **raw** transcript field (decoded bytes), while the other pending fields are
string fields — Shell Body reproduces both asymmetries exactly.

**Body Authentication v1 (in-band, on `/ws`, Ed25519):**
1. Body sends `body.auth.hello` `{body_id, body_nonce, algorithms:["Ed25519"]}`.
2. Core replies `body.auth.challenge` — Core signs `(core_id, body_id, session_id,
   body_nonce, core_nonce, expires_at, algorithm)` with its Ed25519 key.
3. Body **verifies** the challenge with Core's public key (its trust anchor), then sends
   `body.auth.proof` — Body signs `(core_id, body_id, session_id, body_nonce, core_nonce,
   algorithm)` with its *enrolled* Ed25519 key.
4. Core verifies the proof against the enrolled body key, replies `body.auth.accept`
   `{status:"authenticated"}` signed by Core. The connection is then execution-capable.
   (`Core/DollLink/Authentication`, wired in production via `SetBodyAuthenticator`.)

**Execution session (`/ws`):**
- An authenticated Body advertises **`body.capabilities`** and answers **`execution.request`**
  (Core→Body) with a correlated **`execution.result`** (`correlation_id = request.id`, same
  execution_id/body_id). Canonical statuses: `success | denied | unsupported | invalid |
  unavailable | failed | cancelled | expired`.

## 3. What Shell Body ships in this PR (smallest useful conformance surface)

All production code is an **independent reimplementation** — it does not import neondoll
(only the integration test may).

| Path | What it is |
|---|---|
| `internal/enrollment/` | Core-4-conformant enrollment client: `types.go` (canonical wire structs incl. pending `approval_code`), `signing.go` (independent `NEONDOLL-SIGN-V1` transcript incl. **raw `core_nonce`** field), `keypair.go`, `store.go` (persists `core_public_key` trust anchor), `client.go` (begin → owner approve → result, or redeem). |
| `internal/auth/` | Independent **Body Authentication v1** package: `body.auth.hello/challenge/proof/accept` wire types + transcript sign/verify (mirrors `DollLink/Authentication`), reusing the exported transcript primitives like Core reuses `DollNetwork`. |
| `internal/link/core4/` | Canonical Doll Link wire types + validation mirroring Core 4. |
| `internal/execution/core4.go` | Adapter: canonical `execution.request{capability,operation,arguments}` → host-process executor → canonical `execution.result`. Advertises `process`/`run`. |
| `internal/body/core4session.go` | Conformant `/ws` session that **authenticates (Body Auth v1)**, advertises capabilities, and serves `execution.request`→`execution.result`. |
| `cmd/neondoll-shellbody` | `--enroll --core-url` (signed enrollment; Core identity resolved via `GET /v1/core/identity` discovery, optional `--core-id`/`--core-key` cross-checked/offline-anchor, persists Core trust anchor) and `--serve <ws://…/ws>` (authenticate with the enrolled identity, advertise, run). |
| `test/m10_2_core4_conformance_test.go` | The one sanctioned Core-importing test: **real Core 4 over HTTP + WebSocket** — owner-controlled enrollment (begin→approve→result), **real Body Auth v1 handshake**, capability advertisement landing in Core's registry, one **authorized `process` execution** reported by Core's own guard, plus negative denials; capabilities/execution byte-parity against Core's canonical validators. |

## 4. Compatibility matrix

| "Can it talk to Core 4 today?" | Legacy client | Core4 client (this PR) |
|---|---|---|
| Enrollment (signed) | ✗ (legacy `/v1/pair`, not wired) | ✅ real Core accepted begin + owner approval + result (verify under Core's key) |
| Session authentication | ✗ (body.hello, non-canonical) | ✅ **Body Auth v1 handshake accepted by real Core** (verified accept under Core's key) |
| Capability advertisement | ✗ (wrong shape) | ✅ lands in real Core's RemoteBody registry |
| Authorized execution | ✗ | ✅ **real process runs, result round-trips through Core's guard as `success`**; unauthorized ops denied |
| Requires Core to implement hello/session | yes | **no** |

## 5. Blockers: RESOLVED + remaining gaps

**Resolved (Core side, since the M10.2 draft):** the reported `AuthenticateExecution` gap is
obsolete. Current Core wires `wsServer.SetBodyAuthenticator(enrollmentService)` in
`cmd/neondolld/main.go` and supplies an in-band Ed25519 handshake
(`Core/DollLink/Authentication`); owner control (`Core/OwnerControl`, admin socket approval)
replaces the deny-by-default authorizer and provides real, permission-gated authorization.
Shell Body now reproduces and proves that full contract.

**Remaining gaps (honest):**
1. **~~No Core identity bootstrap over the wire~~ — RESOLVED in M10.4.** Core now serves its
   Ed25519 public identity at `GET /v1/core/identity` (Core PR #71). Shell Body's `--enroll`
   discovers and validates this document and binds it to the owner-approved ceremony by
   receipt signature verification, so `--core-key` is no longer required for a reachable
   Core. `--core-key`/`--core-id` remain as the explicit/offline trust-anchor path and are
   cross-checked against the discovered identity; an existing pinned anchor is never silently
   replaced. Discovery alone never establishes trust.
2. **CLI enroll path vs. owner-control path.** The CLI `--enroll` uses the canonical
   invitation-`redeem` register path (owner consents by creating the invitation). The
   **owner-control** `begin → owner approve (permissions) → result` path is proven in the
   integration test against a real Core + real owner admin socket; wiring it into the CLI's
   interactive flow is follow-up.
3. **Production execution trigger is Core-side.** Execution dispatch is driven through Core's
   `Guard`/`Registry` (as the reference harness `/reference/execute` does); `neondolld` does
   not yet expose an owner-facing execution endpoint for remote bodies. That is Core work,
   orthogonal to Shell Body conformance (which is proven here).

## 6. Tests

- `internal/enrollment`: signed-enrollment round-trips, transcript known-vector, wg
  std-base64, **raw `core_nonce` pending field**, client against a Core-mirroring stub.
- `internal/auth`: Body Auth v1 transcript round-trip, tamper rejection, status/binding
  checks, rogue-trust-anchor rejection.
- `internal/link/core4`: capabilities/request/result validation + correlation rules.
- `internal/execution`: adapter — success, unsupported capability, invalid args, failed,
  cancelled context.
- `internal/body`: full `/ws` **authenticated** round-trip against an independent fake Core
  (hello→challenge→proof→accept→advertise→request→correlated result), rogue trust anchor.
- `test/m10_2_core4_conformance_test.go`: **real Core 4** — owner-controlled enrollment;
  **real Body Auth v1**; advertised capability lands in the registry; one **authorized
  `process` execution** (real spawned process, output round-trips through Core's guard) plus
  unauthorized/unknown-body denials; capabilities + execution byte-parity with Core's
  canonical validators.

**Verification:** `gofmt` clean, `go vet ./...` clean, `go test -race ./... -count=1` — all
14 packages (incl. integration) pass. **Core commit tested: `f55199f`** (post-`c1132d3`,
incl. PR #67/#68/#69). Distinguishing proof types: the **real end-to-end** tests are
`TestShellBodyAuthenticationAndProcessExecutionAgainstCore` (owner-controlled enrollment +
Body Auth v1 + capability advertisement + authorized process execution round-trip over a
real Core WebSocket) and `TestShellBodyEnrollmentConformsToCore4` (owner-control
begin→approve→result); the **byte-parity/validator** tests are
`TestShellBodyCapabilitiesConformToCore4` and `TestShellBodyExecutionConformsToCore4`
(compare against Core's canonical `capabilities.Validate` / `execution.Result.Validate`).