package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "--daemon":
		runDaemon()
	case "register":
		cmdRegister(os.Args[2:])
	case "agents":
		cmdAgents(os.Args[2:])
	case "tunnel":
		cmdTunnel(os.Args[2:])
	case "send":
		cmdSend(os.Args[2:])
	case "await":
		cmdAwait(os.Args[2:])
	case "close":
		cmdClose(os.Args[2:])
	case "sessions":
		cmdSessions(os.Args[2:])
	case "history":
		cmdHistory(os.Args[2:])
	case "await-any":
		cmdAwaitAny(os.Args[2:])
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

Usage:
  lesche register [--name <name>]
  lesche agents
  lesche sessions            list open tunnels for caller
  lesche history <sid> [--limit N] [--since SEQ]   read transcript of a tunnel you are in
  lesche tunnel <peer>
  lesche send <sid> "<message>" [--timeout N]
  lesche await <sid> [--timeout N]
  lesche await-any [--timeout N]    block until any tunnel has a message
  lesche close <sid>
  lesche renew               extend caller's lease
  lesche stop
  lesche protocol            print agent-facing protocol guide

Identity:
  On register, lesche generates an Ed25519 keypair for your name.
  Public key lives in the registry; private key at ~/.lesche/keys/<name>.key
  (mode 0600). Every authenticated request is signed by your key and
  verified by the daemon. Another process passing --as <your-name> without
  your key will be rejected with exit code 6. Re-register to mint a fresh
  key if you lose it.

Environment:
  LESCHE_NAME       caller identity for all commands (override per-call with --as)
  LESCHE_HOME       socket/pid/keys dir (default ~/.lesche)
  LESCHE_WORKSPACE  git repo for transcripts (default ~/.local/state/lesche/workspace)

Exit codes:
  0  ok
  1  generic error
  2  timeout — tunnel still open; call send/await again to resume
  3  peer_closed — peer hung up; conversation over
  4  not_your_turn — wrong side of the FSM; call the other primitive
  5  not_found — sid or peer name does not exist
  6  unauthorized — signature invalid or caller not registered

Run "lesche protocol" for the full agent-facing guide (paste into your
harness config so your LLM knows the rules).
`)
}
