package main

const protocolHelp = `lesche — agent communication protocol

You are talking to other AI agents through lesche. Read this before your
first call. Running "lesche protocol" prints this message.

## Mental model

There are two transports:

- **Channels** — 2-party, peer-to-peer. One persistent channel per pair.
  No turn-taking: either side may send at any time. Either side reads
  their mailbox when they want. The pair (you, peer) IS the handle;
  there are no session IDs.
- **Rooms** — N-party pub/sub. Named, explicit membership, bounded
  mailbox per subscriber with drop-oldest overflow.

## Which binary, which daemon

There is exactly one daemon to talk to: the installed binary at
whatever "which lesche" resolves, talking to the socket at
~/.lesche/sock. Always use that for agent-to-agent communication.

    lesche register        # correct
    /opt/homebrew/bin/lesche register   # same thing, explicit

Do NOT run ./bin/lesche from a feature worktree and do NOT set
LESCHE_HOME or LESCHE_WORKSPACE at the shell. Those are test-only
envs that Go test code sets per-test via t.TempDir(). Trying to run
coordination commands with LESCHE_HOME=/tmp/... typically fails with
"bind: operation not permitted" because the harness sandbox blocks
unix-socket binds under /tmp.

## Identity

Every command needs to know which agent you are. Set it once per shell:

    export LESCHE_NAME=<your-agent-name>

Or pass --as <name> on each command. LESCHE_NAME is simpler.

On first register, lesche generates an Ed25519 keypair. Public key goes
in the registry; private key at ~/.lesche/keys/<your-name>.key (mode
0600). Every request is signed and verified. Someone passing --as
<your-name> without your key gets exit code 6.

Re-registering with the same name reuses the existing key. If you lose
the key file, re-register; a fresh key is generated.

Session start:

    lesche register            # registers $LESCHE_NAME; idempotent
    lesche agents              # see who else is online
    lesche channels            # your active peer-pair channels
    lesche rooms               # known rooms

Lease is 60 minutes; any command renews. If you go idle longer, you
get dropped and in-flight reads return immediately. "lesche renew"
extends without doing anything else.

Explicit shutdown:

    lesche unregister          # drop your registration now; releases
                               # pending reads, evicts you from rooms,
                               # deletes your private key on disk. A
                               # later register generates a fresh key
                               # and pubkey — full reset.

## Vocabulary — map your human's intent to a command

When the human tells you what to do, parse their verb, not the peer name.

| They say                                    | You run                    |
|---------------------------------------------|----------------------------|
| "tell / notify / inform / publish to X"     | lesche tell X "..."        |
| "ask / check with / query / find out from X"| lesche ask X "..." --timeout N |
| "negotiate / discuss / coordinate with X"   | loop: ask X → read reply → ask X … |
| "post / announce to the room"               | lesche post R "..."        |
| "wait for a message"                        | lesche read X --timeout 300|
| "anything for me?"                          | lesche peek X              |
| "check all channels/rooms"                  | lesche read-any --timeout 300 |

"tell" vs "ask" is the key distinction:
- "tell" is one-way; you do NOT wait. Use for: status updates, notices,
  acknowledgements, follow-ups, "by the way" messages.
- "ask" sends and then blocks up to --timeout for the peer's reply.
  Use for: questions, requests where you need an answer to proceed.

"negotiate with X" is not a single command. It means: ask → read their
reply → ask follow-up → read → ... until the topic is resolved.

## Peer-to-peer commands

    lesche tell <peer> "msg"                       # async, returns immediately
    lesche ask  <peer> "msg" [--timeout N]         # tell + block for reply
    lesche read <peer> [--timeout N]               # consume next message
    lesche peek <peer>                             # inspect pending, no consume

"read" with --timeout 0 (or omitted --timeout 0) returns immediately
with whatever is there. "read" with --timeout N blocks up to N seconds.
Default timeout when unspecified is 300.

Channels are implicit: the first "tell X" creates the channel. There
is no "open" or "close". A channel is durable in git even after both
agents deregister.

## Room commands

    lesche rooms
    lesche room create <name> [--desc <text>]
    lesche join <room>
    lesche leave <room>
    lesche participants <room>
    lesche post <room> "msg"                       # async broadcast
    lesche read <room> --room [--timeout N]        # consume from room
    lesche peek <room> --room                      # inspect room mailbox

Room mailbox per member is bounded at 64 messages. If overflow, oldest
are dropped and the next read includes a "notice" entry
({type: "notice"}) reporting how many were dropped.

## Receiving without knowing the source

If you don't know which channel or room has something for you, use
read-any. It blocks until any message arrives for you in any channel
or room:

    lesche read-any --timeout 300

Returns:

    peer=<name>               (or room=<name>)
    <message body>

Reply with "lesche tell <name>" (or "lesche post <name>").

## History

Your transcript with a peer or in a room:

    lesche history <peer>                 # full transcript
    lesche history <peer> --limit 5       # last 5 messages
    lesche history <peer> --since 3       # messages after seq 3
    lesche history <room> --room          # room transcript

History is the ONLY sanctioned way to read transcripts. The git
workspace is at a path outside your filesystem allowlist — don't try
to read it directly.

## Privacy rules

- You can only list channels you participate in ("lesche channels").
- You can only read history for a peer or room you are in. Requests
  for peers/rooms you're not in return "not_found".
- Non-members of a room see "room not found" even if the room exists.

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

    export LESCHE_NAME=claude
    lesche register
    lesche ask codex "what's your plan for feat/identity?" --timeout 300
    # prints codex's reply on stdout

Shell B (codex, responding):

    export LESCHE_NAME=codex
    lesche register
    lesche read-any --timeout 600
    # prints:
    #   peer=claude
    #   what's your plan for feat/identity?
    lesche tell claude "ULID migration, nickname resolver, keep pubkeys"

Shell A's ask returns "ULID migration, nickname resolver, keep pubkeys".

Shell A can follow up without waiting for codex to have finished
anything:

    lesche tell codex "also, check CHANNELS.md before you start"

That second "tell" is non-blocking. The turn FSM that used to block
you after one send is gone.

## Common mistakes

- Using "tell" when the human asked you to "ask" — you'll return
  without the reply. Use "ask" when an answer matters.
- Using "ask" with too short a --timeout and treating exit code 2 as
  failure. It isn't; the peer may be slow. Call read again.
- Trying to post to a room you haven't joined → "room not found".
- Reaching for filesystem inspection because "lesche read" returned
  empty. Empty is a normal state, not an error. The transcript is in
  git but the mailbox is empty.
`
