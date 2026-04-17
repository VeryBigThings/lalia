package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// persistAgent writes <workspace>/registry/<agent_id>.json via the writer queue.
func (s *State) persistAgent(a *Agent) {
	data, _ := json.MarshalIndent(a, "", "  ")
	data = append(data, '\n')
	s.enqueueWrite(
		filepath.Join("registry", a.AgentID+".json"),
		data,
		fmt.Sprintf("register %s/%s (lease until %s)", a.Name, a.AgentID, a.ExpiresAt.Format(time.RFC3339)),
	)
}

// removeAgentFile deletes an agent's registry file. The invariant is
// "file absent ⇒ agent unknown on restart" — idempotent and correct.
func (s *State) removeAgentFile(agentID string) {
	p := filepath.Join(workspacePath(), "registry", agentID+".json")
	_ = os.Remove(p)
}

// indexAgent adds an agent to the in-memory indexes. Must be called with s.mu held.
func (s *State) indexAgent(a *Agent) {
	s.agents[a.AgentID] = a
	s.nameIdx[a.Name] = a.AgentID // last registration for this name wins
}

// unindexAgent removes an agent from both indexes. Must be called with s.mu held.
func (s *State) unindexAgent(agentID string) {
	a, ok := s.agents[agentID]
	if !ok {
		return
	}
	delete(s.agents, agentID)
	if s.nameIdx[a.Name] == agentID {
		delete(s.nameIdx, a.Name)
	}
}

// loadRegistry rehydrates the in-memory agents map from files on startup.
// Handles migration: legacy name-keyed files (no agent_id) are backfilled
// with a ULID and renamed to <agent_id>.json, preserving pubkeys.
// Skips records whose lease has already expired.
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
		fpath := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(fpath)
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
			_ = os.Remove(fpath)
			continue
		}

		// Migration: backfill AgentID for legacy name-keyed records
		if a.AgentID == "" {
			a.AgentID = newAgentID()
			newPath := filepath.Join(dir, a.AgentID+".json")
			data, _ := json.MarshalIndent(&a, "", "  ")
			data = append(data, '\n')
			if err := os.WriteFile(newPath, data, 0600); err == nil {
				_ = os.Remove(fpath)
			}
		}

		s.indexAgent(&a)
	}
	return nil
}
