# Kopos — Supervisor Notes

If you're reading this, a human or the supervisor agent just handed
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

A plan for running multiple coding agents in parallel on kopos without
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

1. **`BACKLOG.md`** (this file) — top to bottom. Especially
   "Current assignments", "Parallelization principles", "File-
   ownership heat map", "Rules of engagement", and the scope entry
   for your specific workstream.
2. **`docs/ARCHITECTURE.md`** — how the pieces fit. Daemon/client/channel/
   room/writer/registry model.
3. **`docs/IDEA.md`** — why kopos exists. Short.
4. **`docs/MVP.md`** — what is shipping and what isn't. Large parts are
   retrospective now that channels / rooms / queue have landed.
5. **`docs/CHANNELS.md`** — the messaging redesign (shipped). Read if you
   are confused about why there is no `tunnel` / `send` / `await`
   / `sid` anywhere.
6. **`protocol.go`** — wire-level request/response shapes. Currently
   safe to extend additively; workstream F (structured errors) is
   the only task allowed to rework the `Response` shape.
7. **`help.go`** and run `kopos protocol` — the agent-facing
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
collisions announce them via `tell supervisor "..."` before
writing.

## Coordination default: rooms for feature work

For any active feature/workstream, coordination happens in a room
named after the slug (`feat/identity`, `feat/errors`, …). Supervisor
and assigned worker are members; other agents (reviewers, future
inheritors, onlookers needing context) can join to peek. The room's
git transcript survives session kills — kill a harness, come back
the next day, `kopos history <slug> --room` replays the thread.

Direct peer-to-peer channels (`tell` / `ask`) are the edge case:
private 1:1 problem-solving, identity questions, anything the rest
of the project genuinely shouldn't see. If the conversation is about
a specific workstream, it probably belongs in the slug's room.

This is automated by `kopos task publish`: the supervisor publishes a
structured plan and kopos creates the workstream room, joins the
supervisor, and posts the context bundle as the first message. A
worker's `kopos task claim <slug>` auto-joins the worker to that
room.

## If you are a worker agent — bootstrap

You are a worker if the human told you "you are a worker" or
assigned you a specific branch/workstream. The supervisor agent
(`supervisor`) is run separately and drives review + merge.

### 1. Identity

Pick the identity that matches your harness. The human has agreed
on these three names and nothing else:

- `copilot` — for the GitHub Copilot harness.
- `codex` — for the GPT-5 / Codex CLI harness.
- `claude-code` — for the Claude Code harness.

Set it once per shell:

```
export KOPOS_NAME=<your-name>
```

Do not invent a new name. Do not use `claude`, `supervisor`,
`sonnet`, or any variant — those either collide with the supervisor
or are reserved. If your harness has already been handed a different
name by the human, use that instead and tell the supervisor in your
first message.

### 2. Register

The kopos daemon is already running at `~/.kopos/sock`. Use the
installed binary (`which kopos`). Register yourself — idempotent,
reuses your existing Ed25519 key if you registered in a previous
session:

```
kopos register
kopos agents                  # sanity-check: see who else is online
```

If you see `supervisor` in the agents list, the supervisor
is up and expecting you. If not, wait and re-run `kopos agents`
every minute or so until it shows up.

### 3. Announce yourself

Tell the supervisor, in one message, exactly these things:

- Your harness (`copilot` / `codex` / `claude-code`).
- The workstream you were assigned (or "unassigned, awaiting
  direction").
- Confirmation that you have read `BACKLOG.md` (or a question
  if you haven't and don't understand it).
- Any blockers before you start (e.g. nickname storage question
  for `feat/identity`).

```
kopos tell supervisor "harness: codex. assigned feat/keychain. BACKLOG.md read. no blockers."
```

No tunnel to open, no sid to track. Your channel with the supervisor
is implicit on the first `tell`.

### 4. Receive messages from the supervisor

The supervisor may send you a message before you send one to them.
To receive anything arriving on any channel or room you're in:

```
kopos read-any --timeout 300
# prints kind=peer target=supervisor (or kind=room target=…)
# then the message body
```

`read-any` blocks up to `--timeout` seconds for the next inbound.
Reply to a peer with:

```
kopos tell supervisor "<your reply>"
```

Or if you want to pull from just one specific peer:

```
kopos read supervisor --timeout 300
```

Need a synchronous question-and-answer in one call? Use `ask`:

```
kopos ask supervisor "can I rebase on main now?" --timeout 60
```

`ask` sends, then blocks for the peer's next message and prints it
on stdout.

### 5. Keep your lease alive

Leases are 60 minutes; any command renews. If your harness sits idle
for longer than that, you get dropped and any blocking read returns
immediately. Two habits that prevent this:

- Call `kopos renew` right before a long run of edits.
- Or just run any kopos command occasionally — they all renew.

### 6. Announce key moments

`tell supervisor` at these checkpoints:

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
export KOPOS_NAME=<your-name>
kopos register
kopos agents | grep supervisor || echo "supervisor not up"
kopos channels                       # who do I already have a channel with?
kopos peek supervisor        # anything pending?
kopos read-any --timeout 60          # or block for the first inbound
```

If `read-any` times out and `peek` shows nothing, push first with
`tell supervisor "..."` per step 3.

## How to coordinate (general)

All agents coordinate through kopos itself. Full protocol guide:
run `kopos protocol`. Full help: `kopos help`.

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
  `~/.local/state/kopos/workspace` (outside harness allowlists).
- Ed25519 signed requests for every authenticated op.
- Install pipeline: `make install` places binary on `$PATH`.
- Protocol help (`kopos protocol`) and short help (`kopos help`)
  current for everything shipped.
- Test suite: 32 tests via `make test`, ~2.2s runtime.

**Active branches (not on main):**
- `feat/identity` — reassigned to Copilot (see below). Head still at
  `a907186`; not started. Needs rebase on new main before work begins.

**Designed, not implemented (priority order):**
- **I. `kopos init` + `kopos prompt` + `kopos run`** (unclaimed,
  top of queue). Role-specific onboarding prompts + harness spawn
  wrappers.
- **H. Plan primitive + supervisor/worker roles** (unclaimed).
  Parallel with I — zero file collision.
- **J. Daemon-restart mailbox persistence** (unclaimed). Parallel
  with I and H. Hot-path instrumentation.
- Multi-project workspace isolation (no design yet).

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
| `copilot` | `feat/identity` | A. Identity refactor + nicknames | `~/Obolos/kopos-identity` | Assigned. Prior shipped: feat/write-queue. Biggest-context task; copilot is the best-warmed agent on internals. Worktree exists at head `a907186`; rebase on main before starting. |
| `codex` | `feat/errors` | F. Structured error payloads | `~/Obolos/kopos-errors` | Assigned. Prior shipped: feat/rooms. Touches every handler but mechanically simple once the helper is in place; codex is warm on the handler pattern from rooms. Create worktree + branch from current main. |
| `claude-code` | `feat/keychain` | E. Keychain integration | `~/Obolos/kopos-keychain` | Assigned. Cold-start agent; E is the smallest and most self-contained workstream available. Create worktree + branch from current main. No `protocol.go` change, no `state.go` change — touches only `signing.go`, new `keystore.go`, `help.go`. |

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
`tell supervisor "ready for review: ..."`.

### Settled in prior batches

- Lease TTL raised to 60 minutes (was 10, then 30). Still skips renewal on
  `agents`; that's a follow-up if idle-drop keeps hurting.
- Channels redesign shipped (workstream G). See `docs/CHANNELS.md` for
  the plan document, kept as historical context. Current behavior is
  documented in `help.go` and `kopos protocol`.

### Per-workstream reading list

Read this after the cold-start list above, before writing code.

**A. Identity refactor + nicknames (`copilot`, `feat/identity`)**

1. `docs/IDENTITY.md` — the spec you're implementing. Read this first;
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
   `docs/IDENTITY.md` proposes `~/.kopos/nicknames.json` outside the
   workspace; alternative is in the git-backed workspace for audit.
   Raise this in your kickoff message to `supervisor`
   (e.g. `ask supervisor "nickname storage: home or workspace?"`).

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
   `~/.kopos/keys/<name>.key`. You will extract a keystore
   interface with two backends: file (current default) and keychain
   (macOS Security framework). Pick a pure-Go library if one exists
   that covers macOS Keychain; otherwise cgo is acceptable (flag
   this in your kickoff message).
2. `help.go` — document `KOPOS_KEYSTORE=keychain` env switch.
3. `signing_test.go` — existing coverage pattern for the file
   backend. Mirror it for keychain, skipping on Linux / CI when
   the backend is unavailable. Verify fallback-to-file when
   keychain init fails.

No `protocol.go`, no `state.go`, no `channel.go`, no `room.go`
changes. Your footprint is `signing.go` + one new file + a help
paragraph. If you find yourself editing anything else, stop and
`ask supervisor`.

Everyone: before writing, run `kopos protocol` to see the
agent-facing guide verbatim, and `make test` to confirm the baseline
suite (32 tests, ~2.2s) is green on your branch.

Each agent owns its branch end-to-end (code + tests + docs + help
updates). Merge gate is `make test` passing plus human approval via
`tell supervisor "ready for review: ..."`.

### Settled design notes from the first batch

1. **Copilot git identity on commits.** Settled as
   `user.email=copilot@local`, `user.name=Copilot`. Used on the
   feat/write-queue and feat/rooms merges (codex's branch was also
   committed under Copilot attribution on the shared machine; not
   worth unwinding retroactively).
2. **SQLite dependency.** `modernc.org/sqlite` — pure Go, no cgo,
   static-binary story preserved. Shipped in write-queue.
3. **Nickname storage location** still open; pick before feat/identity
   starts. `docs/IDENTITY.md` proposes `~/.kopos/nicknames.json` (outside
   workspace). Alternative: in the workspace for git audit. Copilot
   should `ask supervisor` on kickoff.

## Parallelization principles

1. **Each workstream is a branch in its own git worktree.** Naming:
   `feat/<slug>`. Worktree: `~/Obolos/kopos-<slug>`.
2. **One agent owns the branch end-to-end.** Feature code + unit tests
   + integration tests (where needed) + doc updates + help output
   updates. Merge blocker: `make test` passes locally.
3. **The binary at `/opt/homebrew/bin/kopos` stays built from main.**
   In-flight work lives only in its worktree's local `bin/kopos`.
   Agents do not `make install` from a feature branch except on their
   own isolated `KOPOS_HOME`.
4. **Coordination happens through kopos.** Agents announce start of
   work and report completion by `tell supervisor "..."`.
   Real-time questions use the same channel via `ask` or `tell`.
5. **Merge gate is a human decision** (for now — the user). Agent
   reports "ready for review" over kopos; human merges when
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

### I. `kopos init` + `kopos run` harness integration  **← top of queue**

**Goal**: Onboard a worker or supervisor agent into a kopos-
coordinated session with one command. Three-level surface, same
prompt content underneath:
- `kopos init <role>` — emit the role prompt to stdout (pipe,
  inspect, diff).
- `kopos prompt <role>` — write the prompt to `./KOPOS.md` in
  cwd. Happy-path convenience when the human will wire the harness
  up themselves.
- `kopos run <role> --<harness> [...args]` — write the prompt to
  the harness's preferred location and exec the harness with args
  forwarded.

**Scope**:
- New commands: `kopos init`, `kopos prompt`, `kopos run`, each
  with `worker` | `supervisor` subcommand.
- Embedded prompts via `//go:embed`: `prompts/worker.md`,
  `prompts/supervisor.md`. Edit these as markdown, print verbatim.
- Both prompts follow the 5-part skeleton: role posture, three
  questions for the human (name / workstream / context), bootstrap
  commands in order (register, join or create the slug's room,
  peek), ongoing rules (rooms-first, checkpoint reports, never run
  `./bin/kopos`, never set KOPOS_HOME/WORKSPACE), exit protocol
  (`kopos unregister` on permanent shutdown).
- `kopos run` harness mapping (verified via local CLI inspection,
  see research in git history for citations):
  - `--claude-code`: write KOPOS.md to cwd, exec
    `claude --append-system-prompt-file KOPOS.md "$@"`.
  - `--codex`: write KOPOS.md to cwd, exec
    `codex -c experimental_instructions_file='"'$PWD/KOPOS.md'"' "$@"`.
    Flag is experimental; fall back to writing AGENTS.md to cwd if
    the key renames.
  - `--copilot`: no instructions-file flag exists. Write (or append
    with a heading marker) `.github/copilot-instructions.md`, then
    exec `copilot "$@"`. Require `--force` to overwrite an existing
    file without a kopos marker.
**Files**: new `prompts/worker.md`, new `prompts/supervisor.md`, new
`run.go` (exec-wrapper), `client.go` (cmdInit, cmdPrompt, cmdRun),
`main.go` (dispatch), `help.go` (document the commands).

**Tests**:
- `kopos init worker` stdout matches embedded file byte-for-byte.
- `kopos prompt worker` writes `./KOPOS.md` with the same content;
  refuses to overwrite an existing file without `--force` unless the
  file carries a kopos-written marker on the first line.
- `kopos run worker --claude-code` with stubbed `claude` on PATH
  writes KOPOS.md and execs with the expected flag set.
- `kopos run worker --codex` stub test likewise.
- `kopos run worker --copilot` without `--force` on an existing
  `.github/copilot-instructions.md` (without kopos marker) errors
  before touching the file.
- No daemon calls: all three commands work with no running daemon.
- Cold exec: `kopos init` / `prompt` / `run` succeed before
  `kopos register`.

**Blockers**: None. Pure client-side. Does not touch `state.go`,
`channel.go`, `room.go`, or any daemon internals.

**Agent fit**: Any agent that can write Go and a few test cases.
Self-contained; good cold-start workstream — smaller surface than
F or H. Doesn't collide with F, H, or J.

### J. Daemon-restart mailbox persistence

**Goal**: When the daemon restarts (binary install, crash, host
reboot), pending unread messages in peer-channel and room mailboxes
should survive. Today the git transcript is preserved but per-recipient
unread state is in-memory only — any message not yet consumed by the
recipient at restart time is lost from their inbox (though the git log
still has it). This has cost us live-session messages twice in the
current batch.

**Scope**:
- Persist mailbox deltas on every delivery path: `channel.tell`'s
  mailbox append, `room.opPost`'s per-member mailbox append, and
  their drop-oldest-on-overflow (room) events.
- On consume (`channel.read`, `roomRead`), record the consumption so
  the replay doesn't re-deliver already-read messages.
- Storage: extend the SQLite queue with a `mailbox` table, or a new
  sibling DB, keyed by `(recipient_name, channel_or_room_id, seq)`.
  Call it during implementation — one table with a `kind` column is
  simpler than two sibling stores.
- On `newState()`, replay any undelivered rows into in-memory
  mailboxes before the daemon starts accepting connections.
- Cleanup: rows consumed by a `read` / `roomRead` are deleted from
  the persistence table (they survived the transcript in git; no
  need to keep them in the mailbox DB).

**Files**: `queue.go` (new mailbox table + schema migration),
`channel.go` (instrument `tell` deliver path + `read` consume path),
`room.go` (same), `state.go` (hook replay into `newState()` before
daemon listen), new tests exercising restart-survival.

**Tests**:
- `tell` → daemon restart → recipient `read` returns the message.
- `post` → daemon restart → members' `read` returns the message.
- `tell` → `read` → daemon restart → recipient's next `read` empty.
- Overflow: drop-oldest behavior replays identically post-restart
  (drop counter preserved).
- Concurrent delivery + restart: no message lost, no duplicate
  delivered.

**Blockers**: None. Parallel-safe against I and H — I is pure client-
side, H adds a new table but doesn't touch mailbox paths. Small
collision with H on the write queue if H adds its own table, but
both can live in the same SQLite DB additively.

**Agent fit**: Warm agent. Touches the queue + delivery hot paths.
Not a cold start.

### A. Identity refactor + nicknames

**Goal**: Replace name-as-primary-key with ULID agent_id + rich
metadata (project, branch, harness, model) auto-detected on register;
add user-assigned nicknames.

**Scope**: Implement everything described in `docs/IDENTITY.md`. Preserve
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
already have full kopos internals loaded.

### ~~B. Room mode~~ — shipped at `e4e7186`.
### ~~C. SQLite write queue~~ — shipped at `d113b02`.
### ~~D. Resumable blocking~~ — killed by channels redesign (no FSM, no waiter state to resume).
### ~~G. Channels redesign~~ — shipped at `9d192bf`. See `docs/CHANNELS.md`.

### E. Keychain integration

**Goal**: Store private keys in macOS Keychain (or system keyring on
Linux) instead of plain files at `~/.kopos/keys/*.key`.

**Scope**: Provide a key-store abstraction with two backends: file
(current default) and keychain. Selectable via env (`KOPOS_KEYSTORE=keychain`).
Keychain items named `kopos:<agent_name>`. Fallback to file when
keychain unavailable.

**Files**: `signing.go` (swap direct-file reads for a keystore
interface), new `keystore.go` (backends), `help.go` (env).

**Tests**: round-trip through file backend; round-trip through
keychain on macOS (skip on Linux CI if unavailable); correct fallback
behavior when backend is unavailable.

**Blockers**: None. Zero protocol change. Fully parallel-safe.

**Agent fit**: Self-contained. Works for any agent that can read
`signing.go`.

### H. Task primitive + supervisor/worker roles

**Goal**: Replace this markdown file as the source of truth for work
assignments. Move the assignment table into a git-backed `task-list.json`
per project, mutable via `kopos task …` commands. Introduce a role axis
on Agent (supervisor vs worker) so the daemon knows who can mutate what.
BACKLOG.md stays for rationale, rules of engagement, cold-start reading
— it stops holding live state.

**Scope** (as shipped — see git history for the evolution):
- Roles set at `register --role worker|supervisor`. Stored on Agent.
  No cross-agent privilege beyond command-surface gating.
- One supervisor per project. Unregister rejects (`SupervisorBusy`) if
  the supervisor still owns non-merged tasks; must `task handoff
  <agent>` first.
- Project id auto-derived from `git remote get-url origin` slugified;
  fallback to repo basename. `repo_root` captured at register time.
- Task list storage: `<workspace>/tasks/<project-id>/task-list.json`.
- Task shape: `{slug, branch, brief, owned_paths, contracts, worktree,
  owner, status, updated_at}` with status ∈ `open | assigned |
  in-progress | ready | blocked | merged`.
- Supervisor-only mutations: `task publish --file <payload>`,
  `task unassign <slug>`, `task reassign <slug> <agent>`,
  `task handoff <new-supervisor>`. publish creates N worktrees + N
  rooms + N bundle posts atomically per slug (one slug failing does
  not block the rest); republish against the same commit is a no-op.
- Worker self-service: `task bulletin [--project <id>]` lists open
  tasks regardless of caller role (this is the discovery surface);
  `task claim <slug>` atomically flips open → in-progress, auto-joins
  the room, returns the bundle; `task status <slug>
  in-progress|ready|blocked` mutates the caller's own row only.
- Anyone can read: `task show [<slug>] [--project <id>]` defaults to
  cwd's project; `task list` returns lists where caller is supervisor
  or owner.
- **Workstream-scoped rooms**: `task publish` creates the slug-named
  room, joins the supervisor, and posts the bundle as the room's
  first message. `task claim` auto-joins the worker. `task handoff`
  rewires room membership. Setting status to `merged` does not
  archive the room — `kopos rooms gc` is the opt-in cleanup step.
- Worktree ownership: `task publish` shells out to `git worktree add`
  on behalf of the supervisor under `<parent-of-repo_root>/wt/<slug>`,
  with per-repo serialization and per-slug rollback on partial
  failure. Supervisors never run `git worktree add` themselves.

**Files**: `task.go` (core + publish + migration), `state.go`
(dispatch + role-gated checks), `registry.go` (Role + RepoRoot on
Agent), `client.go` (cmdTask), `main.go` (dispatch), `help.go`
(document), `protocol.go` (new error codes `SupervisorBusy`,
`ProjectIdentityMismatch`), `prompts/{worker,supervisor}.md`,
`completions/{_kopos,kopos.bash}`.

**Status**: Shipped. Evolution:
- `b809316` — publish-pull rewrite; initial `plan_*` surface
  (with `plan assign` + pre-registration kickoff delivery) replaced
  by publish-pull; adds `task unpublish` primitive.
- `2ed889f` — unpublish safety follow-up: worktree preserved by
  default, `--wipe-worktree` opt-in, `--evict-owner` required to
  wipe over a live owner lease, accurate `worktree_removed` field
  (now derived from real filesystem check), republish clears
  archived flag, `kopos agents` grows a `lease` column, macOS
  `/var`→`/private/var` symlink normalization in
  `ensureWorktree`.

Rolling external feedback against shipped versions lives in
`/Users/neektza/Code/obolos/obolos/kopos-feedback.md` (obolos
supervisor's notes). Outstanding items from that doc are tracked
as separate workstreams below (K/L/M).

### S. `task spawn` — kopos as agent lifecycle bus (future)

**Goal**: Let a supervisor-class agent spawn one-shot or multi-shot
sub-agent processes (claude-code, codex, gemini, copilot, …) against
a specific workstream and read their room traffic to guide the next
iteration. kopos becomes the communication bus **and** the process
manager for those sub-agents.

**Why**: Today a human has to stand up each worker harness in a
shell, set `KOPOS_NAME`, and direct it to the right slug. For fully
autonomous orchestration the supervisor needs a primitive to say
"spin up a worker of runtime R against slug S, seat it, let it work,
report back when it exits." This is the right home for the spawn
semantics that `plan assign` vaguely gestured at but never did
cleanly.

**Sketch** (not final):
- `task spawn <slug> --runtime <claude-code|codex|…> [--one-shot]`:
  supervisor-only. Registers a transient agent, launches the
  configured harness in the workstream's worktree with the role
  prompt wired in, links its stdout/stderr into the room, claims the
  slug on its behalf, and monitors the process.
- Multi-shot: the spawned agent can emit structured "iterate" /
  "done" messages in-room; supervisor re-prompts on iterate, tears
  down on done.
- Lifecycle signals piggyback on rooms (supervisor posts a control
  message; harness interprets). No new transport.

**Non-goals**: replacing human supervisors; auto-merging; scheduling
across machines. Local-first, per-repo, per-user.

**Status**: Future work. Captured after the publish-pull rewrite to
make clear that kopos-initiated agent lifecycle is the expected home
for the assignment-push semantics removed from `task publish`.

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

### K. `loadRooms` transcript rehydration on boot

**Status**: Shipped at `8752028` + `6e9780a`.

Three correlated fixes landed alongside the headline feature:

1. **`parseRoomMsgFile` + `loadRooms` transcript walk** (room.go,
   registry.go): walks `rooms/<slug>/*.md` on boot, parses
   frontmatter, rebuilds `r.log` and `r.seq`. Malformed files
   skipped, not fatal.

2. **`ensureRoomWithMembers` SQLite persistence** (task.go): rooms
   created by `task_publish` were never written to SQLite — only
   to the async git write queue — so `loadRooms` found nothing
   after restart. Fixed by calling `queue.roomUpsert` +
   `queue.roomAddMember` on new rooms. Secondary bug: `newRoom()`
   pre-populates `createdBy` in `r.members`, so the creator was
   absent from `added`; fix flushes `r.members` directly for new
   rooms.

3. **`flushPendingWrites` boot ordering** (writer.go, state.go):
   the write-queue replay goroutine started after `newState()`
   returned, so transcript files not yet git-committed before
   shutdown were absent from disk during `loadRooms`. Extracted
   `flushPendingWrites()`, called synchronously in `newState()`
   before `loadRooms`.

Tests in `room_rehydration_test.go`:
- `TestLoadRoomsRehydratesLogAndSeq`
- `TestLoadRoomsHandlesMissingTranscriptDir`
- `TestLoadRoomsSkipsMalformedTranscriptFiles`
- `TestParseRoomMsgFileRoundTrip`
- `TestEnsureRoomWithMembersPersistedToSQLite`
- `TestRepublishBundleSurvivesRestart` (integration)

### L. `kopos rename <new>` — identity lifecycle primitive

**Source**: `kopos-feedback.md` (external) — obolos-supervisor
observed that renaming `supervisor` → `obolos-supervisor` required
three out-of-band steps (register-new + task-handoff +
unregister-old) and still fragmented channel history across two
peer-pair keys.

**Goal**: Single atomic `kopos rename <new>` that preserves
`agent_id` + keypair and migrates every name-indexed surface so
the audit trail stays coherent across a rename.

**Problem**: Agent identity is keyed by name in too many places:
- `Agent.Name`, `nameIdx[name]` in registry.
- `Task.Owner`, `TaskList.Supervisor` in task lists.
- `Room.members[name]` keys.
- `Channel.PeerA` / `PeerB`, `channelKey(a, b)` for DM history.
- Private key file `~/.kopos/keys/<name>.key`.
- Nickname `Address` strings (`name@project:branch`).
- `anyWaiter[name]`, `channel.waiter[name]` map keys.
- SQLite mailbox rows keyed by `(owner_name, kind, target, seq)`.

`register --name <new>` today just creates a fresh identity with a
new keypair and ULID; nothing migrates.

**Scope**:
1. **Registry**: update `nameIdx`, preserve `agent_id` and pubkey,
   rename key file via `renameKey(old, new)` (new keystore op).
2. **Tasks**: walk `s.tasks[*]`; rewrite `Supervisor` + `Task.Owner`
   fields matching the old name; persist.
3. **Rooms**: walk `s.rooms[*]`; rewrite `members` map keys; persist
   via `queue.roomRemoveMember` + `queue.roomAddMember`.
4. **Channels**: walk `s.channels`; rewrite `PeerA`/`PeerB`;
   re-key the map under the new `channelKey`. Rewrite
   `mailbox`/`log`/`waiter` map keys where they reference the old
   name.
5. **SQLite mailbox**: new `queue.mailboxRename(old, new)` that
   updates both `recipient` and `from_name` columns in the mailbox
   table.
6. **Nicknames**: rewrite `Address` strings matching
   `<old>@...` to `<new>@...`.
7. **Waiters**: migrate `anyWaiter[old]` → `anyWaiter[new]`;
   in-flight blocking calls stay on their goroutines but their
   next delivery targets the new key.
8. **Safety**: refuse on collision with an existing name unless
   `--force`. Verify caller holds the old name's key (signature
   check applies like any authenticated op).
9. **Atomicity**: hold `s.mu` for the duration; ensure partial
   failure rolls back the structural changes (hard — the SQLite
   update is the point of no return, so either do it last, or wrap
   in a transaction and rollback in-memory on SQLite failure).

**New op**: `rename` (args: `from`, `to`, optional `force`).
**New CLI**: `kopos rename <new>`.
**New error code**: `CodeNameConflict` (9).

**Files**: `state.go` (new op + dispatch), `registry.go` (Agent
rename helpers), `keystore.go` + `keystore_*.go` (key file rename),
`task.go` (task-side migration), `room.go` (room-side migration),
`channel.go` (channel-side migration), `nickname.go` (nickname
rewrite), `queue.go` (`mailboxRename`), `client.go` (`cmdRename`),
`main.go` (dispatch), `help.go` (doc), `protocol.go`
(`CodeNameConflict`), `prompts/*.md` (note the new primitive,
replace any "drop and re-register" guidance).

**Tests**: round-trip rename preserves agent_id + keypair; task
ownership moves; room membership moves; channel DM history is
accessible under the new name; SQLite mailbox rows carry over;
nickname references are rewritten; collision refused without
`--force`; wrong-key caller rejected; rollback on SQLite failure.

**Blockers**: Depends on workstream M (re-register semantics)
being decided, because the rename code needs to know whether it's
conceptually "a live agent changing name" or "drop-and-restore"
— affects behavior when the caller is also currently a member of
channels/rooms with pending reads.

**Agent fit**: Highest context. Touches every name-indexed surface.
Not cold-start.

### M. Re-register and room membership (design question)

**Source**: `kopos-feedback.md` (external) — a worker followed the
documented exit protocol (unregister after `ready`), then
re-registered to look at review, and had to `kopos join <slug>`
manually because unregister had dropped it from the room.

**Goal**: Pick a consistent answer to "what does re-register under
an existing name mean for prior room memberships / channel
subscriptions" and update the worker/supervisor prompts to match.

**The two stances**:
- **Re-register = resume**. Unregister drops the agent but
  preserves its room memberships on disk under a "paused" marker;
  re-register rehydrates them. Matches chat-client expectations.
- **Re-register = fresh identity event**. Unregister is fully
  terminal; re-register is explicit arrival; rejoining is opt-in.
  Matches the rest of kopos's explicit-state posture.

**Recommendation**: Fresh-identity. Unregister currently deletes
the private key on disk (state.go:558 — `_ = removeKey(from)`);
if re-register were resume, that key deletion would need to go
because the same keypair is expected to be usable afterwards.
Fresh-identity preserves the cleanest story: unregister is
irrevocable, re-register creates a new agent_id that happens to
share a name. Any "I want to come back and read" flow uses
`kopos rename` (workstream L) to change name without losing
state, not unregister/re-register.

**Scope**:
- Decide the stance.
- Update `prompts/worker.md` exit-protocol section to spell it
  out explicitly.
- Update `prompts/supervisor.md` similarly for the supervisor
  lifecycle.
- Update `kopos protocol` / `help.go` identity section if needed.

**Files**: `prompts/worker.md`, `prompts/supervisor.md`, `help.go`.
(No daemon code change under the recommended stance — it
describes the existing behavior and brings the docs in line.)

**Tests**: Prompt byte-equality across `init`/`prompt` already
covered; add a test that the updated text mentions the chosen
stance so future edits don't silently drift.

**Blockers**: None, but blocks L (rename primitive) which wants
this decided first so its own prompt updates can reference the
consistent model.

**Agent fit**: Low code, high care. Mostly prose.

### N. `kopos agents` — decomposed columns + worktree-kind tracking

**Source**: user feedback, this session. Two parts of the same
theme:
- The `qualified` column (`name@project:branch`) is a single
  squashed string that humans can't scan visually. The underlying
  metadata is already on `Agent`; it just isn't surfaced as
  independent columns.
- "Topology" in the user's words: for each agent we want to know
  whether its cwd is the main worktree of a repo, a secondary
  worktree (branch worktree), or outside any repo. Today nothing
  distinguishes these.

**Goal**: Capture the missing "what kind of worktree is this
agent in" metadata, and rework `kopos agents` so project /
branch / worktree / worktree-kind / lease / role are separate
columns. Keep `qualified` in the response for scripting use.

**Scope**:

Metadata capture (identity.go + state.go):
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
  `role` in the `opAgents` response (see state.go:820-847).

**Handling "outside any repo" agents** (three variants):
1. *Outside, no --project override*: `git rev-parse` fails; all
   git-derived fields empty; `WorktreeKind = "outside"`. Drop
   the current `basename(CWD)` fallback for `Project` in
   identity.go — it creates meaningless collisions between
   unrelated agents in different dirs. Let `Project` stay
   empty when no git context exists.
2. *Outside, --project X explicit*: user forces association.
   `Project = X`, `WorktreeKind = "outside"`, git fields empty.
   Agent can participate in rooms and peer messaging for that
   project but cannot claim/publish tasks (no worktree).
3. *Inside a repo with no remote*: existing fallback behavior
   (Project = repo basename); `WorktreeKind` set normally.

Behavior impact:
- Peer messaging (`tell`/`ask`/`read`/`read-any`) works
  regardless of repo state.
- Task ops need `Project`; variant 1 agents simply won't appear
  in any project's bulletin and their `task claim` / `task
  status` calls fail with `missing_project`. That's correct.
- `kopos task claim` for variant 2 agents would fail during the
  worktree path check — task rows carry a `Worktree` field; no
  code change needed here, just doc clarity.
- Table display: render empty repo/branch/worktree as `—`;
  `wt-kind` column shows `outside` explicitly so it is obvious
  this agent isn't tied to any repo.

Display surface (client.go):

Default `kopos agents` output becomes a **grouped view by repo**
— the primary question the command answers is "which agents are
clustered together?" and the grouped layout shows that
structurally instead of making the user mentally sort by project.
The `main:` / `worktree:` line labels replace the `wt-kind`
column from the flat design.

Example:

        repo: /Users/neektza/Code/obolos (obolos)
          main:       supervisor        master         live     claude-code  3s ago
          worktree:   codex             feat/bb-core   live     codex        42s ago  (wt/bb-core)
          worktree:   sonnet            feat/shell-b   live     claude-code  7m ago   (wt/shell-budgetbot)
        repo: /Users/neektza/Code/kopos (kopos)
          main:       kopos-maintainer  main           live     claude-code  just now
        outside:
          orphan-tool     (cwd: /tmp/scratch)           live     claude-code  1m ago
          analysis        (--project=obolos, no wt)    live     codex        5m ago

Key surface changes:
- The default view shows **last activity** (from
  `Agent.LastSeenAt`, renewed by `renewLease` on every
  authenticated request) instead of `started_at`. "When did this
  agent last talk to kopos?" is more actionable than "when did
  it first register," and lines up with the lease/liveness story
  from the feedback-doc fix.
- `last_seen` rendered as a relative duration (`just now`, `42s
  ago`, `3m ago`, `1h ago`). For ages > 24h, fall back to the
  date.
- `wt-kind` column is gone in the grouped view — the tree
  structure carries it (`main:` vs `worktree:` labels; `outside:`
  bucket at the bottom).
- Repos sorted by agent count desc; within a repo, main first
  then secondary worktrees alphabetical.
- `agent_id` not shown in default view (long ULID); keep behind
  `--wide`.

Flags:
- `--grouped` — explicit request for the grouped-by-repo tree
  view shown above. **This is the default**; the flag exists so
  invocations can be explicit, and so the default is easy to flip
  later by swapping which flag is default without reworking the
  command surface.
- `--flat` — flat table (one row per agent, explicit `project` +
  `wt-kind` columns) for scripts and for cases where you really
  do want a position-stable column grid:

        name              role        project  branch        wt-kind    lease    harness      last_seen
        supervisor        supervisor  obolos   master        main       live     claude-code  3s ago
        codex             worker      obolos   feat/bb-core  secondary  live     codex        42s ago

  `--grouped` and `--flat` are mutually exclusive; passing both
  errors out.
- `--wide` — in either layout, include `agent_id`, `cwd`,
  `expires_at`, `main_repo_root`, `started_at` (for the cases
  where original register time still matters).
- `--json` — pass-through of the raw response. The JSON retains
  both `started_at` and `last_seen_at` as full RFC3339
  timestamps; the relative-duration formatting is strictly a
  human-display concern. `--json` ignores `--grouped`/`--flat`
  (they are display-only concerns).

Collapses workstream O (grouped topology view) into N — no
separate `kopos topology` verb needed.

**Files**: `identity.go` (new detection helpers), `state.go`
(AgentInfo → Agent propagation, opAgents response fields),
`client.go` (cmdAgents formatter, --wide / --json flags).

**Tests**:
- `TestDetectWorktreeKindMain` / `...Secondary` / `...Outside` /
  `...Detached` — seed a git repo + a secondary worktree in a
  tempdir, assert detection from each cwd.
- `TestAgentsResponseHasTopologyFields` — register agents from
  each kind of cwd; assert response fields populate correctly.
- Keep existing `TestAgentsIncludesLeaseStatus` shape.

**Blockers**: None. No protocol break; only additive fields on
the existing `opAgents` response.

**Agent fit**: Small to medium. Identity detection has a few git
edge cases (detached HEAD, bare repos) that need test coverage;
the rest is straightforward formatter work.

## Sequencing after the current batch

A, E, F merged. Three parallel workstreams open (I, H, J); worktrees,
rooms, and WORKER_TASK.md briefs in place, awaiting worker claim.

1. **I. `kopos init` + `kopos prompt` + `kopos run`** — room
   `feat-init-run`, branch `feat/init-run`. Self-contained, no
   daemon touch.
2. **H. Plan primitive + supervisor/worker roles** — room
   `feat-plan`, branch `feat/plan`. Daemon-heavy; parallel-safe
   with I and J.
3. **J. Daemon-restart mailbox persistence** — room
   `feat-mailbox-persist`, branch `feat/mailbox-persist`. Hot-path
   instrumentation; parallel-safe with I and H.
4. **Multi-project workspace isolation** — no design doc yet; draft
   when the next batch opens.

## Rules of engagement

1. **Branch from main. Worktree at `~/Obolos/kopos-<slug>`.**
   `git worktree add -b feat/<slug> ../kopos-<slug> main`.
2. **Tests must pass before you report done.** Run `make test`. If
   it fails, do not report done.
3. **`protocol.go` struct shapes are owned by workstream F** this
   batch. If you are not F, add fields additively only. F is allowed
   to rework `Response`.
4. **If you need to touch a file outside your heat-map column**,
   stop and `tell supervisor "..."` to check for collisions
   before writing.
5. **When done, `tell supervisor` and report**: branch name,
   commit SHA, `make test` output summary, any bugs found-but-not-
   fixed, any new env vars or commands added. Supervisor will
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
- **Touching `/opt/homebrew/bin/kopos`** from a feature branch. The
  production binary is rebuilt from main only.
