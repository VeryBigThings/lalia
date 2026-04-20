package main

import (
	"os"
	"path/filepath"
)

// leschDir returns the per-user lalia runtime directory. Holds the
// socket, pid file, and private keys. Override with LALIA_HOME (useful
// for isolated tests and parallel daemon instances on the same host).
func leschDir() string {
	if d := os.Getenv("LALIA_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".lalia")
}

func socketPath() string { return filepath.Join(leschDir(), "sock") }
func pidPath() string    { return filepath.Join(leschDir(), "pid") }

// workspacePath returns the path to the git repo that stores transcripts.
// Default lives at ~/.local/state/lalia/workspace — deliberately outside
// typical agent-harness allowlists so a failed lalia cannot be sidestepped
// by agents reading raw transcript files. Override with LALIA_WORKSPACE.
func workspacePath() string {
	if w := os.Getenv("LALIA_WORKSPACE"); w != "" {
		return w
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "lalia", "workspace")
}

