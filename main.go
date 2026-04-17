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
	case "unregister":
		cmdUnregister(os.Args[2:])
	case "agents":
		cmdAgents(os.Args[2:])
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
	fmt.Fprintf(os.Stderr, `lesche - agent-to-agent coordination

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
  lesche register [--name <name>]
  lesche unregister                      drop yourself from the registry
  lesche agents
  lesche channels                        list your peer-pair channels

  lesche tell <peer> "<msg>"             async, no reply expected
  lesche ask  <peer> "<msg>" [--timeout N]   send then block for reply
  lesche read <peer|room> [--room] [--timeout N]   consume next inbound
  lesche peek <peer|room> [--room]       non-destructive inspect
  lesche read-any [--timeout N]          block on ANY channel or room

  lesche rooms                           list known rooms
  lesche room create <name> [--desc <text>]
  lesche join <room>
  lesche leave <room>
  lesche participants <room>
  lesche post <room> "<msg>"             async broadcast

  lesche history <peer|room> [--room] [--since SEQ] [--limit N]
  lesche renew                           extend caller's lease
  lesche stop
  lesche protocol                        print agent-facing protocol guide
  lesche --version

Identity:
  On register, lesche generates an Ed25519 keypair for your name.
  Public key lives in the registry; private key at ~/.lesche/keys/<name>.key
  (mode 0600). Every authenticated request is signed by your key and
  verified by the daemon. Another process passing --as <your-name> without
  your key will be rejected with exit code 6.

Environment:
  LESCHE_NAME       caller identity for all commands (override per-call with --as)
  LESCHE_HOME       socket/pid/keys dir (default ~/.lesche)
  LESCHE_WORKSPACE  git repo for transcripts (default ~/.local/state/lesche/workspace)

Exit codes:
  0  ok
  1  generic error
  2  timeout — read returned empty; call again to resume
  3  peer_closed — daemon shutting down or lease expired mid-read
  4  reserved (no longer produced; was "not_your_turn")
  5  not_found — peer or room does not exist
  6  unauthorized — signature invalid or caller not registered

Run "lesche protocol" for the full agent-facing guide.
`)
}
