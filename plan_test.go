package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// mustRegisterRole registers an agent with a specific role.
func mustRegisterRole(t *testing.T, s *State, name, role string, pid int) {
	t.Helper()
	resp := s.opRegister(Request{Args: map[string]any{"name": name, "pid": float64(pid), "role": role}})
	if !resp.OK {
		t.Fatalf("register %s (role=%s) failed: %+v", name, role, resp)
	}
}

// seedPlan inserts a plan directly into state for test setup.
func seedPlan(s *State, pid, supervisor string) *Plan {
	p := &Plan{ProjectID: pid, Supervisor: supervisor, UpdatedAt: time.Now()}
	s.mu.Lock()
	s.plans[pid] = p
	s.mu.Unlock()
	return p
}

// writePlanToDisk writes a Plan as JSON directly to the workspace for round-trip tests.
func writePlanToDisk(t *testing.T, ws string, p *Plan) {
	t.Helper()
	dir := filepath.Join(ws, "plans", p.ProjectID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		t.Fatalf("marshal plan: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plan.json"), data, 0600); err != nil {
		t.Fatalf("write plan.json: %v", err)
	}
}

// TestRolePersistsAcrossReRegister verifies that role is stored on the Agent
// and survives a subsequent register call that does not re-supply the role.
func TestRolePersistsAcrossReRegister(t *testing.T) {
	s := newFixtureState()
	mustRegisterRole(t, s, "alice", "supervisor", 1)

	s.mu.Lock()
	a := s.agentByName("alice")
	if a == nil || a.Role != "supervisor" {
		s.mu.Unlock()
		t.Fatalf("expected role=supervisor after first register, got %+v", a)
	}
	s.mu.Unlock()

	// Re-register without supplying role; should retain "supervisor".
	resp := s.opRegister(Request{Args: map[string]any{"name": "alice", "pid": float64(2)}})
	if !resp.OK {
		t.Fatalf("re-register failed: %+v", resp)
	}

	s.mu.Lock()
	a = s.agentByName("alice")
	role := ""
	if a != nil {
		role = a.Role
	}
	s.mu.Unlock()
	if role != "supervisor" {
		t.Fatalf("role should persist across re-register; got %q", role)
	}
}

// TestWorkerCannotMutateOtherWorkersRows verifies that a worker cannot flip
// the status of a row owned by another agent.
func TestWorkerCannotMutateOtherWorkersRows(t *testing.T) {
	s := newFixtureState()
	mustRegisterRole(t, s, "sup", "supervisor", 1)
	mustRegisterRole(t, s, "alice", "worker", 2)
	mustRegisterRole(t, s, "bob", "worker", 3)

	p := seedPlan(s, "myproject", "sup")
	p.Assignments = append(p.Assignments, Assignment{
		Slug: "task-1", Owner: "alice", Status: statusAssigned,
	})

	resp := s.opPlanStatus(Request{Args: map[string]any{
		"from": "bob", "project": "myproject", "slug": "task-1", "status": "in-progress",
	}})
	if resp.OK || resp.Code != CodeUnauthorized {
		t.Fatalf("worker mutating other worker's row should be unauthorized: %+v", resp)
	}
}

// TestWorkerCanFlipOwnStatus verifies that a worker can set in-progress/ready/blocked
// on their own row, but not merged.
func TestWorkerCanFlipOwnStatus(t *testing.T) {
	s := newFixtureState()
	mustRegisterRole(t, s, "sup", "supervisor", 1)
	mustRegisterRole(t, s, "alice", "worker", 2)

	p := seedPlan(s, "myproject", "sup")
	p.Assignments = append(p.Assignments, Assignment{
		Slug: "task-1", Owner: "alice", Status: statusAssigned,
	})

	for _, st := range []string{"in-progress", "ready", "blocked"} {
		resp := s.opPlanStatus(Request{Args: map[string]any{
			"from": "alice", "project": "myproject", "slug": "task-1", "status": st,
		}})
		if !resp.OK {
			t.Fatalf("worker flip to %s failed: %+v", st, resp)
		}
	}

	// Worker may not set merged.
	resp := s.opPlanStatus(Request{Args: map[string]any{
		"from": "alice", "project": "myproject", "slug": "task-1", "status": "merged",
	}})
	if resp.OK || resp.Code != CodeUnauthorized {
		t.Fatalf("worker setting merged should be unauthorized: %+v", resp)
	}
}

// TestSupervisorBusyBlocksUnregister verifies that a supervisor with active
// (non-merged) assignments cannot unregister.
func TestSupervisorBusyBlocksUnregister(t *testing.T) {
	t.Setenv("KOPOS_HOME", t.TempDir())
	t.Setenv("KOPOS_WORKSPACE", filepath.Join(t.TempDir(), "workspace"))
	s := newFixtureState()
	mustRegisterRole(t, s, "sup", "supervisor", 1)

	p := seedPlan(s, "myproject", "sup")
	p.Assignments = append(p.Assignments, Assignment{
		Slug: "task-1", Owner: "alice", Status: statusAssigned,
	})

	resp := s.opUnregister(Request{Args: map[string]any{"from": "sup"}})
	if resp.OK || resp.Code != CodeSupervisorBusy {
		t.Fatalf("unregister with active plan should be supervisor_busy: %+v", resp)
	}

	// After all assignments are merged, unregister should succeed.
	p.Assignments[0].Status = statusMerged
	resp = s.opUnregister(Request{Args: map[string]any{"from": "sup"}})
	if !resp.OK {
		t.Fatalf("unregister after merging all assignments should succeed: %+v", resp)
	}
}

// TestPlanHandoffTransfersSupervisorAndRewiresMembership verifies that
// plan handoff atomically transfers supervisor rights and updates room membership.
func TestPlanHandoffTransfersSupervisorAndRewiresMembership(t *testing.T) {
	s := newFixtureState()
	mustRegisterRole(t, s, "sup", "supervisor", 1)
	mustRegisterRole(t, s, "newsup", "supervisor", 2)
	mustRegisterRole(t, s, "alice", "worker", 3)

	p := seedPlan(s, "myproject", "sup")
	p.Assignments = append(p.Assignments, Assignment{
		Slug: "task-1", Owner: "alice", Status: statusInProgress,
	})

	// Create the task-1 room with sup and alice.
	r := s.ensureRoomWithMembers("task-1", "sup", []string{"sup", "alice"})

	resp := s.opPlanHandoff(Request{Args: map[string]any{
		"from": "sup", "project": "myproject", "to": "newsup",
	}})
	if !resp.OK {
		t.Fatalf("handoff failed: %+v", resp)
	}

	s.mu.Lock()
	plan := s.plans["myproject"]
	newSupervisor := plan.Supervisor
	s.mu.Unlock()
	if newSupervisor != "newsup" {
		t.Fatalf("supervisor should be newsup after handoff, got %s", newSupervisor)
	}

	// Room membership should have newsup instead of sup.
	r.mu.Lock()
	_, supMember := r.members["sup"]
	_, newsupMember := r.members["newsup"]
	r.mu.Unlock()
	if supMember {
		t.Fatalf("old supervisor should be removed from room after handoff")
	}
	if !newsupMember {
		t.Fatalf("new supervisor should be added to room after handoff")
	}
}

// TestProjectIDDerivation verifies projectID slugifies remote URLs and falls
// back to the provided basename.
func TestProjectIDDerivation(t *testing.T) {
	cases := []struct {
		repoURL  string
		fallback string
		want     string
	}{
		{"https://github.com/org/my-repo.git", "", "my-repo"},
		{"git@github.com:org/my-repo.git", "", "my-repo"},
		{"https://github.com/org/My_Repo.git", "", "my-repo"},
		{"", "kopos", "kopos"},
		{"", "", "default"},
	}
	for _, c := range cases {
		got := projectID(c.repoURL, c.fallback)
		if got != c.want {
			t.Errorf("projectID(%q, %q) = %q, want %q", c.repoURL, c.fallback, got, c.want)
		}
	}
}

// TestPlanFileRoundTrip verifies that a plan written to disk is correctly
// reloaded by loadPlans on a fresh State (simulates daemon restart).
func TestPlanFileRoundTrip(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "workspace")
	t.Setenv("KOPOS_WORKSPACE", ws)

	original := &Plan{
		ProjectID:  "myproject",
		Supervisor: "sup",
		Assignments: []Assignment{
			{Slug: "task-1", Goal: "do the thing", Status: statusAssigned, Owner: "alice", UpdatedAt: time.Now().Truncate(time.Second)},
		},
		UpdatedAt: time.Now().Truncate(time.Second),
	}
	writePlanToDisk(t, ws, original)

	s2 := newFixtureState()
	if err := s2.loadPlans(); err != nil {
		t.Fatalf("loadPlans: %v", err)
	}
	s2.mu.Lock()
	p, ok := s2.plans["myproject"]
	s2.mu.Unlock()
	if !ok {
		t.Fatalf("plan not loaded after restart")
	}
	if p.Supervisor != "sup" {
		t.Fatalf("supervisor mismatch: %s", p.Supervisor)
	}
	if len(p.Assignments) != 1 || p.Assignments[0].Slug != "task-1" {
		t.Fatalf("assignments mismatch: %+v", p.Assignments)
	}
}

// TestMergedAssignmentRestoresArchivedRoomOnLoad verifies that loadPlans
// recreates an archived room stub for merged assignments (daemon restart case).
func TestMergedAssignmentRestoresArchivedRoomOnLoad(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "workspace")
	t.Setenv("KOPOS_WORKSPACE", ws)

	original := &Plan{
		ProjectID:  "myproject",
		Supervisor: "sup",
		Assignments: []Assignment{
			{Slug: "done-task", Status: statusMerged, Owner: "alice", UpdatedAt: time.Now()},
		},
		UpdatedAt: time.Now(),
	}
	writePlanToDisk(t, ws, original)

	s := newFixtureState()
	if err := s.loadPlans(); err != nil {
		t.Fatalf("loadPlans: %v", err)
	}
	s.mu.Lock()
	r, ok := s.rooms["done-task"]
	s.mu.Unlock()
	if !ok {
		t.Fatalf("archived room stub should have been created for merged assignment")
	}
	r.mu.Lock()
	archived := r.Archived
	r.mu.Unlock()
	if !archived {
		t.Fatalf("room should be archived for merged assignment")
	}
}

// TestPlanAssignAutoCreatesRoom verifies that plan assign creates the slug's
// room and adds both supervisor and owner as members.
func TestPlanAssignAutoCreatesRoom(t *testing.T) {
	s := newFixtureState()
	mustRegisterRole(t, s, "sup", "supervisor", 1)
	mustRegisterRole(t, s, "alice", "worker", 2)

	seedPlan(s, "myproject", "sup")

	resp := s.opPlanAssign(Request{Args: map[string]any{
		"from": "sup", "project": "myproject", "slug": "feat-x", "owner": "alice",
	}})
	if !resp.OK {
		t.Fatalf("plan assign failed: %+v", resp)
	}

	s.mu.Lock()
	r, ok := s.rooms["feat-x"]
	s.mu.Unlock()
	if !ok {
		t.Fatalf("room feat-x should have been created")
	}
	r.mu.Lock()
	supMember := r.members["sup"]
	aliceMember := r.members["alice"]
	r.mu.Unlock()
	if !supMember {
		t.Fatalf("supervisor should be a room member after assign")
	}
	if !aliceMember {
		t.Fatalf("owner should be a room member after assign")
	}
}

// TestUnassignAndMergedArchiveRoom verifies that both plan unassign and
// status=merged archive the slug room, blocking further posts.
func TestUnassignAndMergedArchiveRoom(t *testing.T) {
	for _, scenario := range []string{"unassign", "merged"} {
		t.Run(scenario, func(t *testing.T) {
			s := newFixtureState()
			mustRegisterRole(t, s, "sup", "supervisor", 1)
			mustRegisterRole(t, s, "alice", "worker", 2)

			p := seedPlan(s, "myproject", "sup")
			p.Assignments = append(p.Assignments, Assignment{
				Slug: "feat-x", Owner: "alice", Status: statusInProgress,
			})
			// Create room and join sup so opPost membership check passes later.
			r := s.ensureRoomWithMembers("feat-x", "sup", []string{"sup", "alice"})

			var resp Response
			if scenario == "unassign" {
				resp = s.opPlanUnassign(Request{Args: map[string]any{
					"from": "sup", "project": "myproject", "slug": "feat-x",
				}})
			} else {
				resp = s.opPlanStatus(Request{Args: map[string]any{
					"from": "sup", "project": "myproject", "slug": "feat-x", "status": "merged",
				}})
			}
			if !resp.OK {
				t.Fatalf("%s failed: %+v", scenario, resp)
			}

			r.mu.Lock()
			archived := r.Archived
			r.mu.Unlock()
			if !archived {
				t.Fatalf("room should be archived after %s", scenario)
			}

			// Post to archived room must be refused.
			post := s.opPost(Request{Args: map[string]any{
				"from": "sup", "room": "feat-x", "body": "hello",
			}})
			if post.OK {
				t.Fatalf("post to archived room should fail after %s", scenario)
			}
		})
	}
}

// TestKickoffDeliveredOnFirstRegisterNotReplayed verifies that a kickoff is
// delivered on the owner's first register and is not replayed on subsequent registers.
func TestKickoffDeliveredOnFirstRegisterNotReplayed(t *testing.T) {
	s := newFixtureState()
	mustRegisterRole(t, s, "sup", "supervisor", 1)

	p := seedPlan(s, "myproject", "sup")
	p.Assignments = append(p.Assignments, Assignment{
		Slug:             "feat-x",
		Owner:            "alice",
		Status:           statusAssigned,
		Kickoff:          "here is your kickoff briefing",
		KickoffDelivered: false,
	})

	// Register alice — should trigger kickoff delivery.
	mustRegisterRole(t, s, "alice", "worker", 2)

	s.mu.Lock()
	plan := s.plans["myproject"]
	delivered := plan.Assignments[0].KickoffDelivered
	s.mu.Unlock()
	if !delivered {
		t.Fatalf("kickoff_delivered should be true after first register")
	}

	s.mu.Lock()
	r, ok := s.rooms["feat-x"]
	s.mu.Unlock()
	if !ok {
		t.Fatalf("assignment room should have been created for kickoff delivery")
	}
	r.mu.Lock()
	msgCount := len(r.log)
	r.mu.Unlock()
	if msgCount == 0 {
		t.Fatalf("kickoff message should have been posted to room")
	}

	// Re-register alice — kickoff must NOT be re-delivered.
	s.opRegister(Request{Args: map[string]any{"name": "alice", "pid": float64(3)}})
	r.mu.Lock()
	msgCountAfter := len(r.log)
	r.mu.Unlock()
	if msgCountAfter != msgCount {
		t.Fatalf("kickoff should not be re-delivered on subsequent register; msg count %d → %d", msgCount, msgCountAfter)
	}
}
