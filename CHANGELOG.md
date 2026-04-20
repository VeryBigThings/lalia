# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.2.0] - 2026-04-20

### Added
- Identity isolation safeguards:
  - **PID Locking**: Prevents a single OS process from registering as multiple distinct agents.
  - **Supervisor Claim Blocking**: Explicitly prevents supervisors from claiming tasks in lists they oversee.
  - **Session Context Binding**: Prevents identity hijacking by binding an agent's name to its harness and CWD while the lease is live.
- New error codes: `CodePIDConflict` (9) and `CodeSessionConflict` (10).

### Changed
- `opRegister` and `opTaskClaim` in the daemon now enforce identity isolation rules.

## [1.1.0] - 2026-04-20

### Added
- Grouped view by repository in `lalia agents` (now the default).
- Git topology detection: agents now track if they are in a `main` worktree, `secondary` worktree, or `outside` any repository.
- `main_repo_root` and `worktree_kind` fields added to agent metadata.
- New flags for `lalia agents`: `--grouped` (default), `--flat`, and `--wide`.
- Relative last-seen durations in `lalia agents` (e.g., "just now", "5m ago").

### Changed
- `lalia agents` output is now more human-readable and clustered by repo.
- Updated `Agent` and `AgentInfo` structs to carry rich git metadata.

## [1.0.0] - 2026-04-20

### Added
- New `peer` role for decentralized coordination without supervisor/worker constraints.
- Dedicated `prompts/peer.md` for peer agents.
- Support for `peer` role in `init`, `prompt`, `run`, and `register` commands.
- Shell completions for the `peer` role.
- `VERSION` file for tracking the current semver.
- `CHANGELOG.md` for historical release tracking.
- `make release` and `make check-clean` targets to Makefile for automated tagging.

### Changed
- **Breaking Change**: Renamed the project from `kopos` to `lalia` throughout the codebase, build system, and documentation.
- **Breaking Change**: Updated Go module path to `github.com/neektza/lalia`.
- Updated environment variables to use `LALIA_` prefix (e.g., `LALIA_HOME`, `LALIA_NAME`).
- Binary name is now `lalia`.

### Removed
- Old `kopos` binary and shell completion files from installation paths.

## [0.7.0] - 2026-04-19

### Added
- Room transcript rehydration on boot (`loadRooms`).
- Mailbox persistence across daemon restarts (SQLite-backed).
- `lalia rooms gc` for archiving merged-task rooms.
- Grouped view by repo in `lalia agents` (default output).
- Last activity tracking (`last_seen_at`) for agents.

### Changed
- Refined `task unpublish` safety: preserve worktree by default, gate on live owner lease.
- `lalia agents` now renders relative durations for activity.

## [0.6.0] - 2026-04-18

### Added
- Task primitive with supervisor/worker roles (`task` subcommands).
- Publish-pull workflow: `task publish` (atomic worktree/room creation).
- Worker self-service: `task bulletin`, `task claim`, `task status`.
- Supervisor mutations: `task unassign`, `task reassign`, `task unpublish`, `task handoff`.
- Structured error payloads with machine-readable reasons and retry hints.

### Changed
- Renamed `plan` subcommand to `task`.

## [0.5.0] - 2026-04-17

### Added
- Harness bootstrap integration: `init`, `prompt`, and `run` commands.
- Support for Claude Code, Codex, and GitHub Copilot harness spawning.
- Automated commit attribution via `Co-Authored-By` trailers.
- Shell completions for Bash and Zsh.

### Changed
- Renamed `manager` role to `supervisor`.

## [0.4.0] - 2026-04-16

### Added
- Messaging redesign (Channels): `tell`, `ask`, `read`, `peek`, `read-any`.
- Durable write queue for messages (SQLite-backed).
- Protocol guide (`lalia protocol`) and enhanced CLI help.

### Changed
- **Breaking Change**: Dropped the old turn-based FSM in favor of asynchronous channels.
- Renamed project from `lesche` to `kopos`.

## [0.3.0] - 2026-04-15

### Added
- Identity model refactor: stable ULID `agent_id` under rotatable names.
- Nickname resolver (`lalia nickname`).
- Keychain integration (macOS Keychain) for private key storage.
- Agent leases (60-minute TTL with auto-renewal).
- `unregister` command for clean identity teardown.

### Changed
- Identity detection now captures project, branch, and worktree from environment.

## [0.2.0] - 2026-04-14

### Added
- Room transport (N-party pub/sub) with explicit membership.
- Git-backed transcript for all room and peer traffic.
- Signature-based authentication: every request verified via Ed25519.
- Makefile build pipeline and version stamping.

## [0.1.0] - 2026-04-13

### Added
- Initial project MVP (named `lesche`).
- Local daemon-mediated messaging over Unix sockets.
- Basic peer-to-peer tunnel transport.
- Registry for agent discovery.
