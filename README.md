# neondoll-shellbody

The NeonDoll **Shell Body** reference implementation — lets a Doll inhabit a
terminal.

This repository implements **M1 — A Doll Has a Terminal**: a standalone
terminal Body with a stable persistent identity, implementation metadata, a
configurable state directory, and a clean interactive terminal lifecycle. It
runs with **no Core required** and makes **no network connections**.

**M2 — The Terminal Pairs** is now implemented: the Shell Body can pair with a
real NeonDoll Core using the canonical Doll Network pairing v1 protocol,
persisting the resulting relationship across restarts.

Deliberately **not** implemented yet (later milestones):

- WireGuard tunnel establishment
- Doll Network packet transport
- Doll Link (`body.hello` over a connection)
- Interaction Sessions
- Shell command execution
- Relay

---
## Build

Requires Go 1.25+.

```sh
go build ./cmd/neondoll-shellbody
```

## Run

```sh
# Interactive — creates an identity on first run, then reads/writes a terminal
neondoll-shellbody

# Point state somewhere explicit
neondoll-shellbody --state-dir /var/lib/dolls/athena

# Initialize a fresh state directory with a new Body identity, then exit
neondoll-shellbody --init --state-dir ./state-a

# Print the persisted identity without starting the terminal
neondoll-shellbody --status --state-dir ./state-a

# Pair with a real NeonDoll Core using a canonical invitation
neondoll-shellbody --pair ./invitation.json --state-dir ./state-paired
```

Flags:

|| Flag | Default | Meaning |
|| --- | --- | --- |
|| `--state-dir` | `.neondoll-shellbody` | directory for durable Body state |
|| `--init` | off | initialize a fresh state directory and exit |
|| `--status` | off | print the persisted Body identity and exit |
|| `--pair` | *empty* | path to a canonical Doll Network invitation JSON (pair and exit) |
|| `--name` | `` | display name on the identity |
|| `--implementation` | `neondoll-shellbody` | implementation identifier |
|| `--platform` | runtime `GOOS` | platform identifier |
|| `--arch` | runtime `GOARCH` | architecture identifier |
|| `--build` | `dev` | build/version stamp |

`--build` is typically stamped at link time:

```sh
go build -ldflags "-X main.version=$(git describe --tags)" ./cmd/neondoll-shellbody
```

## Pairing workflow

To pair with a real NeonDoll Core:

1. **Obtain a canonical invitation** from the Core (via its admin CLI or API).
   The invitation is a JSON file containing:
   ```json
   {
     "Version": 1,
     "InvitationID": "...",
     "InvitationSecret": "...",
     "BootstrapEndpoints": [
       {"URL": "https://core.example.com"},
       {"URL": "relay://relay.example.com"}
     ],
     "ExpiresAt": "2026-10-01T12:00:00Z"
   }
   ```

2. **Run the pairing command** (one-time):
   ```sh
   neondoll-shellbody --pair ./invitation.json --state-dir ./state-paired
   ```
   - The Shell Body validates the invitation, selects the first `http(s)` bootstrap
     endpoint, and POSTs to `<endpoint>/v1/pair`.
   - On success, it validates the full Core response (protocol version, IDs,
     IPv6 addresses, WireGuard public key, endpoints) and **atomically**
     persists a membership record.
   - If the response is malformed, denied, expired, or the invitation is
     replayed, the command exits with an error and **no membership is written**.
   - If a valid membership already exists, pairing is refused to prevent
     silent replacement.

3. **Verify the relationship** (optional):
   ```sh
   neondoll-shellbody --status --state-dir ./state-paired
   ```
   Output includes:
   ```
   paired: true
   network id: alice-net-...
   body id: 7f8c... (stable across restarts)
   body ipv6: fd00:abcd::1
   core id: a1b2... (from Core)
   core wg public key: BASE64...
   ```

4. **Restart the Shell Body** (interactive or headless) against the same
   `--state-dir`. It will load the persisted identity and membership,
   proving the relationship survived the restart.

   > The private WireGuard key **never leaves** the state directory, is never
   > transmitted, and never appears in logs or status output.

## Durable state

State lives under `--state-dir` (default `.neondoll-shellbody`). On first run
(either `--init` or starting interactively) a Body identity is created once in
`body.json` inside the directory. The directory (mode `0700`) and identity
file (mode `0600`) are created atomically, world-unreadable.

**The identity is the Body.** It is produced once and re-derived from disk on
every restart. A run never rewrites durable state, so clean EOF / Ctrl-C
shutdown cannot corrupt it. A separate state directory yields a distinct Body.

When paired, the Shell Body additionally stores:
- `wg_private.key` (X25519 private key, mode `0600`) — never exported
- `membership.json` — the validated Core relationship (network ID, peer IDs,
  IPv6 addresses, core WG public key, core endpoints)

The membership file is written atomically via temp-write → fsync → rename and
loaded with fail-closed semantics: any corruption or missing field causes the
load to return an error, leaving the Body effectively unpaired until a
successful re-pair.

## M1 acceptance proof

Run the tests (includes the acceptance proof end-to-end):

```sh
go test -race ./...
```

The proof covers:

1. initialize a fresh state directory and record the Body ID
2. exit
3. restart against the same state directory → the **same** Body ID is loaded
4. a separate state directory → a **distinct** Body
5. the interactive terminal exits cleanly on EOF (and on SIGINT) without
   corrupting state

## M2 acceptance proof

In addition to M1, the test suite includes a **real rendezvous integration
test** against a live NeonDoll Core. It proves:

- The Shell Body and Core agree on network ID, body peer ID, body IPv6,
  core peer ID, and core WG public key after pairing.
- Core membership becomes active.
- Shell Body identity and WG key remain unchanged across pairing.
- Durable Shell Body membership matches the relationship after restart.
- Replaying a consumed invitation is rejected by the Core
  (`invitation_already_consumed`).

## Layout

```
cmd/neondoll-shellbody   CLI (flag parsing, modes, signal wiring)
internal/identity        Body identity, metadata, durable store
internal/terminal        line-oriented terminal embodiment + lifecycle
internal/dollnetwork     Independent canonical Doll Network v1 wire types
internal/body            X25519/WG keypair, membership, store, pairing client
```

## License

See [LICENSE](LICENSE).