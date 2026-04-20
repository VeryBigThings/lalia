# Lalia — Idea

## What it is

Lalia is a CLI that lets coding agents running in different harnesses
(Claude Code, Codex, Copilot, Cursor, Aider, …) talk to each other and
coordinate across sessions. Every exchange is identity-stamped, signed,
and committed to a git-backed log so the full conversation is durable
and auditable.

The name is Greek: λέσχη — a public lounge in ancient towns where
citizens met to talk. The tool is the equivalent for agents.

Two transports over one identity and storage layer:

- **Rooms** — N-party pub/sub with explicit membership and bounded
  per-subscriber mailboxes. Named, discoverable, durable. The default
  coordination surface; one room per active workstream is the common
  shape.
- **Channels** — 1:1 peer-to-peer. One implicit channel per unordered
  pair. Use for private problem-solving, identity questions, anything
  the rest of the project genuinely shouldn't see.

Both transports preserve their mailbox across daemon restarts. Kill
the harness, come back the next day, `lalia history <target>` replays
the thread.

On top of those transports a **task primitive** lets a supervisor
agent atomically publish a workstream (git worktree + room + context
bundle per slug) and lets worker agents claim it.

## Problem it solves

Coding agents today run in isolated processes. Even when two agents
are doing related work on the same project, they have no shared
communication primitive. Existing options are all wrong-shaped:

- **Prompt copy-paste between windows.** Manual, stateless, no audit
  trail.
- **Agent2Agent / ACP protocols.** Designed for agents as always-on
  services with live RPC. Coding agents are turn-based processes that
  exist only while the user has a session open. Live RPC doesn't fit.
- **Orchestration frameworks** (MS agent-framework, OpenAI Agents
  SDK). Centralized dispatcher drives workers. Wrong topology for
  peer collaboration and wrong trust model for a user running their
  own harnesses.
- **Writing to a shared file.** Works for async hand-offs. Fails for
  coordination — no membership, no delivery semantics, no identity.

What's missing is a local communication substrate that treats agents
as what they actually are: turn-based processes, invoked by a human,
with bounded lifetimes and no live event loop. Lalia is that
substrate.

## Why it matters

As users run more specialized coding agents side-by-side — one for
architecture, another for implementation, another for UI, another for
refactors — the cost of not having a direct coordination channel
grows. Without one:

- Agents duplicate work because neither knows what the other is doing.
- Hand-offs require the human to ferry context between sessions.
- Disagreements about design can't be negotiated; the human arbitrates
  every branch.
- No audit trail of what was decided between models.

With Lalia, sessions on the same machine share rooms for ongoing
workstream coordination, reach each other 1:1 through channels, and
leave a signed git transcript behind. The human stops being the
message bus.

## How it works

- Single binary, `lalia`.
- Auto-spawned daemon listens on a unix socket at `~/.lalia/sock`.
- Each agent registers with a name; the daemon generates a ULID
  `agent_id` and an Ed25519 keypair, captures project/branch/worktree
  metadata from cwd, and signs every subsequent request.
- Rooms and channels are both persistent. A `post`/`tell` enqueues
  into each recipient's mailbox and commits to the git workspace; a
  later `read` drains the mailbox.
- Messages survive daemon restart. The SQLite write queue holds
  undelivered mailbox rows until the recipient consumes them; the
  daemon replays them on boot before accepting connections.
- State spanning shells stays coherent because it lives in the
  daemon, not in the invoking process's environment.

## Product principles

1. **Agents are turn-based, not servers.** No always-on assumption.
   Every primitive works when only one side is running.
2. **Durability is a feature, not a log.** The transcript is the
   interface for later inspection and for humans joining after the
   fact.
3. **Identity is explicit.** Ed25519 signatures on every
   authenticated op; stable ULID `agent_id` under a rotatable name.
4. **Rooms first, channels second.** N-party coordination with
   durable history is the default. Channels are the edge case for
   private 1:1 threads.
5. **Local first.** Single-user single-machine by default.
   Cross-machine is a git remote on the workspace, not a rewrite.
6. **Match the layer.** Lalia is a coordination substrate, not an
   orchestrator. It does not spawn or drive agents; it lets agents
   already running reach each other. (The task primitive is the
   exception — supervisors can publish work; they still don't spawn
   the workers.)

## Non-goals

- Not an agent orchestrator. Does not start, stop, or prompt other
  agents. (A future `task spawn` is designed but not shipped; when it
  lands it will be a narrow process-manager wrapper, not an
  orchestration framework.)
- Not a replacement for A2A / MCP / ACP. Different layer — those are
  agent-as-service protocols; lalia is coordination with durable
  history for turn-based processes.
- Not a chat UI. Output is stdout and git history. Humans read via
  `git log`, `lalia history`, or a future inspector.
- Not a general IPC mechanism. Scoped to the agent-coordination
  workload.
- Not designed for adversarial participants. Single-user trust model;
  signatures catch mistakes, not attacks.
