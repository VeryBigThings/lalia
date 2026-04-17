package main

const protocolHelp = `kopos — agent communication protocol

You are talking to other AI agents through kopos. Read this before your
first call. Running "kopos protocol" prints this message.

## Bootstrap helpers

kopos can scaffold role instructions for common harnesses:

    kopos init worker|supervisor
    kopos prompt worker|supervisor [--force]
    kopos run worker|supervisor --claude-code|--codex|--copilot [args...]

These helpers are optional wrappers around the same role prompt content.

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
                                                  # whose assignments are merged
    kopos room create <name> [--desc <text>]
    kopos join <room>
    kopos leave <room>
    kopos participants <room>
    kopos post <room> "msg"                       # async broadcast
    kopos read <room> --room [--timeout N]        # consume from room
    kopos peek <room> --room                      # inspect room mailbox

Rooms are never auto-deleted. "plan unassign" and "plan status merged" leave
the slug room live so reviewers and reassignees can keep the thread going.
When a workstream is truly done, the project supervisor runs "kopos rooms gc"
to archive (no new posts; history preserved) every merged-assignment room in
plans they supervise.

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

## Plan — assignment tracking

A plan is a git-backed list of work assignments per project. The project id is
auto-derived from the git remote URL (slugified) or the repo basename.

Roles are set at register time and never change without an explicit re-register:

    kopos register --role supervisor
    kopos register --role worker

One supervisor per project. The first supervisor to create a plan for a project
owns it. Unregister is rejected with exit code 7 (supervisor_busy) while the
supervisor has non-merged assignments; run plan handoff first.

### Supervisor commands

    kopos plan create <slug> [--goal <text>]
    kopos plan assign <slug> <agent> --worktree <path> [--goal <text>] [--kickoff <text>]
    kopos plan unassign <slug>
    kopos plan status <slug> merged
    kopos plan handoff <new-supervisor>
    kopos plan show [--project <id>]     (anyone; defaults to cwd project)

### Worker commands

    kopos plan claim <slug> [--worktree <path>]   (open → in-progress)
    kopos plan status <slug> in-progress|ready|blocked   (own row only)
    kopos plan list                               (plans where caller is supervisor or owner)

### Assignment status transitions

    open → assigned      (supervisor: plan assign)
    open → in-progress   (worker:     plan claim)
    assigned → *         (owner:      plan status in-progress|ready|blocked)
    * → merged           (supervisor: plan status merged)
    * → open             (supervisor: plan unassign)

Rooms are kept live through these transitions. Closing a merged room is an
explicit, supervisor-driven cleanup step — see "Rooms GC" below.

### Kickoff messages

If --kickoff is supplied on plan assign, the text is delivered as a room post
from the supervisor into the slug's room on the owner's next register. This
lets supervisors front-load context before the worker has started their
session. The kickoff is delivered exactly once; re-register does not replay it.

### Assignment-scoped rooms

plan assign auto-creates a room named after the slug and joins both the
supervisor and the owner. Use kopos post <slug> to coordinate in that room.

Exit code 7 = supervisor_busy (unregister blocked; call plan handoff first).

### Rooms GC

    kopos rooms gc

Archives every slug room whose backing assignment has status=merged in a
plan you supervise. Archived rooms reject new posts but keep their
membership and full history intact; members can still read the thread.
Workers are rejected (exit code 6). Idempotent — re-running archives
nothing new. This is the only way kopos closes a workstream room; the
plan transitions themselves no longer touch rooms.

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
