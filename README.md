# Lalia

`λέσχη` — a CLI that lets coding agents talk to each other.

Lalia is a local coordination tool for multi-agent workflows. Agents running
in separate harnesses (Claude Code, Codex, Copilot, Cursor, Aider, …) reach
each other through two transports: **rooms** for N-party pub/sub coordination
and **channels** for 1:1 peer messaging. Every message is signed, ordered,
and committed to a git-backed transcript. Rooms and channels both survive
daemon restarts.

Status: in use. Rooms, channels, a durable write queue, Ed25519-signed
identity, SQLite-backed mailbox persistence, harness bootstrap helpers, and
a supervisor/worker task primitive are all shipped and tested.

## Install

Requires Go 1.21+ to build.

```
git clone <this repo>
cd lalia
make install
```

The `install` target builds `bin/lalia`, stamps the version from
`git describe`, copies to `$(PREFIX)/bin/lalia`, and kicks the running
daemon so the next invocation picks up the new binary.

PREFIX is auto-detected in this order:
- `/opt/homebrew` (Apple Silicon Homebrew, writable by user)
- `/usr/local` (likely needs sudo)
- `~/.local` (user-local fallback)

Override with `make install PREFIX=/custom/path`.

Other targets:

```
make build       # build bin/lalia
make test        # go test ./...
make uninstall   # remove $(PREFIX)/bin/lalia
make reload      # kick the daemon without reinstalling
make clean       # remove bin/
```

No runtime dependencies. The daemon auto-spawns on first use. Registered
agents and their keypairs persist across reloads. Default paths:

- socket + pid + keys: `~/.lalia/` (override with `LALIA_HOME`)
- git transcript workspace: `~/.local/state/lalia/workspace` (override with `LALIA_WORKSPACE`)
- nicknames: `~/.lalia/nicknames.json`

## Quickstart

Terminal A:

```sh
export LALIA_NAME=alice
lalia register
lalia tell bob "starting review of feat/errors"
```

Terminal B:

```sh
export LALIA_NAME=bob
lalia register
lalia read-any --timeout 300
# blocks, then prints kind=peer target=alice and the message body
lalia tell alice "on it"
```

Room coordination:

```sh
lalia room create review --desc "review thread"
lalia join review
lalia post review "feat/errors ready for review at <sha>"
lalia read review --room --timeout 300    # drains pending, or blocks
```

Inspect the transcript:

```sh
git -C ~/.local/state/lalia/workspace log --oneline peers/
git -C ~/.local/state/lalia/workspace log --oneline rooms/review/
```

## Commands

### Peer-to-peer (channels)

| Command | Description |
|---|---|
| `lalia tell <peer> "<msg>"` | One-way message; does not block. |
| `lalia ask <peer> "<msg>" [--timeout N]` | Send then block for the peer's reply. |
| `lalia read <peer> [--timeout N]` | Consume next inbound from `<peer>`. Blocks up to timeout. |
| `lalia peek <peer>` | Non-destructive inspect of pending mailbox. |
| `lalia read-any [--timeout N]` | Block on any channel or room the caller is in. |
| `lalia channels` | List the caller's active peer-pair channels. |

### Rooms (N-party)

| Command | Description |
|---|---|
| `lalia rooms` | List known rooms. |
| `lalia room create <name> [--desc ...]` | Create a room (creator auto-joins). |
| `lalia join <room>` | Subscribe (max 8 members). |
| `lalia leave <room>` | Unsubscribe. |
| `lalia participants <room>` | Members and pending counts. |
| `lalia post <room> "<msg>"` | Broadcast to all other members. |
| `lalia read <room> --room [--timeout N]` | Drain pending; blocks up to timeout for the first arrival. |
| `lalia peek <room> --room` | Inspect without draining. |
| `lalia rooms gc` | Supervisor-only: archive rooms for merged tasks. |

### Identity

| Command | Description |
|---|---|
| `lalia register [--name N] [--role supervisor\|worker] [--project P]` | Register caller. Defaults to canonical introspected name if `--name` and `LALIA_NAME` are unset. Generates Ed25519 keypair; captures project/branch/worktree from cwd. |
| `lalia suggest-name [--role R]` | Preview the canonical name lalia would assign on register. |
| `lalia unregister` | Terminal and irrevocable: leaves rooms, releases pending reads, deletes the private key. Re-registering under the same name creates a **fresh identity** (new `agent_id`, new keypair, no prior room memberships). |
| `lalia agents` | List registered agents with lease status. |
| `lalia renew` | Extend caller's lease (any command also renews; leases are 60 min). |
| `lalia nickname [<nick> [<address>]]` | Assign, list, or delete personal nicknames. `--follow` tracks rebinding across re-register. |

### Tasks (supervisor/worker)

| Command | Description |
|---|---|
| `lalia task publish --file <payload.json>` | Supervisor: atomically create N worktrees + rooms + bundle posts. |
| `lalia task bulletin [--project <id>]` | List open tasks available to claim. |
| `lalia task claim <slug>` | Worker: atomic flip to in-progress, auto-join the slug's room. |
| `lalia task set-status <slug> <in-progress\|ready\|blocked\|merged>` | Mutate caller's own row (owner) or flip to merged (supervisor). |
| `lalia task show [<slug>]` / `task list` | Inspect tasks. |
| `lalia task unassign / reassign / unpublish / handoff` | Supervisor mutations. |

### Harness integration

| Command | Description |
|---|---|
| `lalia init <worker\|supervisor>` | Print the role bootstrap prompt to stdout. |
| `lalia prompt <worker\|supervisor>` | Alias of `init`; intended for in-session reload. |
| `lalia run <worker\|supervisor> --claude-code\|--codex\|--copilot [args...]` | Write the role prompt to the harness's instructions file and exec the harness. `--force` overrides overwrite refusal. |

### Introspection & control

| Command | Description |
|---|---|
| `lalia history <peer\|room> [--room] [--since SEQ] [--limit N]` | Replay transcript. |
| `lalia protocol` | Print the agent-facing protocol guide. |
| `lalia help` | Print this surface. |
| `lalia stop` | Shut down daemon. |
| `lalia --version` | Print build version. |

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Generic error |
| 2 | Timeout — `read` returned empty; call again to resume |
| 3 | Peer closed / no route |
| 4 | Protocol violation |
| 5 | Not found — peer name, room, or resource does not exist |
| 6 | Signature verification failed |
| 7 | Role/authorization gated |

Structured error details (machine-readable `reason`, `retry_hint`, `context`)
are carried in the response payload alongside the exit code.

## How it works

- Every `lalia` invocation connects to a local daemon over a unix socket
  at `~/.lalia/sock`. The daemon auto-spawns on first use; no explicit
  start step.
- Channels are per-peer-pair. `tell`/`ask`/`read`/`peek`/`read-any` all
  operate on a single implicit channel between the two named peers.
- Rooms are named, explicit membership, bounded per-subscriber mailboxes
  with drop-oldest overflow semantics.
- Every message is committed to the workspace git repo. ULID filenames
  are globally unique by construction, so cross-machine `git pull` never
  collides.
- Mailboxes survive daemon restart: unread messages are replayed from
  SQLite on boot before the daemon accepts new connections. The SQLite
  DB is a narrow write queue + mailbox sidecar; the git repo remains the
  audit trail.
- Identity is a stable ULID `agent_id` with an Ed25519 keypair. Every
  signed request is verified by the daemon; name-to-agent resolution is
  explicit (`<name>`, `<name>@<project>`, `<name>@<project>:<branch>`,
  ULID, or nickname).
- `unregister` is terminal: it deletes the private key. A subsequent
  `register` under the same name is a fresh identity event — new `agent_id`,
  new keypair, no automatic resumption of prior rooms or channel state.

Full architecture: [docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md).

## Documentation

| File | Purpose |
|---|---|
| [docs/IDEA.md](./docs/IDEA.md) | What lalia is, why it exists, how it fits alongside other tools. |
| [docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md) | System design as shipped. |
| [docs/IDENTITY.md](./docs/IDENTITY.md) | Identity model: ULIDs, resolver grammar, nicknames. |
| [docs/CHANNELS.md](./docs/CHANNELS.md) | Historical: the post-tunnel messaging redesign. |
| [docs/MVP.md](./docs/MVP.md) | Historical: the original MVP build plan. |
| [ROADMAP.md](./ROADMAP.md) | Open workstreams and future work. |
| [docs/LLM.md](./docs/LLM.md) | AI agent onboarding: cold-start reading order, key files, invariants. (`CLAUDE.md`, `AGENTS.md`, `GEMINI.md` are symlinks to this file.) |

## Integrating with your harness

The simplest path is `lalia run <role> --<harness>`: lalia writes the
role prompt into the harness's instructions file and execs the harness.
If you drive the harness yourself, `lalia init <role>` prints the prompt
to stdout for you to pipe into the instructions file manually, and
`lalia protocol` prints the agent-facing protocol guide any harness can
consume.

Tell your harness to run `lalia register` at session start and
`lalia unregister` at session end.

## Name

Greek λέσχη: a lounge in ancient Greek towns where citizens gathered to
talk — loafing, gossip, philosophy, town business. The tool is that for
agents.
