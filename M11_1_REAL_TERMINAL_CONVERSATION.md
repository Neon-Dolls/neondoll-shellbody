# M11.1 — Real Terminal Conversation

**Status:** delivered — open one PR, not merged.
**Scope:** a human types into the Shell Body's terminal, the input reaches a real
Core 5 Interaction Session over the authenticated Doll Link WebSocket, and the
Doll's response appears in the terminal, then the session closes cleanly.

## What was added

* **`internal/link/core5`** — an independent, byte-for-byte mirror of the Core 5
  Doll Link wire types for Interaction Sessions and chat events
  (`DollLink/SessionControl` + `DollLink/Events`). No production import of the
  `neondoll` repository; conformance is proven against the real binary by the
  harness.
  * `session.create` / `session.list` / `session.select` / `session.close`
    requests and the single `session.result` response envelope (`version:1`,
    `correlation_id` echo).
  * the `interaction` message: one legacy `events.Event` routed through an
    explicit `session_id`, and the response event (`message`/`system`) correlated
    by `correlation_id` = the request event's `id`.
  * `events.Event` with `MessagePayload{text}` for the terminal payload.
* **`internal/interaction`** — the conversation runtime:
  `OpenSession` → `SendLine` → `CloseSession`, plus `RunConversation` which
  drives the existing `internal/terminal` runtime against the transport with a
  per-line reflex. Transport errors cancel the run; Core-reported errors are
  displayed inline and the session stays open. A clean EOF closes the session
  and awaits its result.
* **`internal/body`** — one exported seam, `DialFrameSession` (returns an
  exported `FrameSession` handle), so a non-execution adapter can reuse the
  Body's existing Core4Session wire + Body Authentication v1 without exposing
  the package-internal methods.
* **CLI `--converse <ws-url> [--doll <id>]`** in `cmd/neondoll-shellbody`
  (`--doll` defaults to `core`; set it to the Core's host doll, e.g. `default`).

## Canonical wire (Core 5, commit `ed473ca`)

```
auth:      body.auth.hello → challenge → proof → accept (Body Auth v1, reused)
open:      {version:1, type:"session.create", id:"<body-uuid>", payload:{}}          (or implicit)
send:      {version:1, type:"interaction", session_id:"<sid>",
             event:{id, type:"message", source:"client", doll_id, payload:{text}}}
reply:     {id, type:"message"|"system", source:"core", correlation_id:<evt id>, payload:{text}}
close:     {version:1, type:"session.close", id:"<fresh-uuid>", payload:{session_id}}
result:    {version, type:"session.result", id, correlation_id, payload:{operation,status,session|error}}
```

Core authorizes each session by the authenticated connection's owner Body identity;
the message event must carry a `doll_id` that the Core recognizes.

## Real end-to-end proof (`test/deploy/run_m11_1_harness.sh`, opt-in)

Runs the **production** binaries — real Core 5 `neondolld`, real `neondoll-shellbody`,
real owner-invitation enrollment — against a fixture OpenAI-compatible model
server (so Core's real cognition → inference path can complete deterministically):

```
connected to Core core_ff908a19-… as body_id=body_37ec2a16-… (session=session_8241684a-…)
type and press Enter to talk to your Doll; Ctrl-D / Ctrl-C to close
re: hello neon doll — hello from the Core doll
re: what is up? — hello from the Core doll
conversation ended (eof, 2 line(s))
```
Core's log for the same turn: `interaction served and persisted`
(`doll_id:"default", provider:"openai", interaction_id:…, turn_id:…`) — the
interaction memory was written to Core's SQLite. Harness exit 0.

## Design decisions

* **No Core imports in production code.** `internal/link/core5` is an
  independent mirror; the conformance surface is the real binary (harness), not
  a Go import.
* **Reuse the existing transport + terminal.** The conversation rides the
  existing Core4Session wire, Body Auth v1, credentials store, and the
  `internal/terminal` runtime — it does not create a parallel stack.
* **Single-shot and honest.** A terminal conversation is session-scoped; a
  transport drop mid-conversation ends it with an explicit error (the process
  exits non-zero) rather than silently reconnecting a severed human session.
  The served-execution path's reconnection (`body.RunRecovery`) is unchanged.
* **Focused.** No cognition/inference implementation, no host-execution or
  Relay changes, no invented message types.

## Core-side notes (dependencies, not Shell Body blockers)

* The interaction session, cognition, and persistence paths are **real and
  working** in Core 5 (`interaction served and persisted`).
* Two Core runtime dependencies made explicit by the harness, neither of which
  requires a Shell Body change:
  1. The addressed `doll_id` must exist. A fresh Core creates its host doll
     (`<profile>`, e.g. `default`) only when Pulse is enabled; the harness
     enables Pulse and addresses `--doll default`.
  2. Cognition needs a live inference endpoint (`provider`/`endpoint` in Core's
     `inference.models`); the harness presents a fixture OpenAI-compatible
     server so the real path completes deterministically. Without it, Core
     cannot produce a reply (a model dependency, not a Shell Body blocker).

## Quality gates

`gofmt -w`, `go vet ./...`, `go build ./...`, `go test -race ./...` — all green.
New unit coverage: `internal/link/core5/types_test.go`,
`internal/interaction/converse_test.go` (open/send/close, correlation skipping,
Core-error surfacing, EOF + cancellation cleanup).