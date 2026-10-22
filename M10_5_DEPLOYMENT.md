# M10.5 — Independent Process Deployment Proof

Proves a **real Shell Body binary** can enroll, authenticate, advertise, execute
(blocked — see below), disconnect, recover, and reconnect against a **real
released Core 4 daemon**, every binary launched as a separate OS process with
isolated temporary state and loopback networking.

## Reproducible harness

`test/deploy/run_m10_5_harness.sh` does all of it with the **production**
binaries. It never mocks Core, never uses in-process fixtures, never builds the
acceptance-tagged daemon, and never bypasses owner approval or Guard
authorization.

```sh
NEONDOLLD=/path/to/neondolld \
NEONDOLLCTL=/path/to/neondollctl \
NEONDOLL_SHELLBODY=/path/to/neondoll-shellbody \
./test/deploy/run_m10_5_harness.sh
```

Optional env: `M10_5_CORE_PORT` (fixed loopback HTTP port), `M10_5_WORKDIR`
(pin + keep the workspace), `M10_5_TRACE=1`.

A passing run exits `0` and prints `M10.5 RESULT: PASS (steps 1-4, 6, 7) /
step 5 BLOCKED+documented`. It builds the Core binaries first only if you ask —
the harness itself launches prebuilt binaries. To build them:

```sh
# Core (Neon-Dolls/neondoll, this run: 40a14cb)
(cd ../neondoll && go build -o /tmp/bin/neondolld    ./cmd/neondolld \
                       && go build -o /tmp/bin/neondollctl ./cmd/neondollctl)
# Shell Body (this repo)
go build -o /tmp/bin/neondoll-shellbody ./cmd/neondoll-shellbody
```

## What the harness covers (all against real binaries)

| # | Requirement | How it's proven | Result (this run) |
|---|---|---|---|
| 1 | Build + launch `neondolld`, `neondollctl`, `neondoll-shellbody` as separate OS processes | `exec` into each binary; isolated `mktemp -d` workspace; loopback `127.0.0.1:<port>` | PASS |
| 2 | Isolated temp state + loopback networking | separate `data/state/secrets` dirs + one body state dir per body | PASS |
| 3 | Enroll through Core identity discovery + the real owner-approved ceremony | `GET /v1/core/identity` → `--enroll-owner` (begin) prints owner code → `neondollctl body approve <CODE>` → body polls `result` → bound receipt persisted | PASS (`body_3b166d2b…`, `permissions=[process.run]`) |
| 3\* | (also) invitation redeem path | `neondollctl body invite` → `--enroll <invite>` against the real Core | PASS (this required a Shell Body conformance fix, below) |
| 4 | Authenticated Body session + capability advertisement | `--serve ws://…/ws`, waits for `authenticated with Core as body_id=…` | PASS |
| 5 | Authorized shell execution through Core's Guard, result returned | **BLOCKED** (below) — probed honestly, not simulated | BLOCKED |
| 6 | Terminate + restart Core; Body reconnects with persisted identity, no re-enroll | SIGTERM daemon; restart on same data dir; assert core identity stable; Body emits a new `authenticated` line (no `--enroll`) | PASS (auth 1→2 lines) |
| 7 | Revoke the Body; subsequent auth/exec denied | `neondollctl body revoke <id>` → status `revoked`; assert **no new** `authenticated` line after revoke | PASS |

The harness waits on observable readiness (`neondoll started:` daemon line,
`authenticated with Core as body_id=…` body line) and a `trap`-based `cleanup`
kills every spawned process and removes the temp workspace regardless of
outcome.

## Why step 5 ("shell execution through Guard") is BLOCKED by released Core

Per the milestone constraint — *"if a required operation is unavailable
through the production Core daemon or CLI, document the exact blocker and
provide the smallest proposed Core-side follow-up"* — the harness does **not**
simulate execution. Examining the released Core (Neon-Dolls/neondoll @ 40a14cb):

1. **No production execution endpoint.** The only route that reaches
   `Guard.Execute` is `POST /execute`, compiled **only** into an
   `-tags=acceptance` daemon build and served on an owner-local 0600 Unix socket
   behind `NEONDOLL_ACCEPTANCE_SOCKET`. The production `cmd/neondolld/main.go`
   wires `startAcceptanceControl` to the `acceptance_disabled.go` no-op, so the
   real daemon shadows no execution route (confirmed by probe: `POST
   /v1/execute`, `/execute`, `/v1/body/execute` all `404`).
2. **The acceptance authority is hardcoded, not permission-driven.** Its
   `Evaluate` allows only `doll=="core4-acceptance"` **and**
   `capability=="test.echo"` **and** `operation=="echo"` — it does **not**
   consult the Body's approved `Permissions` at all.
3. **No shell/process capability exists in Core.** Core ships no executor for a
   shell or for the Shell Body's advertised `process::run`. There is no `shell`
   permission string anywhere in the repo. `neondollctl` has no exec subcommand
   and `neondoll-client` is a chat client — neither can request execution.

So even the acceptance build could not demonstrate *permission-gated* shell
execution, and there is nothing in the real daemon that can exercise the Shell
Body's real `process::run` executor. This is a genuine, precise Core-side gap,
not a Shell Body one.

### Smallest proposed Core-side follow-up (for a follow-on milestone)

1. Expose a canonical guarded route on the production daemon, e.g.
   `POST /v1/execute`, that builds `body.NewGuard(registry, <permission-driven
   evaluator>)` where the evaluator allows an execution iff the enrolled Body's
   persisted `Permissions` match the requested
   `(body, capability, operation)` (add a `process.run`/`shell` permission
   string + a `process` capability executor on the Core side).
2. Add requester authentication to that route (the current `/execute` has
   none; the relevant trade-off should be a first-class requester identity with
   its own permission scope).
3. Add a `neondollctl` (or `neondoll-client`) exec subcommand so an operator can
   submit a guarded execution from the CLI.

With that in place, `M10.5` step 5 unblocks and the harness would swap its probe
for a real `POST $HTTP_URL/v1/execute` that round-trips `process::run` through
the Shell Body's executor and returns the result.

## Shell Body conformance fixes this milestone surfaced

Running against the real Core proved the `--enroll` path had never been
exercised against a real invitation and the owner ceremony had a latent bug:

- **`--enroll` rejected Core's real invitation.** `neondollctl body invite`
  emits an invitation with **no** `version` field and (without `-endpoint`)
  no bootstrap endpoint. `--enroll` reused the legacy pairing loader which
  required both, failing with `unsupported invitation version 0`. Added
  `LoadEnrollmentInvitation` (lenient: absent version ⇒ current protocol,
  bootstrap not required, still validates id/secret/expiry) and routed
  `--enroll` through it. Legacy `--pair` stays strict.
- **Owner-approved ceremony couldn't run from the CLI.** Core's canonical
  ceremony is `begin → owner `neondollctl body approve CODE` → result`, which
  the client library already implemented but the CLI never exposed. Added a
  `--enroll-owner` mode that runs the full ceremony (prints the approval code,
  polls result until the owner's decision, binds + persists the receipt).
- **`client.Result()` reused the Begin nonce.** Core's `result` route
  consumes and anti-replays each poll nonce; reusing one returns `409 replayed`
  on the second poll. `Result()` now mints a fresh nonce per poll while keeping
  the pinned `request_id`. A regression test
  (`TestClientResultUsesFreshNoncePerPoll`) proves the fix (it fails with the
  bug re-introduced).

All are covered by unit tests in `internal/enrollment/client_test.go` and
`internal/body/pairing_client_test.go`.

## Observed real-run output (this environment)

```
[step 3]      begin -> approval_code=MIRW-R3DC-6ZMLQ -> approve -> result
              enrolled body_id=body_3b166d2b… permissions=[process.run]
[step 3*]     invitation-enrolled body_id=body_b3015674…
[step 4]      authenticated with Core as body_id=body_3b166d2b… (session=session_f7b9292b…)
[step 5]      POST /v1/execute -> 404 ; POST /execute -> 404 ; POST /v1/body/execute -> 404
              => BLOCKED (documented)
[step 6]      core identity stable across restart: core_68b692e7…
              reconnected using persisted identity (authenticated lines: 2)
[step 7]      revoke -> status revoked ; no new authenticated line (auth denied)
```

## Repository state

- Feature branch from `main`, one focused PR (open, **not merged**).
- No production imports from the Core repository. The harness launches the Core
  binaries as external processes; the Shell Body code imports nothing from
  `neondoll`.
- The harness is **opt-in**: it is not part of `go test ./...` and is gated on
  the three `NEONDOLL*` binary env vars being provided, so it costs nothing in
  regular CI.