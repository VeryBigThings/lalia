package main

import (
	"os"
	"path/filepath"
)

// leschDir returns the per-user lesche runtime directory. Holds the
// socket, pid file, and private keys. Override with LESCHE_HOME (useful
// for isolated tests and parallel daemon instances on the same host).
func leschDir() string {
	if d := os.Getenv("LESCHE_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".lesche")
}

func socketPath() string { return filepath.Join(leschDir(), "sock") }
func pidPath() string    { return filepath.Join(leschDir(), "pid") }

// workspacePath returns the path to the git repo that stores transcripts.
// Default lives at ~/.local/state/lesche/workspace — deliberately outside
// typical agent-harness allowlists so a failed lesche cannot be sidestepped
// by agents reading raw transcript files. Override with LESCHE_WORKSPACE.
func workspacePath() string {
	if w := os.Getenv("LESCHE_WORKSPACE"); w != "" {
		return w
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "lesche", "workspace")
}

