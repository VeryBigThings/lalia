package main

const protocolHelp = `lesche — agent communication protocol

You are talking to another AI agent through lesche. Read this carefully before
your first call. Running "lesche protocol" prints this message.

## Identity

Every command needs to know which agent you are. Set it once per shell:

    export LESCHE_NAME=<your-agent-name>

You can also pass --as <name> on any command. LESCHE_NAME is simpler; use it.

At session start run:

    lesche register            # registers $LESCHE_NAME; idempotent
    lesche agents              # prints who else is registered

## Two transports (MVP ships with Tunnel only)

Tunnel: synchronous two-party channel. One speaker at a time. Like TCP.
Your send blocks until the peer replies. Your await blocks until the peer sends.

## Opening a tunnel

    lesche tunnel <peer-name>

Prints "sid=<session-id>". The peer must be registered first. Save the sid;
you pass it on every subsequent command in this conversation.

## Turn-taking — READ THIS

A tunnel has strict alternation. At any moment exactly one side holds "the turn"
(the right to speak). The initiator holds the turn first.

- If it is your turn, call "lesche send <sid> \"...\"". This blocks until the
  peer replies. Your send command prints the peer's reply on stdout when it
  returns.
- If it is NOT your turn, call "lesche await <sid>". This blocks until the
  peer's message arrives and then prints it on stdout.
- You cannot send twice in a row; you cannot await when it is your turn to
  speak. Those calls exit with code 4 ("not_your_turn") and a clear error.

After "send" returns with the peer's reply, it is your turn again.
After "await" returns with the peer's message, it is your turn — reply with send.

## Ending the conversation

When you are done:

    lesche close <sid>

This is the hangup. The other side's blocked call returns immediately with
exit code 3 ("peer hung up" or similar). Do not expect further messages.
If the other side sees code 3, treat the conversation as over.

## Exit codes

Every command returns one of:

    0  success
    1  generic error (malformed args, etc)
    2  timeout — peer did not respond within --timeout seconds (default 300).
       The tunnel is still open. You can call send or await again to resume.
    3  peer_closed — the peer called close. Conversation is over.
    4  not_your_turn — you violated the alternation rule. Check whether you
       should send or await.
    5  not_found — sid or peer name does not exist.

Check the exit code after every call. Do not assume success from stdout alone.

## Minimal working conversation

Shell A (initiator, name=claude, peer=codex):

    export LESCHE_NAME=claude
    lesche register
    lesche tunnel codex           # prints sid=<sid>
    lesche send <sid> "hi codex"  # blocks; returns codex's reply
    lesche send <sid> "follow-up" # blocks; returns codex's reply
    lesche close <sid>

Shell B (responder, name=codex):

    export LESCHE_NAME=codex
    lesche register
    lesche await <sid>            # blocks; returns "hi codex"
    lesche send <sid> "hello claude, ready" # blocks; returns claude's follow-up
    lesche send <sid> "ok"        # blocks; returns exit 3 after claude closes

## Transcript

The entire conversation is committed to a git repo at $LESCHE_WORKSPACE
(default ~/.lesche/workspace). Each message is one commit under
tunnels/<sid>/. You can inspect after the fact with:

    git -C ~/.lesche/workspace log --oneline tunnels/<sid>/

## Common mistakes

- Calling "send" when it is not your turn → exit 4. Call "await" instead.
- Calling "await" when it is your turn → exit 4. Call "send" instead.
- Assuming a timeout (exit 2) means the tunnel died. It does not. Call
  send or await again.
- Forgetting to call "close" at the end. The tunnel stays open; the peer
  may block on await forever.
- Using a sid from a different conversation. Each tunnel has its own sid.
`
