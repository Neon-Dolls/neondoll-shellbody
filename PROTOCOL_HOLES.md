# Protocol holes

Short, precise notes on gaps between what the current public NeonDoll
specifications define and what the Shell Body needs. These are written when
implementing a milestone straight from the public protocol is blocked —
recorded here rather than guessed around.

## M4 — terminal interaction needs Core 5 Interaction Session semantics that are out of scope

**Date:** 2026-10-02 (PR #6, Patch 3 investigation)
**Files consulted (public specs only):** `body-contract.md`, `body-protocol.md`
(v1), `doll-link.md` (v1 draft). No Core internals used as authority.

**Question investigated:** can the CURRENT public Body/Doll Link protocol
express the Shell Body's primary interaction — terminal user input → Doll/Core →
Doll response → terminal output — without adopting Core 5 Interaction Session
semantics, host shell execution, or invented message types/fields?

**Result: protocol hole.** YES for the terminal round-trip, but only through
Interaction Session messages, which are out of scope here.

### Why

The authoritative frozen wire vocabulary is **Body Protocol v1**
(`body-protocol.md`), whose canonical sender rules are:

| Message | Sender |
|---|---|
| `body.hello` / `core.hello` | Body / Core (version negotiation) |
| `body.capabilities` / `body.ready` / `body.health` | Body |
| `body.event` | Body |
| `body.reconcile` / `core.reconcile` | Body / Core |
| `execution.request` / `execution.cancel` | Core |
| `execution.progress` / `execution.result` | Body |
| `session.open` | Body |
| `session.opened` | Core |
| `session.event` | Body **or** Core |
| `session.close` | Body or Core |
| `delegation.start` / `delegation.cancel` | Core |
| `delegation.status` / `delegation.result` | Body |

The Shell Body's primary interaction is the only *bidirectional* conversation
in this vocabulary, and it maps onto exactly one message: **`session.event`
(Body **or** Core)**. `body-contract.md` §Interaction Sessions confirms:
"A Body-originated interaction sent to Core MUST identify the relevant
Interaction Session or explicitly request creation of one." So sending the
human's terminal text to the Doll *is* `session.event` (Body→Core), and the
Doll's text returned for the terminal *is* `session.event` (Core→Body) — but
every `session.event` requires an Interaction Session established by
`session.open` → `session.opened` (and ended by `session.close`).

Applying `session.open`/`session.opened`/`session.event`/`session.close` is
adopting Core 5 Interaction Session semantics, which Patch 2 deliberately
removed from this client and which this milestone must not introduce.

### Why the other canonical messages are not sufficient

- **`body.event` (Body→Core only).** It carries an *observation* payload:
  `{event_id, capability, event, occurred_at, data}` — e.g. `filesystem`
  `changed`. Direction is Body→Core only (Sender Rules), so it cannot return
  the Doll's response to the terminal. Using it for terminal text would also
  require inventing the payload (`capability: "terminal"` + free-form
  `data.text`), which `body-protocol.md` does not define (no canonical
  terminal capability/event exists; the capability registry "may be defined
  separately" and is not).
- **`execution.request` / `execution.result` (Core↔Body).** This is Core
  invoking a *Body capability operation* (e.g. `filesystem.read`) and the Body
  returning a correlated result. It is Core-initiated, not a channel for the
  human's terminal input, and neither request nor result carries conversation
  text to/from the Doll. Host shell execution is explicitly out of scope
  (`BUILD_PLAN.md` M1/M5 boundary; this milestone must not add it).
- **`delegation.*`, `body.health`, `body.reconcile`, `core.reconcile`.** Not
  interaction text.
- **`doll-link.md` v1 draft `core.text` / `doll.*`.** This *draft* defines a
  session-free flat-text path (`core.text` Body→Core, `doll.*` Core→Body
  actions), which would fit. But it is an unreconciled draft with a *different*
  vocabulary (`core.body_hello`, `core.text`, `doll.send`, ...) that conflicts
  with the frozen `body-protocol.md` v1 sender rules the Shell Body implements
  (`body.hello`, `session.event`, ...), and `body-protocol.md` explicitly
  states it defines "the canonical Doll Link messages used to realize" Body
  Contract semantics.

### Consequence for the Shell Body

- The client currently advertises a `terminal` capability with operations
  `["input", "output"]`. Those operation names have **no canonical
  definition** in the public protocol and must not be relied on to carry
  terminal text yet; they are a placeholder, not a wire contract.
- To close the hole without touching Core: adopt **`session.event`** as the
  canonical downstream text carrier. That is the protocol-canonical answer —
  but it requires re-introducing Interaction Session handling
  (`session.open`/`session.opened`), i.e. exactly the Core 5 session work this
  milestone excludes.
- Until then, terminal text I/O must not be faked over `body.event`,
  `execution.*`, or ad-hoc payloads, and no invented terminal/text message
  type should be added to close the gap. This milestone stops at negotiation +
  capability advertisement (`body.hello`/`core.hello`/`body.capabilities`/
  `body.ready`), which the public protocol defines cleanly.

## M5 — process capability contract hole

**Date:** 2026-10-03 (M5 investigation)
**Files consulted (public specs only):** `body-contract.md`, `body-protocol.md` (v1)

**Question investigated:** does the public Body Protocol canonically define the process capability's operations and operation-specific request/result schema for host shell execution?

**Result: protocol hole.** The public specification names "process" as a preferred capability identifier but does NOT define:
- Any canonical operations for the process capability (e.g. "run")
- How command/argument representation maps to execution.request.Arguments
- How "env", "dir", "stdin", and timeout map to process execution
- Process-specific result semantics (exit code, stdout/stderr handling)

Therefore "process/run" is NOT a canonical wire contract. An independent Body cannot interoperably expose host process execution without inventing semantics.

### Why

The authoritative frozen wire vocabulary is **Body Protocol v1** (`body-protocol.md`). While it lists "process" as a preferred capability in the capabilities advertisement example, it does not define:
- What operations the process capability supports
- What request/result schema those operations use
- Whether execution.request/result should be used for process execution (or if a different mechanism is intended)

The execution.request/result framework exists in the protocol but is not explicitly tied to the process capability in the public specification.

### Consequence for the Shell Body

- The process capability **can be advertised** (the identifier "process" is canonical)
- But **no wire-compatible operation or payload** is defined for it in the public protocol
- To implement M5 without inventing a private contract:
  - Keep the internal process executor as local implementation machinery (internal/execution/process)
  - Do NOT advertise "process/run" via body.capabilities
  - Do NOT interpret execution.request as process/run
  - Do NOT map execution.request.Arguments to process execution fields
  - The Body-side implementation machinery remains but is deliberately not wired to the protocol

This is distinct from the M4 Interaction Session hole (which concerns terminal I/O). M5 is blocked on process capability contract definition, not interaction session semantics.

M5 wire integration is blocked until the process capability's operations and operation-specific request/result schema are canonicalized in the public specification.


