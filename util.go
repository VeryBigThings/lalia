package main

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
)

func leschDir() string {
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

func newSID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
