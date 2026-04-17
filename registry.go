package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// persistAgent writes <workspace>/registry/<name>.json via the writer queue.
func (s *State) persistAgent(a *Agent) {
	data, _ := json.MarshalIndent(a, "", "  ")
	data = append(data, '\n')
	s.enqueueWrite(
		filepath.Join("registry", a.Name+".json"),
		data,
		fmt.Sprintf("register %s (lease until %s)", a.Name, a.ExpiresAt.Format(time.RFC3339)),
	)
}

// removeAgentFile enqueues deletion of an agent's registry file.
// We do not git-rm (would require a different writeOp variant for MVP);
// instead, overwrite the file with a tombstone record so the history is
// preserved and the sweeper can re-pick it up as expired.
// Simpler approach: just delete the file directly without a git commit
// for the removal, since expiry-sweep is idempotent. The invariant is
// "file absent ⇒ agent unknown on restart," which is what we want.
func (s *State) removeAgentFile(name string) {
	p := filepath.Join(workspacePath(), "registry", name+".json")
	_ = os.Remove(p)
}

// loadRegistry rehydrates the in-memory agents map from files on startup.
// Skips records whose lease has already expired; the sweeper will catch any
// we miss here on its next tick.
func (s *State) loadRegistry() error {
	dir := filepath.Join(workspacePath(), "registry")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	now := time.Now()
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var a Agent
		if err := json.Unmarshal(b, &a); err != nil {
			continue
		}
		if a.Name == "" {
			continue
		}
		if !a.ExpiresAt.IsZero() && now.After(a.ExpiresAt) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
			continue
		}
		s.agents[a.Name] = &a
	}
	return nil
}
