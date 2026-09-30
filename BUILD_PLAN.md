# NeonDoll Shell Body — Build Plan

## Purpose

`neondoll-shellbody` is an independent reference Body that lets a Doll inhabit a terminal.

It is intentionally small, inspectable, and useful for protocol/conformance work. It MUST be implementable from the public NeonDoll specifications without access to Doll Core internals.

The Shell Body is **not** a second Core and is **not** implicitly a remote shell.

> A Shell Body means the Doll can interact through a terminal. Host shell/process execution is a separate explicit Body capability.

## Sources of truth

Protocol and semantics are defined by the public NeonDoll specifications, especially:

- `body-contract.md`
- `body-protocol.md`
- `doll-link.md`
- `doll-network.md`
- `doll-network-protocol.md`
- `doll-relay.md`
- `doll-relay-protocol.md`

The `Neon-Dolls/neondoll` repository may be inspected as a compatibility oracle and rendezvous target. Its `Body/` implementation and `DollNetwork/` wire types are **not** a private API dependency for this repository.

If Shell Body needs Core-internal knowledge to interoperate, stop and report a protocol/specification hole rather than importing Core internals or inventing wire semantics.

## Implementation shape

Use Go and produce one executable:

```text
neondoll-shellbody/
├── cmd/
│   └── neondoll-shellbody/
├── internal/ or pkg/
│   ├── identity/
│   ├── terminal/
│   ├── pairing/
│   ├── network/
│   └── link/
├── testdata/
├── BUILD_PLAN.md
├── README.md
└── go.mod
```

Keep reusable behavior out of `main`. Prefer small files; roughly 300 lines is comfortable and 500 lines is a soft ceiling.

Persist durable Body state under an explicit configurable state directory. Secrets/private keys MUST NOT be logged.

## Architectural boundaries

- Body identity is stable across process, transport, endpoint, and session changes.
- Network reachability is not Body identity.
- Doll Network membership is not Doll Link authorization.
- Capability availability is not execution authority.
- WireGuard private keys remain Body-owned and never cross the wire.
- Pairing creates/authorizes the Body relationship; transport changes do not create a new Body.
- The Shell Body does not perform Doll cognition.
- Body Reflex, if any, remains deterministic.
- Terminal input/output is embodiment interaction, not host command authority.
- Do not implement Core 5 semantic Body Resolver or model/session lifecycle work early.

## M1 — A Doll Has a Terminal

Build the standalone local Shell Body runtime without requiring a running Core.

Deliver:

- Go module and `cmd/neondoll-shellbody`
- persistent stable Body identity
- implementation metadata:
  - implementation `neondoll-shellbody`
  - platform
  - architecture
  - build/version
- configurable state directory
- terminal input/output abstraction separated from `main`
- interactive terminal lifecycle with clean EOF / Ctrl-C shutdown
- a status/identity command or startup diagnostic sufficient to prove restart stability
- no network requirement
- no fake Doll response generation
- README with build/run/state behavior
- CI: gofmt, vet, test -race, build

Acceptance proof:

1. initialize a fresh state directory
2. record Body ID
3. exit
4. restart against same state directory
5. same Body ID is loaded
6. separate state directory creates a distinct Body
7. terminal runtime exits cleanly without corrupting state

M1 deliberately does **not** implement pairing, WireGuard, Doll Link, Interaction Sessions, or shell execution.

## M2 — The Terminal Pairs

Implement canonical Doll Network pairing against a real NeonDoll Core.

Use the public pairing protocol v1:

- invitation document
- `POST /v1/pair`
- Body metadata
- Body-owned X25519/WireGuard keypair
- standard RFC4648 base64 32-byte public key on wire
- validate Core response as untrusted input
- persist membership only after complete validation
- never transmit/persist invitation secret beyond what the protocol requires
- never transmit Body private key

The current Core/reference Body may be used to prove compatibility, but Shell Body owns its own implementation.

Acceptance proof:

- real Core creates invitation
- Shell Body pairs through canonical handler
- both agree on Network ID, Body peer ID/address, Core peer ID/key/address
- restart preserves Body identity, WG key, and membership
- malformed/denied/replayed pairing fails explicitly
- existing valid membership is not silently replaced

If Relay bootstrap requires an endpoint distinction the public protocol has not yet frozen, stop/report the protocol hole rather than guessing.

## M3 — The Doll Has a Private Path

Establish real Doll Network connectivity over the direct path.

Deliver:

- userspace or otherwise self-contained WireGuard path appropriate for the Shell Body
- assigned Doll Network IPv6
- Core WG public key and direct endpoint use
- lifecycle/start/stop/reconnect behavior
- no manual general-purpose VPN configuration exposed as the product model

Acceptance proof:

- paired Shell Body establishes a real WG handshake
- Body reaches Core's Doll Network IPv6
- private key never leaves Body
- restart reconnects as the same Body/membership
- endpoint change does not create a new identity

## M4 — The Doll Speaks Through the Terminal

Layer Doll Link and Body Protocol over the working Doll Network path.

Deliver the smallest useful conversational vertical slice supported by the current public protocol/Core:

- Doll Link v1 negotiation
- `body.hello` / `core.hello`
- capability advertisement
- `body.ready`
- terminal-originated interaction through the canonical Body/session protocol available at that milestone
- Doll output rendered to terminal
- explicit protocol errors; no guessed message semantics

Do not invent Core 5 Interaction Session semantics if the Core/public protocol has not reached the required implementation point. If a public protocol exists but Core cannot yet rendezvous, keep the implementation behind tests/fixtures and report the missing Core counterpart.

Acceptance proof:

- Shell Body connects over Doll Network
- negotiation succeeds
- user types a message in terminal
- Core receives it through canonical Doll Link/Body Protocol
- Doll response is rendered in terminal
- transport reconnect does not silently create a new Body

At this milestone the Shell Body becomes a genuinely usable conversational Body.

## M5 — The Doll Can Use the Host Shell

Add host command/process execution only as an explicit advertised Body capability.

This is deliberately separate from terminal embodiment.

Define/consume the canonical capability contract available at this milestone. Do not invent a privileged generic escape hatch.

Required behavior:

- explicit capability advertisement
- explicit operation request
- correlated result
- structured failure
- cancellation/timeout where supported
- local OS denial remains authoritative
- no execution merely because text appeared in the conversation

Acceptance proof:

- Core discovers the capability
- an authorized execution request runs
- result is correlated and returned
- unsupported/denied/invalid requests fail explicitly
- ordinary terminal conversation cannot accidentally invoke host execution

## M6 — The Body Finds Home Through Relay

Add Core 4 Relay paths without changing Body identity or Doll Link semantics.

Order:

1. direct WireGuard remains first-class
2. Relay UDP fallback
3. Relay WSS/TCP443 restrictive-network fallback

Relay carries opaque WireGuard datagrams and never terminates Doll Network privacy.

Acceptance proofs include:

- direct path with Relay absent
- Relay UDP path
- outbound UDP blocked, WSS/443 allowed, real WG handshake still succeeds
- Doll Network IPv6 and Doll Link work through fallback
- Relay failure is distinguishable from pairing/auth/revocation/Core failure

## M7 — The Body Returns

Harden mobility and restart behavior.

Prove:

- process restart
- network/interface change
- endpoint change
- direct → Relay fallback
- Relay → direct when explicitly supported by current path-selection rules
- retained stable Body ID, WG identity, membership, and Doll Network address
- no automatic re-pairing for ordinary transport changes

## M8 — The Doll Moves House

Prove Core migration/portable relationship behavior defined by Core 4.

The Shell Body must reconnect to the migrated Core relationship without becoming a new Body or requiring private Core implementation knowledge.

## M9 — Conformance Body

Turn Shell Body into a boring, trustworthy external conformance client.

Add integration/conformance coverage for:

- identity stability
- pairing
- direct Doll Network
- Relay UDP
- Relay WSS/443
- Doll Link negotiation
- interaction
- explicit capability execution
- reconnect/mobility
- Core migration
- protocol failure behavior

Document exactly which public protocol versions are supported.

## Non-goals

- second Doll Core
- model/provider integration inside Body
- Doll Mind or autonomous cognition inside Body
- Auris/GUI work
- Flutter/mobile work
- general-purpose VPN product
- NAT hole punching / STUN / TURN for the Core 4 reference path
- implicit arbitrary shell access
- Core 5 Body Resolver
- silently compensating for missing public protocol

## Working rule for every milestone

Each milestone gets:

1. focused implementation
2. tests
3. `gofmt`
4. `go vet ./...`
5. `go test -race ./...`
6. `go build ./...`
7. PR
8. green CI
9. concise note of any public-protocol assumptions or holes

Do not start the next milestone in the same PR.

## Closing principle

The Shell Body should be simple enough that it can answer a very useful architectural question:

> Could somebody implement a real NeonDoll Body from the public protocols alone?

If the answer becomes “only if they know how Core works internally,” stop and fix the contract rather than teaching the Body private knowledge.
