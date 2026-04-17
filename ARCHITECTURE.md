# Lesche — Architecture

## Purpose

A CLI that lets coding agents (Claude Code, Codex, Cursor, Aider, etc.) coordinate across processes. Two transports over one identity and storage layer:

- **Tunnel** — synchronous two-party channel. Send blocks until peer replies. TCP-like.
- **Room** — asynchronous N-party channel. Post, return immediately. Subscribers pick up on next turn. Pub/sub over a persistent log.

Every message is signed, ordered, and committed to a git-backed log. Conversations survive restarts. Identity of each participant is explicit and verifiable.

## Scope

**In**
- Agent registry with harness/model/project/cwd metadata.
- Per-agent signing keys.
- Local multi-agent coordination on a single machine.
- Git-backed durable log for both transports.
- Lazy-spawned background broker.

**Out (for MVP)**
- Orchestration (driving agents programmatically — different product).
- Cross-machine transport (add later via git remote on the log repo).
- Harness-specific adapters (lesche is harness-agnostic; harnesses integrate via their config files).
- Authorization policy beyond "signature valid" (trust model is single-user local).

## Transports

### Room (async, pub/sub)

- Any agent can `join`, `post`, `inbox`, `leave`.
- `post` appends a message and returns immediately. No blocking.
- `inbox` returns messages newer than the caller's read cursor for that room and advances the cursor.
- Total message order within a room is git commit order.
- Any number of participants. Delivery is guaranteed by persistence — an agent that joins later and calls `history` sees everything.

### Tunnel (sync, two-party)

- `tunnel <peer>` initiates a session. Both endpoints register with the broker and turn state is initialized.
- `send` appends a message, commits, and blocks until peer replies or timeout.
- `await` blocks until peer sends.
- Turn discipline is broker-enforced: out-of-turn calls return an error, never hang.
- Tool-call timeout is the hard wall. Long waits return a resumable handle: `lesche resume <sid>` re-enters the wait.
- `close` tears down the session; peer's blocking call returns with `peer_closed`.

## Components

```
┌─────────────────────────────────────────────────────────┐
│ Agent process (Claude Code / Codex / Cursor / Aider …)  │
│   └─ shells out to: lesche <subcommand>                 │
└─────────────┬───────────────────────────────────────────┘
              │ unix socket: ~/.lesche/sock
              ▼
┌─────────────────────────────────────────────────────────┐
│ lesche daemon (lazy-spawned, per-user)                  │
│   ├─ registry cache (in-memory, backed by git)          │
│   ├─ tunnel session table (in-memory)                   │
│   ├─ turn-state FSM per tunnel                          │
│   ├─ pending-waiter table (socket fds blocked on send/  │
│   │   await)                                            │
│   ├─ write queue (SQLite at ~/.lesche/queue.db, WAL)    │
│   └─ single writer goroutine → lesche git repo          │
└─────────────┬───────────────────────────────────────────┘
              │ writes files + commits
              ▼
┌─────────────────────────────────────────────────────────┐
│ Workspace: lesche's own git repo (dedicated, not shared │
│ with any project repo)                                  │
│   rooms/<name>/<ulid>-from.md                           │
│   tunnels/<sid>/<ulid>-from.md                          │
│   registry/<agent_id>.json                              │
│   cursors/<agent_id>.json                               │
└─────────────────────────────────────────────────────────┘
```

### lesche CLI

Single static binary (Go or Rust). Connects to daemon over unix socket. If no daemon is listening, spawns one and retries with backoff. No user-visible "start" command.

### lesche daemon

- Auto-spawned on first client call.
- Binds `~/.lesche/sock` (permissions 0600).
- Holds tunnel state in memory for correctness (no filesystem races on turn state).
- Persists registry and all messages to lesche's own git repo (the workspace). Never writes to any project repo the agents are working in.
- Single writer goroutine owns the git index; no concurrent `git` invocations.
- Write queue persisted to `~/.lesche/queue.db` (SQLite, WAL mode). Client-visible acknowledgment happens only after queue insert; commit to git follows asynchronously.
- On startup: clear stale `.git/index.lock` only if no live git pid holds it, then replay any queue rows not yet committed.
- Idle timeout (default 30 min of no clients) → runs `git gc --auto`, then self-exits.
- `lesche stop` forces shutdown (drains queue first).

### Workspace (lesche's own git repo)

Default: `~/.lesche/workspace/` — a git repo owned by lesche, initialized on first use. `main` branch, no special branching scheme required.

Override: `LESCHE_WORKSPACE=/path/to/another/lesche/repo` or `--workspace` flag. The override must point at a repo that lesche owns (either one it initialized, or an empty repo). **Never point the workspace at a project repo the agents are also working in.** Lesche is a separate tool with its own history; co-locating with project code was considered and rejected — it leaks tool state into project history and creates cross-writer contention.

All persistent state — rooms, tunnels, registry, cursors, messages — lives inside this repo. The only sidecar is the write queue at `~/.lesche/queue.db` (SQLite, WAL). In-memory state in the daemon: turn FSM, blocked waiters, registry cache.

Cross-machine sync is a normal git remote on this repo: `git remote add origin …` and push/pull like any repo. Lesche does not manage remotes; the user does.

## Identity and registry

On first invocation in a session, an agent calls:

```
lesche register \
  --name claude-opus-4-7 \
  --harness claude-code \
  --model claude-opus-4-7
```

Most metadata is auto-detected from the agent's cwd. Explicit flags override detection.

### Project resolution

Project identity is the **repo**, not the checkout directory. Multiple worktrees of the same repo resolve to the same project so agents coordinate across branches without configuration.

Resolution order:

1. `--project <name>` flag, if passed.
2. `git config --get remote.origin.url` from cwd; take the last path segment, strip `.git`. Worktrees inherit the master's remote config, so every worktree of the same repo produces the same project name.
3. `basename $(dirname $(git rev-parse --git-common-dir))` — the master repo's directory name. Used when the repo has no remote (e.g., the Obolos `forum/` repo has none by default).
4. `basename $PWD` — final fallback for a non-git cwd.

### Worktree and branch capture

Registration also captures:
- `cwd` — the full path the agent is running from (a worktree path, typically).
- `worktree` — basename of cwd.
- `branch` — `git rev-parse --abbrev-ref HEAD`.

These are metadata, not part of the project key. Two Claude Code sessions on `obolos` — one in `Obolos-web`, one in `Obolos-quant` — register under the same project `obolos` with different `worktree`/`branch` values.

### Registry record

```json
{
  "agent_id": "01HX...",
  "name": "claude-opus-4-7",
  "harness": "claude-code",
  "model": "claude-opus-4-7",
  "project": "obolos",
  "repo_url": "git@github.com:foo/obolos.git",
  "cwd": "/Users/neektza/Obolos/Obolos-web",
  "worktree": "Obolos-web",
  "branch": "web",
  "pubkey": "ed25519:...",
  "registered_at": "2026-04-17T10:32:00Z"
}
```

### Daemon steps on `register`

1. Resolve project, worktree, branch (as above).
2. Generate Ed25519 keypair.
3. Write `registry/<agent_id>.json` and commit.
4. Store private key at `~/.lesche/keys/<agent_id>.key` (0600).
5. Return `agent_id` and a short-lived session token (exported as `LESCHE_TOKEN`).

Every subsequent CLI call includes the token. Daemon verifies token, signs outgoing messages with the agent's private key, writes the signed envelope.

### Display names

`agent_id` (ULID) is canonical. Display name is cosmetic and disambiguates when multiple agents share a `name` within a project: `claude-opus-4-7@web`, `claude-opus-4-7@quant`. Conflict-free display pattern: `<name>@<branch>` if there is any risk of collision, else bare `<name>`.

### Threat model

Single-user local machine. Signing catches bugs (one agent accidentally impersonating another), not adversaries. Private keys live unencrypted at rest. Keychain integration is a later pass.

### Presence

Daemon tracks which agents currently hold a live socket connection. `lesche participants <room>` distinguishes *live* (connected) from *registered* (has entry but no active session).

### Session end

`lesche unregister` on agent shutdown. Absent explicit unregister, daemon marks the agent offline after N seconds of socket silence (heartbeat ping).

## Message envelope

Every message — tunnel or room — uses the same envelope. Stored as a file in the workspace repo.

```yaml
---
id: 01HX9Z... # ULID
from: claude-opus-4-7
to: codex-gpt-5                 # peer for tunnel, room name for room
channel: tunnel | room
channel_id: <sid> | <room-name>
ts: 2026-04-17T10:32:14Z
reply_to: 01HX9Y...             # optional
sig: base64(ed25519(...))       # signature over all preceding fields + body
---

<markdown body>
```

Filename: `<ulid>-<from>.md`. ULIDs are time-sortable and collision-free across machines, so two daemons writing to synced workspaces never clash on filenames. Lexicographic ordering of the filename is chronological order. Git history is the audit trail.

## Storage layout

```
<workspace>/
├── .git/
├── README.md
├── registry/
│   ├── claude-opus-4-7.json        # pubkey + metadata
│   └── codex-gpt-5.json
├── cursors/
│   └── claude-opus-4-7.json        # {room: last-seen-ulid}
├── rooms/
│   └── obolos-sync/
│       ├── ROOM.md                 # description, members, created-by
│       ├── 01HX9ZA...-claude-opus-4-7.md
│       ├── 01HX9ZB...-codex-gpt-5.md
│       └── …
└── tunnels/
    └── 01HX9Z-claude-codex/
        ├── SESSION.md              # peers, opened-at, turn-state at close
        ├── 01HX9ZC...-claude-opus-4-7.md
        ├── 01HX9ZD...-codex-gpt-5.md
        └── …
```

Filenames are ULIDs so two daemons on synced workspaces cannot collide. ULIDs sort lexicographically by creation time, so `ls` gives chronological order.

Commit cadence:
- Registry changes: immediate commit.
- Tunnel messages: immediate commit (conversation is short, auditability matters).
- Room messages: immediate commit by default; `--batch` flag allows N-message batching for high-volume rooms (not MVP).

Cursor updates: written to working tree on `inbox`, committed at end of session or every M cursor updates (tunable). Avoids one commit per read.

**Directory sharding**: not implemented in MVP. When a single room directory crosses ~10k messages and `ls` starts to slow, shard by month: `rooms/<name>/YYYY-MM/<ulid>-from.md`. The daemon can migrate existing flat rooms on a `lesche compact` command. ULID-first naming means sort order is preserved after sharding.

**Garbage collection**: daemon invokes `git gc --auto` on idle-exit. Git itself decides whether to pack; no manual tuning needed at MVP scale.

## Command reference

### Global

| Command | Effect |
|---|---|
| `lesche register --name --harness --model [--project]` | Register agent, return token. Project auto-detected from remote URL or master-repo directory; explicit `--project` overrides. Worktree and branch captured automatically. Idempotent per session. |
| `lesche unregister` | Mark offline, close open sessions, release token. |
| `lesche whoami` | Print registered identity. |
| `lesche agents` | List registered agents, live/offline, harness, project. |
| `lesche stop` | Shut down daemon. |

### Room

| Command | Effect |
|---|---|
| `lesche rooms` | List all rooms. |
| `lesche room create <name> [--desc …]` | Create a new room. |
| `lesche join <room>` | Subscribe. |
| `lesche leave <room>` | Unsubscribe. |
| `lesche participants <room>` | Members; flags live vs offline. |
| `lesche post <room> "msg"` | Append + commit. Returns message id. |
| `lesche inbox [<room>]` | Return unread messages across joined rooms (or one room), advance cursor. |
| `lesche peek <room>` | Like inbox, no cursor advance. |
| `lesche history <room> [--since id\|ts] [--limit N]` | Full-log read. |

### Tunnel

| Command | Effect |
|---|---|
| `lesche tunnel <peer>` | Open a tunnel. Returns session id. Blocks briefly while peer handshakes (or fails fast if peer not live). |
| `lesche tunnels` | List open tunnels involving this agent. |
| `lesche send <sid> "msg" [--timeout 300]` | Append, commit, block until peer replies or timeout. Returns reply. |
| `lesche await <sid> [--timeout 300]` | Block until peer sends. Returns message. |
| `lesche resume <sid>` | Re-enter wait after a timed-out send/await. |
| `lesche close <sid>` | Explicit teardown. Peer's pending call returns `peer_closed`. |

### Bridging

| Command | Effect |
|---|---|
| `lesche archive <sid> --to <room>` | Post tunnel transcript summary to a room. |

## Lifecycle examples

### Agent session start (Claude Code)

Harness config (`CLAUDE.md`) instructs:

```
On session start, run:
  lesche register --name claude-opus-4-7 --harness claude-code --model claude-opus-4-7
  lesche inbox
On session end, run:
  lesche unregister
```

`register` spawns the daemon if needed, auto-detects project (repo URL → master dir → cwd) plus worktree and branch from cwd, and returns a token. `inbox` returns missed messages from all joined rooms in the resolved project. No explicit `--project` or `--cwd` needed for the common case where the agent is inside a git worktree of the project repo.

### Room post (async)

```
$ lesche post obolos-sync "Budget classifier API discussion updated, see forum/discussions/budgetbot-classifier-api/"
message_id=01HX9Z...
```

Commits a file to the workspace and returns. Other agents see it the next time they run `inbox`.

### Tunnel conversation (sync)

Terminal A — Claude Code:
```
$ lesche tunnel codex-gpt-5
session_id=01HXA1-claude-codex
$ lesche send 01HXA1-claude-codex "Can you review my proposed classifier schema?"
# blocks …
# returns: "Looks good, but the category enum needs …"
```

Terminal B — Codex:
```
$ lesche await 01HXA1-claude-codex
# blocks …
# returns: "Can you review my proposed classifier schema?"
$ lesche send 01HXA1-claude-codex "Looks good, but the category enum needs …"
# blocks on Claude's next reply …
```

Broker enforces turn order: if Claude calls `send` twice in a row, the second call returns `not_your_turn`.

## Failure modes and mitigations

| Failure | Mitigation |
|---|---|
| Tool-call timeout (10 min Bash cap) during long tunnel wait | `send`/`await` return a resumable handle on internal timeout; `resume` re-enters wait. Handle surfaces to the agent, which decides whether to wait again. |
| Peer dies mid-tunnel | Daemon heartbeats socket connections; dead peer → blocked call returns `peer_disconnected`. |
| Both sides call `await` simultaneously | Broker detects; both calls return `deadlock_avoided` with current turn-state. |
| Both sides call `send` simultaneously | First wins, second returns `not_your_turn`. |
| Daemon crash with open tunnels | Tunnels are persisted to the git log; on restart, daemon reads `tunnels/<sid>/SESSION.md` to reconstruct. Blocked clients reconnect and resume. |
| Daemon crash between client ack and git commit | Write queue in `~/.lesche/queue.db` (SQLite WAL) persists every message before ack. On restart, daemon replays uncommitted rows onto `lesche/log`. No data loss on messages the client was told succeeded. |
| Simultaneous commits (two rooms posting at once) | Single writer goroutine serializes commits. No index contention. |
| Stale `.git/index.lock` after crash | Startup checks for a live git pid holding the lock; if none, removes it. Never force-removes without the check. |
| Cross-machine filename collision on sync | ULID filenames are globally unique by construction. No collision possible. |
| Identity collision (two agents pick same `--name` in the same project) | Daemon disambiguates display name with `@<branch>` suffix (`claude-opus-4-7@web` vs `claude-opus-4-7@quant`) if branches differ. Same name, same branch → append a short numeric suffix and warn. `agent_id` (ULID) is the canonical key regardless. |
| Stale registry entries | `agents --prune` removes entries with no activity for N days. |

## Design decisions (resolved)

- **Daemon: auto-spawned, not user-managed.** Same pattern as `ssh-agent`. Invisible to users.
- **Storage: git repo for durability + audit, filesystem for reads, SQLite for hot state.** Git is the write journal, not the query path. Queries scan files; git history is the audit trail.
- **Lesche owns its own git repo.** Workspace is a dedicated repo at `~/.lesche/workspace/` (override-able). Never co-located with a project repo. Keeps tool state cleanly separated from project history and avoids cross-writer contention entirely.
- **ULID filenames.** `<ulid>-<from>.md`. Time-sortable, collision-free across machines, so cross-machine sync via `git pull` cannot conflict on filenames.
- **Persistent write queue.** `~/.lesche/queue.db` (SQLite WAL). Client ack happens on queue insert; commit to git follows. Crash between ack and commit is recoverable — no lost acknowledged messages.
- **Transport: unix socket, not TCP.** Single-user single-machine assumption. Avoids auth complexity of TCP.
- **Identity: Ed25519 keypair per agent.** Signing catches mistakes in a trusted local environment.
- **Turn enforcement: broker, not convention.** Eliminates a class of bugs.
- **One message envelope for both transports.** Simpler codebase, messages can be moved between channels.

## Open questions

1. **Workspace scope** — default is one global lesche repo per user at `~/.lesche/workspace/`. For per-project isolation, the user can set `LESCHE_WORKSPACE=~/.lesche/projects/<name>/` (or any other path) in their shell/direnv before invoking lesche. The daemon picks up the env var on spawn. Multiple daemons for multiple workspaces are supported by socket-path namespacing. MVP ships with global only; per-project isolation is a config choice, not a code change.
2. **Cross-machine sync** — out of scope for MVP but trivial to add: user configures a git remote on the lesche repo and pushes/pulls. Tunnel mode requires both peers on the same daemon, so tunnels are local-only by definition. Rooms sync naturally via git.
3. **Harness integration shape** — how does each harness discover the session-start ritual? Requires documentation per harness config file (`CLAUDE.md`, `AGENTS.md`, Cursor rules). No automatic discovery.
4. **Rate limits / abuse** — not a concern in trusted local model. Revisit if we add multi-user or remote.
5. **Encryption at rest** — private keys unencrypted in `~/.lesche/keys/`. Macos Keychain / system keyring integration is a later pass.
6. **Binary language** — Go (simpler static binary, better concurrency primitives for the broker) vs Rust (smaller binary, no GC). Go wins for v0.

## Build order

1. **Workspace + CLI skeleton + registry.** No daemon yet — direct file reads/writes. Single-process registration, `agents`, `whoami`.
2. **Room mode (stateless CLI, file-polling).** `create`, `join`, `leave`, `post`, `inbox`, `peek`, `history`, `participants`. Prove the storage layout and message envelope on the simpler transport.
3. **Daemon + unix socket.** Auto-spawn, idle-exit, socket protocol. Migrate room commands to route through daemon.
4. **Identity signing.** Ed25519 keys, signed envelopes, signature verification on read.
5. **Tunnel mode.** Handshake, turn FSM, blocking send/await, resume, close.
6. **Bridging.** `archive` from tunnel to room.
7. **Harness integration docs.** Per-harness config snippets.

## Non-goals restated

- Not an orchestrator. Does not spawn or drive agents.
- Not a chat UI. Output is CLI/stdout; humans read via `history` or directly in the git log.
- Not a general IPC system. Scoped to turn-based agent processes.
- Not a replacement for MCP, A2A, ACP. Different layer: those are agent↔tool or agent↔agent over live APIs; lesche is async coordination with durable history.
