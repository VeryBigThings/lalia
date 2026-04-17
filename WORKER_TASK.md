# Workstream H — Plan primitive + supervisor/worker roles

**Status**: unclaimed.

## How to pick this up

1. Register: `lesche register` (installed binary, no env overrides).
2. `lesche join feat-plan` + `lesche history feat-plan --room` to see
   if anyone else is already on it.
3. If nobody is, post `starting feat-plan as <your-name>` in the room
   and begin. That's the whole claim protocol until this very
   workstream lands automated plan assignments.

## Identity and coordination

- **Branch**: `feat/plan`.
- **Worktree**: `~/Obolos/lesche-plan` (this directory).
- **Coordination room**: `feat-plan`.
- **Supervisor**: `supervisor`. Report checkpoints via
  `lesche post feat-plan "..."`. DMs (`lesche tell supervisor`) only
  for private issues.

## Goal

Replace BACKLOG.md as the source of truth for assignments. Move the
assignment table into a git-backed `plan.json` per project, mutable
via `lesche plan ...` commands. Introduce a role axis on Agent
(supervisor vs worker) so the daemon knows who can mutate what. Auto-
create an assignment-scoped room on `plan assign`, deliver a pre-
registration kickoff on the owner's first register.

## Decisions already made (do not reopen)

- **Roles at register**: `lesche register --role worker|supervisor`.
  Stored on Agent. No cross-agent privilege beyond command gating.
- **One supervisor per project.** Unregister rejects with
  `SupervisorBusy` if the supervisor still owns a non-empty plan;
  must `plan handoff <agent>` first.
- **Project id auto-derived** from `git remote get-url origin`,
  slugified; fallback to repo basename when no remote.
- **Plan storage**: `<workspace>/plans/<project-id>/plan.json`. Same
  write queue as registry / room writes.
- **Assignment shape**: `{slug, goal, worktree, owner, status,
  kickoff, kickoff_delivered, updated_at}` with status ∈
  `open | assigned | in-progress | ready | blocked | merged`.
- **Supervisor-only mutations**: `plan create <goal>`, `plan assign
  <slug> <agent> --worktree <path> --goal "..." [--kickoff "..."]`,
  `plan unassign <slug>`, `plan handoff <new-supervisor>`. `assign`
  verifies the worktree path exists on the supervisor's machine
  before writing.
- **Worker self-service**: `plan status <slug>
  in-progress|ready|blocked` flips the caller's own row only; daemon
  rejects writes to rows the caller does not own. `plan claim <slug>`
  verifies worktree exists on caller's machine, sets owner=self,
  status=in-progress.
- **Anyone can read**: `plan show [--project <id>]` defaults to cwd's
  project; `plan list` returns plans where caller is supervisor or
  owner.
- **Assignment-scoped rooms**: `plan assign` auto-creates a room
  named after the slug, auto-joins supervisor + owner. `plan handoff`
  rewires room membership. status=merged archives the room (no new
  posts allowed, transcript kept).
- **Pre-registration kickoff**: if `plan assign --kickoff` supplies
  text, store it with `kickoff_delivered=false`. When the owner next
  calls `register`, `opRegister` scans plans, finds undelivered
  kickoffs for this agent, synthesizes a post from the supervisor
  into the assignment room, flips `kickoff_delivered=true`.
  Idempotent on re-register.

Note: you are building H atop what was originally called the "manager"
role — one of the sub-tasks in this workstream is renaming manager →
supervisor everywhere. Workstream I (if merged before you) already
does this cascade; if I has not merged, you do it as part of H.

## Files to touch

- **New**: `plan.go` (core ops), `project.go` (git-remote → project-id
  resolver).
- **Modify**: `state.go` (new ops + role-gated dispatch checks),
  `registry.go` (Role field on Agent, persist), `client.go` (cmdPlan*
  subcommands), `main.go` (dispatch), `help.go` (document),
  `protocol.go` (new error code `SupervisorBusy`, additive).
- **New tests** for the cases listed below.

## Tests

- Role persists across re-register.
- Worker cannot mutate other workers' rows (signed-dispatch rejection).
- Worker can flip own status.
- Supervisor cannot unregister while holding a non-empty plan
  (`SupervisorBusy`).
- `plan handoff` atomically transfers supervisor rights, including
  room membership.
- Project id derivation: from remote, and from repo-basename fallback.
- Plan file round-trips through git (replay on daemon restart).
- `plan assign` auto-creates the slug's room with supervisor + owner.
- `plan unassign` or `status=merged` archives the room (post refused).
- `--kickoff` is delivered on first register, not replayed on
  subsequent registers.

## Blockers / notes

- **Heavy collision with state.go dispatch** — the F workstream
  (structured errors) already landed the `errorResponse()` helper; use
  it on every error path in your new handlers.
- **Write queue shared** with workstream J (`feat/mailbox-persist`).
  Both add SQLite tables. Additive; no schema collision.
- Parallel-safe with I (`feat/init-run`) — zero file overlap.
- If I merges before H: pick up the `supervisor` vocabulary from the
  already-updated BACKLOG.md. If I merges after H: do the rename
  cascade yourself as part of H.

## Reporting checkpoints (all in `feat-plan` room)

- Start: `starting feat-plan as <your-name>`.
- Any open question: `ask supervisor "..."` (DM is fine for private
  concerns; room post for anything workstream-related).
- Ready for review: `ready for review: branch=feat/plan sha=<sha>
  make test: <summary>`.
