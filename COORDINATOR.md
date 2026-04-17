# Lesche — Coordinator Notes

If you're reading this, a human or the coordinator agent just handed
you a workstream. You are about to write code in a repo you may not
have seen before, alongside other agents doing the same. This file
is the single source of truth for:

1. What state the codebase is in.
2. Who owns what right now.
3. The rules of engagement so you don't step on another agent's work.
4. A **cold-start reading list every agent on the current batch must
   read before writing a single line of code** (see the dedicated
   section below).

## What this file is for

A plan for running multiple coding agents in parallel on lesche without
stepping on each other. Each workstream is designed as a single atomic
unit: the agent writes the feature, the tests that prove it works, and
any doc updates. A workstream only merges when its test suite is green.

We do **not** split "agent A writes code, agent B writes tests." That
split couples two agents through a review loop on every change and
defeats the point of parallelism. One agent owns a workstream
end-to-end.

## Cold-start reading list (ALL agents, every batch)

Every agent in the current batch starts cold — nobody has repo
context from a previous session. Read these files in order before
writing a line of code. Your workstream section below adds extra
files on top of this.

1. **`COORDINATOR.md`** (this file) — top to bottom. Especially
   "Current assignments", "Parallelization principles", "File-
   ownership heat map", "Rules of engagement", and the scope entry
   for your specific workstream.
2. **`ARCHITECTURE.md`** — how the pieces fit. Daemon/client/channel/
   room/writer/registry model.
3. **`IDEA.md`** — why lesche exists. Short.
4. **`MVP.md`** — what is shipping and what isn't. Large parts are
   retrospective now that channels / rooms / queue have landed.
5. **`CHANNELS.md`** — the messaging redesign (shipped). Read if you
   are confused about why there is no `tunnel` / `send` / `await`
   / `sid` anywhere.
6. **`protocol.go`** — wire-level request/response shapes. Currently
   safe to extend additively; workstream F (structured errors) is
   the only task allowed to rework the `Response` shape.
7. **`help.go`** and run `lesche protocol` — the agent-facing
   protocol guide. If your workstream adds a user-visible command,
   you update both.
8. **`state.go`** — the dispatch switch is the entry point for
   every op. You will almost certainly add a case here.
9. **`channel.go`** — peer-to-peer primitive. Tell / read / peek
   semantics.
10. Your **workstream-specific files** — listed in the "Per-
    workstream reading list" section further down.

After reading: identify **which files your workstream is going to
touch**, cross-check them against the heat map, and if you see
collisions announce them via `tell claude-coordinator "..."` before
writing.

## If you are a worker agent — bootstrap

You are a worker if the human told you "you are a worker" or
assigned you a specific branch/workstream. The coordinator agent
(`claude-coordinator`) is run separately and drives review + merge.

### 1. Identity

Pick the identity that matches your harness. The human has agreed
on these three names and nothing else:

- `copilot` — for the GitHub Copilot harness.
- `codex` — for the GPT-5 / Codex CLI harness.
- `claude-code` — for the Claude Code harness.

Set it once per shell:

```
export LESCHE_NAME=<your-name>
```

Do not invent a new name. Do not use `claude`, `claude-coordinator`,
`sonnet`, or any variant — those either collide with the coordinator
or are reserved. If your harness has already been handed a different
name by the human, use that instead and tell the coordinator in your
first message.

### 2. Register

The lesche daemon is already running at `~/.lesche/sock`. Use the
installed binary (`which lesche`). Register yourself — idempotent,
reuses your existing Ed25519 key if you registered in a previous
session:

```
lesche register
lesche agents                  # sanity-check: see who else is online
```

If you see `claude-coordinator` in the agents list, the coordinator
is up and expecting you. If not, wait and re-run `lesche agents`
every minute or so until it shows up.

### 3. Announce yourself

Tell the coordinator, in one message, exactly these things:

- Your harness (`copilot` / `codex` / `claude-code`).
- The workstream you were assigned (or "unassigned, awaiting
  direction").
- Confirmation that you have read `COORDINATOR.md` (or a question
  if you haven't and don't understand it).
- Any blockers before you start (e.g. nickname storage question
  for `feat/identity`).

```
lesche tell claude-coordinator "harness: codex. assigned feat/keychain. COORDINATOR.md read. no blockers."
```

No tunnel to open, no sid to track. Your channel with the coordinator
is implicit on the first `tell`.

### 4. Receive messages from the coordinator

The coordinator may send you a message before you send one to them.
To receive anything arriving on any channel or room you're in:

```
lesche read-any --timeout 300
# prints kind=peer target=claude-coordinator (or kind=room target=…)
# then the message body
```

`read-any` blocks up to `--timeout` seconds for the next inbound.
Reply to a peer with:

```
lesche tell claude-coordinator "<your reply>"
```

Or if you want to pull from just one specific peer:

```
lesche read claude-coordinator --timeout 300
```

Need a synchronous question-and-answer in one call? Use `ask`:

```
lesche ask claude-coordinator "can I rebase on main now?" --timeout 60
```

`ask` sends, then blocks for the peer's next message and prints it
on stdout.

### 5. Keep your lease alive

Leases are 60 minutes; any command renews. If your harness sits idle
for longer than that, you get dropped and any blocking read returns
immediately. Two habits that prevent this:

- Call `lesche renew` right before a long run of edits.
- Or just run any lesche command occasionally — they all renew.

### 6. Announce key moments

`tell claude-coordinator` at these checkpoints:

- **Start of work** — confirmed in step 3.
- **Open question in your scope entry** — don't guess; use `ask`.
- **Need to touch a file outside your heat-map column** — collision
  check before writing.
- **Ready for review** — branch name, commit SHA, `make test`
  summary, any bugs found-but-not-fixed, any new env vars or
  commands added.

### Minimal loop if you get stuck

Worker session template; safe to run verbatim after step 1:

```
export LESCHE_NAME=<your-name>
lesche register
lesche agents | grep claude-coordinator || echo "coordinator not up"
lesche channels                       # who do I already have a channel with?
lesche peek claude-coordinator        # anything pending?
lesche read-any --timeout 60          # or block for the first inbound
```

If `read-any` times out and `peek` shows nothing, push first with
`tell claude-coordinator "..."` per step 3.

## How to coordinate (general)

All agents coordinate through lesche itself. Full protocol guide:
run `lesche protocol`. Full help: `lesche help`.

## Current state (snapshot at commit 9d192bf)

**Shipped on main:**
- Peer-to-peer channels (one per unordered pair; no turn FSM, no sid
  in CLI; `tell`/`ask`/`read`/`peek`/`read-any`). Git transcripts
  under `peers/<lo>--<hi>/`. Merged at `9d192bf`.
- Room mode (N-party pub/sub, bounded per-subscriber mailbox with
  overflow notice, commands `rooms`, `room_create`, `join`, `leave`,
  `participants`, `post`, plus unified `read` / `peek` with `--room`).
  Room `read` drains all pending (blocks up to timeout for the first
  arrival). Merged at `e4e7186`, adjusted in channels redesign.
- SQLite write queue (crash-safe message persistence, WAL mode,
  dead-letter after 3 failed commits). Merged at `d113b02`.
- Registry with persisted JSON, 60-minute lease + renew, workspace at
  `~/.local/state/lesche/workspace` (outside harness allowlists).
- Ed25519 signed requests for every authenticated op.
- Install pipeline: `make install` places binary on `$PATH`.
- Protocol help (`lesche protocol`) and short help (`lesche help`)
  current for everything shipped.
- Test suite: 32 tests via `make test`, ~2.2s runtime.

**Active branches (not on main):**
- `feat/identity` — reassigned to Copilot (see below). Head still at
  `a907186`; not started. Needs rebase on new main before work begins.

**Designed, not implemented:**
- Structured error payloads (workstream F), keychain integration
  (workstream E), plan primitive + manager/worker roles (workstream
  H; unassigned), multi-project workspace isolation.

**Killed:**
- Resumable blocking (workstream D). The turn FSM is gone, so there
  is no waiter state to "resume" after timeout — `read` returns empty
  and the caller calls `read` again. No command needed.

## Current assignments

Post-channels batch. Assignments were reshuffled once D was killed:
cold agents get smaller-surface workstreams; agents with prior
shipped work get the heavier ones.

| Agent | Branch | Workstream | Worktree path | Status |
|-------|--------|------------|---------------|--------|
| `copilot` | `feat/identity` | A. Identity refactor + nicknames | `~/Obolos/lesche-identity` | Assigned. Prior shipped: feat/write-queue. Biggest-context task; copilot is the best-warmed agent on internals. Worktree exists at head `a907186`; rebase on main before starting. |
| `codex` | `feat/errors` | F. Structured error payloads | `~/Obolos/lesche-errors` | Assigned. Prior shipped: feat/rooms. Touches every handler but mechanically simple once the helper is in place; codex is warm on the handler pattern from rooms. Create worktree + branch from current main. |
| `claude-code` | `feat/keychain` | E. Keychain integration | `~/Obolos/lesche-keychain` | Assigned. Cold-start agent; E is the smallest and most self-contained workstream available. Create worktree + branch from current main. No `protocol.go` change, no `state.go` change — touches only `signing.go`, new `keystore.go`, `help.go`. |

Sequencing: A, E, F can all run in parallel.
- A ↔ F overlap on `state.go` dispatch (lightly). Last-to-merge rebases.
- E has no overlap with anything — merges whenever it's ready.
- F was originally sequenced last to avoid protocol churn against A/E;
  we run it in parallel now because A is heavier on `registry.go` and
  E doesn't touch `state.go` at all, so the overlap is small.

All three agents start cold this batch (no prior-session context).
Read the "Cold-start reading list" section above plus your workstream's
extras from "Per-workstream reading list" below before writing code.

Merge gate unchanged: `make test` passing + human approval via
`tell claude-coordinator "ready for review: ..."`.

### Settled in prior batches

- Lease TTL raised to 60 minutes (was 10, then 30). Still skips renewal on
  `agents`; that's a follow-up if idle-drop keeps hurting.
- Channels redesign shipped (workstream G). See `CHANNELS.md` for
  the plan document, kept as historical context. Current behavior is
  documented in `help.go` and `lesche protocol`.

### Per-workstream reading list

Read this after the cold-start list above, before writing code.

**A. Identity refactor + nicknames (`copilot`, `feat/identity`)**

1. `IDENTITY.md` — the spec you're implementing. Read this first;
   everything else is just how to land it in the current code.
2. `registry.go` — persistence of the agent table. You will rework
   the keying from name to ULID, preserving pubkeys during migration.
3. `signing.go` — how pubkeys are generated and stored. Keys are
   keyed by name today; identity needs a path from name → agent_id
   without losing a registered agent's key.
4. `client.go` — every command that resolves a peer by name. You
   will add a resolver (bare-name / `name@project` / `name@project:branch`
   / ULID / nickname) that wraps this.
5. `daemon_integration_test.go` — the registration + signed-request
   flow you must not break during migration.
6. Open question before you start: **nickname storage location**.
   `IDENTITY.md` proposes `~/.lesche/nicknames.json` outside the
   workspace; alternative is in the git-backed workspace for audit.
   Raise this in your kickoff message to `claude-coordinator`
   (e.g. `ask claude-coordinator "nickname storage: home or workspace?"`).

**F. Structured error payloads (`codex`, `feat/errors`)**

You are adding a structured `error` object alongside the existing
`Error` / `Code` fields in `Response`. Every error-returning handler
becomes eligible to emit structured fields (`reason`, `retry_hint`,
`context`). Clients keep consuming the string `Error` for
pretty-print; machine-readable fields land in `Data.error` (or
alongside it — your call, document it in `help.go`).

1. `protocol.go` — `Response` struct. This is the only struct in
   the codebase you're allowed to extend (additively). Do not rename
   or remove existing fields.
2. `state.go` — every `Response{Error: "...", Code: ...}` site.
   Most are one-liners; your job is to wrap them in a helper that
   also populates a structured `error` object.
3. `room.go`, `channel.go` — same pattern, fewer sites.
4. `client.go` — `handle()` currently prints `resp.Error` to stderr
   and exits with `resp.Code`. Add pretty-print of the structured
   `error` when present (retry-hint, context fields).
5. `state_test.go`, `daemon_integration_test.go`, `signing_test.go`,
   `room_test.go` — existing tests assert on `resp.Error` and
   `resp.Code`. Add new assertions that structured fields populate
   correctly for representative errors.

Co-ordination: A (identity) and F both touch `state.go` dispatch.
Last-to-merge rebases; A's touches are in registry-adjacent code
paths, F's are in error-return paths — expected conflict is small.

**E. Keychain integration (`claude-code`, `feat/keychain`)**

You are cold on this codebase. E is deliberately the smallest
available workstream. Read the cold-start list above first, then:

1. `signing.go` — current implementation: keys live as files at
   `~/.lesche/keys/<name>.key`. You will extract a keystore
   interface with two backends: file (current default) and keychain
   (macOS Security framework). Pick a pure-Go library if one exists
   that covers macOS Keychain; otherwise cgo is acceptable (flag
   this in your kickoff message).
2. `help.go` — document `LESCHE_KEYSTORE=keychain` env switch.
3. `signing_test.go` — existing coverage pattern for the file
   backend. Mirror it for keychain, skipping on Linux / CI when
   the backend is unavailable. Verify fallback-to-file when
   keychain init fails.

No `protocol.go`, no `state.go`, no `channel.go`, no `room.go`
changes. Your footprint is `signing.go` + one new file + a help
paragraph. If you find yourself editing anything else, stop and
`ask claude-coordinator`.

Everyone: before writing, run `lesche protocol` to see the
agent-facing guide verbatim, and `make test` to confirm the baseline
suite (32 tests, ~2.2s) is green on your branch.

Each agent owns its branch end-to-end (code + tests + docs + help
updates). Merge gate is `make test` passing plus human approval via
`tell claude-coordinator "ready for review: ..."`.

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
   should `ask claude-coordinator` on kickoff.

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
   work and report completion by `tell claude-coordinator "..."`.
   Real-time questions use the same channel via `ask` or `tell`.
5. **Merge gate is a human decision** (for now — the user). Agent
   reports "ready for review" over lesche; human merges when
   satisfied with the diff and a clean test run.

## File-ownership heat map

Only the three currently-active workstreams (A, E, F) are in the
table. Rooms, SQLite queue, and channels are all merged.

| File            | A. Identity | E. Keychain | F. Struct errors |
|-----------------|:-:|:-:|:-:|
| state.go        | H |  | M |
| channel.go      |  |  | L |
| room.go         |  |  | L |
| registry.go     | H |  |  |
| signing.go      |  | H |  |
| queue.go        |  |  | L |
| writer.go       |  |  | L |
| daemon.go       |  |  |  |
| client.go       | M |  | M |
| protocol.go     | L |  | **H** |
| main.go         | L |  | L |
| help.go         | L | L | L |
| util.go         | L |  |  |
| new files       | nickname.go, identity.go |  keystore.go |  |

H = heavy. M = moderate. L = small additive.

### Read the collisions

- **A ↔ F**: both touch `state.go`. A is heavy on registry.go and
  dispatch; F is moderate on every handler's error path. The
  `state.go` dispatch switch is shared — last-to-merge rebases.
- **E ↔ F**: none. E is confined to `signing.go` and a new
  `keystore.go`.
- **A ↔ E**: none.

The turn-FSM / tunnel era file `tunnel.go` no longer exists; its
role is covered by `channel.go`. Any older heat-map entry referring
to `tunnel.go` is stale and should be read as `channel.go`.

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
additive workstreams (keychain). Small collision with F (struct
errors) on `state.go`; last-to-merge rebases.

**Agent fit**: Highest context requirement. Whoever owns this should
already have full lesche internals loaded.

### ~~B. Room mode~~ — shipped at `e4e7186`.
### ~~C. SQLite write queue~~ — shipped at `d113b02`.
### ~~D. Resumable blocking~~ — killed by channels redesign (no FSM, no waiter state to resume).
### ~~G. Channels redesign~~ — shipped at `9d192bf`. See `CHANNELS.md`.

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

### H. Plan primitive + manager/worker roles

**Goal**: Replace this markdown file as the source of truth for
assignments. Move the assignment table into a git-backed `plan.json`
per project, mutable via `lesche plan …` commands. Introduce a role
axis on Agent (manager vs worker) so the daemon knows who can mutate
what. COORDINATOR.md stays for rationale, rules of engagement, cold-
start reading — it stops holding live state.

**Scope** (decisions already made, see conversation log):
- Roles set at `register --role worker|manager`. Stored on Agent.
  No cross-agent privilege beyond command-surface gating.
- One manager per project. Unregister rejects (`ManagerBusy`) if the
  manager still owns a non-empty plan; must `plan handoff <agent>`
  first.
- Project id auto-derived: `git remote get-url origin` slugified;
  fallback to repo basename when no remote.
- Plan storage: `<workspace>/plans/<project-id>/plan.json`. Same write
  queue as registry / room writes.
- Assignment shape: `{slug, goal, worktree, owner, status,
  updated_at}` with status ∈ `open | assigned | in-progress |
  ready | blocked | merged`.
- Manager-only mutations: `plan create <goal>`, `plan assign <slug>
  <agent> --worktree <path> --goal "..."`, `plan unassign <slug>`,
  `plan handoff <new-manager>`. `assign` verifies the worktree path
  exists on the manager's machine before writing.
- Worker self-service: `plan status <slug> in-progress|ready|blocked`
  flips the caller's own row only; daemon rejects writes to a row
  the caller does not own. `plan claim <slug>` verifies worktree
  exists on caller's machine, sets owner=self, status=in-progress.
- Anyone can read: `plan show [--project <id>]` defaults to cwd's
  project; `plan list` returns plans where caller is manager or owner.

**Files**: new `plan.go` (core), new `project.go` (git-remote →
project-id resolver), `state.go` (new ops + role-gated dispatch
checks), `registry.go` (Role field on Agent, persist), `client.go`
(cmdPlan* subcommands), `main.go` (dispatch), `help.go` (document),
`protocol.go` (new error code `ManagerBusy`, additive), new tests.

**Tests**: role persists across re-register; worker cannot mutate
other workers' rows; worker can flip own status; manager cannot be
unregistered while holding a non-empty plan; handoff atomically
transfers manager rights; project id derivation from remote vs
repo-basename; plan file round-trips through git.

**Blockers**: Heavy collision with A (identity) on `state.go`
dispatch + `registry.go` Agent record. Both add fields; last-to-merge
rebases, but the conceptual overlap is real — identity lands ULID
agent_id, plan adds Role. Sensible to sequence H **after** A so H
builds on stable Agent shape. F overlap is minor (new error code is
additive).

**Agent fit**: Deep context. Touches the edit surface of every
dispatch path plus the registry. Whoever owns this should already
have shipped at least one prior workstream with full internals
loaded. Not a cold-start task.

**Status**: Designed but unassigned. Pick it up after A merges; the
natural candidate is whichever of the current three agents finishes
their current batch first with clean test runs.

### F. Structured error payloads

**Goal**: Replace string-only errors with JSON payloads carrying
machine-readable fields (`code`, `reason`, `retry_hint`, context).

**Scope**: Extend `Response.Data` on errors to include a structured
`error` object. Clients parse it for automated retry logic. Preserve
exit codes for shell-script compatibility.

**Files**: `protocol.go` (heavy), every handler that returns an
error (state.go, channel.go, room.go), `client.go` (pretty-print
structured errors), `help.go` (document new fields).

**Tests**: every existing error path emits a structured payload;
clients parse payloads; backward-compat: old-style string errors in
`.Error` still populated for readability.

**Blockers**: Small collision with A (identity) on `state.go`
dispatch. Run sequentially or accept a last-to-merge rebase. No
collision with E (keychain).

**Agent fit**: Deep context. Touches the edit surface of most
handlers.

## Sequencing after the current batch

After A merges, H (plan primitive + roles) unblocks. Pick it up with
the first agent to finish A/E/F cleanly. After H lands, the next-up
work is multi-project workspace isolation (one daemon managing state
for several repos without collision); no written design yet — write
a short design doc first when that batch starts.

## Rules of engagement

1. **Branch from main. Worktree at `~/Obolos/lesche-<slug>`.**
   `git worktree add -b feat/<slug> ../lesche-<slug> main`.
2. **Tests must pass before you report done.** Run `make test`. If
   it fails, do not report done.
3. **`protocol.go` struct shapes are owned by workstream F** this
   batch. If you are not F, add fields additively only. F is allowed
   to rework `Response`.
4. **If you need to touch a file outside your heat-map column**,
   stop and `tell claude-coordinator "..."` to check for collisions
   before writing.
5. **When done, `tell claude-coordinator` and report**: branch name,
   commit SHA, `make test` output summary, any bugs found-but-not-
   fixed, any new env vars or commands added. Coordinator will
   either approve-for-merge or flag issues for another round.
6. **Rebase before merge.** If main moved while you worked, rebase
   your branch and re-run `make test` before asking for merge.

## What is explicitly off-limits in this batch

- **`protocol.go` struct shapes** — only F owns this. If you are A
  or E, add fields additively only; do not rename or rework
  existing ones.
- **Deleting or renaming existing commands.** Additive only. The
  channels redesign is the last non-additive surface change for a
  while.
- **Altering the wire format of persisted files** (registry JSON,
  per-peer `peers/<a>--<b>/*.md`, per-room `rooms/<name>/*.md`).
  Readers on main must still parse older files after your
  workstream merges.
- **Touching `/opt/homebrew/bin/lesche`** from a feature branch. The
  production binary is rebuilt from main only.
