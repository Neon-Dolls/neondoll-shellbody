# M9 Conformance Body - Conformance Inventory

This document provides an honest assessment of the Shell Body's conformance to the public NeonDoll protocol across milestones M1-M9, based solely on implemented and tested behavior against public specifications. No private Core internals or invented contracts are relied upon.

## Conformance Classification

- **Implemented + Tested**: Behavior is implemented, tested against public specifications, and verified to work in relevant test environments.
- **Implemented but not externally conformance-tested**: Behavior is implemented but lacks external conformance tests (e.g., requires privileges not available in standard CI).
- **Blocked by public protocol contract**: Behavior cannot be implemented without inventing semantics due to gaps in public specifications.
- **Not implemented**: Behavior has not been implemented yet.

## Detailed Assessment

### M1 — A Doll Has a Terminal
**Classification**: Implemented + Tested  
**Proof**: `internal/identity/identity_test.go`  
- Stable persistent identity generation (`NewBodyID` produces unique IDs with `body_` prefix)  
- Identity state marshaling/unmarshaling round-trip preservation  
- Identity metadata (implementation, platform, architecture, build) preservation  
- Version rejection for invalid/missing fields  
- Acceptance proof: initialize identity → exit → restart → same ID loaded  

### M2 — The Terminal Pairs
**Classification**: Implemented + Tested  
**Proof**: `internal/body/pairing_client_test.go`  
- Success path: valid invitation → POST to bootstrap endpoint → validate full Core response → persist membership  
- Failure coverage: malformed/expired invitation, denied by Core, malformed Core response, invalid WG key, unsupported protocol version  
- Durability: persisted membership survives restart (LoadMembership)  
- Security: private WG key never transmitted or leaked in wire format  
- Re-pair protection: existing membership refuses accidental replacement  
- Fail-closed: corrupt membership fails pairing rather than assuming unpaired  

### M3 — The Private Path (Direct Doll Network)
**Classification**: Implemented but not externally conformance-tested  
**Proof**: `internal/tunnel/conformance_test.go` (`TestBodyPrivatePathToCore`)  
- Genuine WireGuard tunnel creation using production code path (`NewWireGuardTunnel → Configure → Start → Ping6Core`)  
- End-to-end ICMPv6 proof: Shell Body ULA → real WireGuard → Core ULA  
- Privileged test requires: root (CAP_NET_ADMIN/CAP_SYS_ADMIN), WireGuard tools, network namespaces, IPv6  
- Skips gracefully when host cannot perform proof (documents reason)  
- **Note**: Blocked in practice by [Known protocol hole: no direct WireGuard UDP endpoint advertised](#known-protocol-hole-no-direct-wireguard-udp-endpoint-is-advertised)  

### M4 — Interaction / Session Behavior
**Classification**: Blocked by public protocol contract  
**Proof**: `PROTOCOL_HOLES.md` (M4 section)  
- Primary interaction (terminal user input → Doll/Core → Doll response → terminal output) maps to `session.event` (Body ↔ Core)  
- Every `session.event` requires Interaction Session established by `session.open` → `session.opened` (ended by `session.close`)  
- Adopting `session.open`/`session.opened`/`session.event`/`session.close` would reintroduce Core 5 Interaction Session semantics, which this milestone excludes  
- No alternative in public Body Protocol v1:  
  - `body.event` (Body→Core only) cannot return Doll's response  
  - `execution.request`/`execution.result` (Core↔Body) is for Body capability operations, not conversation text  
  - Other canonical messages (`delegation.*`, `body.health`, etc.) do not carry interaction text  
- **Consequence**: The client currently advertises a `terminal` capability with operations `["input", "output"]` — these have **no canonical definition** in public protocol and must not be relied on to carry terminal text  

### M5 — Explicit Capability Execution (Process/Shell Command Execution)
**Classification**: Blocked by public protocol contract  
**Proof**: `PROTOCOL_HOLES.md` (M5 section)  
- Public specification names "process" as preferred capability identifier but does NOT define:  
  - Any canonical operations for process capability (e.g., "run")  
  - How command/argument representation maps to `execution.request.Arguments`  
  - How "env", "dir", "stdin", and timeout map to process execution  
  - Process-specific result semantics (exit code, stdout/stderr handling)  
- `execution.request`/`execution.result` framework exists but is not explicitly tied to process capability in public specification  
- **Consequence**:  
  - Process capability **can be advertised** (identifier "process" is canonical)  
  - But **no wire-compatible operation or payload** is defined for it in public protocol  
  - To comply: keep internal process executor as local machinery but do NOT advertise "process/run", do NOT interpret `execution.request` as process/run, do NOT map `execution.request.Arguments` to process fields  

### M6 — Relay Connectivity (Relay UDP and Relay WSS/443)
**Classification**: Blocked by public protocol contract  
**Proof**: `PROTOCOL_HOLES.md` (M6 section)  
- Public contracts do NOT specify mechanisms for:  
  A. Relay UDP endpoint discovery (how Body learns Relay's public UDP endpoint and associated route_id)  
  B. Route ID discovery/persistence (how Body obtains route_id or persists it across restarts)  
  C. Relay WSS endpoint discovery (how Body learns Relay's WSS endpoint host/port/path and associated route_id)  
  D. Direct/UDP/WSS path selection (how Body learns about available direct path or advertised relay path)  
- **Consequence**: Shell Body cannot determine Relay endpoint, obtain route_id, or choose between direct and relayed paths without additional, unspecified mechanisms  

### M7 — Doll Link Negotiation
**Classification**: Not implemented  
**Proof**: README.md (deliberately not implemented yet section)  
- Doll Link (`body.hello` over a connection) is not implemented  
- This is a prerequisite for any Doll Link-based behavior (including capability negotiation)  

### M8 — Explicit Capability Execution
**Classification**: Not implemented (blocked by M5/M6/M7)  
**Proof**:  
- Depends on M5 (process capability contract) being resolved  
- Depends on M6 (relay connectivity) being resolved for relay-based execution  
- Depends on M7 (Doll Link negotiation) being established  
- No implementation or tests exist for explicit capability execution via Doll Link  

### M9 — Reconnect / Mobility / Restart / Core Migration
**Classification**: Partially implemented (restart/Core migration tested, reconnect/mobility not externally tested)  
**Proof**:  
- **Restart/Core migration**: Implemented + Tested  
  - `internal/body/migration_test.go` and `internal/body/mobility_test.go` exist  
  - M2 acceptance proof includes: "Durable Shell Body membership matches the relationship after restart"  
  - Identity and WG key persist across restarts (never rewritten)  
- **Reconnect/Mobility**: Implemented but not externally conformance-tested  
  - `internal/body/mobility.go` and `mobility_test.go` implement mobility logic  
  - Tests exist but require privileges or specific network conditions not available in standard CI  
  - No end-to-end test proving mobility/reconnect with real Core/Relay in CI  

## Known Protocol Holes (Must Remain Visible)

These holes are **not** presented as supported behavior and block conformance where indicated:

1. **No Direct WireGuard UDP Endpoint Advertised**  
   - `--connect` requires explicit structured direct descriptor: `{ "type": "direct", "host": "...", "port": ..., "transport": "udp" }`  
   - Current public Doll Network contract (M2 pairing) advertises Core endpoints only as pairing/bootstrap URLs  
   - Membership from real M2-era Core carries **no** unambiguous direct WireGuard UDP endpoint  
   - `--connect` fails closed with clear protocol-hole error rather than guessing  
   - **Blocks**: M3 private path in practice (though implementation exists)

2. **M4: Terminal Interaction Requires Core 5 Interaction Session Semantics**  
   - As detailed above — blocks M4 interaction/session behavior  

3. **M5: Process Capability Contract Hole**  
   - As detailed above — blocks M5 explicit capability execution  

4. **M6: Relay Connectivity Requires Endpoint/Route Discovery Mechanisms**  
   - As detailed above — blocks M6 Relay UDP and Relay WSS/443  

## Updated Protocol Version Claims

The Shell Body supports the following public protocol versions **as implemented and tested against public specifications**:

- **Doll Network Protocol: v1**  
  - Implemented + Tested: M1 (identity), M2 (pairing)  
  - Implemented but not externally conformance-tested: M3 (private path - privileged conformance test exists)  
  - Blocked by public protocol contract: M4 (interaction/session), M5 (process capability), M6 (relay connectivity)  
  - Not implemented: M7 (Doll Link negotiation), M8 (explicit capability execution)  
  - Partially implemented: M9 (restart/Core migration tested; reconnect/mobility not externally tested)

- **Doll Link Protocol: v1**  
  - **Not implemented** — Shell Body does not implement Doll Link (`body.hello` over a connection)  
  - Any claim of Doll Link v1 support would require implementing M7, which is not done  

## Summary

The Shell Body has established a conformance baseline for M1-M9 by:

1. **Honestly implementing and testing** what the public specifications allow without inventing contracts  
2. **Clearly marking** what is blocked by public protocol holes (M4, M5, M6)  
3. **Not presenting** hoped-for or planned behavior as currently supported  
4. **Providing test pointers** for all implemented+tested claims  

This inventory satisfies M9's goal: "Turn Shell Body into a boring, trustworthy external conformance client" by providing a trustworthy account of what the Shell Body can and cannot do based solely on public protocol specifications.
