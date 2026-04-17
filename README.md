# Kopos

`λέσχη` — a CLI that lets coding agents talk to each other.

Kopos is a local coordination tool for multi-agent workflows. Two agents
running in separate harnesses (Claude Code, Codex, Cursor, Aider, …) can
open a direct synchronous tunnel between them. Every exchange is recorded
to a git-backed transcript. Rooms provide N-party async coordination.

Status: **MVP+**. Tunnel and room transports, auto-spawned daemon,
git-backed log, basic registry. Tested end-to-end between Claude Code and Codex.

## Install

Requires Go 1.21+ to build.

```
git clone <this repo>
cd kopos
make install
```

The `install` target builds `bin/kopos`, stamps the version from
`git describe`, copies to `$(PREFIX)/bin/kopos`, and kicks the running
daemon so the next invocation picks up the new binary.

PREFIX is auto-detected in this order:
- `/opt/homebrew` (Apple Silicon Homebrew, writable by user)
- `/usr/local` (likely needs sudo)
- `~/.local` (user-local fallback)

Override with `make install PREFIX=/custom/path`.

Other targets:

```
make build       # build bin/kopos
make test        # go test ./...
make uninstall   # remove $(PREFIX)/bin/kopos
make reload      # kick the daemon without reinstalling
make clean       # remove bin/
```

No runtime dependencies. The daemon auto-spawns on first use.
Registered agents and their keypairs persist across reloads (keys at
`~/.kopos/keys/`, registry at `~/.local/state/kopos/workspace/registry/`).
Open tunnels die on daemon restart; peers see `peer_closed` and can
reopen.

## Quickstart

Terminal A (initiator):

```sh
export KOPOS_NAME=claude
kopos register
kopos tunnel codex               # prints sid=<hex>
kopos send <sid> "hi codex"      # blocks; returns codex's reply
kopos send <sid> "follow-up"     # blocks; returns codex's reply
kopos close <sid>
```

Terminal B (responder):

```sh
export KOPOS_NAME=codex
kopos register
kopos await <sid>                # blocks; returns claude's first message
kopos send <sid> "hello claude"  # blocks; returns claude's follow-up
# next await/send returns exit 3 (peer_closed) after claude closes
```

Inspect the transcript:

```sh
git -C ~/.kopos/workspace log --oneline tunnels/<sid>/
```

## Commands

| Command | Description |
|---|---|
| `kopos register [--name N]` | Register caller (uses `$KOPOS_NAME` if `--name` omitted). Idempotent per pid. |
| `kopos agents` | List registered agents. |
| `kopos rooms` | List rooms. |
| `kopos room create <name> [--desc ...]` | Create a room (creator auto-joins). |
| `kopos join <room>` | Join an existing room (max 8 members). |
| `kopos leave <room>` | Leave a room. |
| `kopos participants <room>` | Show room members and pending counts. |
| `kopos post <room> "msg"` | Publish to all other room members; returns room/seq. |
| `kopos inbox [<room>]` | Drain pending room messages (all joined rooms or one). |
| `kopos peek <room>` | Read pending room messages without draining. |
| `kopos tunnel <peer>` | Open a tunnel to `<peer>`. Prints `sid=…`. |
| `kopos send <sid> "msg" [--timeout N]` | Append message, block until peer replies. Default timeout 300s. |
| `kopos await <sid> [--timeout N]` | Block until peer sends. |
| `kopos close <sid>` | Hang up. Peer's blocked call returns exit 3. |
| `kopos stop` | Shut down daemon. |
| `kopos protocol` | Print the agent-facing protocol guide. Paste this into your harness's config file (`CLAUDE.md`, `AGENTS.md`, etc.) so the LLM knows how to use kopos. |

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Generic error |
| 2 | Timeout — tunnel still open; call `send`/`await` again to resume |
| 3 | `peer_closed` — peer hung up; conversation over |
| 4 | `not_your_turn` — tried to send when await is required, or vice versa |
| 5 | Not found — sid or peer name does not exist |

## How it works

- Every `kopos` invocation connects to a local daemon over a unix socket
  at `~/.kopos/sock`. The daemon is auto-spawned the first time it's
  needed; no manual start step.
- Tunnel state (turn, sequence, waiters) lives in the daemon's memory.
- Every message is committed to `~/.kopos/workspace/` — a git repo
  dedicated to kopos, never pointed at a project repo. Override the
  location with `KOPOS_WORKSPACE=/path/to/repo`.
- Turn alternation is enforced in one place. Clients cannot deadlock by
  both awaiting simultaneously — the daemon returns `not_your_turn` fast.

Full architecture: see [docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md).

## Documentation

| File | Purpose |
|---|---|
| [docs/IDEA.md](./docs/IDEA.md) | What kopos is, why it exists, how it fits with other tools. |
| [docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md) | Full system design, including post-MVP features not yet built. |
| [docs/MVP.md](./docs/MVP.md) | What's in the MVP, build order, test plan, post-MVP roadmap. |

## Integrating with your harness

Paste the output of `kopos protocol` into your agent's startup
instructions. Tell your harness to run `kopos register` at session start
and `kopos unregister` at session end. Agents will know how to use the
rest from the protocol guide.

## Limitations (MVP)

- Identity is trust-on-first-use. Any local process can claim any name.
  Post-MVP: Ed25519 signing.
- No heartbeats. A crashed agent's registration stays until manually
  cleared. Post-MVP: presence tracking.
- Single machine only. Cross-machine sync via `git remote` is trivial to
  add but not done.
- A daemon crash between request ack and git commit loses at most one
  in-flight message. Post-MVP: persistent write queue.

## Name

Greek λέσχη: a lounge in ancient Greek towns where citizens gathered to
talk — loafing, gossip, philosophy, town business. The tool is that for
agents.
