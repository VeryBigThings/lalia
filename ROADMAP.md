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

### L-WEB-1. Landing page — fix scroll velocity of chatboxes

**Priority**: Medium

**Goal**: Fix the scroll/animation velocity of the chatbox demo on the landing page (verybigthings.github.io/lalia/). Current speed feels off.

**Scope**:
- Identify the scroll/animation logic for the chatbox demo on the landing page
- Adjust velocity to feel natural and readable
- Test across screen sizes

---

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

### Z. `lalia task gc` — prune merged task rows

**Goal**: Remove merged task rows from task-list.json so the list doesn't
grow indefinitely.

**Scope**:
- New command `lalia task gc [--project <id>]`, supervisor-only.
- Drops all rows with `status=merged` from the project's task-list.json.
- Prints a summary of what was pruned.
- Update `lalia help` and `lalia protocol`.

**Priority**: Low. Independent of `rooms gc`.

---

### W. Registry eviction of expired agents

**Goal**: Automatically evict agents whose lease has expired from the registry,
freeing their name and releasing any resources they hold.

**Background**: Currently, lease expiry only removes an agent from the active
list (`lalia agents` stops showing them). The registry entry persists
indefinitely — the name stays reserved, the keypair stays on disk, and a
supervisor keeps holding the supervisor slot. The only way to clean up is
manual intervention (editing task-list.json + daemon reload).

**Scope**:
- On daemon boot and/or periodically, evict registry entries whose
  `last_seen_at` is older than the lease TTL (60 min).
- Eviction should: remove the registry entry, release the supervisor slot if
  held, and optionally notify any rooms the agent was in.
- Consider a grace period (e.g. 2× TTL) before eviction to tolerate transient
  disconnects.
- Update `lalia help` and `lalia protocol`.

**Priority**: Medium. Related to Y (expired-supervisor handoff) but broader.

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
