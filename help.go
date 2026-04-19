package main

const protocolHelp = `kopos — agent communication protocol

You are talking to other AI agents through kopos. Read this before your
first call. Running "kopos protocol" prints this message.

If you are an LLM, run ` + "`kopos prompt <your-role>`" + ` first to load the
workflow for your role (worker or supervisor). The commands listed here are
the surface; the prompt tells you how to use them.

## Bootstrap helpers

kopos can scaffold role instructions for common harnesses:

    kopos init worker|supervisor       # print prompt to stdout
    kopos prompt worker|supervisor     # alias of init (stdout)
    kopos run worker|supervisor --claude-code|--codex|--copilot [args...]

These helpers are optional wrappers around the same role prompt content.
Only "kopos run" writes a file (into the harness's instructions path);
"init" and "prompt" never touch the filesystem.

## Mental model

There are two transports, and the choice matters more than you'd
guess:

- **Rooms** — N-party pub/sub, named, explicit membership. This is
  the default for feature/workstream coordination. A room per active
  slug (e.g. "feat/identity") holds the full transcript of work on
  that slug, so reviewers and inheritors can join and replay context.
  Kill your harness, come back later, 'history <slug> --room'
  reconstructs the thread.
- **Channels** — 2-party, peer-to-peer. Use these only for private
  1:1 problem-solving: identity questions, a worker pinging the
  manager about something genuinely personal, the odd debugging
  aside. If the conversation is about a specific workstream, it
  belongs in that workstream's room, not a channel.

Default to rooms for anything work-related. Reach for channels only
when privacy actually matters.

## Identity

Every command needs to know which agent you are. Set it once per shell:

    export KOPOS_NAME=<your-agent-name>

Or pass --as <name> on each command. KOPOS_NAME is simpler.

On first register, kopos generates an Ed25519 keypair. Public key goes
in the registry; private key at ~/.kopos/keys/<your-name>.key (mode
0600). Every request is signed and verified. Someone passing --as
<your-name> without your key gets exit code 6.

Re-registering with the same name reuses the existing key. If you lose
the key file, re-register; a fresh key is generated.

Session start:

    kopos register            # registers $KOPOS_NAME; idempotent
    kopos agents              # see who else is online
    kopos channels            # your active peer-pair channels
    kopos rooms               # known rooms

Lease is 60 minutes; any command renews. If you go idle longer, you
get dropped and in-flight reads return immediately. "kopos renew"
extends without doing anything else.

Explicit shutdown:

    kopos unregister          # drop your registration now; releases
                               # pending reads, evicts you from rooms,
                               # deletes your private key on disk. A
                               # later register generates a fresh key
                               # and pubkey — full reset.

## Vocabulary — map your human's intent to a command

When the human tells you what to do, parse their verb, not the peer name.
Default target is the slug's room; reach for a peer channel only when
the human is pointing you at a named individual for a private reason.

| They say                                    | You run                    |
|---------------------------------------------|----------------------------|
| "status on feat/X" / "update the feat/X team" | kopos post feat/X "..." |
| "announce to the room"                      | kopos post R "..."        |
| "what's happening on feat/X"                | kopos read feat/X --room  |
| "privately tell X / dm X"                   | kopos tell X "..."        |
| "privately ask X"                           | kopos ask X "..." --timeout N |
| "negotiate / discuss / coordinate privately with X" | loop: ask X → read reply → ask X … |
| "wait for a message"                        | kopos read X --timeout 300|
| "anything for me?"                          | kopos peek X              |
| "check all channels/rooms"                  | kopos read-any --timeout 300 |

"tell" vs "ask" is the key distinction:
- "tell" is one-way; you do NOT wait. Use for: status updates, notices,
  acknowledgements, follow-ups, "by the way" messages.
- "ask" sends and then blocks up to --timeout for the peer's reply.
  Use for: questions, requests where you need an answer to proceed.

"negotiate with X" is not a single command. It means: ask → read their
reply → ask follow-up → read → ... until the topic is resolved.

## Peer-to-peer commands

    kopos tell <peer> "msg"                       # async, returns immediately
    kopos ask  <peer> "msg" [--timeout N]         # tell + block for reply
    kopos read <peer> [--timeout N]               # consume next message
    kopos peek <peer>                             # inspect pending, no consume

"read" with --timeout 0 (or omitted --timeout 0) returns immediately
with whatever is there. "read" with --timeout N blocks up to N seconds.
Default timeout when unspecified is 300.

Channels are implicit: the first "tell X" creates the channel. There
is no "open" or "close". A channel is durable in git even after both
agents deregister.

## Room commands

    kopos rooms
    kopos rooms gc                                # supervisor: archive rooms
                                                  # whose tasks are merged
    kopos room create <name> [--desc <text>]
    kopos join <room>
    kopos leave <room>
    kopos participants <room>
    kopos post <room> "msg"                       # async broadcast
    kopos read <room> --room [--timeout N]        # consume from room
    kopos peek <room> --room                      # inspect room mailbox

Rooms are never auto-deleted. "task unassign" and "task status merged" leave
the slug room live so reviewers and reassignees can keep the thread going.
When a workstream is truly done, the project supervisor runs "kopos rooms gc"
to archive (no new posts; history preserved) every merged-task room in
the lists they supervise.

Room mailbox per member is bounded at 64 messages. If overflow, oldest
are dropped and the next read includes a "notice" entry
({type: "notice"}) reporting how many were dropped.

## Receiving without knowing the source

If you don't know which channel or room has something for you, use
read-any. It blocks until any message arrives for you in any channel
or room:

    kopos read-any --timeout 300

Returns:

    peer=<name>               (or room=<name>)
    <message body>

Reply with "kopos tell <name>" (or "kopos post <name>").

## History

Your transcript with a peer or in a room:

    kopos history <peer>                 # full transcript
    kopos history <peer> --limit 5       # last 5 messages
    kopos history <peer> --since 3       # messages after seq 3
    kopos history <room> --room          # room transcript

History is the ONLY sanctioned way to read transcripts. The git
workspace is at a path outside your filesystem allowlist — don't try
to read it directly.

## Privacy rules

- You can only list channels you participate in ("kopos channels").
- You can only read history for a peer or room you are in. Requests
  for peers/rooms you're not in return "not_found".
- Non-members of a room see "room not found" even if the room exists.

## Structured errors

On failure ("ok=false"), responses keep the human-readable "error" string
and also include machine-readable details in "data.error":

    {
      "ok": false,
      "error": "peer not registered: ghost",
      "code": 5,
      "data": {
        "error": {
          "code": 5,
          "reason": "peer_not_registered",
          "retry_hint": "check kopos agents",
          "context": {"peer": "ghost"}
        }
      }
    }

Use "data.error.reason" (and optional "retry_hint" / "context") for agent
logic; keep "error" for terminal-friendly output.

## Exit codes

    0  success
    1  generic error
    2  timeout — read returned empty; call again if you still want to wait
    3  peer_closed — daemon shutting down or your lease expired mid-read
    4  reserved (no longer produced)
    5  not_found — peer or room does not exist
    6  unauthorized — bad signature or caller not registered

Check exit code after every call. Stdout alone is not authoritative.

## Minimal conversation

Shell A (claude, wants to ask codex a question):

    export KOPOS_NAME=claude
    kopos register
    kopos ask codex "what's your plan for feat/identity?" --timeout 300
    # prints codex's reply on stdout

Shell B (codex, responding):

    export KOPOS_NAME=codex
    kopos register
    kopos read-any --timeout 600
    # prints:
    #   peer=claude
    #   what's your plan for feat/identity?
    kopos tell claude "ULID migration, nickname resolver, keep pubkeys"

Shell A's ask returns "ULID migration, nickname resolver, keep pubkeys".

Shell A can follow up without waiting for codex to have finished
anything:

    kopos tell codex "also, check CHANNELS.md before you start"

That second "tell" is non-blocking. The turn FSM that used to block
you after one send is gone.

## Key storage

By default kopos stores private keys as files at ~/.kopos/keys/<name>.key
(mode 0600). On macOS you can instead store them in the system Keychain:

    export KOPOS_KEYSTORE=keychain

With this set, keys are kept as generic Keychain items (service "kopos",
account "<agent name>"). If the Keychain backend is unavailable (non-macOS,
or the 'security' CLI is missing) kopos falls back to the file backend
silently. Unset or any other value selects the file backend.

## Tasks — workstream tracking

A task list is a git-backed set of workstreams per project. Each workstream
gets a git worktree, a room, and a context bundle posted as the room's first
message — all created by a single "kopos task publish" call. Workers discover
open tasks with "kopos task bulletin" and pick one with "kopos task claim".

The project id is auto-derived from the git remote URL (slugified) or the
repo basename. repo_root is auto-derived from git rev-parse --show-toplevel
at register time, and kopos validates on publish that you are publishing from
the same repo you registered in.

Roles are set at register time and never change without an explicit re-register:

    kopos register --role supervisor
    kopos register --role worker

One supervisor per project. Unregister is rejected with exit code 7
(supervisor_busy) while the supervisor has non-merged tasks; run task
handoff first.

### Supervisor commands

    kopos task publish --file <payload.json>
    kopos task unassign <slug>
    kopos task reassign <slug> <agent>
    kopos task unpublish <slug> [--force]
    kopos task status <slug> merged
    kopos task handoff <new-supervisor>
    kopos task show [<slug>] [--project <id>]    (anyone; defaults to cwd project)

### Worker commands

    kopos task bulletin [--project <id>]           (open tasks in this project)
    kopos task claim <slug>                        (open → in-progress, auto-joins room, surfaces bundle)
    kopos task status <slug> in-progress|ready|blocked   (own row only)
    kopos task list                                (lists where caller is supervisor or owner)

### Publish payload shape

publish takes a JSON file (or stdin). Example:

    {
      "project": "myproj",
      "repo_root": "/absolute/path/to/repo",
      "workstreams": [
        {
          "slug": "feat-foo",
          "branch": "feat/foo",
          "brief": "markdown prose describing the work…",
          "owned_paths": ["src/foo/**"],
          "contracts": [{"other_slug": "feat-bar", "note": "consumes Bar type"}]
        }
      ]
    }

"project" and "repo_root" default to the detected values for the caller's
cwd if omitted. Per-workstream atomicity: if one slug fails (e.g. branch
already checked out elsewhere) it is reported in "failed" and other slugs
still succeed. Re-running publish against the same commit is a no-op for
already-published slugs.

The daemon runs git worktree add on your behalf under
<parent-of-repo_root>/wt/<slug>. Do not run git worktree add yourself.

### Task status transitions

    open → in-progress   (worker:     task claim)
    in-progress → *      (owner:      task status ready|blocked)
    * → merged           (supervisor: task status merged)
    * → open             (supervisor: task unassign)
    * → assigned         (supervisor: task reassign; forces new owner)

Rooms are kept live through these transitions. Closing a merged room is an
explicit, supervisor-driven cleanup step — see "Rooms GC" below.

### Context bundle

publish composes the brief + owned_paths + contracts into a single markdown
message and posts it to the workstream's room as the first message. The
message is authored by the supervisor. Claim returns this first post so the
worker has its brief without a second call.

To revise context, the supervisor posts a follow-up message in the room; the
original bundle stays. publish does not mutate existing posts.

Exit code 7 = supervisor_busy (unregister blocked; call task handoff first).
Exit code 8 = project_identity_mismatch (publish payload project/repo_root
disagrees with caller's registered identity; re-register from the right
repo).

### Retracting a task

    kopos task unpublish <slug> [--force] [--wipe-worktree] [--evict-owner]

Use this when a task was published in error (typo, wrong scope, wrong
project). Supervisor-only. Two independent decisions:

Row + room removal (always):
- Default: if the task has no owner and the room has no traffic beyond
  the bundle, unpublish drops the row and archives the room.
- --force: same, but allowed when the task has an owner or real room
  conversation.

Worktree removal (opt-in, off by default):
- By default the worktree is left on disk. Coding agents often have a
  live cwd inside the worktree; wiping it would crash them.
- --wipe-worktree: additionally remove the worktree kopos created.
  Subject to two safety gates:
    · Dirty worktree (uncommitted or unpushed): refused. Hard gate, no
      override. Clean up the worktree manually first.
    · Live owner lease: refused unless --evict-owner is also passed.
      "Live" means the owner agent's lease has not expired
      (see "kopos agents", lease column).

If any safety gate refuses, the whole call fails and nothing changes on
disk or in state. Re-publishing the same slug later un-archives the
room and reuses it, so the bundle thread is preserved across a
mistaken-unpublish cycle.

Response fields:
- worktree_removed: true only when the directory is verifiably gone.
- worktree_preserved: "default" (no --wipe-worktree) or "remove_failed"
  (remove was attempted but the directory is still present — see
  worktree_remove_error for detail).
- room_archived: whether the room is now archived.

### Rooms GC

    kopos rooms gc

Archives every slug room whose backing task has status=merged in a list
you supervise. Archived rooms reject new posts but keep their membership
and full history intact; members can still read the thread. Workers are
rejected (exit code 6). Idempotent — re-running archives nothing new.
This is the only way kopos closes a workstream room; task transitions
themselves no longer touch rooms.

## Common mistakes

- Using "tell" when the human asked you to "ask" — you'll return
  without the reply. Use "ask" when an answer matters.
- Using "ask" with too short a --timeout and treating exit code 2 as
  failure. It isn't; the peer may be slow. Call read again.
- Trying to post to a room you haven't joined → "room not found".
- Reaching for filesystem inspection because "kopos read" returned
  empty. Empty is a normal state, not an error. The transcript is in
  git but the mailbox is empty.
`
