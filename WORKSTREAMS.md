# Lesche — Workstreams

A plan for running multiple coding agents in parallel on lesche without
stepping on each other. Each workstream is designed as a single atomic
unit: the agent writes the feature, the tests that prove it works, and
any doc updates. A workstream only merges when its test suite is green.

We do **not** split "agent A writes code, agent B writes tests." That
split couples two agents through a review loop on every change and
defeats the point of parallelism. One agent owns a workstream
end-to-end.

## Current state (snapshot at commit e4e7186)

**Shipped on main:**
- Tunnel transport (2-party sync, turn FSM, git-backed transcript).
- Registry with persisted JSON, lease + renew, session discovery,
  history read via daemon, workspace moved outside harness allowlists.
- Ed25519 signed requests for every authenticated op.
- Install pipeline: `make install` places binary on `$PATH`.
- Protocol help (`lesche protocol`) and short help (`lesche help`)
  current for everything shipped.
- Room mode (N-party pub/sub, bounded per-subscriber mailbox with
  overflow notice, commands `rooms`, `room_create`, `join`, `leave`,
  `participants`, `post`, `inbox`, `peek`). Merged at `e4e7186`.
- SQLite write queue (crash-safe message persistence, WAL mode,
  dead-letter after 3 failed commits). Merged at `d113b02`.
- Test suite now 22 tests via `make test`, ~5.5s runtime.

**Active branches (not on main):**
- `feat/identity` — reassigned to Copilot (see below). Head still at
  `a907186`; not started.

**Designed, not implemented:**
- Resumable blocking, structured error payloads, keychain integration,
  multi-project workspace isolation, cross-machine sync.

## Current assignments

Second batch after the rooms + write-queue merges. Identity is the only
A-tier workstream remaining; handed to Copilot since they shipped the
write queue with a tight review cycle and have fresh context on the
coordinator tunnel protocol.

| Agent | Branch | Workstream | Worktree path | Status |
|-------|--------|------------|---------------|--------|
| Copilot | `feat/identity` | A. Identity refactor + nicknames | `~/Obolos/lesche-identity` | Assigned. Branch head at `a907186`; rebase on `main@e4e7186` before starting. |
| GPT-5 via Codex | — | — | — | feat/rooms merged at `e4e7186`. Idle; pick next workstream from the catalog below. |
| Claude Code | — | — | — | Unassigned this batch. Pick next workstream from the catalog below. |

Merge gate unchanged: `make test` passing + human approval over a
lesche tunnel to `claude-coordinator`.

Rules repeated for clarity: each agent owns its branch end-to-end (code +
tests + docs + help updates). Merge gate is `make test` passing plus human
approval over a lesche tunnel to `claude`.

### Settled design notes from the first batch

1. **Copilot git identity on commits.** Settled as
   `user.email=copilot@local`, `user.name=Copilot`. Used on the
   feat/write-queue and feat/rooms merges (codex's branch was also
   committed under Copilot attribution on the shared machine; not
   worth unwinding retroactively).
2. **SQLite dependency.** `modernc.org/sqlite` — pure Go, no cgo,
   static-binary story preserved. Shipped in write-queue.
3. **Nickname storage location** still open; pick before feat/identity
   starts. `IDENTITY.md` proposes `~/.lesche/nicknames.json` (outside
   workspace). Alternative: in the workspace for git audit. Copilot
   should surface this in the kickoff tunnel.

### UX issue flagged, not in a workstream yet

4. **Default lease TTL is too short.** 10 minutes. Agents doing
   independent implementation work between lesche calls get dropped
   silently; open tunnels close with `peer lease expired` on the peer
   side. Observed during the doc-writing push before this restart.
   Easiest fix: raise TTL to 30 or 60 minutes. Slightly better fix:
   also renew lease on `agents` and other from-less listings. Scope:
   tiny; can land as a spot patch to main outside the parallel batch,
   or bundled into whichever workstream merges first.

## Parallelization principles

1. **Each workstream is a branch in its own git worktree.** Naming:
   `feat/<slug>`. Worktree: `~/Obolos/lesche-<slug>`.
2. **One agent owns the branch end-to-end.** Feature code + unit tests
   + integration tests (where needed) + doc updates + help output
   updates. Merge blocker: `make test` passes locally.
3. **The binary at `/opt/homebrew/bin/lesche` stays built from main.**
   In-flight work lives only in its worktree's local `bin/lesche`.
   Agents do not `make install` from a feature branch except on their
   own isolated `LESCHE_HOME`.
4. **Coordination happens through lesche.** Agents announce start of
   work and report completion by opening a tunnel to Claude. Real-time
   questions during work use the same channel.
5. **Merge gate is a human decision** (for now — the user). Agent
   reports "ready for review" in a tunnel; human merges when satisfied
   with the diff and a clean test run.

## File-ownership heat map

| File            | Identity | Rooms | SQLite queue | Resumable | Keychain | Struct errors |
|-----------------|:-:|:-:|:-:|:-:|:-:|:-:|
| state.go        | H | M |  |  |  | M |
| tunnel.go       |  |  |  | M |  | M |
| registry.go     | H |  |  |  |  |  |
| signing.go      |  |  |  |  | H |  |
| writer.go       |  | L | H |  |  |  |
| daemon.go       |  |  | M |  |  |  |
| client.go       | M | M |  | L |  | M |
| protocol.go     | L | L |  |  |  | **H** |
| main.go         | L | L |  | L |  | L |
| help.go         | L | L |  | L | L | L |
| util.go         | L |  |  |  |  |  |
| new files       | nickname.go, identity.go | room.go | queue.go |  |  |  |

H = heavy edits likely. M = moderate. L = small additive changes.
A cell marked H in two columns means those workstreams cannot run
concurrently without collision.

### Read the collisions

- **Identity ↔ Rooms**: both touch `state.go` moderately. Manageable
  if rooms v1 uses the current bare-name addressing and upgrades to
  rich addressing *after* identity lands.
- **Rooms ↔ SQLite queue**: both touch `writer.go`. Light collision —
  rooms only adds new write paths, queue only wraps existing writes.
- **Structured errors ↔ everything**: hits `protocol.go` heavy and
  every error-returning handler. Must be done solo or sequenced last.
  Do **not** run in the same batch as any other feature.

## Workstream catalog

Each entry lists: one-line goal, scope, primary files, test
requirements, and any sequencing constraints.

### A. Identity refactor + nicknames

**Goal**: Replace name-as-primary-key with ULID agent_id + rich
metadata (project, branch, harness, model) auto-detected on register;
add user-assigned nicknames.

**Scope**: Implement everything described in `IDENTITY.md`. Preserve
existing pubkeys during migration (signatures keep working). Update
address resolution throughout.

**Files**: `state.go` (heavy), `registry.go` (heavy), `client.go`
(resolution sites), new `nickname.go`, new `identity.go` (resolver),
minor edits to `main.go`, `help.go`.

**Tests**: ULID stability across re-register; resolver grammar
(bare-name, `name@project`, `name@project:branch`, ULID, nickname);
nickname assign/show/list/delete; stable vs --follow binding;
migration of legacy name-keyed records; collision handling (two
`claude`s register, daemon disambiguates).

**Blockers**: None to start. Sequencing: run alone or with purely
additive workstreams (queue/keychain). Do not run concurrently with
rooms if you want rooms to benefit from rich addressing at merge time.

**Agent fit**: Highest context requirement. Whoever owns this should
already have full lesche internals loaded.

### B. Room mode (N-party pub/sub)

**Goal**: Implement async rooms per the phase-1 spec in `MVP.md`. Max
8 members, per-sender FIFO, per-subscriber bounded mailbox, explicit
join/leave.

**Scope**: New transport alongside tunnels. Commands: `rooms`, `room
create`, `join`, `leave`, `post`, `inbox`, `peek`, `participants`.
Room messages commit under `rooms/<name>/` in the workspace.

**Files**: new `room.go` (primary), `state.go` (add rooms map +
dispatch), `client.go` (new commands), `writer.go` (new write paths),
`help.go` (usage + protocol).

**Tests**: join/leave changes membership; post broadcasts to all
members' mailboxes except sender; slow subscriber does not block
senders; mailbox overflow drops oldest with notice; peer-only reads
via `peek`/`inbox` mirror the history-peer-check pattern from
tunnels.

**Blockers**: None if v1 uses bare-name addressing. If identity
lands first, rebase to use the resolver. Concurrent with identity is
fine; just accept a post-merge rebase on addressing sites.

**Agent fit**: Medium context. Whoever does this should understand
the existing `tunnel.go` patterns for mailboxes and waiters — rooms
reuse the same primitives generalized to N peers.

### C. SQLite write queue

**Goal**: Persist messages between client ack and git commit so a
daemon crash cannot lose acknowledged but uncommitted messages.

**Scope**: Add `~/.lesche/queue.db` (SQLite, WAL). Client ack happens
on queue insert. Writer goroutine drains the queue, commits to git,
deletes from queue. On daemon startup, any rows in the queue replay
through the writer.

**Files**: new `queue.go`, `writer.go` (wrap writes in queue
insert + drain), `daemon.go` (startup replay).

**Tests**: ack happens before git commit; simulated crash (kill
mid-commit) and restart replays the queue; queue entries clear after
successful commit; schema migration smoke test.

**Blockers**: None. Zero surface change. Fully parallel-safe with
everything except structured errors.

**Agent fit**: Self-contained. A new agent with no lesche history
could pick this up reading `writer.go` and `ARCHITECTURE.md`. Best
workstream for Copilot or any fresh agent.

**Dependency**: requires the SQLite stdlib or a pure-Go driver
(`modernc.org/sqlite` is pure-Go; `mattn/go-sqlite3` needs cgo).
Agent should pick pure-Go to keep the static-binary story intact.

### D. Resumable blocking

**Goal**: `lesche resume <sid>` re-enters a blocked wait on a tunnel
after a timeout, instead of losing turn state and forcing the caller
to reason about it.

**Scope**: On `send`/`await` timeout, the daemon keeps the tunnel
state and the caller's pending-reply expectation intact. The client
can call `resume` with the same sid to block again for the same
waited-for message. Idempotent in the sense that calling `resume`
twice doesn't double-register.

**Files**: `tunnel.go` (persist waiter state across timeouts),
`client.go` (new `resume` command), `state.go` (dispatch), `main.go`
and `help.go` (usage + protocol).

**Tests**: timeout returns exit 2 with tunnel state preserved;
`resume` on the same sid blocks and returns the peer's message when
it arrives; `resume` without a pending wait returns a clear error.

**Blockers**: None. Small collision with other tunnel.go changes,
but unlikely unless run with identity or rooms.

**Agent fit**: Medium. Requires understanding of the turn FSM and
mailbox mechanism in `tunnel.go`.

### E. Keychain integration

**Goal**: Store private keys in macOS Keychain (or system keyring on
Linux) instead of plain files at `~/.lesche/keys/*.key`.

**Scope**: Provide a key-store abstraction with two backends: file
(current default) and keychain. Selectable via env (`LESCHE_KEYSTORE=keychain`).
Keychain items named `lesche:<agent_name>`. Fallback to file when
keychain unavailable.

**Files**: `signing.go` (swap direct-file reads for a keystore
interface), new `keystore.go` (backends), `help.go` (env).

**Tests**: round-trip through file backend; round-trip through
keychain on macOS (skip on Linux CI if unavailable); correct fallback
behavior when backend is unavailable.

**Blockers**: None. Zero protocol change. Fully parallel-safe.

**Agent fit**: Self-contained. Works for any agent that can read
`signing.go`.

### F. Structured error payloads

**Goal**: Replace string-only errors with JSON payloads carrying
machine-readable fields (`code`, `reason`, `retry_hint`, context).

**Scope**: Extend `Response.Data` on errors to include a structured
`error` object. Clients parse it for automated retry logic. Preserve
exit codes for shell-script compatibility.

**Files**: `protocol.go` (heavy), every handler that returns an
error (state.go, tunnel.go), `client.go` (pretty-print structured
errors), `help.go` (document new fields).

**Tests**: every existing error path emits a structured payload;
clients parse payloads; backward-compat: old-style string errors in
`.Error` still populated for readability.

**Blockers**: **Run solo.** Touches every error-returning function
across the codebase. Collides with identity, rooms, resumable, and
any other workstream that edits handlers.

**Agent fit**: Deep context. Touches the edit surface of everything.
Schedule after the next parallel batch lands.

## Sequencing after the current batch

Resumable blocking (D) and keychain (E) can be slotted in once A, B, C
land. Structured errors (F) waits until everything else is in.

## Rules of engagement

1. **Branch from main. Worktree at `~/Obolos/lesche-<slug>`.**
   `git worktree add -b feat/<slug> ../lesche-<slug> main`.
2. **Use isolated runtime for your own testing.** Set
   `LESCHE_HOME=~/.lesche-<slug>` and
   `LESCHE_WORKSPACE=/tmp/lesche-<slug>/workspace` so your test daemon
   does not disturb the shared production daemon at `~/.lesche/sock`.
3. **Tests must pass before you report done.** Run `make test`. If it
   fails, do not report done.
4. **Do not edit `protocol.go` types** during this batch. They are
   locked for structured-errors to handle later.
5. **If you need to touch a file outside your heat-map column**, stop
   and announce in a tunnel to `claude`. Collision is possible; get
   alignment before writing.
6. **When done, open a tunnel to `claude` and report**: branch name,
   commits pushed, `make test` output summary, any bugs found-but-not-
   fixed, any new env vars or commands added. Claude will either
   approve-for-merge or flag issues for another round.
7. **Rebase before merge.** If main moved while you worked, rebase
   your branch and re-run `make test` before asking for merge.

## What is explicitly off-limits in this batch

- **Changing `protocol.go` request/response struct shapes** beyond
  purely additive fields. That refactor belongs to structured-errors.
- **Deleting or renaming existing commands.** Additive only.
- **Altering the wire format of persisted files** (registry JSON,
  tunnel messages, SESSION.md). Readers on main must still be able to
  parse older files after your workstream merges.
- **Touching `/opt/homebrew/bin/lesche`** from a feature branch. The
  production binary is rebuilt from main only.

## Pointer

Open questions blocking work are listed at the top of this file under
"Current assignments". Once answered, they land here as settled design
notes.
