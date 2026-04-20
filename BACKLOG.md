# Lalia — Backlog

Active and historical workstreams. Rationale, design sketches, and
the state of shipped vs open work. `BACKLOG.md` is planning and
history; ARCHITECTURE.md and IDEA.md describe the shipped system.

## Current state (snapshot at commit `0845bb4`)

**Shipped on main.** The channel-based messaging layer, rooms,
SQLite write queue + mailbox persistence, Ed25519-signed identity,
60-minute leases, harness bootstrap (`init`/`prompt`/`run`),
supervisor/worker task primitive, keychain integration, structured
error payloads, and room transcript rehydration on boot are all
shipped.

Test suite: ~98 tests across 11 files via `make test`; runs in
~17–18s.

**Active branches (not on main).** None at snapshot time.

**Currently open work.** See the workstream catalog further down.
The live queue is L / M / N, plus S as a future item, plus
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

## Open workstreams

### L. `lalia rename <new>` — identity lifecycle primitive

**Source**: `lalia-feedback.md` (external) — obolos-supervisor
observed that renaming `supervisor` → `obolos-supervisor` required
three out-of-band steps (register-new + task-handoff +
unregister-old) and still fragmented channel history across two
peer-pair keys.

**Goal**: Single atomic `lalia rename <new>` that preserves
`agent_id` + keypair and migrates every name-indexed surface so
the audit trail stays coherent across a rename.

**Problem**: Agent identity is keyed by name in too many places:
- `Agent.Name`, `nameIdx[name]` in registry.
- `Task.Owner`, `TaskList.Supervisor` in task lists.
- `Room.members[name]` keys.
- `Channel.PeerA` / `PeerB`, `channelKey(a, b)` for DM history.
- Private key file `~/.lalia/keys/<name>.key`.
- Nickname `Address` strings (`name@project:branch`).
- `anyWaiter[name]`, `channel.waiter[name]` map keys.
- SQLite mailbox rows keyed by `(owner_name, kind, target, seq)`.

`register --name <new>` today creates a fresh identity with a new
keypair and ULID; nothing migrates.

**Scope**:
1. **Registry**: update `nameIdx`, preserve `agent_id` and pubkey,
   rename key file via `renameKey(old, new)` (new keystore op).
2. **Tasks**: walk `s.tasks[*]`; rewrite `Supervisor` + `Task.Owner`
   fields matching the old name; persist.
3. **Rooms**: walk `s.rooms[*]`; rewrite `members` map keys;
   persist via `queue.roomRemoveMember` + `queue.roomAddMember`.
4. **Channels**: walk `s.channels`; rewrite `PeerA`/`PeerB`;
   re-key the map under the new `channelKey`. Rewrite
   `mailbox`/`log`/`waiter` map keys.
5. **SQLite mailbox**: new `queue.mailboxRename(old, new)` that
   updates both `recipient` and `from_name` columns.
6. **Nicknames**: rewrite `Address` strings matching `<old>@...`
   to `<new>@...`.
7. **Waiters**: migrate `anyWaiter[old]` → `anyWaiter[new]`;
   in-flight blocking calls stay on their goroutines but their
   next delivery targets the new key.
8. **Safety**: refuse on collision with an existing name unless
   `--force`. Verify caller holds the old name's key.
9. **Atomicity**: hold `s.mu` for the duration; the SQLite update
   is the point of no return — do it last, or wrap in a
   transaction and roll back in-memory on SQLite failure.

**New op**: `rename` (args: `from`, `to`, optional `force`).
**New CLI**: `lalia rename <new>`.
**New error code**: `CodeNameConflict`.

**Files**: `state.go` (new op + dispatch), `registry.go` (rename
helpers), `keystore.go` + `keystore_*.go` (key file rename),
`task.go`, `room.go`, `channel.go`, `nickname.go`, `queue.go`
(`mailboxRename`), `client.go` (`cmdRename`), `main.go`,
`help.go`, `protocol.go`, `prompts/*.md`.

**Tests**: round-trip rename preserves agent_id + keypair; task
ownership moves; room membership moves; channel DM history
accessible under the new name; SQLite mailbox rows carry over;
nickname references rewritten; collision refused without
`--force`; wrong-key caller rejected; rollback on SQLite failure.

**Blockers**: Depends on M (re-register semantics) being decided;
the rename code needs to know whether it's "a live agent changing
name" or "drop-and-restore" — affects behavior when the caller is
also currently a member of channels/rooms with pending reads.

### M. Re-register and room membership

**Source**: `lalia-feedback.md` (external) — a worker followed the
documented exit protocol (unregister after `ready`), then
re-registered to look at review, and had to `lalia join <slug>`
manually because unregister had dropped it from the room.

**Goal**: Pick a consistent answer to "what does re-register under
an existing name mean for prior room memberships / channel
subscriptions" and update the worker/supervisor prompts to match.

**The two stances**:
- **Re-register = resume.** Unregister drops the agent but
  preserves its room memberships on disk under a "paused" marker;
  re-register rehydrates them. Matches chat-client expectations.
- **Re-register = fresh identity event.** Unregister is fully
  terminal; re-register is explicit arrival; rejoining is opt-in.
  Matches the rest of lalia's explicit-state posture.

**Recommendation**: Fresh-identity. Unregister currently deletes
the private key on disk; if re-register were resume, that
deletion would need to go because the same keypair is expected to
be usable afterwards. Fresh-identity preserves the cleanest story:
unregister is irrevocable, re-register creates a new agent_id that
happens to share a name. Any "I want to come back and read" flow
uses `lalia rename` (workstream L) to change name without losing
state, not unregister/re-register.

**Scope**:
- Decide the stance.
- Update `prompts/worker.md` exit-protocol section to spell it out.
- Update `prompts/supervisor.md` similarly.
- Update `lalia protocol` / `help.go` identity section if needed.

**Files**: `prompts/worker.md`, `prompts/supervisor.md`, `help.go`.
(No daemon code change under the recommended stance.)

**Tests**: Prompt byte-equality across `init`/`prompt` already
covered; add a test that the updated text mentions the chosen
stance so future edits don't silently drift.

**Blockers**: None, but blocks L (rename primitive) which wants
this decided first so its own prompt updates can reference the
consistent model.

### N. `lalia agents` — decomposed columns + worktree-kind tracking

**Source**: user feedback. Two parts of the same theme:
- The `qualified` column (`name@project:branch`) is a single
  squashed string that humans can't scan. The metadata is already
  on `Agent`; it's not surfaced as independent columns.
- Topology: for each agent we want to know whether its cwd is the
  main worktree of a repo, a secondary worktree (branch worktree),
  or outside any repo. Today nothing distinguishes these.

**Goal**: Capture the missing "what kind of worktree is this
agent in" metadata, and rework `lalia agents` so project / branch
/ worktree / worktree-kind / lease / role are separate columns.
Keep `qualified` in the response for scripting.

**Scope**:

Metadata capture (`identity.go` + `state.go`):
- `AgentInfo.MainRepoRoot` — absolute, symlink-normalized path of
  the main worktree for the current repo. Derived from
  `git rev-parse --git-common-dir` → parent dir → `canonicalPath`.
  Stable across all worktrees of the same repo, unlike `RepoRoot`
  which points at the current (possibly secondary) worktree.
- `AgentInfo.WorktreeKind` ∈ `{"main", "secondary", "detached",
  "outside"}`:
    - `outside`: `git rev-parse` fails (not inside a git repo).
    - `main`: `show-toplevel` == parent of `--git-common-dir`.
    - `secondary`: `show-toplevel` != parent of
      `--git-common-dir` (cwd is inside `.git/worktrees/<name>/`).
    - `detached`: HEAD is detached (no branch ref).
- Propagate both onto `Agent` and persist (registry write).
- Include both plus existing `project` / `branch` / `worktree` /
  `role` in the `opAgents` response.

**Handling "outside any repo" agents**:
1. *Outside, no --project override*: git-derived fields empty;
   `WorktreeKind = "outside"`. Drop the current `basename(CWD)`
   fallback for `Project` in `identity.go` — it creates
   meaningless collisions between unrelated agents in different
   dirs. Let `Project` stay empty when no git context exists.
2. *Outside, --project X explicit*: user forces association.
   `Project = X`, `WorktreeKind = "outside"`, git fields empty.
   Agent can participate in rooms and peer messaging for that
   project but cannot claim/publish tasks.
3. *Inside a repo with no remote*: existing fallback behavior
   (Project = repo basename); `WorktreeKind` set normally.

Display surface (`client.go`):

Default `lalia agents` output becomes a **grouped view by repo**
— the primary question the command answers is "which agents are
clustered together?" and the grouped layout shows that
structurally instead of making the user mentally sort by project.
The `main:` / `worktree:` line labels replace a `wt-kind` column.

Example:

        repo: /Users/neektza/Code/obolos (obolos)
          main:       supervisor        master         live     claude-code  3s ago
          worktree:   codex             feat/bb-core   live     codex        42s ago  (wt/bb-core)
        repo: /Users/neektza/Code/lalia (lalia)
          main:       lalia-maintainer  main           live     claude-code  just now
        outside:
          orphan-tool     (cwd: /tmp/scratch)           live     claude-code  1m ago
          analysis        (--project=obolos, no wt)    live     codex        5m ago

Key surface changes:
- Default view shows **last activity** (from `Agent.LastSeenAt`,
  renewed by `renewLease` on every authenticated request) instead
  of `started_at`. "When did this agent last talk to lalia?" is
  more actionable than "when did it first register."
- `last_seen` rendered as a relative duration (`just now`,
  `42s ago`, `3m ago`, `1h ago`). For ages > 24h, fall back to
  the date.
- Repos sorted by agent count desc; within a repo, main first
  then secondary worktrees alphabetical.
- `agent_id` not shown in default view; keep behind `--wide`.

Flags:
- `--grouped` — explicit request for grouped view. **Default.**
- `--flat` — flat table (one row per agent, explicit `project` +
  `wt-kind` columns) for scripts. Mutually exclusive with
  `--grouped`.
- `--wide` — in either layout, include `agent_id`, `cwd`,
  `expires_at`, `main_repo_root`, `started_at`.
- `--json` — pass-through of the raw response. Retains both
  `started_at` and `last_seen_at` as full RFC3339 timestamps.
  Ignores `--grouped`/`--flat` (display-only concerns).

**Files**: `identity.go` (new detection helpers), `state.go`
(AgentInfo → Agent propagation, opAgents response fields),
`client.go` (cmdAgents formatter, flags).

**Tests**:
- `TestDetectWorktreeKindMain` / `...Secondary` / `...Outside` /
  `...Detached` — seed a git repo + secondary worktree in a
  tempdir; assert detection from each cwd.
- `TestAgentsResponseHasTopologyFields` — register agents from
  each kind of cwd; assert fields populate.
- Keep existing `TestAgentsIncludesLeaseStatus` shape.

**Blockers**: None. Only additive fields on `opAgents`.

### S. `task spawn` — lalia as agent lifecycle bus (future)

**Goal**: Let a supervisor-class agent spawn one-shot or
multi-shot sub-agent processes (claude-code, codex, gemini,
copilot, …) against a specific workstream and read their room
traffic to guide the next iteration. lalia becomes the
communication bus **and** the process manager for those
sub-agents.

**Why**: Today a human has to stand up each worker harness in a
shell, set `LALIA_NAME`, and direct it to the right slug. For
fully autonomous orchestration the supervisor needs a primitive
to say "spin up a worker of runtime R against slug S, seat it,
let it work, report back when it exits." This is the right home
for the spawn semantics that the early `plan assign` design
vaguely gestured at but never implemented cleanly.

**Sketch** (not final):
- `task spawn <slug> --runtime <claude-code|codex|…> [--one-shot]`:
  supervisor-only. Registers a transient agent, launches the
  configured harness in the workstream's worktree with the role
  prompt wired in, links its stdout/stderr into the room, claims
  the slug on its behalf, and monitors the process.
- Multi-shot: the spawned agent can emit structured "iterate" /
  "done" messages in-room; supervisor re-prompts on iterate,
  tears down on done.
- Lifecycle signals piggyback on rooms (supervisor posts a
  control message; harness interprets). No new transport.

**Non-goals**: replacing human supervisors; auto-merging;
scheduling across machines. Local-first, per-repo, per-user.

**Status**: Future work. Captured after the publish-pull rewrite
to make clear that lalia-initiated agent lifecycle is the
expected home for the assignment-push semantics removed from
`task publish`.

### Multi-project workspace isolation

**Status**: No design doc yet. Drafted when the feature becomes
necessary. Current behavior: a single global workspace per user
at `~/.local/state/lalia/workspace/`, with `LALIA_WORKSPACE`
override for ad-hoc isolation.

## What is explicitly off-limits

- **Wire format of persisted files** (registry JSON, per-peer
  `peers/<a>--<b>/*.md`, per-room `rooms/<name>/*.md`,
  `tasks/<project-id>/task-list.json`) — changes require a
  migration plan.
- **Deleting or renaming existing commands** — additive only
  unless the change has a migration note in `help.go` and
  `lalia protocol`.
- **Touching `/opt/homebrew/bin/lalia`** from a feature branch.
  The production binary is rebuilt from main only.
