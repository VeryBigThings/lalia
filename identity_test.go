package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestULIDStabilityAcrossReRegister: re-registering the same name reuses the
// same AgentID (as long as the key file is intact).
func TestULIDStabilityAcrossReRegister(t *testing.T) {
	t.Setenv("KOPOS_HOME", t.TempDir())
	t.Setenv("KOPOS_WORKSPACE", filepath.Join(t.TempDir(), "workspace"))
	s := newFixtureState()

	r1 := s.opRegister(Request{Args: map[string]any{"name": "alice", "pid": float64(1)}})
	if !r1.OK {
		t.Fatalf("first register failed: %+v", r1)
	}
	id1 := r1.Data.(map[string]any)["agent_id"].(string)

	r2 := s.opRegister(Request{Args: map[string]any{"name": "alice", "pid": float64(2)}})
	if !r2.OK {
		t.Fatalf("second register failed: %+v", r2)
	}
	id2 := r2.Data.(map[string]any)["agent_id"].(string)

	if id1 != id2 {
		t.Fatalf("re-register changed agent_id: %s → %s", id1, id2)
	}
}

// TestULIDFreshOnKeyDelete: if the key file is deleted, a new register mints
// a different AgentID.
func TestULIDFreshOnKeyDelete(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KOPOS_HOME", home)
	t.Setenv("KOPOS_WORKSPACE", filepath.Join(t.TempDir(), "workspace"))
	s := newFixtureState()

	r1 := s.opRegister(Request{Args: map[string]any{"name": "alice", "pid": float64(1)}})
	if !r1.OK {
		t.Fatalf("first register failed: %+v", r1)
	}
	id1 := r1.Data.(map[string]any)["agent_id"].(string)

	// Delete the key — simulates lost keypair
	_ = os.Remove(filepath.Join(home, "keys", "alice.key"))
	// Also remove from state to simulate fresh daemon restart
	s.mu.Lock()
	s.unindexAgent(id1)
	s.mu.Unlock()

	r2 := s.opRegister(Request{Args: map[string]any{"name": "alice", "pid": float64(3)}})
	if !r2.OK {
		t.Fatalf("fresh register failed: %+v", r2)
	}
	id2 := r2.Data.(map[string]any)["agent_id"].(string)

	if id1 == id2 {
		t.Fatalf("new register after key delete should produce new agent_id, got same: %s", id1)
	}
}

// TestResolverBareNameUnique: bare name resolves when exactly one agent has it.
func TestResolverBareNameUnique(t *testing.T) {
	s := newFixtureState()
	mustRegister(t, s, "alice", 1)

	s.mu.Lock()
	agentID, err := s.resolveAddressInner("alice", nil)
	s.mu.Unlock()

	if err != nil {
		t.Fatalf("bare name resolve failed: %v", err)
	}
	s.mu.Lock()
	a := s.agents[agentID]
	s.mu.Unlock()
	if a == nil || a.Name != "alice" {
		t.Fatalf("resolved to wrong agent: %+v", a)
	}
}

// TestResolverBareNameAmbiguous: two agents with same name → error listing both.
func TestResolverBareNameAmbiguous(t *testing.T) {
	t.Setenv("KOPOS_HOME", t.TempDir())
	t.Setenv("KOPOS_WORKSPACE", filepath.Join(t.TempDir(), "workspace"))
	s := newFixtureState()
	mustRegister(t, s, "alice", 1)

	// Inject a second alice with a different agent_id manually
	secondID := "01ALICE20000000000000000000"
	s.mu.Lock()
	s.agents[secondID] = &Agent{
		AgentID:   secondID,
		Name:      "alice",
		Project:   "other",
		Branch:    "feat",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	// nameIdx only holds one; bare-name resolution scans s.agents directly
	s.mu.Unlock()

	s.mu.Lock()
	_, err := s.resolveAddressInner("alice", nil)
	s.mu.Unlock()

	if err == nil {
		t.Fatalf("expected ambiguous error for two alices")
	}
}

// TestResolverQualified: name@project and name@project:branch resolve correctly.
func TestResolverQualified(t *testing.T) {
	t.Setenv("KOPOS_HOME", t.TempDir())
	t.Setenv("KOPOS_WORKSPACE", filepath.Join(t.TempDir(), "workspace"))
	s := newFixtureState()

	id := newAgentID()
	s.mu.Lock()
	s.agents[id] = &Agent{
		AgentID:   id,
		Name:      "alice",
		Project:   "myproject",
		Branch:    "main",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	s.nameIdx["alice"] = id
	s.mu.Unlock()

	cases := []string{"alice@myproject", "alice@myproject:main"}
	for _, addr := range cases {
		s.mu.Lock()
		resolved, err := s.resolveAddressInner(addr, nil)
		s.mu.Unlock()
		if err != nil {
			t.Fatalf("resolve %q failed: %v", addr, err)
		}
		if resolved != id {
			t.Fatalf("resolve %q got %s, want %s", addr, resolved, id)
		}
	}
}

// TestResolverULID: a bare ULID resolves directly.
func TestResolverULID(t *testing.T) {
	s := newFixtureState()
	mustRegister(t, s, "alice", 1)

	s.mu.Lock()
	id := s.nameIdx["alice"]
	s.mu.Unlock()

	s.mu.Lock()
	resolved, err := s.resolveAddressInner(id, nil)
	s.mu.Unlock()

	if err != nil {
		t.Fatalf("ULID resolve failed: %v", err)
	}
	if resolved != id {
		t.Fatalf("ULID resolve got %s, want %s", resolved, id)
	}
}

// TestResolverNicknameStable: a stable nickname resolves to the stored agent_id.
func TestResolverNicknameStable(t *testing.T) {
	s := newFixtureState()
	mustRegister(t, s, "alice", 1)

	s.mu.Lock()
	id := s.nameIdx["alice"]
	s.mu.Unlock()

	nicknames := map[string]Nickname{
		"buddy": {Mode: "stable", AgentID: id, Address: "alice"},
	}

	s.mu.Lock()
	resolved, err := s.ResolveAddress("buddy", nicknames)
	s.mu.Unlock()

	if err != nil {
		t.Fatalf("nickname resolve failed: %v", err)
	}
	if resolved != id {
		t.Fatalf("nickname resolved to %s, want %s", resolved, id)
	}
}

// TestNicknameAssignListDelete: nickname file CRUD works end-to-end.
func TestNicknameAssignListDelete(t *testing.T) {
	t.Setenv("KOPOS_HOME", t.TempDir())

	nicknames := map[string]Nickname{
		"rev":   {Mode: "stable", AgentID: "01FAKEID0000000000000000000", Address: "alice@proj:main"},
		"draft": {Mode: "follow", Address: "bob@proj:feat"},
	}
	if err := saveNicknames(nicknames); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := loadNicknames()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2 nicknames, got %d", len(loaded))
	}
	if loaded["rev"].AgentID != "01FAKEID0000000000000000000" {
		t.Fatalf("stable AgentID mismatch: %+v", loaded["rev"])
	}
	if loaded["draft"].Mode != "follow" {
		t.Fatalf("follow mode not preserved: %+v", loaded["draft"])
	}

	delete(loaded, "rev")
	if err := saveNicknames(loaded); err != nil {
		t.Fatalf("save after delete: %v", err)
	}

	after, _ := loadNicknames()
	if _, ok := after["rev"]; ok {
		t.Fatalf("rev should have been deleted")
	}
	if _, ok := after["draft"]; !ok {
		t.Fatalf("draft should still exist")
	}
}

// TestAgentRegistryMigration: legacy name-keyed JSON files are migrated to
// agent_id-keyed files on loadRegistry, preserving pubkeys.
func TestAgentRegistryMigration(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "workspace")
	t.Setenv("KOPOS_HOME", home)
	t.Setenv("KOPOS_WORKSPACE", workspace)

	// Write a legacy name-keyed registry file (no agent_id field)
	regDir := filepath.Join(workspace, "registry")
	if err := os.MkdirAll(regDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	legacy := `{"name":"alice","pid":999,"pubkey":"deadbeef","started_at":"2024-01-01T00:00:00Z","last_seen_at":"2024-01-01T00:00:00Z","expires_at":"2099-01-01T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(regDir, "alice.json"), []byte(legacy), 0600); err != nil {
		t.Fatalf("write legacy: %v", err)
	}

	s := newFixtureState()
	if err := s.loadRegistry(); err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}

	s.mu.Lock()
	a := s.agentByName("alice")
	s.mu.Unlock()

	if a == nil {
		t.Fatalf("alice not found after migration")
	}
	if a.AgentID == "" {
		t.Fatalf("AgentID not backfilled after migration")
	}
	if a.Pubkey != "deadbeef" {
		t.Fatalf("pubkey not preserved: %s", a.Pubkey)
	}

	// The old name-keyed file should be gone
	if _, err := os.Stat(filepath.Join(regDir, "alice.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy alice.json should have been removed after migration")
	}
	// A new ULID-keyed file should exist
	entries, _ := os.ReadDir(regDir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 registry file after migration, got %d", len(entries))
	}
	if entries[0].Name() == "alice.json" {
		t.Fatalf("new registry file should not be alice.json")
	}
}

// TestAgentsOutputHasNewFields: opAgents now returns agent_id, qualified, harness.
func TestAgentsOutputHasNewFields(t *testing.T) {
	t.Setenv("KOPOS_HOME", t.TempDir())
	s := newFixtureState()
	mustRegister(t, s, "alice", 1)

	resp := s.opAgents()
	if !resp.OK {
		t.Fatalf("agents failed: %+v", resp)
	}
	rows := resp.Data.([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	m := rows[0].(map[string]any)
	if m["agent_id"] == "" || m["agent_id"] == nil {
		t.Fatalf("agent_id missing from agents output: %+v", m)
	}
	if _, ok := m["qualified"]; !ok {
		t.Fatalf("qualified missing from agents output: %+v", m)
	}
}
