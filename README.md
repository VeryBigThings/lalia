# Lesche

`λέσχη` — a CLI that lets coding agents talk to each other.

Lesche is a local coordination tool for multi-agent workflows. Two agents
running in separate harnesses (Claude Code, Codex, Cursor, Aider, …) can
open a direct synchronous tunnel between them. Every exchange is recorded
to a git-backed transcript. Rooms provide N-party async coordination.

Status: **MVP+**. Tunnel and room transports, auto-spawned daemon,
git-backed log, basic registry. Tested end-to-end between Claude Code and Codex.

## Install

Requires Go 1.21+ to build.

```
git clone <this repo>
cd lesche
make install
```

The `install` target builds `bin/lesche`, stamps the version from
`git describe`, copies to `$(PREFIX)/bin/lesche`, and kicks the running
daemon so the next invocation picks up the new binary.

PREFIX is auto-detected in this order:
- `/opt/homebrew` (Apple Silicon Homebrew, writable by user)
- `/usr/local` (likely needs sudo)
- `~/.local` (user-local fallback)

Override with `make install PREFIX=/custom/path`.

Other targets:

```
make build       # build bin/lesche
make test        # go test ./...
make uninstall   # remove $(PREFIX)/bin/lesche
make reload      # kick the daemon without reinstalling
make clean       # remove bin/
```

No runtime dependencies. The daemon auto-spawns on first use.
Registered agents and their keypairs persist across reloads (keys at
`~/.lesche/keys/`, registry at `~/.local/state/lesche/workspace/registry/`).
Open tunnels die on daemon restart; peers see `peer_closed` and can
reopen.

## Quickstart

Terminal A (initiator):

```sh
export LESCHE_NAME=claude
lesche register
lesche tunnel codex               # prints sid=<hex>
lesche send <sid> "hi codex"      # blocks; returns codex's reply
lesche send <sid> "follow-up"     # blocks; returns codex's reply
lesche close <sid>
```

Terminal B (responder):

```sh
export LESCHE_NAME=codex
lesche register
lesche await <sid>                # blocks; returns claude's first message
lesche send <sid> "hello claude"  # blocks; returns claude's follow-up
# next await/send returns exit 3 (peer_closed) after claude closes
```

Inspect the transcript:

```sh
git -C ~/.lesche/workspace log --oneline tunnels/<sid>/
```

## Commands

| Command | Description |
|---|---|
| `lesche register [--name N]` | Register caller (uses `$LESCHE_NAME` if `--name` omitted). Idempotent per pid. |
| `lesche agents` | List registered agents. |
| `lesche rooms` | List rooms. |
| `lesche room create <name> [--desc ...]` | Create a room (creator auto-joins). |
| `lesche join <room>` | Join an existing room (max 8 members). |
| `lesche leave <room>` | Leave a room. |
| `lesche participants <room>` | Show room members and pending counts. |
| `lesche post <room> "msg"` | Publish to all other room members; returns room/seq. |
| `lesche inbox [<room>]` | Drain pending room messages (all joined rooms or one). |
| `lesche peek <room>` | Read pending room messages without draining. |
| `lesche tunnel <peer>` | Open a tunnel to `<peer>`. Prints `sid=…`. |
| `lesche send <sid> "msg" [--timeout N]` | Append message, block until peer replies. Default timeout 300s. |
| `lesche await <sid> [--timeout N]` | Block until peer sends. |
| `lesche close <sid>` | Hang up. Peer's blocked call returns exit 3. |
| `lesche stop` | Shut down daemon. |
| `lesche protocol` | Print the agent-facing protocol guide. Paste this into your harness's config file (`CLAUDE.md`, `AGENTS.md`, etc.) so the LLM knows how to use lesche. |

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

- Every `lesche` invocation connects to a local daemon over a unix socket
  at `~/.lesche/sock`. The daemon is auto-spawned the first time it's
  needed; no manual start step.
- Tunnel state (turn, sequence, waiters) lives in the daemon's memory.
- Every message is committed to `~/.lesche/workspace/` — a git repo
  dedicated to lesche, never pointed at a project repo. Override the
  location with `LESCHE_WORKSPACE=/path/to/repo`.
- Turn alternation is enforced in one place. Clients cannot deadlock by
  both awaiting simultaneously — the daemon returns `not_your_turn` fast.

Full architecture: see [ARCHITECTURE.md](./ARCHITECTURE.md).

## Documentation

| File | Purpose |
|---|---|
| [IDEA.md](./IDEA.md) | What lesche is, why it exists, how it fits with other tools. |
| [ARCHITECTURE.md](./ARCHITECTURE.md) | Full system design, including post-MVP features not yet built. |
| [MVP.md](./MVP.md) | What's in the MVP, build order, test plan, post-MVP roadmap. |

## Integrating with your harness

Paste the output of `lesche protocol` into your agent's startup
instructions. Tell your harness to run `lesche register` at session start
and `lesche unregister` at session end. Agents will know how to use the
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
