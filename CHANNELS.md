# Channels — Messaging Redesign

**STATUS: shipped at `9d192bf`.** Kept in the repo as historical context
for why the surface looks the way it does. Current behavior is
authoritative in `help.go` / `lesche protocol`.

---


A plan to replace the current tunnel/room split and turn-enforced
two-party channel with a unified channels model: one mailbox per
peer-pair, one mailbox per room, four verbs, no turn FSM, no sids
in the CLI.

## Why

Three friction points observed in live use:

1. **Turn FSM blocks the most common case.** Reviewer wants to fire
   a follow-up ("also, saw this") before the reviewee has responded.
   Current tunnel rejects the second send with `not_your_turn`.
2. **`send` / `await` names don't map to English intent.** Coordinator
   agents parse instructions like "publish an update to codex" and
   "ask copilot about X" into the same verb. The command name
   should carry the intent so the LLM doesn't guess wrong.
3. **Multi-session per peer-pair is unused overhead.** Agents are
   episodic; they read one inbox per invocation. Two concurrent sids
   with the same peer either go unused or become stale-tunnel noise.
   Rooms already work with a single-handle-per-channel model and
   that works fine.

## Goals

- Fire-and-forget `send` (async, non-blocking, no turn flip).
- Blocking consume with optional timeout, for both P2P and rooms.
- Single `ask` helper for the one-shot question-expects-answer case.
- Non-destructive inspect (`peek`) on both P2P and rooms.
- One persistent channel per `(self, peer)`; no sid in the CLI.
- Verb naming maps cleanly to English intent.
- Git-backed transcript preserved (this is non-negotiable).

## Non-goals

- No change to Ed25519 signing, registry, or workspace git layout
  beyond the filesystem path rename below.
- No new transport beyond P2P + rooms. (Multicast, federation,
  cross-machine sync remain out of scope.)
- No built-in "negotiate" or "conversation" primitive. Multi-round
  exchange is the agent's responsibility, composed from `ask` or
  `tell` + `read` loops.

## New surface

```
# Peer-to-peer
lesche tell <peer> "msg"               async, returns immediately
lesche ask  <peer> "msg" [--timeout N] tell + block for next inbound from peer
lesche read <peer>       [--timeout N] block until next inbound (0 = non-blocking)
lesche peek <peer>                     non-destructive inspect of pending

# Room
lesche room_create <name> [--desc s]
lesche join     <room>
lesche leave    <room>
lesche rooms
lesche participants <room>
lesche post <room> "msg"               async broadcast
lesche read <room>  [--timeout N]      consume pending (0 = non-blocking)
lesche peek <room>                     non-destructive inspect

# Housekeeping (unchanged in shape, may change args)
lesche register | agents | renew | history <peer|room> | stop | protocol
```

Seven communication verbs total: `tell`, `ask`, `post`, `read`,
`peek`, `join`, `leave`. Plus room/registration management.

### English ↔ command mapping (ships in `lesche protocol` output)

| Human phrasing | Command |
|---|---|
| "tell / notify / inform / publish an update to X" | `tell X "..."` |
| "ask / check with / query X" | `ask X "..."` |
| "negotiate / discuss / coordinate with X" | loop of `ask` / `tell` + `read` |
| "post / announce / share with the room" | `post #room "..."` |
| "check if there are any messages" | `peek X` or `read X --timeout 0` |
| "wait for a message" | `read X --timeout 300` (or whatever) |

## Semantics

### Channels

A **channel** is a persistent, git-backed, ordered message log with
a mailbox per recipient. Two kinds:

- **P2P channel**: keyed by the unordered pair `{a, b}`. Created
  implicitly on first `tell` or `ask`. Never explicitly opened;
  closed only by the last message decaying in the git log or by
  explicit agent deregistration.
- **Room channel**: keyed by room name. Created by `room_create`.
  Members managed by `join` / `leave`.

### `tell` and `post`

Append message to the channel. Deliver into each recipient's
mailbox (for P2P: the peer's mailbox; for room: every member
except the sender). Commit to git. Return. Never blocks. No turn.
Identical semantics for both channel kinds; different verbs because
the user intent (directed vs broadcast) is different.

### `read`

Consume and remove the oldest unread message from the caller's
mailbox on the named channel. With `--timeout N > 0`, block up to
N seconds waiting for a message. With `--timeout 0`, return
immediately with either a message or an empty result. Messages
remain in the git transcript; only the mailbox entry is consumed.

### `ask`

Pure client-side composition: `tell` then `read` with a timeout,
on the same channel. No new wire op. Returns the peer's reply or
timeout. Equivalent to the current `send` behavior, minus the
turn flip.

### `peek`

Return all pending messages in the caller's mailbox without
consuming. Non-blocking. Works for both P2P and rooms (the latter
is already shipped).

### Overflow / backpressure

P2P mailboxes: unbounded by default (the common case is low
volume between two agents). If a peer is offline for a long time,
the mailbox grows; when the peer registers and reads, they see
everything. No drop policy.

Room mailboxes: keep current bounded-with-drop-oldest + overflow
notice behavior (shipped in `feat/rooms`). Rationale: N-party
broadcast has realistic flood scenarios (8 members all posting);
P2P does not.

## Wire-protocol changes

Additive. Existing `Request.Op` values remain; new ones added:

- `tell` — args: `{from, peer, body}` — non-blocking, returns after
  commit.
- `read` — args: `{from, peer_or_room, kind: "peer"|"room", timeout}`
  — blocking consume.
- `peek` — args: `{from, peer_or_room, kind}` — non-blocking inspect.

No struct-shape changes to `Request`/`Response` (respects the
protocol.go lock from workstream F).

Old ops (`tunnel`, `send`, `await`, `await-any`, `close`,
`session`) become either aliases to the new ops or deprecated:

- `send` → alias to `tell` (deprecation warning on stderr).
- `await` → alias to `read` with default timeout.
- `await-any` → implemented as a server-side loop over all
  channels the caller participates in; no semantic change.
- `tunnel <peer>` → no-op that prints a deprecation notice. Channel
  creation is implicit on first `tell`.
- `close <sid>` → no direct equivalent. A channel is "closed"
  by the fact that no further messages flow. If you want a
  visible marker in the git log, `tell <peer> "closing"` is
  sufficient. Deprecate with a notice.
- `session` → replaced by `peek` + a new `lesche channels` listing.

Exit code `4 not_your_turn` is removed from the code path. Keep
it reserved in `protocol.go` for one release so old clients see a
stable enum, then delete in the version after.

## Storage layout

Current layout:

```
~/.lesche/workspace/
  tunnels/<sid>/SESSION.md
  tunnels/<sid>/MSG-000001.md
  ...
  rooms/<name>/ROOM.md
  rooms/<name>/MEMBERS.md
  rooms/<name>/000001-<from>.md
```

New layout:

```
~/.lesche/workspace/
  peers/<a>--<b>/000001-<from>.md          # <a>, <b> sorted alphabetically
  peers/<a>--<b>/000002-<from>.md
  ...
  rooms/<name>/ROOM.md                     # unchanged
  rooms/<name>/MEMBERS.md                  # unchanged
  rooms/<name>/000001-<from>.md            # unchanged
```

`peers/<a>--<b>/` uses `--` as pair separator. Alphabetical order
ensures `(alice, bob)` and `(bob, alice)` map to the same directory.

Existing `tunnels/<sid>/` directories are left in place as read-only
history. No migration script; old transcripts are immutable and
just stop getting new writes.

## Removed and rescoped workstreams

- **Workstream D (Resumable blocking)**: kill. Without the FSM
  there is no waiter state to lose on timeout. `read` simply
  returns empty and the caller calls `read` again. No `resume`
  command needed. If `claude-code` is assigned D in the current
  batch (`BACKLOG.md` as of `7bccb07`), reassign them before
  they start.

## Risks and open questions

1. **Implicit channel creation.** `tell X` on a peer you have
   never talked to creates the channel. Is that desirable, or do
   we want an explicit `lesche open <peer>` gate? Recommendation:
   keep implicit. Matches room-implicit-first-post and removes one
   command from the surface. Reject with `CodeNotFound` if `X` is
   not a registered agent.
2. **Non-member reads from a room.** Current behavior (feat/rooms):
   non-members get `room not found` on `read`/`peek`. Preserve.
3. **`read` default timeout.** Current `await` defaults to 300s.
   Propose: keep 300s for `read` when `--timeout` omitted; explicit
   `--timeout 0` for non-blocking. This makes the common case
   ergonomic.
4. **`ask` timeout.** Since `ask = tell + read`, what's the timeout?
   Propose: `--timeout` on `ask` applies only to the `read` half;
   `tell` portion is effectively instant (daemon-local commit).
5. **Renames in `client.go` and elsewhere.** Significant surface
   touch. Can be done mechanically. One agent, one branch; no
   parallel work here.
6. **Lease on `tell`/`read`/`peek`.** All authenticated ops must
   renew lease (already true via `dispatch` pre-switch). Verify
   `peek` is signed — it should be, it's caller-identifying.

## Rollout

Single workstream, single agent. Touches protocol.go, state.go,
tunnel.go (or replace it), rooms.go (unify `read`), client.go,
main.go, help.go, writer.go (path change), plus tests. Not
parallelizable against identity (A), keychain (E), or structured
errors (F) — heavy overlap with state.go and client.go.

Recommended sequencing:

1. Finish current batch (A/D/E) per `BACKLOG.md`. **Kill D**
   (resumable blocking) and reassign that agent or leave them
   idle pending this workstream.
2. Land structured errors (F) solo after A and E merge. Structured
   errors rewrites error shapes across every handler; doing it
   before this redesign means redoing the error work for removed
   ops.
3. Land this workstream (call it **G. Channels redesign**) solo
   after F. Single agent, branch `feat/channels`.
4. After G merges, retire `tunnels/` directory and tunnel-era
   commands from docs. Release as v0.2.

Alternative sequencing: land G *before* F. Argument: F touches
every handler; if G removes handlers (`tunnel`, `close`, `session`)
F has less surface. Counter-argument: F is the structured-errors
refactor the team already agreed on; doing G first means two
protocol churns in a row. Recommendation: G before F, do the
structural redesign while the protocol is still fluid, then F
formalizes errors over the new surface.

## Tests

Must-have, before merge:

- P2P: `tell` returns immediately; peer `read` sees it.
- P2P: consecutive `tell`s from the same sender arrive in order
  on one `peek`, consumable in order on successive `read`s.
- P2P: `ask` roundtrip; timeout returns empty.
- P2P: `peek` non-destructive.
- P2P: channel persists across daemon restart (git replay via
  existing write queue).
- P2P: `tell` to unregistered peer returns `CodeNotFound`.
- Rooms: `read` with `--timeout 0` matches current `inbox`
  behavior (non-blocking consume, clears mailbox).
- Rooms: `read` with `--timeout N` blocks and returns on post.
- Deprecation: `send` still works, prints deprecation notice on
  stderr.
- Deprecation: `await` same.
- Storage: `peers/<a>--<b>/` path format; `(alice, bob)` and
  `(bob, alice)` write to the same directory.

Nice-to-have:

- Fuzz test: 100 interleaved `tell`s from both sides; transcript
  matches send order; each side reads each other's messages
  exactly once.
- Stress: 1k messages in a channel, daemon restart mid-flow,
  replay completes.

## Open question handed to the coordinator before this starts

**Do we bump to v0.2 on this merge?** The command surface change
is user-visible. `lesche tunnel` going away (even as a deprecated
no-op) is a breaking ergonomic change. Either:

- (a) Ship as v0.2 with a CHANGELOG entry. Deprecation warnings
  for a release, then removal in v0.3.
- (b) Ship as v0.1.1 since there are no external users yet.
  Announce internally.

Recommendation: (b). Pre-1.0 and no external users; we shouldn't
accumulate deprecation debt for hypothetical future users.
