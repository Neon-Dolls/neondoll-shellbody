# M10.1 — Real Core ↔ Shell Body Integration Reconnaissance

Status: investigation complete · Integration not implemented
Date: 2026-10-20
Head commits verified:

- Shell Body `Neon-Dolls/neondoll-shellbody @ a20430f` (`main`)
- Core `Neon-Dolls/neondoll @ d24571c` (`main`, "Merge PR #62 feat/m7-remote-execution-migration")
- Public spec `dividebyzero/NeonDoll-Concepts @ 9d81b5f` (`master`)

## 1. Goal

Shortest path to a **real, working terminal conversation** between the Shell
Body and the Real Core: a human types in the shellbody's terminal, the lineage
reaches the Core over the network, and Core's response comes back for display.

This document traces the intended runtime path, states which pieces already
interoperate and which are mock-only, compares the actual wire messages on both
sides against the public specification, names the first hard blocker and its
owner, and gives the smallest ordered set of patches to close the path.

## 2. Intended runtime path (per public spec)

```
Pairing            body --HTTP(invitation response)--> Core /v1/pair
Endpoint discovery mem endpooint (direct WG UDP descriptor OR relay URL)
Transport          body ---WireGuard/Relay tunnel---> Core IPv6 ULA
Doll Link          via WebSocket over that path (/ws)
Negotiation        body.hello -> core.hello -> body.capabilities -> body.ready
Interaction Sess.  session.open -> session.opened -> session.event <-> session.event
Terminal I/O       session.event carries terminal_input/terminal_output
Execution          execution.request (core->body) -> execution.result (body->core)
```

Authoritative sources: `NeonDoll-Concepts/body-protocol.md`, `body-contract.md`,
`doll-link.md`, `doll-network.md`, `doll-relay-protocol.md` (all `master`).

## 3. Compatibility matrix

| Layer | Public spec | Shell Body (shellbody) | Real Core (neondoll) | Interop |
|---|---|---|---|---|
| Envelope | `{type,id,timestamp,doll_id,body_id,correlation_id,payload}` (body-protocol.md §Common Envelope) | `link.Envelope` — byte-identical fields/tags | `execution.Request/Result` match for execution; chat `events.Event` for everything else | ❌ divergent |
| Handshake | `body.hello→core.hello→body.capabilities→body.ready` | Implemented + tested (mock transport) | **absent** — `/ws` parses all as chat events | ❌ |
| Interaction Session | `session.open→opened→session.event→close` | Implemented + tested (mock transport); stream model (PR #13) | **absent** — no session vocabulary | ❌ |
| Terminal | not a distinct type; carried in `session.event` (capability `use_terminal`) | `session.event` w/ `event:"terminal_input"/"terminal_output"` | **absent** | ❌ |
| Execution | `execution.request`(core)→`execution.result`(body), correlated | Implemented + tested (mock transport) | Implemented (`DollLink/Execution`), real `/ws` dispatch | 🔶 (partial, separate) |
| Pairing | `POST /v1/pair` | Real HTTP client (`--pair`); real rendezvous test passes vs M2-pinned Core | `Core/Pairing` handler exists; **daemon does not mount it** | 🔶 (works in-test only) |
| Endpoint discovery | direct WG UDP descriptor `{type:direct,host,port,transport:udp}` and/or relay URL | Parses both; `--connect` requires unambiguous direct UDP (M3 behavior) | Pairing response (M2) yields bootstrap URL only — **no direct UDP descriptor** | ❌ |
| Auth on Doll Link | "authenticated sender"; token/signature/binding to body_id | none implemented on the wire | `/ws` upgrade: optional `AuthenticateExecution` callback (not set by daemon) | ❌ |
| Remote terminal loop | shellbody `main` should attach stdin/stdout to session | **`main.go` never invokes the Doll Link client** — terminal is local `terminal.Run`, no Core connection | n/a | ❌ |

## 4. What already interoperates (proven)

- **Pairing (M2), in-process:** `test/rendezvous_test.go` runs a real
  `Core/Pairing` + `Core/Invitation` + `/v1/pair` handler over real HTTP and
  proves membership + id + WG key agreement with the shellbody's `--pair`
  pipeline. Green locally (`go test -race ./...`).
- **WireGuard private path (privileged):** `internal/tunnel`
  `TestBodyPrivatePathToCore` builds real WG peers in netns and proves
  ICMPv6 reaches a Core ULA. Requires root; CI (non-root) skips it.
- Core's **DollLink/Execution wire** and the shellbody's `execution.request/
  result` types are both present and correlated; this is the one vocabulary
  the two implementations share (though a remote execution is not yet
  exercised cross-implementation).

Everything **Doll Link negotiation / Interaction Session / terminal** is, on
both sides, tested only against **mocks or the local process**: the shellbody
drives `body.NewClient` exclusively with an in-memory fake `Transport`, and
Core's `/ws` server has no handler for any `body.*`/`session.*` message.

## 5. First actual blocking issue

**The Real Core does not implement Body Protocol v1 negotiation, Interaction
Sessions, or the terminal capability. Its `/ws` transport treats every
non-execution envelope as a chat `events.Event`.**

Empirical proof (new integration probe, no privileged hardware):

```
test/m10_dolllink_probe_test.go
  boots the real Core DollLink/WebSocket server (pinned neondoll),
  connects over a real WebSocket, sends the canonical public-spec body.hello:

  M10_PROBE body.hello ->
    {"id":"","type":"system","source":"core",
     "correlation_id":"evt_m10_1",
     "payload":{"event":"error","data":"unknown message type"}}

  (no core.hello, no body.capabilities acknowledgement, no negotiation)
```

Facts behind it, verified in source:

- `neondoll/DollLink/WebSocket/types.go` `readLoop` dispatches exactly three
  cases: `execution.result` → execution session; `execution.request` → rejected
  as body→core direction error; **anything else → unmarshal as `events.Event`,
  forward to `Handler.HandleEvent`** (chat). There is no `body.hello`,
  `core.hello`, `body.capabilities`, `body.ready`, `session.open/opened/event/
  close`, `body.reconcile`, or `body.health` case anywhere.
- `DollLink/Events/types.go` defines a **different** vocabulary:
  `message/command/system/presence/internal_wake/pulse_spontaneous`. This is
  not the Body Protocol v1 message set.
- `Core/Interaction/service.go` `HandleEvent` loads a `doll_id` and runs
  inference to return a response — a chat responder, **not** an Interaction
  Session lifecycle.
- Production daemon `cmd/neondolld/main.go` wires the WG overlay + WS server +
  Interaction service, **but does not mount `/v1/pair`** (pairing handler only
  exists as a library, used by tests) **and does not set
  `AuthenticateExecution`**.

**Owner: Core (`Neon-Dolls/neondoll`).** The public spec (`body-protocol.md`,
canonical, "Target: Doll Core 2") explicitly mandates the negotiation + session
messages and marks them as the frozen public Doll Link vocabulary. The Shell
Body implements that vocabulary correctly; Core has not implemented it.

> Secondary spec gap (owner: public spec, non-blocking): the shellbody models
> terminal as `session.event` with `event:"terminal_input"/"terminal_output"`.
> `terminal_input`/`terminal_output` appear **nowhere** in the spec — the spec
> says terminal rides in `session.event` via the `use_terminal` capability but
> does not pin a payload shape. This is an unreconciled detail: `doll-link.md`
> (older draft) and `body-protocol.md` (canonical) are also not reconciled in
> `NeonDoll-Concepts`. Resolution is a spec note, not a Code change.

## 6. Smallest ordered patches

Doll Link v1 is already implemented and green in the shellbody. The critical
path is all on the **Core** side. Order matters; each unblocks the next.

1. **Core: implement Body Protocol v1 negotiation on `/ws`.**
   In `DollLink/WebSocket.readLoop`, before the chat fallback, recognize
   `body.hello` and reply `core.hello` (echo negotiated Doll Link + Body
   Contract versions); accept `body.capabilities`/`body.ready` and advance the
   connection state. Smallest: a `body.hello`→`core.hello` case plus a per-conn
   "ready" flag. (No spec change — the messages are already canonical.)

2. **Core: implement `session.open/opened/event/close` routing.**
   Add session lifecycle handling to the WS transport or a thin wrapper over
   `Core/Interaction`: `session.open`→allocate `session_id`→`session.opened`;
   `session.event`→feed interaction→echo `session.event` back; `session.close`
   closes. Persist session identity independent of transport (spec invariant).

3. **Core: mount `/v1/pair` in `neondolld` and add a `use_terminal` execution
   capability (or session terminal route).** So the daemon is actually
   reachable for pairing and can accept the shellbody's terminal capability.

4. **Core: set `AuthenticateExecution` / bind authenticated `body_id` to the
   connection** (token or WG-peer-derived identity) per spec's "authenticated
   sender" rule, so session/terminal traffic isn't anonymous.

5. **Spec (tiny): pin the terminal `session.event` payload** — either bless the
   shellbody's `event:"terminal_input"/"terminal_output"` shape or define one —
   and reconcile `doll-link.md` vs `body-protocol.md`.

6. **Shell Body: attach the real transport.** Add a production `Transport`
   implementation (WebSocket over the WireGuard/relay path the tunnel already
   builds) and wire `body.NewClient` + the terminal into `main` so `--connect`
   transitions from "ping then exit" to "open session, attach stdio." This is
   step **after** 1–4 so it has a live peer to dial.

> Constraint honored: no new wire messages were invented; no Core code was
> changed just to make a probe pass. The probe observes, it doesn't fake.

## 7. Reproducible integration commands and observed results

```bash
# Build + test BOTH repositories
cd /srv/vfac/neondoll          && go build ./...        # GREEN
cd /srv/vfac/neondoll-shellbody && go build ./...       # GREEN

# Shell Body full suite (includes real-Core rendezvous, non-privileged)
cd /srv/vfac/neondoll-shellbody && go test -race ./... -count=1   # all GREEN

# NEW M10 probe — boots real Core WS transport, sends canonical body.hello
cd /srv/vfac/neondoll-shellbody && go test ./test/ -run TestM10DollLinkNegotiationProbe -count=1 -v
#   => M10_PROBE body.hello ->
#      {"id":"","type":"system","source":"core","correlation_id":"evt_m10_1",
#       "payload":{"event":"error","data":"unknown message type"}}
#   => observed gap confirmed — Core does NOT answer body.hello with core.hello

# Core suite on main (short)
cd /srv/vfac/neondoll && go test -short -count=1 ./...   # all 34 packages GREEN
```

Observed (this machine): 34/34 Core packages pass `-short`; 10/10 shellbody
packages pass `-race`; both repos build; the new probe demonstrates the
blocker live.

## 8. Useful integration tests (no privileged hardware)

- `test/m10_dolllink_probe_test.go` (**new**) — real Core WS + canonical
  `body.hello`, documents the negotiation gap as a canary.
- `test/rendezvous_test.go` — existing real `/v1/pair` interop proof.
- `internal/body/*` tests — negotiation, session, terminal stream, concurrency,
  reconnect, all against a fake transport (ready to be pointed at a real one
  once Core speaks the wire).

## 9. Summary

- The shellbody's Body Protocol v1 client is complete and correct but, today,
  **has no live Core to talk to**: Core's `/ws` speaks a different chat/event +
  execution vocabulary and never implemented negotiation/sessions/terminal.
- First blocker is owned by **Core**: add `body.hello/core.hello` negotiation
  and `session.open/opened/event/close` to `DollLink/WebSocket` per the already
  frozen public spec. Smallest fix = one `body.hello`→`core.hello` case + a
  per-connection ready flag + minimal session routing.
- After that, the shellbody needs only (a) a real transport and (b) `main`
  wiring to reach the (now cooperative) Core — no wire changes on its side.