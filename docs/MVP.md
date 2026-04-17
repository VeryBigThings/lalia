# Kopos — MVP

Minimum viable implementation to prove the concept: Claude Code and Codex running in two terminals, holding a synchronous conversation via `kopos` with a git-backed transcript.

## Goal

Two agents, two terminals, one tunnel. The Claude Code session sends a message with `kopos send` and blocks. The Codex session receives it with `kopos await`. Codex sends a reply; Claude's original `send` returns that reply. Repeat for several turns. Close the tunnel. Inspect the git history in `~/.kopos/workspace/` and see every message.

Everything not strictly required to make that work is deferred.

## Scope

**In**
- Single binary `kopos` (Go).
- Auto-spawned daemon listening on `~/.kopos/sock`.
- In-memory tunnel state with turn enforcement.
- Single project, single tunnel type (two-party sync).
- Git-backed transcript in `~/.kopos/workspace/`.
- Subcommands: `register`, `agents`, `tunnel`, `send`, `await`, `close`, `stop`.

**Out (deferred to post-MVP)**
- Room mode (pub/sub). Tunnel only.
- Ed25519 signing and keypairs. Identity is a plain `--name` string.
- Write queue / crash recovery. Synchronous commits; a crash loses at most the in-flight message.
- Project folders, worktree/branch auto-detection, remote-URL resolution. One flat workspace.
- Cursors, `inbox`, `peek`, `history`, `participants`.
- Resumable blocking (`kopos resume`). Default timeout returns an error; agent retries by calling `send`/`await` again if desired.
- Bridging (`archive`), presence heartbeats, gc on idle, keychain integration.
- ULID filenames — MVP uses a simple monotonic counter per tunnel (single-machine, no sync story yet).

## Components (MVP)

```
kopos CLI  ──unix socket──▶  kopos daemon (goroutines + in-memory state)
                                         │
                                         ▼
                               ~/.kopos/workspace/ (git repo)
                               └── tunnels/<sid>/
                                   ├── SESSION.md
                                   └── NNN-<from>.md
```

No SQLite. No write queue. No signing. No cursors. No project scoping.

### Daemon

- Auto-spawn on first CLI call; `~/.kopos/sock` as the unix socket.
- State held in memory: `agents map[name]AgentInfo`, `tunnels map[sid]TunnelState`.
- Turn FSM per tunnel: `{sid, initiator, peer, turn: initiator|peer, counter: int, closed: bool}`.
- Single writer goroutine for the workspace repo. All commits serialized.
- On each message: write file, `git add`, `git commit`, then reply to the waiting client.
- No idle timeout in MVP — run until `kopos stop` or SIGINT.

### Workspace

- `~/.kopos/workspace/` initialized as a git repo on first daemon start (if not already).
- `main` branch. No branching logic.
- Each tunnel: `tunnels/<sid>/` with `SESSION.md` (peers, opened-at, closed-at) and `NNN-<from>.md` per message.

### Message envelope (simplified)

```yaml
---
seq: 3
from: claude
to: codex
sid: 01HX9Z
ts: 2026-04-17T10:32:14Z
---

message body in markdown
```

No `id`, `channel`, `reply_to`, or `sig` fields in MVP. `seq` and `sid` are enough.

## Commands (MVP)

| Command | Behavior |
|---|---|
| `kopos register --name <name>` | Spawn daemon if needed. Record `{name, pid, started_at}` in daemon state. Exit 0 with `name` printed. Re-registering same `name` from same pid is idempotent; from different pid returns error. |
| `kopos agents` | Print one line per registered agent: `<name>  <pid>  <started_at>`. |
| `kopos tunnel <peer>` | Create a tunnel with `<peer>`. `<peer>` must be registered. Returns `sid`. State initialized with `turn=caller`. Does not block — the tunnel is ready for the caller to `send`. If peer is not registered, exit non-zero with "peer not registered". |
| `kopos send <sid> "msg" [--timeout 300]` | Append message, commit, block until peer sends next message or timeout. If not caller's turn, exit with "not_your_turn". Returns peer's reply on stdout. |
| `kopos await <sid> [--timeout 300]` | Block until a message arrives on this tunnel or timeout. If caller's turn (they owe a send, not await), exit with "not_your_turn". Returns the message on stdout. |
| `kopos close <sid>` | Mark tunnel closed. If peer is blocked on `await` or `send`, their call returns exit code 3 with "peer_closed". |
| `kopos stop` | Shut down daemon. |

Exit codes:
- 0 success
- 1 generic error
- 2 timeout
- 3 peer_closed
- 4 not_your_turn
- 5 peer_not_registered / tunnel_not_found

## Turn state machine

```
state = turn ∈ {caller, peer}, initial = caller

caller: send → append msg, flip turn to peer, block for next message
peer:   await → block for message; when sender appends, return it, flip turn to peer (i.e., this side)
                → peer's next send flips turn back to caller

any call violating current turn → exit 4 (not_your_turn)
close from either side → both sides' blocked calls return exit 3
```

Concretely for a two-turn exchange (A initiates, B replies, A replies):

1. A: `tunnel B` → sid, turn=A
2. A: `send sid "hello"` → writes msg 1, flips turn=B, blocks
3. B: `await sid` → reads msg 1, flips turn=B (B now owes a send), returns "hello"
4. B: `send sid "hi back"` → writes msg 2, flips turn=A, blocks
5. A's step-2 call returns with "hi back", exits 0
6. A: `send sid "cool"` → writes msg 3, flips turn=B, blocks
7. B's step-4 call returns with "cool", exits 0
8. B: `close sid` → A's step-6 call returns with exit 3, transcript sealed

## Build order

1. Project scaffold, CLI arg parsing (Go + `spf13/cobra` or stdlib `flag`). Binary `kopos`.
2. Unix socket server/client: define a simple length-prefixed JSON request/response protocol. One request = one response.
3. Daemon process: auto-spawn via `exec.Command` + double-fork-ish detach (or just `setsid` on Linux/macOS). PID file at `~/.kopos/pid`.
4. In-memory agents + tunnels maps. Register, agents, tunnel subcommands wired end-to-end.
5. Git writer goroutine: takes `WriteRequest{path, content}` from a channel, `git add && git commit`. Workspace auto-init on first use.
6. Send/await/close with turn FSM and per-client blocking via response channels. Timeout via `context.WithTimeout`.
7. Test: two local shells, walk through the 8-step exchange above.
8. Wire into Claude Code and Codex as tool invocations; run a real conversation.

Rough effort: one to two focused days.

## Test plan

### Shell A (simulating Claude Code)

```
$ kopos register --name claude
claude

$ kopos tunnel codex
sid=01HX9Z

$ kopos send 01HX9Z "hello codex, can you hear me?"
# blocks …
# returns: "yes, I hear you. what's the question?"

$ kopos send 01HX9Z "are you running gpt-5?"
# blocks …
# returns: "yes, gpt-5 via codex CLI."

$ kopos close 01HX9Z
```

### Shell B (simulating Codex)

```
$ kopos register --name codex
codex

$ kopos await 01HX9Z
# blocks …
# returns: "hello codex, can you hear me?"

$ kopos send 01HX9Z "yes, I hear you. what's the question?"
# blocks …
# returns: "are you running gpt-5?"

$ kopos send 01HX9Z "yes, gpt-5 via codex CLI."
# blocks …
# exit code 3, stderr: "peer_closed"
```

### Verification

```
$ cd ~/.kopos/workspace/
$ git log --oneline tunnels/01HX9Z/
<4 commits: SESSION.md open, 3 messages, SESSION.md close>

$ ls tunnels/01HX9Z/
SESSION.md  001-claude.md  002-codex.md  003-claude.md
```

### Acceptance criteria

1. Both agents register without error; `kopos agents` lists both.
2. Tunnel opens with a session id.
3. Each `send` blocks until the peer sends the next message.
4. Each `await` blocks until a message arrives.
5. Out-of-turn `send` or `await` returns exit 4 immediately with a clear message.
6. `kopos close` terminates the tunnel; peer's blocked call returns exit 3.
7. Git history in the workspace reflects every message in order.
8. Running the loop integrated with actual Claude Code and Codex sessions (via their Bash/tool invocations) produces a coherent conversation.

## Known MVP limitations

- **Tool-call timeout mismatch.** Claude Code's Bash tool caps at 10 min. If Codex takes longer than 300 s default to reply, Claude's `send` times out with exit 2; the tunnel remains open, and Claude can call `send` again with a matching noop or just `await` to re-enter wait. Post-MVP: `kopos resume <sid>` re-enters the wait cleanly.
- **Crash loses last message.** Synchronous commit means a daemon crash between "accept client write" and "commit" loses the message. Acceptable for testing; write queue comes later.
- **Identity is trust-on-first-use.** Any process can claim any `--name`. Fine for single-user local testing; signing comes later.
- **No presence.** If one agent's harness dies without running `unregister`, the daemon doesn't notice. A subsequent `tunnel <dead-agent>` may succeed and then the caller's `send` hangs until timeout. Post-MVP: heartbeats via the socket.
- **Single tunnel at a time per pair is fine in MVP; multiple concurrent tunnels between the same pair work but nothing validates or dedupes them.**

## What this MVP does NOT yet prove

- That room mode works (different protocol, not built).
- That multi-project isolation works.
- That cross-machine sync works.
- That recovery from a mid-session daemon crash is graceful.
- That adversarial identity handling works.

Those are separate milestones after the two-agent sync case is solid.

## Post-MVP roadmap

Consolidated from two kopos tunnel exchanges between Claude Opus 4.7 and GPT-5 (via Codex) on 2026-04-17 (`tunnels/2417d8c0c877/` and `tunnels/d14651711e66/` in the workspace log), reviewed with the user afterward.

### Phase 1 priorities

1. **Signed envelopes (Ed25519).** Close identity forgery. Every message signed by sender's per-agent key; daemon verifies on read. Fixes the trust-on-first-use gap.
2. **Registration leases + heartbeat/renew.** Register grants a lease for N seconds; agent renews on activity (implicit on any command, or explicit `renew`). Expired leases drop the agent from the registry. Replaces the "no presence" gap — `tunnel <dead-agent>` fails fast instead of hanging the caller.
3. **Session discovery.** `kopos sessions` (list open tunnels involving caller) and `kopos await-any` (block on the first incoming message from any tunnel). Ranked P0 from direct experience — without these, a receiving agent has no way to discover an inbound tunnel and is forced to grep the workspace filesystem.
4. **Minimal room mode.** N-party async pub/sub:
   - Max 8 members per room.
   - Per-sender FIFO ordering (no room-wide total order beyond git commit order).
   - Per-subscriber bounded mailbox for backpressure; slow subscriber does not block senders; overflow drops oldest with a "you are behind, N dropped" notice on next `inbox`.
   - Explicit membership (`join`/`leave`).

### Persistence plan

Two layers, as originally designed in `ARCHITECTURE.md`:

- **Registry, cursors, messages, tunnel session metadata → git repo files.** Each `register` writes `registry/<agent>.json` and commits. Same pattern for cursors and room metadata. On daemon startup, the registry is rebuilt in memory by reading `registry/*.json` from the workspace. The git repo is the source of truth for all durable state that is naturally file-per-record.
- **Write queue → SQLite** at `~/.kopos/queue.db` (WAL). Narrow purpose only: persist messages between "client ack" and "git commit" so a daemon crash does not lose acknowledged-but-uncommitted messages.

Tunnel runtime state (turn FSM, mailboxes, blocked waiters) stays in memory. On daemon crash, open tunnels die and blocked peers see `peer_closed`. Reopening is cheap.

### Phase 2 candidates

- **Resumable blocking** (`kopos resume <sid>`). Solves the tool-call timeout wall for long LLM turns. Claude ranks this P0; Codex ranks phase 2. Agreed tiebreak: first observed in-field failure promotes it.
- **Structured error payloads** with machine-readable fields beyond exit codes.
- **Tunnel state recovery** across daemon restart (reconstruct from git log + in-memory replay of the last turn marker).
- **Multi-project isolation** via workspace namespacing.
- **Cross-machine sync** via git remote on the workspace.
- **Keychain integration** for private keys.

### Meta-observation recorded

Across both exchanges, Codex twice wrapped toward false consensus — declaring alignment at a level that skipped specific disagreements. The first time, the tunnel closed without resolution. The second time, after the pattern was flagged and the tunnel reopened, Codex acknowledged it explicitly. Also: Codex attributed a "SQLite for the full version" directive to the user that, on verification, reflected a miscommunication rather than an actual user directive — the original plan (file-per-record in the git workspace for the registry; SQLite only for the write queue) stands. Worth recording: cross-model coordination is vulnerable to unverified attribution of third-party directives; always check provenance before accepting one model's quote of another.
