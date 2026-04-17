package main

import (
	"os"
	"path/filepath"
)

// leschDir returns the per-user kopos runtime directory. Holds the
// socket, pid file, and private keys. Override with KOPOS_HOME (useful
// for isolated tests and parallel daemon instances on the same host).
func leschDir() string {
	if d := os.Getenv("KOPOS_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".kopos")
}

func socketPath() string { return filepath.Join(leschDir(), "sock") }
func pidPath() string    { return filepath.Join(leschDir(), "pid") }

// workspacePath returns the path to the git repo that stores transcripts.
// Default lives at ~/.local/state/kopos/workspace — deliberately outside
// typical agent-harness allowlists so a failed kopos cannot be sidestepped
// by agents reading raw transcript files. Override with KOPOS_WORKSPACE.
func workspacePath() string {
	if w := os.Getenv("KOPOS_WORKSPACE"); w != "" {
		return w
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "kopos", "workspace")
}

