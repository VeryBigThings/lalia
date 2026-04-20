# Lalia — Backlog

Active and historical workstreams. Rationale, design sketches, and
the state of shipped vs open work. `BACKLOG.md` is planning and
history; ARCHITECTURE.md and IDEA.md describe the shipped system.

## Current state (snapshot at commit `588c66c`)

**Shipped on main.** The channel-based messaging layer, rooms,
SQLite write queue + mailbox persistence, Ed25519-signed identity,
60-minute leases, harness bootstrap (`init`/`prompt`/`run`),
decentralized peer role, supervisor/worker task primitive,
keychain integration, structured error payloads, room transcript
rehydration on boot, repository-grouped agent discovery (N),
identity isolation safeguards (V), and canonical introspected
agent naming (U) are all shipped.

Test suite: ~107 tests across 13 files via `make test`; runs in
~19–20s.

**Active branches (not on main).** None at snapshot time.

**Currently open work.** See the workstream catalog further down.
The live queue is X / M / T, plus L and S as future items, plus
multi-project workspace isolation which has no design doc yet.

### Historical note on parallel agent batches

Early batches of work on lalia were run by multiple agents in
parallel, each owning one workstream end-to-end in its own git
worktree. That pattern is not active; the project has settled into
single-track iteration. The parallelization scaffolding (assignments
table, file-ownership heat map, rules of engagement, per-workstream
reading lists) has been removed. If a future batch opens, it can be
rebuilt — the merged feature set is capable of supporting it (see
the task primitive, rooms, and `task publish`).

Commits recording the parallel-batch period are visible in
`git log`; the `feat/identity`, `feat/errors`, `feat/keychain`,
`feat/init-run`, `feat/plan`, and `feat/mailbox-persist` branches
merged that work.

## Cold-start reading list

Read in order before writing code on lalia:

1. [`docs/IDEA.md`](./docs/IDEA.md) — what lalia is and why.
2. [`docs/ARCHITECTURE.md`](./docs/ARCHITECTURE.md) — system as
   shipped. Daemon/client/channel/room/writer/registry/task model.
3. [`docs/IDENTITY.md`](./docs/IDENTITY.md) — identity model: ULIDs,
   resolver grammar, nicknames.
4. [`docs/CHANNELS.md`](./docs/CHANNELS.md) — the messaging
   redesign. Read if confused about why there is no `tunnel` /
   `send` / `await` / `sid` anywhere.
5. [`docs/MVP.md`](./docs/MVP.md) — historical, for context only.
6. `protocol.go` — wire-level request/response shapes.
7. `help.go` and `lalia protocol` — the agent-facing protocol
   surface. User-visible command changes update both.
8. `state.go` — dispatch switch; entry point for every op.
9. `room.go`, `channel.go`, `task.go` — the three domain surfaces.

## Coordination through lalia itself

For ongoing workstream coordination, use a room named after the
slug (`feat/<name>`). The git transcript survives session kills, so
walking back into a workstream after a harness restart just means
`lalia history <slug> --room`.

`task publish` automates the room creation: it publishes a
structured plan and creates the workstream room, joins the
supervisor, and posts the context bundle as the first message. A
worker's `task claim <slug>` auto-joins them. This is the same
mechanism the now-dormant parallel-batch pattern relied on.

Direct peer channels (`tell` / `ask`) are the edge case: private
1:1 problem-solving, identity questions, anything the rest of the
project shouldn't see.

## Rules of engagement

1. **Branch from main.** Test-backed merge gate.
2. **Tests must pass before the work is reported done.** Run
   `make test`. If it fails, don't report done.
3. **`protocol.go` struct shapes are additively extensible.** Do
   not rename or remove fields without an explicit migration plan;
   clients on main must still parse older messages.
4. **Do not alter the wire format of persisted files** (registry
   JSON, per-peer `peers/<a>--<b>/*.md`, per-room
   `rooms/<name>/*.md`, `tasks/<project-id>/task-list.json`)
   without a migration. Readers on main must still parse older
   files after the change merges.
5. **Never `make install` from a feature branch** except on an
   isolated `LALIA_HOME`. The production binary is rebuilt from
   main only.

## Shipped workstreams

- **Rooms** (`e4e7186`) — N-party pub/sub, bounded per-subscriber
  mailbox with overflow notice, explicit membership.
- **SQLite write queue** (`d113b02`) — crash-safe message
  persistence, WAL mode, dead-letter after 3 failed commits.
- **Channels redesign** (`9d192bf`) — dropped the turn FSM, renamed
  verbs to `tell` / `ask` / `read` / `peek` / `read-any`, collapsed
  sids. See `docs/CHANNELS.md`.
- **Registry + leases** — persisted JSON, 60-minute lease + renew,
  workspace at `~/.local/state/lalia/workspace`.
- **Ed25519 signed requests** on every authenticated op.
- **A. Identity refactor + nicknames** (`f27ff42`) — ULID
  `agent_id` under a rotatable name, project/branch/worktree
  auto-detect on register, nickname resolver.
- **E. Keychain integration** (`cf5254f`) — keystore interface with
  file and macOS keychain backends, selectable via
  `LALIA_KEYSTORE=keychain`, fallback on unavailability.
- **F. Structured error payloads** (`b8175e7`) — machine-readable
  `reason` / `retry_hint` / `context` alongside string `Error` /
  exit `Code`.
- **I. `lalia init` / `prompt` / `run`** (`a690c2b`) — role-specific
  onboarding prompts and harness spawn wrappers for Claude Code,
  Codex, and Copilot.
- **H. Task primitive + supervisor/worker roles** (`b51db58`,
  evolved `b809316` / `2ed889f`) — roles at register, per-project
  `task-list.json`, publish-pull workflow, atomic worktree + room +
  bundle creation, supervisor-only mutations, worker self-service
  via `task bulletin` / `task claim` / `task status`. See the
  catalog entry below for detail.
- **J. Daemon-restart mailbox persistence** (`d7a4c0d` /
  `5a41741`) — SQLite mailbox table rehydrated on boot before the
  daemon accepts connections.
- **K. `loadRooms` transcript rehydration on boot** (`8752028`,
  `6e9780a`) — `parseRoomMsgFile` + `loadRooms` walk, SQLite
  persistence for `ensureRoomWithMembers`, `flushPendingWrites`
  boot ordering.
- **Install pipeline** — `make install` with auto-detected PREFIX;
  daemon kick after reinstall.
- **Protocol surface docs** — `lalia help` and `lalia protocol`
  kept current with everything shipped.
- **Peer role** — decentralized coordination prompt (`prompts/peer.md`)
  for agents not in a supervisor/worker workflow. Updated `init` /
  `prompt` / `run` to support the `peer` role.
- **N. `lalia agents` — decomposed columns + worktree-kind tracking**
  (`933a0ce` and subsequent) — repository grouping, relative last-seen
  durations, detection of main vs secondary vs outside-repo worktrees.
- **V. Identity Isolation & Multi-Identity Protection** (`8d43dbc`)
  — PID locking, supervisor claim blocking, and harness session binding.
- **U. Canonical agent naming** (`66246d7`) — Prevent identity collisions
  by defaulting to stable, introspected canonical names during registration.
  Added `lalia suggest-name` and updated role prompts.

## Open workstreams

### X. CLI Polish & Robustness

**Source**: User and worker feedback. `task status` is a bad name
for a mutation; flags like `--as` are eaten by positional args.

**Goal**: Refactor CLI parsing for order-independence and rename
confusing commands.

**Scope**:
- **Rename**: Change `lalia task status` to `lalia task set-status`.
  Keep `status` as a deprecated alias with a warning.
- **Parsing**: Refactor `cmdRead`, `cmdPost`, `cmdTell`, etc., to
  correctly skip flags when identifying positional arguments.
- **Robustness**: Ensure `--as`, `--timeout`, and `--room` work
  correctly regardless of position in the argument list.
- **Updates**: Refresh `lalia help`, `lalia protocol`, shell
  completions, and role prompts.

**Status**: Open. Priority: High.

### M. Re-register and room membership

**Source**: `lalia-feedback.md` (external). Decide whether
re-registering under an existing name mean for prior room
memberships / channel subscriptions.

**Goal**: Pick a consistent answer to "what does re-register under
an existing name mean" and update the worker/supervisor prompts.

**Recommendation**: Fresh-identity. Unregister is terminal;
re-register is explicit arrival; rejoining is opt-in.

**Scope**:
- Update `prompts/worker.md` and `prompts/supervisor.md` exit-protocol
  section to spell it out.
- Update `lalia protocol` / `help.go` identity section.

**Status**: Open. Blocks L.

### T. Branch-aware task defaults

**Goal**: Make the worker's arrival experience smoother by
defaulting to the task that matches the current worktree's branch.

**Scope**:
- **Discovery**: `lalia task bulletin` highlights the task matching
  the caller's current `Branch`.
- **Workflow**: `lalia task claim` defaults to the branch-matched
  slug if one exists.
- **Prompt**: Update `prompts/worker.md` to instruct the agent to
  "confirm and claim the branch-matched task" as its first step.

**Status**: Open.

### L. `lalia rename <new>` — identity lifecycle primitive

**Goal**: Single atomic `lalia rename <new>` that preserves
`agent_id` + keypair and migrates every name-indexed surface so
the audit trail stays coherent across a rename.

**Status**: Future work. Blocked by M.

### S. `task spawn` — lalia as agent lifecycle bus (future)

**Goal**: Let a supervisor-class agent spawn sub-agent processes
against a specific workstream and read their room traffic.

**Status**: Future work.

### Multi-project workspace isolation

**Status**: No design doc yet.

## What is explicitly off-limits

- **Wire format of persisted files** — changes require a migration plan.
- **Deleting or renaming existing commands** — additive only.
- **Touching `/opt/homebrew/bin/lalia`** from a feature branch.
