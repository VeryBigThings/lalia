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
  lesche tunnel <peer>
  lesche send <sid> "<message>" [--timeout N]
  lesche await <sid> [--timeout N]
  lesche close <sid>
  lesche stop
  lesche protocol        print agent-facing protocol guide

Environment:
  LESCHE_NAME       caller identity for all commands (override per-call with --as)
  LESCHE_WORKSPACE  git repo where transcripts are committed (default ~/.lesche/workspace)

Exit codes:
  0 ok, 1 error, 2 timeout, 3 peer_closed, 4 not_your_turn, 5 not_found
`)
}
