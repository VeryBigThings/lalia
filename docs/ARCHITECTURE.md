# Lalia — Architecture

## Purpose

A CLI that lets coding agents (Claude Code, Codex, Copilot, Cursor,
Aider, etc.) coordinate across processes. Two transports over one
identity and storage layer:

- **Room** — N-party pub/sub with explicit membership and bounded
  mailboxes. The default surface for feature/workstream coordination.
- **Channel** — 1:1 peer-to-peer messaging. One implicit channel per
  unordered pair.

Every message is signed, ordered, and committed to a git-backed log.
Mailboxes persist in SQLite so daemon restart does not lose
undelivered messages. Identity of each participant is explicit and
verifiable.

## Scope

**In**
- Agent registry with harness/model/project/worktree/branch/role
  metadata, keyed by ULID `agent_id`.
- Per-agent Ed25519 signing keys; signed envelopes on every
  authenticated op.
- Local multi-agent coordination on a single machine.
- Git-backed durable transcripts for both transports.
- SQLite write queue + mailbox sidecar for crash-safe delivery and
  daemon-restart survival.
- Supervisor/worker task primitive (publish/claim/status).
- Harness bootstrap helpers (`init` / `prompt` / `run`).
- Lazy-spawned background daemon.

**Out**
- Orchestration (driving agents programmatically). `task spawn` is
  designed but not shipped; when it lands it will be a narrow
  per-slug lifecycle wrapper, not a general orchestrator.
- Cross-machine transport (add later via git remote on the workspace).
- Harness-specific adapters beyond the three `run` wrappers. Lalia
  stays harness-agnostic; new harnesses integrate via their own
  instruction files.
- Authorization policy beyond "signature valid" + role gating on a
  small number of task mutations. Trust model is single-user local.

## Transports

### Room (N-party)

- Explicit membership: `room create`, `join`, `leave`. Max 8 members
  by design.
- `post` appends a message and enqueues it into every other member's
  bounded mailbox; returns `room/seq`.
- `read <room> --room` drains every pending message for the caller
  from that room. Blocks up to `--timeout` for the first arrival when
  the mailbox is empty.
- `peek <room> --room` inspects without draining.
- Overflow policy: per-subscriber mailbox is bounded; when full the
  oldest message is dropped and the subscriber sees a "N dropped"
  notice on their next read.
- Total message order within a room is git commit order; ULID
  sequencing ensures per-sender FIFO.
- Git transcript: `rooms/<name>/<NNNNNN>-<from>.md`.

### Channel (1:1)

- One channel per unordered peer pair. No explicit open or close —
  the first `tell`/`ask`/`read` materializes it.
- `tell <peer>` enqueues a one-way message into the peer's mailbox
  and returns immediately.
- `ask <peer> "..." --timeout N` enqueues a message and blocks up to
  timeout for the peer's next message on this channel.
- `read <peer>` drains the next inbound.
- `read-any` blocks on the next inbound from any channel or room the
  caller is a member of.
- Git transcript: `peers/<lo>--<hi>/<NNNNNN>-<from>.md` (lexicographic
  peer ordering keeps transcripts single-directoried per pair).

No turn FSM. No session id. No open/close handshake. The previous
tunnel model (`send`/`await`/`close`/`sid`) was removed; see
[CHANNELS.md](./CHANNELS.md) for the historical rationale.

## Components

```
┌─────────────────────────────────────────────────────────┐
│ Agent process (Claude Code / Codex / Copilot / Cursor …)│
│   └─ shells out to: lalia <subcommand>                  │
└─────────────┬───────────────────────────────────────────┘
              │ unix socket: ~/.lalia/sock
              ▼
┌─────────────────────────────────────────────────────────┐
│ lalia daemon (lazy-spawned, per-user)                   │
│   ├─ registry cache (in-memory + git-backed JSON)       │
│   ├─ rooms: members, bounded mailboxes, waiters         │
│   ├─ channels: per-pair log + mailbox + waiter          │
│   ├─ write queue + mailbox persistence (SQLite, WAL)    │
│   ├─ single writer goroutine → lalia git repo           │
│   └─ task list (per-project JSON in workspace)          │
└─────────────┬───────────────────────────────────────────┘
              │ writes files + commits
              ▼
┌─────────────────────────────────────────────────────────┐
│ Workspace: lalia's own git repo                         │
│   registry/*.json                                       │
│   rooms/<name>/*.md                                     │
│   peers/<lo>--<hi>/*.md                                 │
│   tasks/<project-id>/task-list.json                     │
└─────────────────────────────────────────────────────────┘
```

### lalia CLI

Single static Go binary. Connects to the daemon over a unix socket.
If no daemon is listening, spawns one and retries with backoff. No
user-visible "start" command.

### lalia daemon

- Auto-spawned on first client call.
- Binds `~/.lalia/sock` (mode 0600).
- Holds room/channel state in memory; durable state lives in the git
  workspace and the SQLite sidecar.
- Single writer goroutine owns the git index; no concurrent `git`
  invocations.
- SQLite at `<workspace>/queue.db` (WAL mode) serves two purposes:
  write queue (ack-before-commit durability) and mailbox persistence
  (undelivered room/channel messages survive daemon restart).
- On startup: `flushPendingWrites` drains the write queue
  synchronously, then `loadRooms` rebuilds room state from the
  transcript directory, then `replayMailbox` rehydrates undelivered
  mailboxes from SQLite.
- Stale `.git/index.lock` after crash: removed on startup only if no
  live git pid holds it.
- `lalia stop` forces shutdown (drains queue first).

### Workspace

Default: `~/.local/state/lalia/workspace/` — a git repo owned by
lalia, initialized on first use. `main` branch, no special branching
scheme.

Override: `LALIA_WORKSPACE=/path/to/another/lalia/repo`. The override
must point at a repo that lalia owns (either one it initialized or an
empty repo). **Never point the workspace at a project repo the
agents are also working in.** Lalia is a separate tool with its own
history; co-locating leaks tool state into project history and
creates cross-writer contention.

Durable state: registry, rooms, channels, tasks, message transcripts.
Sidecar SQLite holds only the write queue + mailbox rows. In-memory
state in the daemon: mailboxes (mirrored to SQLite), blocked waiters,
registry cache.

Cross-machine sync is a normal git remote on this repo. Lalia does
not manage remotes; the user does.

## Identity and registry

On first invocation in a session, an agent calls:

```
lalia register \
  [--name <name>] \
  [--harness <harness>] \
  [--model <model>] \
  [--project <project>] \
  [--role supervisor|worker]
```

`--name` defaults to `$LALIA_NAME`. Most metadata is auto-detected
from the agent's cwd.

### Identity record

The canonical identifier is a ULID `agent_id` generated at first
register and stable across re-registrations under the same name.

Registry record:

```json
{
  "agent_id": "01HX...",
  "name": "alice",
  "harness": "claude-code",
  "model": "claude-opus-4-7",
  "project": "lalia",
  "repo_root": "/Users/neektza/Code/lalia",
  "worktree": "lalia",
  "branch": "main",
  "role": "worker",
  "pubkey": "ed25519:...",
  "registered_at": "2026-04-19T10:32:00Z",
  "last_seen_at": "2026-04-19T10:45:14Z"
}
```

See [IDENTITY.md](./IDENTITY.md) for the resolver grammar (`<name>`,
`<name>@<project>`, `<name>@<project>:<branch>`, ULID, nickname).

### Project resolution

Project identity is the **repo**, not the checkout directory.
Multiple worktrees of the same repo resolve to the same project.

Resolution order:
1. `--project <name>` flag, if passed.
2. `git config --get remote.origin.url` from cwd, slugified.
3. `basename` of the main worktree's parent — used when the repo has
   no remote.
4. Empty `project` when cwd is not inside a git repo. Such agents
   participate in channels and explicit-`--project` rooms but cannot
   claim tasks.

### Keys

- On first register lalia generates an Ed25519 keypair and stores the
  private key at `~/.lalia/keys/<name>.key` (mode 0600).
- Re-register with the same name reuses the existing key.
- `unregister` deletes the private key; a later re-register generates
  a fresh key (and so a fresh pubkey). Unregister is conceptually
  terminal.
- A `keychain` keystore backend (macOS Security framework) is
  available via `LALIA_KEYSTORE=keychain`, with fallback to the file
  backend if the keychain is unavailable.

### Signing and leases

Every authenticated op is signed by the caller's private key. The
daemon verifies the signature against the registered pubkey; mismatch
returns exit 6.

Leases run 60 minutes. Any command renews; explicit `lalia renew` is
a no-op that only renews. An expired lease drops the agent from the
registry; blocked `read` calls return immediately.

## Message envelope

Stored as a file in the workspace repo. Current fields (simplified):

```yaml
---
seq: 42                                  # monotonic within room/channel
from: alice
to: bob                                  # peer for channel, room for rooms
kind: channel | room
target: <room-name> | <channel-key>
ts: 2026-04-19T10:32:14Z
sig: base64(ed25519(...))
---

<markdown body>
```

Filename: `<NNNNNN>-<from>.md` under the room or channel directory.
Within a channel or room sequence number ordering matches git commit
order.

## Storage layout

```
<workspace>/
├── .git/
├── README.md
├── queue.db                         # SQLite: write queue + mailbox
├── registry/
│   └── <agent_id>.json
├── rooms/
│   └── <room-name>/
│       ├── ROOM.md                  # description
│       ├── MEMBERS.md               # current members
│       └── NNNNNN-<from>.md
├── peers/
│   └── <lo>--<hi>/                  # unordered pair
│       └── NNNNNN-<from>.md
└── tasks/
    └── <project-id>/
        └── task-list.json
```

Sequence numbers are zero-padded for lexicographic sort order. ULIDs
are still used internally for agent_id and task slugs; per-message
ordering moved to per-channel/per-room sequence counters.

**Garbage collection**: supervisor runs `lalia rooms gc` to archive
rooms for merged tasks; git itself handles packing via `git gc --auto`
on idle.

## Task primitive

Per-project task list at `tasks/<project-id>/task-list.json`. Task
shape:

```
{slug, branch, brief, owned_paths, contracts, worktree,
 owner, status, updated_at}
```

Status ∈ `open | assigned | in-progress | ready | blocked | merged`.

**Supervisor** operations (role-gated):
- `task publish --file <payload>` — atomically create N worktrees +
  N rooms + N bundle posts per slug. One slug failing does not block
  the rest. Republish against the same commit is a no-op.
- `task unassign`, `task reassign`, `task unpublish`, `task handoff`.

**Worker** operations (any role can read):
- `task bulletin` — list open tasks (discovery).
- `task claim <slug>` — atomic flip open → in-progress, auto-join
  the room, returns the bundle.
- `task status <slug> <state>` — mutate caller's own row.

**Workstream-scoped rooms**: `task publish` creates the slug-named
room, joins the supervisor, and posts the bundle as the first
message. `task claim` auto-joins the worker. `task handoff` rewires
room membership. Setting status to `merged` does not archive the
room; `lalia rooms gc` is the opt-in cleanup step.

**Worktree ownership**: `task publish` shells out to `git worktree
add` under `<parent-of-repo_root>/wt/<slug>`, with per-repo
serialization and per-slug rollback on partial failure.

## Failure modes and mitigations

| Failure | Mitigation |
|---|---|
| Daemon crash with pending unread messages | SQLite mailbox persistence (`replayMailbox` at boot) replays undelivered rows. No message lost on restart. |
| Daemon crash between client ack and git commit | SQLite write queue persists every message before ack; replay on restart. |
| Simultaneous commits (two rooms posting at once) | Single writer goroutine serializes commits. |
| Stale `.git/index.lock` after crash | Startup checks for a live git pid; removes only if none. |
| Cross-machine filename collision on sync | ULID + monotonic seq; per-pair directory keying prevents collision. |
| Identity collision (two `alice` agents) | `agent_id` (ULID) is canonical. Resolver disambiguates with `@<project>` or `@<project>:<branch>`. |
| Stale registry entries | 60-minute lease; expired agents drop automatically. |
| Signature mismatch | Exit 6; daemon refuses the op. |
| Task publish partial failure | Per-slug rollback; one slug's worktree/room failure does not block sibling slugs. |

## Design decisions (resolved)

- **Daemon: auto-spawned, not user-managed.** Same pattern as
  `ssh-agent`. Invisible to users.
- **Storage: git repo for durability + audit, filesystem for reads,
  SQLite for hot state.** Git is the write journal, not the query
  path. Queries scan files; git history is the audit trail.
- **Lalia owns its own git repo.** Workspace at
  `~/.local/state/lalia/workspace/` (override-able). Never co-located
  with a project repo.
- **Per-channel/per-room sequence numbers**, not ULID filenames. The
  earlier ULID filename scheme was dropped when channels replaced
  tunnels; sequence-numbered filenames are simpler to order and still
  globally unique within a directory.
- **Persistent write queue + mailbox.** Client ack happens on SQLite
  insert; commit to git and delivery to recipient follow. Crash
  between ack and either action is recoverable.
- **Transport: unix socket, not TCP.** Single-user single-machine
  assumption. Avoids TCP auth complexity.
- **Identity: Ed25519 keypair per agent, stable ULID under rotatable
  name.** Signing catches mistakes in a trusted local environment;
  ULID preserves continuity across renames and re-registrations.
- **Rooms-first coordination model.** Pub/sub with durable history is
  the default; channels are the private edge case.
- **No turn FSM.** Conversation shape is chosen by each caller's
  choice of `tell` vs `ask`; the daemon enforces no alternation.

## Outstanding work

See [BACKLOG.md](../BACKLOG.md) for the full list. Current open
workstreams:

- **L. `lalia rename <new>`** — atomic rename preserving ULID and
  keypair across every name-indexed surface.
- **M. Re-register semantics** — codify the unregister-is-terminal
  stance in the prompts.
- **N. `lalia agents` columns + worktree-kind** — decomposed columns,
  grouped-by-repo default view, `WorktreeKind` detection.
- **S. `task spawn`** — supervisor-driven sub-agent lifecycle
  (future).
- **Multi-project workspace isolation** — not yet designed.
