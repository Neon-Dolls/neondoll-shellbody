# neondoll-shellbody

The NeonDoll **Shell Body** reference implementation — lets a Doll inhabit a
terminal.

This repository implements **M1 — A Doll Has a Terminal**: a standalone
terminal Body with a stable persistent identity, implementation metadata, a
configurable state directory, and a clean interactive terminal lifecycle. It
runs with **no Core required** and makes **no network connections**.

Deliberately **not** implemented yet (later milestones):

- Pairing (`Invitation`/`PairResponse`, rendezvous)
- WireGuard
- Doll Link (`body.hello` over a connection)
- Interaction Sessions
- Shell command execution

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
```

Flags:

| Flag | Default | Meaning |
| --- | --- | --- |
| `--state-dir` | `.neondoll-shellbody` | directory for durable Body state |
| `--init` | off | initialize a fresh state directory and exit |
| `--status` | off | print the persisted Body identity and exit |
| `--name` | `` | display name on the identity |
| `--implementation` | `neondoll-shellbody` | implementation identifier |
| `--platform` | runtime `GOOS` | platform identifier |
| `--arch` | runtime `GOARCH` | architecture identifier |
| `--build` | `dev` | build/version stamp |

`--build` is typically stamped at link time:

```sh
go build -ldflags "-X main.version=$(git describe --tags)" ./cmd/neondoll-shellbody
```

## Durable state

State lives under `--state-dir` (default `.neondoll-shellbody`). On first run
(either `--init` or starting interactively) a Body identity is created once in
`body.json` inside the directory. The directory (mode `0700`) and identity
file (mode `0600`) are created atomically, world-unreadable.

**The identity is the Body.** It is produced once and re-derived from disk on
every restart. A run never rewrites durable state, so clean EOF / Ctrl-C
shutdown cannot corrupt it. A separate state directory yields a distinct Body.

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

## Layout

```
cmd/neondoll-shellbody   CLI (flag parsing, modes, signal wiring)
internal/identity        Body identity, metadata, durable store
internal/terminal        line-oriented terminal embodiment + lifecycle
```

## License

See [LICENSE](LICENSE).