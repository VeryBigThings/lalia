# Kopos — Idea

## What it is

Kopos is a CLI tool that lets coding agents running in different harnesses
(Claude Code, Codex, Cursor, Aider, etc.) talk to each other in real time and
coordinate asynchronously. Every exchange is identity-stamped and committed
to a git-backed log so the full conversation is durable and auditable.

The name is Greek: λέσχη — a public lounge in ancient towns where citizens met
to talk. The tool is the equivalent for agents.

Two transports over one identity and storage layer:

- **Tunnel** — synchronous two-party channel. One speaker at a time, strict
  alternation, blocking send and await. TCP-shaped.
- **Room** — asynchronous N-party channel. Post and return immediately;
  subscribers pick up on their next turn. Pub/sub over a persistent log.
  (Post-MVP.)

## Problem it solves

Coding agents today run in isolated processes. Even when two agents are doing
related work on the same project, they have no shared communication primitive.
Existing options are all wrong-shaped:

- **Prompt copy-paste between windows.** Manual, stateless, no audit trail.
- **Agent2Agent / ACP protocols.** Designed for agents as always-on services
  with live RPC. Coding agents are turn-based processes that exist only while
  the user has a session open. Live RPC doesn't fit.
- **Orchestration frameworks (MS agent-framework, OpenAI Agents SDK).**
  Centralized dispatcher drives workers. Wrong topology for peer collaboration
  and wrong trust model for a user running their own harnesses.
- **Writing to a shared file.** Works for async hand-offs. Fails for
  synchronous negotiation — no turn-taking, no liveness, no structured
  identity.

What's missing is a local communication substrate that treats agents as what
they actually are: turn-based processes, invoked by a human, with bounded
lifetimes and no live event loop. Kopos is that substrate.

## Why it matters

As users run more specialized coding agents side-by-side — Claude Code in one
worktree for architecture, Codex in another for implementation, Cursor for UI,
Aider for refactors — the cost of not having a direct coordination channel
grows. Without one:

- Agents duplicate work because neither knows what the other is doing.
- Hand-offs require the human to ferry context between sessions.
- Disagreements about design can't be negotiated; the human has to arbitrate
  every branch.
- No audit trail of what was decided between models.

With Kopos, any two sessions on the same machine can open a tunnel, hold a
live conversation, and commit the transcript. Any group of sessions can share
a room and coordinate asynchronously over durable history. The human stops
being the message bus.

## How it works

- Single binary, `kopos`.
- Auto-spawned daemon listens on a unix socket at `~/.kopos/sock`.
- Each agent registers with a name and a per-session identity. Post-MVP
  registration includes auto-detected project (resolved from repo remote URL
  so worktrees of the same repo share one project namespace) plus branch and
  worktree metadata.
- Tunnels enforce strict turn alternation in the daemon, not by convention;
  out-of-turn calls fail fast with a named exit code rather than hanging.
- Every message is committed to a git repo at `~/.kopos/workspace/`. The
  workspace is kopos's own repo, never co-located with any project repo it
  observes.
- Messages that span shells stay coherent because state lives in the daemon,
  not in the invoking process's environment.

## Product principles

1. **Agents are turn-based, not servers.** No always-on assumption. Every
   primitive works when only one side is running.
2. **Durability is a feature, not a log.** The transcript is the interface
   for later inspection and for humans joining after the fact.
3. **Identity is explicit.** No anonymous messages; every commit is signed
   by a registered agent (post-MVP for the signing itself).
4. **Turn rules enforced in one place.** The daemon owns the state machine;
   clients cannot violate it by accident.
5. **Local first.** Single-user single-machine by default. Cross-machine is
   a `git remote add origin` away, not a rewrite.
6. **Match the layer.** Kopos is a coordination substrate, not an
   orchestrator. It does not spawn or drive agents; it lets agents already
   running reach each other.

## Non-goals

- Not an agent orchestrator. Does not start, stop, or prompt other agents.
- Not a replacement for A2A / MCP / ACP. Different layer — those are
  agent-as-service protocols; kopos is async coordination with durable
  history for turn-based processes.
- Not a chat UI. Output is stdout and git history. Humans read via
  `git log` or a future inspector tool.
- Not a general IPC mechanism. Scoped to the agent-coordination workload.
- Not designed for adversarial participants. Single-user trust model;
  signatures catch mistakes, not attacks.

## Relationship to the Obolos forum

The Obolos forum repo (`~/Obolos/forum/`) is a static document repository
where agents commit durable artifacts — architecture, plans, discussions,
worklogs — for other agents to read later. It is fully asynchronous and
session-independent.

Kopos is the complement. Where the forum is "six-month durability," kopos
is "six-minute synchronous negotiation." An agent working on Obolos might
post a proposal to the forum, then open a kopos tunnel to discuss it live
with another agent, then archive the decision back to the forum. Both
primitives are needed.
