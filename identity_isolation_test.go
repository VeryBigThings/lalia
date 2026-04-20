package main

import (
	"testing"
	"time"
)

// TestPIDLockingPreventsMultipleIdentities verifies that a single PID cannot
// register as two different agents while both leases are live.
func TestPIDLockingPreventsMultipleIdentities(t *testing.T) {
	s := newFixtureState()
	mustRegister(t, s, "alice", 42)

	resp := s.opRegister(Request{Args: map[string]any{"name": "bob", "pid": float64(42)}})
	if resp.OK {
		t.Fatalf("expected PID conflict, got OK")
	}
	if resp.Code != CodePIDConflict {
		t.Fatalf("expected code %d (pid_conflict), got %d: %s", CodePIDConflict, resp.Code, resp.Error)
	}
}

// TestPIDLockingAllowsSameNameReRegister verifies that re-registering the same
// name (even with a different PID) is not blocked — it is the normal lease
// renewal flow, not an impersonation attempt.
func TestPIDLockingAllowsSameNameReRegister(t *testing.T) {
	s := newFixtureState()
	mustRegister(t, s, "alice", 42)

	resp := s.opRegister(Request{Args: map[string]any{"name": "alice", "pid": float64(42)}})
	if !resp.OK {
		t.Fatalf("re-register same name/pid failed: %+v", resp)
	}
}

// TestPIDLockingAllowsExpiredLease verifies that a PID conflict is not raised
// when the existing registration has an expired lease (the agent is gone).
func TestPIDLockingAllowsExpiredLease(t *testing.T) {
	s := newFixtureState()
	mustRegister(t, s, "alice", 42)

	// Expire alice's lease.
	s.mu.Lock()
	a := s.agentByName("alice")
	a.ExpiresAt = time.Now().Add(-time.Second)
	s.mu.Unlock()

	resp := s.opRegister(Request{Args: map[string]any{"name": "bob", "pid": float64(42)}})
	if !resp.OK {
		t.Fatalf("expected success after alice's lease expired, got: %s (code=%d)", resp.Error, resp.Code)
	}
}

// TestPIDLockingAllowsDistinctPIDs verifies that two agents with different PIDs
// can register without conflict.
func TestPIDLockingAllowsDistinctPIDs(t *testing.T) {
	s := newFixtureState()
	mustRegister(t, s, "alice", 1)

	resp := s.opRegister(Request{Args: map[string]any{"name": "bob", "pid": float64(2)}})
	if !resp.OK {
		t.Fatalf("distinct-PID second registration failed: %+v", resp)
	}
}

// TestSupervisorCannotClaimTask verifies that an agent registered as supervisor
// is rejected when attempting to claim a task.
func TestSupervisorCannotClaimTask(t *testing.T) {
	repoRoot := mustInitGitRepo(t)
	s := newFixtureState()
	mustRegisterFor(t, s, "sup", "supervisor", "myproject", repoRoot, 1)

	tl := seedTaskList(s, "myproject", "sup")
	tl.RepoRoot = repoRoot
	s.mu.Lock()
	tl.Tasks = append(tl.Tasks, Task{Slug: "feat/x", Status: statusOpen, UpdatedAt: time.Now()})
	s.mu.Unlock()

	resp := s.opTaskClaim(Request{Args: map[string]any{
		"from":    "sup",
		"slug":    "feat/x",
		"project": "myproject",
	}})
	if resp.OK {
		t.Fatalf("expected supervisor_cannot_claim, got OK")
	}
	if resp.Code != CodeUnauthorized {
		t.Fatalf("expected code %d (unauthorized), got %d: %s", CodeUnauthorized, resp.Code, resp.Error)
	}
	detail := resp.Data.(map[string]any)["error"].(ErrorDetail)
	if detail.Reason != "supervisor_cannot_claim" {
		t.Fatalf("expected reason supervisor_cannot_claim, got %q", detail.Reason)
	}
}

// TestSessionConflictHarnessMismatch verifies that re-registering a live agent
// from a different harness is rejected.
func TestSessionConflictHarnessMismatch(t *testing.T) {
	s := newFixtureState()
	resp := s.opRegister(Request{Args: map[string]any{
		"name":    "alice",
		"pid":     float64(1),
		"harness": "claude-code",
		"cwd":     "/repo/main",
	}})
	if !resp.OK {
		t.Fatalf("initial register failed: %+v", resp)
	}

	// Re-register alice from a different harness while the lease is live.
	resp2 := s.opRegister(Request{Args: map[string]any{
		"name":    "alice",
		"pid":     float64(1),
		"harness": "codex",
		"cwd":     "/repo/main",
	}})
	if resp2.OK {
		t.Fatalf("expected session_conflict, got OK")
	}
	if resp2.Code != CodeSessionConflict {
		t.Fatalf("expected code %d (session_conflict), got %d: %s", CodeSessionConflict, resp2.Code, resp2.Error)
	}
}

// TestSessionConflictCWDMismatch verifies that re-registering a live agent from
// a different CWD is rejected.
func TestSessionConflictCWDMismatch(t *testing.T) {
	s := newFixtureState()
	resp := s.opRegister(Request{Args: map[string]any{
		"name":    "alice",
		"pid":     float64(1),
		"harness": "claude-code",
		"cwd":     "/repo/worktree-a",
	}})
	if !resp.OK {
		t.Fatalf("initial register failed: %+v", resp)
	}

	resp2 := s.opRegister(Request{Args: map[string]any{
		"name":    "alice",
		"pid":     float64(1),
		"harness": "claude-code",
		"cwd":     "/repo/worktree-b",
	}})
	if resp2.OK {
		t.Fatalf("expected session_conflict, got OK")
	}
	if resp2.Code != CodeSessionConflict {
		t.Fatalf("expected code %d (session_conflict), got %d: %s", CodeSessionConflict, resp2.Code, resp2.Error)
	}
}

// TestSessionConflictSameContextAllowed verifies that re-registering from the
// same harness and CWD is always allowed (normal lease renewal).
func TestSessionConflictSameContextAllowed(t *testing.T) {
	s := newFixtureState()
	resp := s.opRegister(Request{Args: map[string]any{
		"name":    "alice",
		"pid":     float64(1),
		"harness": "claude-code",
		"cwd":     "/repo/main",
	}})
	if !resp.OK {
		t.Fatalf("initial register failed: %+v", resp)
	}

	resp2 := s.opRegister(Request{Args: map[string]any{
		"name":    "alice",
		"pid":     float64(1),
		"harness": "claude-code",
		"cwd":     "/repo/main",
	}})
	if !resp2.OK {
		t.Fatalf("same-context re-register should succeed, got: %s (code=%d)", resp2.Error, resp2.Code)
	}
}

// TestSessionConflictExpiredLeaseAllowed verifies that context changes are
// allowed after the original lease expires.
func TestSessionConflictExpiredLeaseAllowed(t *testing.T) {
	s := newFixtureState()
	resp := s.opRegister(Request{Args: map[string]any{
		"name":    "alice",
		"pid":     float64(1),
		"harness": "claude-code",
		"cwd":     "/repo/main",
	}})
	if !resp.OK {
		t.Fatalf("initial register failed: %+v", resp)
	}

	// Expire the lease.
	s.mu.Lock()
	a := s.agentByName("alice")
	a.ExpiresAt = time.Now().Add(-time.Second)
	s.mu.Unlock()

	resp2 := s.opRegister(Request{Args: map[string]any{
		"name":    "alice",
		"pid":     float64(1),
		"harness": "codex",
		"cwd":     "/other/repo",
	}})
	if !resp2.OK {
		t.Fatalf("expired lease should allow context change, got: %s (code=%d)", resp2.Error, resp2.Code)
	}
}
