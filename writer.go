package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

type writeOp struct {
	relPath    string
	content    []byte
	commitMsg  string
}

func ensureWorkspace() error {
	ws := workspacePath()
	if err := os.MkdirAll(ws, 0700); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(ws, ".git", "HEAD")); os.IsNotExist(err) {
		// clean partial state if any (e.g., from a crashed prior init)
		_ = os.RemoveAll(filepath.Join(ws, ".git"))
		cmd := exec.Command("git", "-C", ws, "init", "-q", "-b", "main")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git init: %s: %w", string(out), err)
		}
		// set a default identity if user has none at repo level; harmless if already set globally
		_ = exec.Command("git", "-C", ws, "config", "user.email", "daemon@lesche.local").Run()
		_ = exec.Command("git", "-C", ws, "config", "user.name", "lesche").Run()

		readme := []byte("# lesche workspace\n\nAgent coordination log. Managed by the lesche daemon.\n")
		if err := os.WriteFile(filepath.Join(ws, "README.md"), readme, 0600); err != nil {
			return err
		}
		_ = exec.Command("git", "-C", ws, "add", "README.md").Run()
		_ = exec.Command("git", "-C", ws, "commit", "-q", "-m", "init workspace").Run()
	}
	return nil
}

func (s *State) enqueueWrite(relPath string, content []byte, commitMsg string) {
	select {
	case s.writes <- writeOp{relPath: relPath, content: content, commitMsg: commitMsg}:
	default:
		// if queue is full, block. Never drop.
		s.writes <- writeOp{relPath: relPath, content: content, commitMsg: commitMsg}
	}
}

func (s *State) runWriter() {
	s.wg.Add(1)
	defer s.wg.Done()
	ws := workspacePath()
	for op := range s.writes {
		full := filepath.Join(ws, op.relPath)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			fmt.Fprintln(os.Stderr, "mkdir:", err)
			continue
		}
		if err := os.WriteFile(full, op.content, 0600); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			continue
		}
		if out, err := exec.Command("git", "-C", ws, "add", op.relPath).CombinedOutput(); err != nil {
			fmt.Fprintln(os.Stderr, "git add:", string(out), err)
			continue
		}
		if out, err := exec.Command("git", "-C", ws, "commit", "-q", "-m", op.commitMsg).CombinedOutput(); err != nil {
			fmt.Fprintln(os.Stderr, "git commit:", string(out), err)
			continue
		}
	}
}
