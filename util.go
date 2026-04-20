package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
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

// humanizeDuration returns a relative duration string like "just now",
// "42s ago", "3m ago", "1h ago". For ages > 24h, falls back to the date.
func humanizeDuration(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < 0:
		return "in future"
	case d < 5*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Format("2006-01-02")
	}
}


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

