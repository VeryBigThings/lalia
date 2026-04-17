# Workstream I — `lesche init` / `lesche prompt` / `lesche run`

**Status**: unclaimed.

## How to pick this up

1. Register: `lesche register` (installed binary, no env overrides).
2. `lesche join feat-init-run` + `lesche history feat-init-run --room`
   to see if anyone else is already on it.
3. If nobody is, post `starting feat-init-run as <your-name>` in the
   room and begin. That's the whole claim protocol until workstream
   H automates it.

## Identity and coordination

- **Branch**: `feat/init-run`.
- **Worktree**: `~/Obolos/lesche-init-run` (this directory).
- **Coordination room**: `feat-init-run`.
- **Supervisor**: `supervisor`. Report checkpoints via
  `lesche post feat-init-run "..."`. DMs (`lesche tell supervisor`)
  only for private issues.

## Goal

Onboard worker/supervisor agents into a lesche-coordinated session
with one command. Three-level surface, same prompt content:

1. `lesche init <role>` → stdout.
2. `lesche prompt <role>` → writes `./LESCHE.md`.
3. `lesche run <role> --<harness> [...args]` → writes harness-specific
   file and execs the harness.

## Surface

```
lesche init worker
lesche init supervisor
lesche prompt worker [--force]
lesche prompt supervisor [--force]
lesche run worker     --claude-code [args...]
lesche run worker     --codex       [args...]
lesche run worker     --copilot     [--force] [args...]
lesche run supervisor --claude-code [args...]
lesche run supervisor --codex       [args...]
lesche run supervisor --copilot     [--force] [args...]
```

`--force` overrides the safety check that refuses to clobber an
existing instructions file without the lesche-written marker on the
first line.

## Harness mapping (verified locally; do not re-research)

| Harness flag    | Mechanism                                                                                          |
|-----------------|----------------------------------------------------------------------------------------------------|
| `--claude-code` | write `./LESCHE.md`; exec `claude --append-system-prompt-file LESCHE.md "$@"`                      |
| `--codex`       | write `./LESCHE.md`; exec `codex -c experimental_instructions_file='"'$PWD/LESCHE.md'"' "$@"`      |
| `--copilot`     | write (or append) `.github/copilot-instructions.md`; exec `copilot "$@"`                           |

Codex's `experimental_instructions_file` config key is experimental and
may rename; fall back to writing `AGENTS.md` to cwd if the key is
missing.

Copilot has no launch flag. Writing the instructions file is the only
option. Default: append with a `<!-- lesche-begin -->` marker so a
second run is idempotent. Refuse to clobber an unmarked existing file
without `--force`.

## Files to touch

- **New**: `prompts/worker.md`, `prompts/supervisor.md` (embedded
  prompts; edit as markdown, print verbatim via `//go:embed`).
- **New**: `run.go` (exec-wrapper, harness mapping, file-write logic
  with marker detection).
- **Modify**: `client.go` (cmdInit, cmdPrompt, cmdRun), `main.go`
  (dispatch), `help.go` (document all three commands — keep existing
  protocol help intact; add a short new section).

## Prompt content (5-part skeleton, both worker and supervisor)

1. Role posture (one paragraph: "You are a worker/supervisor agent on
   lesche.").
2. Three questions for the human up front:
   - Your name (worker: `copilot`/`codex`/`claude-code`; supervisor:
     default `supervisor`).
   - Your workstream slug (worker) / project scope (supervisor).
   - Anything I should know before starting.
3. Bootstrap commands in order: register, join the slug's room
   (worker) or create it (supervisor), peek/read-any for pending
   messages, read `lesche protocol` + `BACKLOG.md` + the
   `WORKER_TASK.md` in cwd (worker).
4. Ongoing rules: rooms-first for workstream coordination, tell/ask
   only for private 1:1; report at checkpoints (start / blocker /
   ready); never run `./bin/lesche`; never set `LESCHE_HOME` /
   `LESCHE_WORKSPACE`.
5. Exit protocol: `lesche unregister` on permanent shutdown.

## Tests (required before "ready for review")

- `lesche init worker` stdout matches `prompts/worker.md`
  byte-for-byte.
- `lesche prompt worker` writes `./LESCHE.md`; refuses to overwrite
  an existing unmarked file without `--force`.
- `lesche run worker --claude-code` with a stubbed `claude` on PATH
  writes `./LESCHE.md` and execs with
  `--append-system-prompt-file LESCHE.md`.
- `lesche run worker --codex` likewise with the config override.
- `lesche run worker --copilot` refuses to clobber an unmarked
  existing `.github/copilot-instructions.md` without `--force`.
- Cold paths: all three commands succeed with no running daemon and
  before `lesche register` is called.

## Blockers / notes

- No daemon surface. Pure client-side. Zero file-overlap with
  workstreams H (`feat/plan`) and J (`feat/mailbox-persist`); all
  three run in parallel.
- Harness stubs for tests: use a shim script on a fake PATH that
  prints its argv and exits 0.

## Reporting checkpoints (all in `feat-init-run` room)

- Start of work.
- Any open question (use `ask` on a DM to supervisor, or post to
  the room).
- Ready for review: `ready for review: branch=feat/init-run
  sha=<sha> make test: <summary>`.
