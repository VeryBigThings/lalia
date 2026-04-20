# Lalia — Roadmap

Open workstreams in priority order. For what has already shipped, see
[CHANGELOG.md](./CHANGELOG.md). For system architecture, see
[docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md).

## Constraints

- **Branch from main.** Every workstream gets its own branch; tests must pass before reporting done (`make test`).
- **`protocol.go` struct shapes are additively extensible.** Do not rename or remove fields without an explicit migration plan; clients on main must still parse older messages.
- **Wire format of persisted files is stable.** Changes to registry JSON, peer/room/task file layouts require a migration. Readers on main must parse older files after the change merges.
- **Never `make install` from a feature branch** except on an isolated `LALIA_HOME`.

## Open workstreams

### X. CLI Polish & Robustness

**Priority**: High

**Goal**: Fix CLI parsing order-independence and rename the confusing `task status` mutation.

**Scope**:
- Rename `lalia task status` → `lalia task set-status`. Old command removed immediately.
- Refactor `cmdRead`, `cmdPost`, `cmdTell`, etc. to correctly skip flags when identifying positional arguments.
- Ensure `--as`, `--timeout`, and `--room` work correctly regardless of position in the argument list.
- Refresh `lalia help`, `lalia protocol`, shell completions, and role prompts.

---

### M. Re-register and room membership

**Goal**: Define consistent semantics for re-registering under an existing name and document them.

**Recommendation**: Fresh-identity. Unregister is terminal; re-register is explicit arrival; rejoining is opt-in.

**Scope**:
- Update `prompts/worker.md` and `prompts/supervisor.md` exit-protocol sections.
- Update `lalia protocol` / `help.go` identity section.

**Blocks**: L

---

### T. Branch-aware task defaults

**Goal**: Smooth worker arrival — default to the task matching the current worktree branch.

**Scope**:
- `lalia task bulletin` highlights the task matching the caller's current branch.
- `lalia task claim` defaults to the branch-matched slug if one exists.
- Update `prompts/worker.md` to instruct "confirm and claim the branch-matched task" as the first step.

---

### Y. Expired-supervisor handoff

**Goal**: Allow `task handoff <new>` to succeed without the outgoing supervisor's
signature when their lease has expired. Prevents the task list from being
permanently locked by an orphaned supervisor with no key on disk.

**Scope**:
- In `state.go` / `task.go`, when `opTaskHandoff` is called, check if the
  current supervisor's lease is expired. If so, skip signature verification
  and allow the handoff.
- The incoming supervisor must still be a registered agent with the supervisor role.
- Update `lalia help` and `lalia protocol` to document the escape hatch.

**Priority**: Medium

---

### L. `lalia rename <new>` — identity lifecycle primitive

**Goal**: Single atomic `lalia rename <new>` that preserves `agent_id` + keypair and migrates every name-indexed surface so the audit trail stays coherent.

**Blocked by**: M

---

### S. `task spawn` — agent lifecycle bus

**Goal**: Let a supervisor-class agent spawn sub-agent processes against a specific workstream and read their room traffic.

**Status**: Future. No design doc yet.

---

### Multi-project workspace isolation

**Goal**: Hard isolation between projects sharing a daemon — prevent cross-project channel/room leakage and make per-project auth boundaries explicit.

**Status**: Future. No design doc yet.
