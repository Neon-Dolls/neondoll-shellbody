# Protocol Holes

## M5: Process Capability Contract Hole
The Body Process protocol does not define how a Core communicates its supported execution environments (e.g., linux/amd64, darwin/arm64) to the Body during Doll Link negotiation. Without this, the Body cannot safely binaries that match the Core's runtime, leading to potential execution failures.

## M6: Relay WSS Endpoint Discovery Hole
The Doll Network Protocol v1 does not define how a Body discovers the WSS/WebSocket endpoint of a Relay for the restrictive-network fallback path.

While the protocol defines:
- Direct endpoints via structured descriptors (`{type: "direct", host: "...", port: ..., transport: "udp"}`)
- Relay endpoints via structured descriptors (`{type: "relay", relay_url: "...", route_id: "..."}`)

...it does **not** define how a Body learns the WSS/WebSocket endpoint (e.g., `wss://relay.example.net/v1/tunnel`) associated with a relay.

The `doll-relay-protocol.md` specifies that the WSS transport is available at `wss://<relay-host>/v1/tunnel`, but there is no corresponding field in the relay endpoint descriptor to convey this information.

Without this information, a Body cannot construct the WSS URL needed for the fallback transport, making the WSS/TCP443 path unimplementable without inventing endpoint semantics.

**Resolution Required:** Extend the relay endpoint descriptor in the Doll Network Protocol to include WSS endpoint information, for example:
```json
{
  "type": "relay",
  "relay_url": "https://relay.example.net",
  "wss_endpoint": "wss://relay.example.net/v1/tunnel",
  "route_id": "opaque-route-id"
}
```
or define a standard path derivation rule (e.g., WSS endpoint is always `wss://<relay_host>/v1/tunnel`).

Until this hole is resolved, the WSS/TCP443 fallback path MUST remain blocked to avoid inventing contract semantics.