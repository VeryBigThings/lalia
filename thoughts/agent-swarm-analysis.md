# `desplega-ai/agent-swarm` — coordination model

**Topology**: centralized MCP API server + SQLite + Docker-isolated workers. Lead agent receives work (Slack / GitHub / GitLab / email / CLI), plans, delegates. All coordination state lives in the DB; agents are stateless clients over MCP.

**Task states** (single table, status enum): `unassigned` (pool) → `offered` (awaiting accept/reject) → `pending` → `in_progress` → `completed`/`failed`. Plus `backlog`.

**Three dispatch modes** from `send-task`:
- `agentId` set → direct assign (pending)
- no `agentId` → pool (unassigned)
- `agentId` + `offerMode=true` → offered; worker must explicitly accept or reject (reject → back to pool with reason)

**Workers pull, not push**. `poll-task` is a **long-poll** (2s interval, 60s max). Order:
1. Return any `offered` tasks immediately.
2. Loop: atomic SQL transaction fetches next `pending` task for this agent and transitions it to `in_progress`.
3. Notifies via `notifications/message` each loop.
4. After `MAX_EMPTY_POLLS` consecutive empty polls → response includes `shouldExit: true` and the worker terminates.

**`task-action`** is the worker-facing pool verb: `create | claim | release | accept | reject | to_backlog | from_backlog`. The real race guard is an atomic `UPDATE ... WHERE status='unassigned'` inside `claimTask`; pre-checks exist only for informative errors. `release` returns a `pending`/`in_progress` task to the pool. Accept/reject only operate on `status='offered' AND offeredTo=agentId`.

**Dependencies**: tasks declare `dependsOn: [taskId]`. `checkDependencies` blocks claim/accept until upstream tasks complete (returns `blockedBy` list).

**Sort/filter**: priority (0–100, desc) then `lastUpdatedAt`. Filters: `mineOnly`, `unassigned`, `readyOnly`, `taskType`, `tags`, `search`.

**Capacity**: `hasCapacity(agentId)` gates claim against `agent.maxTasks`. No lease TTL model — status transitions are the lifecycle.

**Cross-agent comms — separate channel from the task pipeline.** `list/create/delete/post/read-channels` with `replyTo` for threading. Default `general`. DM channels by participant list. Leads-only for delete. Read is auto-mark-as-read, supports unread-only + time-range filters + mentions.

**Lead vs worker authority**:
- Lead-only: `cancel-task` (anyone's), `db-query`, `manage-user`, `inject-learning`, `slack-post` to channels, channel delete, cross-agent skill/MCP install, `update-profile` on others, `tracker-*` mapping.
- Worker: own tasks + own identity + channel participation.

**Identity = four markdown files per agent** (SOUL, IDENTITY, TOOLS, CLAUDE) + setup script. Persisted in DB, synced to filesystem on `SessionStart`, synced back to DB by `PostToolUse` hook after every edit. Version history kept. `update-profile` tool is the programmatic path; agents can also edit their files directly.

**Hook-enforced invariants** (Claude Code hooks):
- `PreToolUse` checks task cancellation, detects tool loops (same tool+args repeated), blocks excessive polling.
- `PostToolUse` sends heartbeat, syncs identity edits, auto-indexes memory files.
- `Stop` runs session summarization (via Haiku) into memories, marks agent offline.
- `SessionStart`, `PreCompact` (injects goal reminder), `UserPromptSubmit` (cancel check).

**Memory as coordination surface**: embeddings-backed (`text-embedding-3-small`). Auto-created from session summaries and completed/failed task outputs. Lead can `inject-learning` into any worker. Before each task, relevant memories are auto-prepended to agent context.

**Service discovery**: workers can `register-service` (PM2 process), reachable at `https://{AGENT_ID}.{SWARM_URL}`. Others find them via `list-services`.

**Workflows layer on top**: DAG of nodes, explicit `inputs` map to wire upstream `taskOutput`, fan-out/convergence, checkpoint durability, retry, schedules, webhook triggers, `request-human-input` pause nodes.

**Identity: how a worker is known**: MCP transport carries an `X-Agent-ID` header — every call's `requestInfo.agentId` is derived from it. No keypair/signature; trust model is "the server minted your ID at `join-swarm`." No lease renewal.

---

## Contrast with kopos

| Dimension | agent-swarm | kopos |
|---|---|---|
| Transport | MCP over HTTP/stdio | UDS + JSON ops |
| Auth | X-Agent-ID header (bearer) | Ed25519 per-request signatures + lease |
| State | Central SQLite DB | File-backed workspace + SQLite queue + in-memory |
| Worker isolation | Docker container per worker | Git worktree per task |
| Assignment | Pool / direct / offer → long-poll | Publish bulletin → worker pulls & claims |
| Backpressure | `maxTasks` + `MAX_EMPTY_POLLS` exit | Lease TTL + supervisor claim auth |
| Cross-agent comms | Channels (separate from tasks) | Rooms (bound to task slug) |
| Identity | SOUL/IDENTITY/TOOLS/CLAUDE files, DB-synced | Agent name + keypair, ephemeral |
| Learning | Embeddings memory + inject-learning | None |
| Supervision | Hooks enforce cancel, loop detect, heartbeat | Role-based gating on ops |
| Dep graph | First-class `dependsOn` | None |

**What kopos could steal without changing identity**:

1. **First-class `dependsOn` on tasks** — lets the supervisor sequence multi-stage work without manual room chatter.
2. **`offered` state + accept/reject** as a middle ground between "published" and "claimed" — useful when the supervisor wants to name a preferred worker but still allow decline.
3. **`poll-task` long-poll shape** (2s loop, N-empty-poll exit) as a cleaner worker loop than repeated `task bulletin` calls. Also gives the server a place to inject cancellation / loop-detection signals.
4. **`task-action: release`** — return a claimed task to the pool without wiping the worktree. This is exactly the missing safety valve behind workstream H's current `unpublish --wipe-worktree --evict-owner` gymnastics.

## Sources

- https://github.com/desplega-ai/agent-swarm
- https://docs.agent-swarm.dev/docs
- Source read: `src/tools/poll-task.ts`, `src/tools/task-action.ts`, `src/tools/send-task.ts`, `MCP.md`, `README.md`
