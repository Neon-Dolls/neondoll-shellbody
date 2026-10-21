# M10.2 — Shell Body ⇄ Core 4 Conformance

**Status:** complete — Shell Body now *adapts to* Core 4's canonical protocol; it no longer
expects Core to speak a legacy vocabulary Core never implemented.

**Branches:** Shell Body `m10-core4-conformance` ⇄ Core `main` (`c1132d3`, Core 4).
**Architecture rule:** NeonDoll Core defines the protocol; Shell Body adapts to Core.

---

## 1. What changed vs. the M10.1 baseline

M10.1 (Core `d24571c`) concluded Core had *no* Body protocol. **That is obsolete.** Core 4
(`c1132d3`) added a complete, production-wired enrollment + execution surface. M10.2
re-does the reconnaissance against the real Core 4 contract and lands conformance code.

| Surface | M10.1 assumed | Core 4 actually has |
|---|---|---|
| Enrollment | none (legacy `/v1/pair` invitation pairing) | `POST /v1/body/enrollment/{begin,redeem,result}` — signed Ed25519 challenge/response |
| Identity | self-minted `body_<hex>`, no keypair | **Core mints `body_id`** after the Body proves possession of an Ed25519 key |
| Handshake | `body.hello` → `core.hello` (blocks) | **none on the socket** — acceptance decided at `/ws` upgrade by `AuthenticateExecution` |
| Capability advertisement | custom `body.capabilities` (terminal in/out) | canonical `body.capabilities` (ID/operations/available/constraints/metadata) |
| Execution | session.* `terminal_input/output` events | canonical `execution.request` / `execution.result` (8 statuses) |

`body.hello`, `core.hello`, `body.ready`, `session.*`, `terminal_input`, `terminal_output`,
`delegation.*` do **not** exist in Core 4. Shell Body no longer introduces them.

## 2. Core 4 canonical contract (what Shell Body now conforms to)

**Enrollment (HTTP, plain POST, no auth token — the signature IS the proof):**
1. Body generates an **Ed25519** keypair (`body_public_key`) + owns an **X25519** WireGuard key.
2. `POST begin` (body_request mode) *or* `POST redeem` (invitation mode — canonical register path).
3. Every request is signed over the **`NEONDOLL-SIGN-V1\x00`** length-prefixed transcript
   (`Core/DollNetwork/enrollment_signing.go`). Anti-replay via a per-flow `RequestID` + 32-byte
   `nonce`, reused across redeem→result.
4. `POST result` polls the terminal state/signed receipt. Core mints `body_id` (`body_<uuid>`),
   a membership reference (WG peer), permissions, and signs the `EnrollmentReceipt`.

**Base64 asymmetry (the subtle, load-bearing detail):** `body_public_key`, `body_nonce`, and
`signature` are **RawURL** base64; **`wg_public_key` is STANDARD base64** (RFC 4648)
(`Core/DollNetwork/pairing.go:34` `DecodeWgPublicKey` uses `base64.StdEncoding`). Shell Body
reproduces this exactly.

**Execution session (`/ws`):**
- A connection becomes an execution session only when `ws.Config.AuthenticateExecution`
  returns a stable Body identity at upgrade time; otherwise it is Events/Actions-only and
  "ineligible for execution" (`Core/DollLink/WebSocket/types.go:104-116`).
- Authenticated Body then advertises **`body.capabilities`** (body_id must equal the
  authenticated identity) and answers **`execution.request`** (Core→Body) with a correlated
  **`execution.result`** (`correlation_id = request.id`, same execution_id/body_id).
- Canonical statuses: `success | denied | unsupported | invalid | unavailable | failed |
  cancelled | expired`.

## 3. What Shell Body shipped in this PR (smallest useful conformance surface)

All production code is an **independent reimplementation** — it does not import neondoll
(keeping the standing rule that only the integration test may import Core).

| Path | What it is |
|---|---|
| `internal/enrollment/` | Core-4-conformant enrollment client: `types.go` (canonical wire structs), `signing.go` (independent `NEONDOLL-SIGN-V1` transcript + verify, wg std-base64), `keypair.go` (Ed25519 identity), `store.go` (persist identity + Core-issued record), `client.go` (begin/redeem/result HTTP client). |
| `internal/link/core4/` | Canonical Doll Link wire types + validation mirroring Core 4 (`body.capabilities`, `execution.request/result`, envelope/timestamp/correlation rules). |
| `internal/execution/core4.go` | Adapter: canonical `execution.request{capability,operation,arguments}` → host-process executor → canonical `execution.result`, with Core status mapping (unsupported/invalid/failed/cancelled/expired/success). Advertises a Body-local `process`/`run` capability contract. |
| `internal/body/core4session.go` | Conformant `/ws` session: dial, advertise capabilities, serve `execution.request`→`execution.result`; resilient to unknown inbound types (like Core's own readLoop). No hello/session vocabulary. |
| `cmd/neondoll-shellbody` | New `--enroll <invite.json> --core-url <base> --core-id <id>` (signed enrollment → persisted body_id + membership) and `--serve <ws://.../ws>` (run the execution session). |
| `test/m10_2_core4_conformance_test.go` | The one sanctioned Core-importing test: proves Shell Body enrolls through a **real Core 4 service over HTTP**, the receipt verifies under **Core's own public key**, the WG membership is active, and capabilities + execution result pass **Core's canonical validators**. |

legacy `internal/body/dollink_client.go` (body.hello/core.hello/session.* etc.) and the M2
pairing client are now **superseded library code**: they are not wired into the production
path and must not be used to build a Core 4 connection. (Left in place to avoid breaking the
legacy test suite; flagged in code comments.)

## 4. Compatibility matrix

| "Can it talk to Core 4 today?" | Legacy client | Core4 client (this PR) |
|---|---|---|
| Enrollment (signed) | ✗ (legacy `/v1/pair`, not wired, non-canonical) | ✅ real Core accepted it |
| Capability advertisement | ✗ (wrong shape, body.hello gated) | ✅ passes Core `capabilities.Validate` |
| Execution result | ✗ (statuses `timeout` etc. non-canonical) | ✅ passes Core `Result.Validate(SenderBody)` + `ValidateResponseTo` |
| Requires Core to implement hello/session | yes | **no** |
| Production-implemented | no (never wired) | `--enroll` + `--serve` |

## 5. Genuine Core blocker (exact source + behavior needed)

Shell Body now *conforms* to Core 4's contract. The remaining gap is **Core-side**, not a
Shell Body adapter gap:

- **`cmd/neondolld/main.go` never sets `ws.Config.AuthenticateExecution`.**
  Line 159 constructs `wsCfg := ws.Config{Listen: listenAddr}` with no authenticator, so
  `Core/DollLink/WebSocket/types.go:129-131` leaves `s.authenticateExecution = nil`. Per
  `types.go:104-116` a nil/failing authenticator leaves the connection "ineligible for
  execution". Consequence: in a production daemon, **no Body can ever obtain an execution
  session**, `RemoteRegistrar.SessionConnected`/`CapabilitiesAdvertised` never fire
  (`Core/Body/remote_registration.go` is dead in production), and every
  `execution.request`/`body.capabilities` a Body sends is dropped with a warning.

- **Enrollment endpoint authorization is denied-by-default.**
  `cmd/neondolld/main.go:172` passes `nil` owners to `enrollment.NewService`, which defaults
  to `denyOwnerAuthorizer` (`Core/Enrollment/service.go:38-43`). The invitation *redeem*
  path works (it does not consult the owner authorizer), but the begin + owner-approve path
  is dead without a real owner/authorizer. Invitation-mode redeem is therefore the only
  production-enrollable path today.

- **No production mechanism maps an enrolled Body's credentials to the `/ws` auth boundary.**
  Even after successful enrollment a Body holds a `body_id` + membership, but the daemon
  provides no `AuthenticateExecution` that verifies those credentials against a request.

**Behavior needed (Core):** wire a production `AuthenticateExecution` into `ws.Config` that
authenticates an enrolled Body (e.g. from an enrollment-issued token/session, or by verifying
the membership) and returns its `body_id`; supply a real owner authorize path (or keep
invitation redeem as the sole supported register path and document it); optionally add a
`/v1/body/enrollment/result`-style admin query. Until then, Shell Body's conformant
`--enroll` work to completion and `--serve` connects, but execution-capable sessions in a
production `neondolld` require Core's auth callback.

## 6. Tests

- `internal/enrollment`: signed-enrollment round-trips, transcript known-vector, wg std-base64,
  client against a Core-mirroring httptest stub.
- `internal/link/core4`: capabilities/request/result validation + correlation rules.
- `internal/execution`: adapter — success, unsupported capability, invalid args, missing
  binary → failed, cancelled context.
- `internal/body`: full `/ws` loopback round-trip (advertise → request → correlated result).
- `test/m10_2_core4_conformance_test.go`: **real Core 4** enrollment over HTTP (redeem
  accepted, receipt verified by Core's own key, active membership), and capabilities +
  execution conformance against Core's canonical validators.

**Verification:** `gofmt` clean, `go vet ./...` clean, `go test -race ./... -count=1` — all
13 packages pass.