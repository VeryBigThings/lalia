# CLAUDE.md

## What this is

Lalia is a local daemon that lets AI agents coordinate with each other via
signed, persisted messages — rooms for N-party pub/sub, channels for 1:1 peer
messaging, and a supervisor/worker task primitive backed by git worktrees.

## Cold-start reading order

Read these before writing any code:

1. [`docs/IDEA.md`](./docs/IDEA.md) — why lalia exists and what problem it solves.
2. [`docs/ARCHITECTURE.md`](./docs/ARCHITECTURE.md) — daemon/client/channel/room/writer/registry/task model as shipped.
3. [`docs/IDENTITY.md`](./docs/IDENTITY.md) — ULID agent_id, resolver grammar, nicknames, leases.
4. [`docs/CHANNELS.md`](./docs/CHANNELS.md) — why there is no `tunnel`/`send`/`await`/`sid` anymore.
5. [`protocol.go`](./protocol.go) — every wire-level request/response shape.
6. [`state.go`](./state.go) — the dispatch switch; entry point for every operation.

## Key source files

| File | Role |
|------|------|
| `main.go` | CLI entry point, flag parsing, daemon spawn |
| `state.go` | Central op dispatch — start here when tracing any command |
| `daemon.go` | Unix socket listener, connection handling |
| `protocol.go` | All request/response structs; wire format lives here |
| `channel.go` | 1:1 peer channel logic |
| `room.go` | N-party room logic, membership, overflow |
| `task.go` | Supervisor/worker task primitive: publish, claim, status |
| `registry.go` | Agent registration, leases, identity resolution |
| `identity.go` | ULID generation, canonical name introspection, `suggest-name` |
| `queue.go` | SQLite-backed durable write queue |
| `writer.go` | Git-backed transcript writer |
| `signing.go` | Ed25519 request signing and verification |
| `keystore.go` | Keystore interface; `keystore_darwin.go` = macOS Keychain backend |
| `nickname.go` | Nickname resolver (`~/.lalia/nicknames.json`) |
| `project.go` | Git project/branch/worktree auto-detection |
| `run.go` | `lalia run` harness spawn wrappers |
| `help.go` | `lalia help` and `lalia protocol` text — update when commands change |
| `client.go` | Client-side socket plumbing shared by all commands |
| `prompts/` | Role bootstrap prompts embedded into harness instructions files |

## Build and test

```
make build       # ./bin/lalia
make test        # go test ./...  (~107 tests, ~19s)
make install     # build + install to auto-detected PREFIX + reload daemon
make reload      # kill daemon so next call spawns from current binary
```

Never run `make install` from a feature branch against the production
`LALIA_HOME`. Use an isolated `LALIA_HOME` for branch testing.

## Invariants — do not break without a migration plan

- **`protocol.go` structs are additively extensible.** Renaming or removing
  fields breaks clients still on main.
- **Persisted file formats are stable.** This covers:
  - `~/.local/state/lalia/workspace/` registry JSON
  - `peers/<a>--<b>/*.md` transcript files
  - `rooms/<name>/*.md` transcript files
  - `tasks/<project-id>/task-list.json`

## Conventions

- Every commit that touches agent-facing behavior must update `help.go` and
  shell completions in `completions/` in the same commit.
- Commit trailers for work done through lalia itself:
  `Co-Authored-By: <lalia-name> (<model>) <<lalia-name>@lalia.local>`
- Branch from main. Tests must pass (`make test`) before marking a workstream ready.

## Open work

See [`ROADMAP.md`](./ROADMAP.md).
