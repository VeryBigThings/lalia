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

func workspacePath() string {
	if w := os.Getenv("LESCHE_WORKSPACE"); w != "" {
		return w
	}
	return filepath.Join(leschDir(), "workspace")
}

func newSID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
