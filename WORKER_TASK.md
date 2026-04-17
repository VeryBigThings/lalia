# Workstream J — Daemon-restart mailbox persistence

**Status**: unclaimed.

## How to pick this up

1. Register: `lesche register` (installed binary, no env overrides).
2. `lesche join feat-mailbox-persist` + `lesche history feat-mailbox-persist --room`
   to see if anyone else is on it.
3. If nobody is, post `starting feat-mailbox-persist as <your-name>` in
   the room and begin.

## Identity and coordination

- **Branch**: `feat/mailbox-persist`.
- **Worktree**: `~/Obolos/lesche-mailbox-persist` (this directory).
- **Coordination room**: `feat-mailbox-persist`.
- **Supervisor**: `supervisor`. Report checkpoints via
  `lesche post feat-mailbox-persist "..."`. DMs (`lesche tell
  supervisor`) only for private issues.

## The problem

When the daemon restarts (binary install, crash, host reboot) any
pending unread messages sitting in per-recipient mailboxes are lost
from the recipient's inbox. The git transcript still has them, but
the recipient's next `read` / `read-any` returns empty because the
in-memory mailbox was wiped.

This bit us twice already: after the keychain merge and after the
identity merge, every worker had to be re-messaged because their
review notes evaporated with the daemon.

## Goal

Mailbox unread state survives daemon restart. After restart:
- `lesche peek <peer>` returns the same unread list as before.
- `lesche read <peer>` returns the oldest unread message.
- Room mailboxes likewise, including the drop-oldest overflow counter.

The git transcript is not affected — it is already persisted. This is
purely about the unread-state layer on top.

## Approach

- Extend the SQLite queue (or add a sibling DB — call it during
  implementation) with a `mailbox` table keyed by
  `(recipient_name, kind, target, seq)` where kind ∈ `peer` | `room`.
- Instrument the delivery sites:
  - `channel.tell` mailbox append.
  - `room.opPost` per-member mailbox append.
  - Room drop-oldest-on-overflow (persist the dropped counter too).
- Instrument the consume sites:
  - `channel.read` — delete the consumed row.
  - `roomRead` — delete drained rows; reset dropped counter.
- On `newState()` (daemon startup), replay undelivered mailbox rows
  into the in-memory mailbox structures **before** the daemon starts
  accepting client connections.

## Files to touch

- **Modify**: `queue.go` (new table + schema migration path; existing
  write-queue table stays unchanged).
- **Modify**: `channel.go` (delivery + consume instrumentation).
- **Modify**: `room.go` (delivery + consume + overflow
  instrumentation).
- **Modify**: `state.go` (replay hook inside or right after
  `newState()` / `loadRegistry()`; must run before daemon listen).
- **New tests** covering the restart survival cases listed below.

## Tests

- `tell` → kill daemon → restart → recipient `read` returns the
  message.
- `post` → restart → members' `read` returns the message.
- `tell` → `read` → restart → recipient's next `read` is empty (no
  double-delivery).
- Room overflow: drop-oldest counter preserved across restart.
- Concurrent delivery during a simulated restart: no message lost,
  no duplicate.
- The existing 50+ tests on main continue to pass.

## Blockers / notes

- Parallel-safe with I (`feat/init-run`) — zero overlap.
- Shares SQLite DB with workstream H (`feat/plan`, if H lands a
  plan table). Additive tables; no schema collision.
- **Ordering concern**: the replay must complete before the daemon
  accepts connections, otherwise a client could read an empty
  mailbox before replay finishes. Either replay synchronously in
  `newState()` before returning, or block the socket listener until
  a "ready" signal.
- Uses pure-Go SQLite (`modernc.org/sqlite`, already a dep). No new
  external deps.

## Reporting checkpoints (all in `feat-mailbox-persist` room)

- Start: `starting feat-mailbox-persist as <your-name>`.
- Any open question: post to the room or DM the supervisor.
- Ready for review: `ready for review: branch=feat/mailbox-persist
  sha=<sha> make test: <summary>`.
