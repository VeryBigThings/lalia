package main

import (
	"fmt"
	"os"
)

// version is stamped at build time via -ldflags "-X main.version=…"
// (see Makefile). Defaults to "dev" for unstamped builds.
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "--version", "-v":
		fmt.Println(version)
		return
	case "--daemon":
		runDaemon()
	case "register":
		cmdRegister(os.Args[2:])
	case "suggest-name":
		cmdSuggestName(os.Args[2:])
	case "unregister":
		cmdUnregister(os.Args[2:])
	case "init":
		cmdInit(os.Args[2:])
	case "prompt":
		cmdPrompt(os.Args[2:])
	case "run":
		cmdRun(os.Args[2:])
	case "agents":
		cmdAgents(os.Args[2:])
	case "nickname":
		cmdNickname(os.Args[2:])
	case "rooms":
		cmdRooms(os.Args[2:])
	case "room":
		cmdRoom(os.Args[2:])
	case "join":
		cmdJoin(os.Args[2:])
	case "leave":
		cmdLeave(os.Args[2:])
	case "participants":
		cmdParticipants(os.Args[2:])
	case "post":
		cmdPost(os.Args[2:])
	case "tell":
		cmdTell(os.Args[2:])
	case "ask":
		cmdAsk(os.Args[2:])
	case "read":
		cmdRead(os.Args[2:])
	case "peek":
		cmdPeek(os.Args[2:])
	case "read-any":
		cmdReadAny(os.Args[2:])
	case "channels":
		cmdChannels(os.Args[2:])
	case "history":
		cmdHistory(os.Args[2:])
	case "task":
		cmdTask(os.Args[2:])
	case "renew":
		cmdRenew(os.Args[2:])
	case "stop":
		cmdStop(os.Args[2:])
	case "protocol":
		fmt.Print(protocolHelp)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `lalia - agent-to-agent coordination

If you are an LLM, run "lalia prompt <your-role>" first to load the
workflow instructions for your role (peer, worker or supervisor). The commands
listed below are the surface; the prompt tells you how to use them.

Peer-to-peer (English intent → command):
  tell X       = one-way notification / "notify, publish, inform"
  ask X        = question expecting answer / "ask, check with, query"
  read X       = pull next inbound from X (blocks with --timeout)
  peek X       = inspect pending without consuming
  read-any     = pull next inbound from ANY channel or room you're in

Rooms (N-party):
  post R       = broadcast / "announce, share with the room"
  read R --room = pull next from room R (blocks with --timeout)
  peek R --room = inspect room mailbox

Usage:
  lalia init <peer|worker|supervisor>  LLM entry point: prints the role
                                        bootstrap prompt to stdout. Pipe into
                                        the harness instructions file before
                                        the session starts.
  lalia prompt <peer|worker|supervisor> LLM entry point (in-session): prints
                                        the same role prompt so the agent can
                                        reload its workflow context on demand.
  lalia run <peer|worker|supervisor> --claude-code [args...]
  lalia run <peer|worker|supervisor> --codex       [args...]
  lalia run <peer|worker|supervisor> --copilot     [--force] [args...]

  lalia register [--name <name>] [--harness H] [--model M] [--project P] [--role peer|supervisor|worker]
  lalia suggest-name [--harness H] [--project P] [--role peer|supervisor|worker]
  lalia unregister                      drop yourself from the registry
  lalia agents
  lalia channels                        list your peer-pair channels
  lalia nickname [<nick> [<address>]] [-d <nick>] [--follow]

  lalia tell <peer> "<msg>"             async, no reply expected
  lalia ask  <peer> "<msg>" [--timeout N]   send then block for reply
  lalia read <peer|room> [--room] [--timeout N]   consume next inbound
  lalia peek <peer|room> [--room]       non-destructive inspect
  lalia read-any [--timeout N]          block on ANY channel or room

  lalia rooms                           list known rooms
  lalia rooms gc                        supervisor: archive rooms for merged
                                        tasks in lists you supervise
  lalia room create <name> [--desc <text>]
  lalia join <room>
  lalia leave <room>
  lalia participants <room>
  lalia post <room> "<msg>"             async broadcast

  lalia history <peer|room> [--room] [--since SEQ] [--limit N]
  lalia renew                           extend caller's lease
  lalia stop

Tasks (supervisor publishes, workers pull):
  lalia task publish --file <payload.json>   one call: worktrees + rooms + bundles
  lalia task bulletin [--project <id>]       list open tasks workers can claim
  lalia task claim <slug> [--project <id>]   atomic: owner=self, status=in-progress, join room
  lalia task set-status <slug> <in-progress|ready|blocked|merged> [--project <id>]
  lalia task unassign <slug> [--project <id>]
  lalia task reassign <slug> <agent> [--project <id>]
  lalia task unpublish <slug> [--force] [--wipe-worktree] [--evict-owner] [--project <id>]
                                             retract a published task; worktree preserved by default
  lalia task show [<slug>] [--project <id>]
  lalia task list
  lalia task handoff <new-supervisor> [--project <id>]
  lalia protocol                        print agent-facing protocol guide
  lalia --version

Run safety:
  "lalia run" writes the role prompt into the harness's instructions file
  (LALIA.md, AGENTS.md, or .github/copilot-instructions.md). Overwrite is
  refused when that file already exists and does not carry a lalia marker.
  Pass --force to override. "lalia prompt" and "lalia init" never touch
  the filesystem — they print the prompt to stdout.

Identity:
  On register, lalia generates an Ed25519 keypair for your name and assigns
  a stable ULID agent_id. The agent_id persists across re-registrations as
  long as the keypair file is intact. Rich metadata (project, branch, harness,
  model) is auto-detected from git and the caller's environment.
  Public key lives in the registry; private key at ~/.lalia/keys/<name>.key
  (mode 0600). Every authenticated request is signed by your key and
  verified by the daemon. Another process passing --as <your-name> without
  your key will be rejected with exit code 6.

  Address forms (accepted wherever a peer is specified):
    <nick>                  user-assigned nickname
    <ULID>                  bare agent_id
    name@project            fully-qualified, project scoped
    name@project:branch     fully-qualified, branch scoped
    name                    bare name (error if ambiguous)

  Nicknames (stored at ~/.lalia/nicknames.json):
    lalia nickname <nick> <address>        assign (stable by default)
    lalia nickname --follow <nick> <addr>  assign (follows address re-resolution)
    lalia nickname <nick>                  show current resolution
    lalia nickname                         list all
    lalia nickname -d <nick>              delete

Environment:
  LALIA_NAME       caller identity for all commands (override per-call with --as)
  LALIA_HOME       socket/pid/keys dir (default ~/.lalia)
  LALIA_WORKSPACE  git repo for transcripts (default ~/.local/state/lalia/workspace)

Exit codes:
  0  ok
  1  generic error
  2  timeout — read returned empty; call again to resume
  3  peer_closed — daemon shutting down or lease expired mid-read
  4  reserved (no longer produced; was "not_your_turn")
  5  not_found — peer or room does not exist
  6  unauthorized — signature invalid or caller not registered

Run "lalia protocol" for the full agent-facing guide.
`)
}
