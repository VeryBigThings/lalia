package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
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

// mustRegisterFor registers an agent with role + project metadata so
// project-identity checks on publish pass.
func mustRegisterFor(t *testing.T, s *State, name, role, project, repoRoot string, pid int) {
	t.Helper()
	resp := s.opRegister(Request{Args: map[string]any{
		"name":      name,
		"pid":       float64(pid),
		"role":      role,
		"project":   project,
		"repo_root": repoRoot,
	}})
	if !resp.OK {
		t.Fatalf("register %s failed: %+v", name, resp)
	}
}

// seedTaskList inserts a task list directly into state for test setup.
func seedTaskList(s *State, pid, supervisor string) *TaskList {
	tl := &TaskList{ProjectID: pid, Supervisor: supervisor, UpdatedAt: time.Now()}
	s.mu.Lock()
	s.tasks[pid] = tl
	s.mu.Unlock()
	return tl
}

// writeTaskListToDisk writes a TaskList as JSON to the workspace for round-trip tests.
func writeTaskListToDisk(t *testing.T, ws string, tl *TaskList) {
	t.Helper()
	dir := filepath.Join(ws, "tasks", tl.ProjectID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	data, err := json.MarshalIndent(tl, "", "  ")
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "task-list.json"), data, 0600); err != nil {
		t.Fatalf("write task-list.json: %v", err)
	}
}

// mustInitGitRepo creates a bare temporary git repo with one commit and
// returns its root. The repo has a single branch `main`.
func mustInitGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "--initial-branch=main", ".")
	// Empty commit so HEAD has a tree.
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("seed\n"), 0644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	run("add", "README")
	run("commit", "-m", "init")
	return dir
}

// TestRolePersistsAcrossReRegister verifies role is stored on Agent and
// survives a re-register that does not re-supply it.
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

func TestWorkerCannotMutateOtherWorkersRows(t *testing.T) {
	s := newFixtureState()
	mustRegisterRole(t, s, "sup", "supervisor", 1)
	mustRegisterRole(t, s, "alice", "worker", 2)
	mustRegisterRole(t, s, "bob", "worker", 3)

	tl := seedTaskList(s, "myproject", "sup")
	tl.Tasks = append(tl.Tasks, Task{Slug: "task-1", Owner: "alice", Status: statusAssigned})

	resp := s.opTaskStatus(Request{Args: map[string]any{
		"from": "bob", "project": "myproject", "slug": "task-1", "status": "in-progress",
	}})
	if resp.OK || resp.Code != CodeUnauthorized {
		t.Fatalf("worker mutating another worker's row should be unauthorized: %+v", resp)
	}
}

func TestWorkerCanFlipOwnStatus(t *testing.T) {
	s := newFixtureState()
	mustRegisterRole(t, s, "sup", "supervisor", 1)
	mustRegisterRole(t, s, "alice", "worker", 2)

	tl := seedTaskList(s, "myproject", "sup")
	tl.Tasks = append(tl.Tasks, Task{Slug: "task-1", Owner: "alice", Status: statusAssigned})

	for _, st := range []string{"in-progress", "ready", "blocked"} {
		resp := s.opTaskStatus(Request{Args: map[string]any{
			"from": "alice", "project": "myproject", "slug": "task-1", "status": st,
		}})
		if !resp.OK {
			t.Fatalf("worker flip to %s failed: %+v", st, resp)
		}
	}
	resp := s.opTaskStatus(Request{Args: map[string]any{
		"from": "alice", "project": "myproject", "slug": "task-1", "status": "merged",
	}})
	if resp.OK || resp.Code != CodeUnauthorized {
		t.Fatalf("worker setting merged should be unauthorized: %+v", resp)
	}
}

func TestSupervisorBusyBlocksUnregister(t *testing.T) {
	t.Setenv("LALIA_HOME", t.TempDir())
	t.Setenv("LALIA_WORKSPACE", filepath.Join(t.TempDir(), "workspace"))
	s := newFixtureState()
	mustRegisterRole(t, s, "sup", "supervisor", 1)

	tl := seedTaskList(s, "myproject", "sup")
	tl.Tasks = append(tl.Tasks, Task{Slug: "task-1", Owner: "alice", Status: statusAssigned})

	resp := s.opUnregister(Request{Args: map[string]any{"from": "sup"}})
	if resp.OK || resp.Code != CodeSupervisorBusy {
		t.Fatalf("unregister with active task should be supervisor_busy: %+v", resp)
	}

	tl.Tasks[0].Status = statusMerged
	resp = s.opUnregister(Request{Args: map[string]any{"from": "sup"}})
	if !resp.OK {
		t.Fatalf("unregister after merging all tasks should succeed: %+v", resp)
	}
}

func TestTaskHandoffTransfersSupervisorAndRewiresMembership(t *testing.T) {
	s := newFixtureState()
	mustRegisterRole(t, s, "sup", "supervisor", 1)
	mustRegisterRole(t, s, "newsup", "supervisor", 2)
	mustRegisterRole(t, s, "alice", "worker", 3)

	tl := seedTaskList(s, "myproject", "sup")
	tl.Tasks = append(tl.Tasks, Task{Slug: "task-1", Owner: "alice", Status: statusInProgress})

	r := s.ensureRoomWithMembers("task-1", "sup", []string{"sup", "alice"})

	resp := s.opTaskHandoff(Request{Args: map[string]any{
		"from": "sup", "project": "myproject", "to": "newsup",
	}})
	if !resp.OK {
		t.Fatalf("handoff failed: %+v", resp)
	}

	s.mu.Lock()
	gotTL := s.tasks["myproject"]
	newSupervisor := gotTL.Supervisor
	s.mu.Unlock()
	if newSupervisor != "newsup" {
		t.Fatalf("supervisor should be newsup after handoff, got %s", newSupervisor)
	}

	r.mu.Lock()
	supMember := r.members["sup"]
	newsupMember := r.members["newsup"]
	r.mu.Unlock()
	if supMember {
		t.Fatalf("old supervisor should be removed from room after handoff")
	}
	if !newsupMember {
		t.Fatalf("new supervisor should be added to room after handoff")
	}
}

func TestProjectIDDerivation(t *testing.T) {
	cases := []struct {
		repoURL, fallback, want string
	}{
		{"https://github.com/org/my-repo.git", "", "my-repo"},
		{"git@github.com:org/my-repo.git", "", "my-repo"},
		{"https://github.com/org/My_Repo.git", "", "my-repo"},
		{"", "lalia", "lalia"},
		{"", "", "default"},
	}
	for _, c := range cases {
		got := projectID(c.repoURL, c.fallback)
		if got != c.want {
			t.Errorf("projectID(%q, %q) = %q, want %q", c.repoURL, c.fallback, got, c.want)
		}
	}
}

func TestTaskListRoundTrip(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "workspace")
	t.Setenv("LALIA_WORKSPACE", ws)

	original := &TaskList{
		ProjectID:  "myproject",
		Supervisor: "sup",
		Tasks: []Task{
			{Slug: "task-1", Brief: "do the thing", Status: statusAssigned, Owner: "alice", UpdatedAt: time.Now().Truncate(time.Second)},
		},
		UpdatedAt: time.Now().Truncate(time.Second),
	}
	writeTaskListToDisk(t, ws, original)

	s := newFixtureState()
	if err := s.loadTaskLists(); err != nil {
		t.Fatalf("loadTaskLists: %v", err)
	}
	s.mu.Lock()
	got, ok := s.tasks["myproject"]
	s.mu.Unlock()
	if !ok {
		t.Fatalf("task list not loaded after restart")
	}
	if got.Supervisor != "sup" {
		t.Fatalf("supervisor mismatch: %s", got.Supervisor)
	}
	if len(got.Tasks) != 1 || got.Tasks[0].Slug != "task-1" {
		t.Fatalf("tasks mismatch: %+v", got.Tasks)
	}
}

func TestUnassignLeavesRoomLive(t *testing.T) {
	s := newFixtureState()
	mustRegisterRole(t, s, "sup", "supervisor", 1)
	mustRegisterRole(t, s, "alice", "worker", 2)

	tl := seedTaskList(s, "myproject", "sup")
	tl.Tasks = append(tl.Tasks, Task{Slug: "feat-x", Owner: "alice", Status: statusInProgress})
	r := s.ensureRoomWithMembers("feat-x", "sup", []string{"sup", "alice"})

	resp := s.opTaskUnassign(Request{Args: map[string]any{
		"from": "sup", "project": "myproject", "slug": "feat-x",
	}})
	if !resp.OK {
		t.Fatalf("unassign failed: %+v", resp)
	}
	r.mu.Lock()
	archived := r.Archived
	r.mu.Unlock()
	if archived {
		t.Fatalf("unassign must not archive room (archival is opt-in via rooms gc)")
	}

	post := s.opPost(Request{Args: map[string]any{
		"from": "sup", "room": "feat-x", "body": "hello",
	}})
	if !post.OK {
		t.Fatalf("post to live room after unassign should succeed: %+v", post)
	}
}

func TestRoomsGCArchivesMergedRooms(t *testing.T) {
	s := newFixtureState()
	mustRegisterRole(t, s, "sup", "supervisor", 1)
	mustRegisterRole(t, s, "alice", "worker", 2)

	tl := seedTaskList(s, "myproject", "sup")
	tl.Tasks = []Task{
		{Slug: "done-a", Owner: "alice", Status: statusMerged},
		{Slug: "done-b", Owner: "alice", Status: statusMerged},
		{Slug: "live-c", Owner: "alice", Status: statusInProgress},
	}
	for _, slug := range []string{"done-a", "done-b", "live-c"} {
		s.ensureRoomWithMembers(slug, "sup", []string{"sup", "alice"})
	}

	resp := s.opRoomsGC(Request{Args: map[string]any{"from": "sup"}})
	if !resp.OK {
		t.Fatalf("rooms gc failed: %+v", resp)
	}
	data := resp.Data.(map[string]any)
	if cnt, _ := data["count"].(int); cnt != 2 {
		t.Fatalf("expected 2 rooms archived, got %v", data["count"])
	}

	for _, slug := range []string{"done-a", "done-b"} {
		s.mu.Lock()
		r := s.rooms[slug]
		s.mu.Unlock()
		r.mu.Lock()
		archived := r.Archived
		r.mu.Unlock()
		if !archived {
			t.Fatalf("merged room %s should be archived after gc", slug)
		}
	}
	s.mu.Lock()
	liveRoom := s.rooms["live-c"]
	s.mu.Unlock()
	liveRoom.mu.Lock()
	liveArchived := liveRoom.Archived
	liveRoom.mu.Unlock()
	if liveArchived {
		t.Fatalf("in-progress room live-c must NOT be archived by gc")
	}

	resp2 := s.opRoomsGC(Request{Args: map[string]any{"from": "sup"}})
	if !resp2.OK {
		t.Fatalf("second rooms gc failed: %+v", resp2)
	}
	data2 := resp2.Data.(map[string]any)
	if cnt, _ := data2["count"].(int); cnt != 0 {
		t.Fatalf("idempotent gc should archive 0 on second run, got %v", cnt)
	}
}

func TestRoomsGCWorkerRejected(t *testing.T) {
	s := newFixtureState()
	mustRegisterRole(t, s, "alice", "worker", 1)
	resp := s.opRoomsGC(Request{Args: map[string]any{"from": "alice"}})
	if resp.OK {
		t.Fatalf("worker must not be allowed to rooms gc")
	}
	if resp.Code != CodeUnauthorized {
		t.Fatalf("expected unauthorized, got code=%d", resp.Code)
	}
}

// TestTaskPublishCreatesWorktreeRoomAndBundle checks the happy path.
func TestTaskPublishCreatesWorktreeRoomAndBundle(t *testing.T) {
	repoRoot := mustInitGitRepo(t)

	s := newFixtureState()
	mustRegisterFor(t, s, "sup", "supervisor", "myproject", repoRoot, 1)

	payload := map[string]any{
		"from":      "sup",
		"project":   "myproject",
		"repo_root": repoRoot,
		"workstreams": []any{
			map[string]any{
				"slug":        "feat-x",
				"branch":      "feat/x",
				"brief":       "Do the X thing.",
				"owned_paths": []any{"src/x/**"},
				"contracts":   []any{map[string]any{"other_slug": "feat-y", "note": "consumes Y"}},
			},
		},
	}
	resp := s.opTaskPublish(Request{Args: payload})
	if !resp.OK {
		t.Fatalf("publish failed: %+v", resp)
	}
	data := resp.Data.(map[string]any)
	ok, _ := data["ok"].([]any)
	if len(ok) != 1 {
		t.Fatalf("expected 1 ok slug, got %+v", data)
	}

	// Worktree created at <parent>/wt/feat-x
	expectedWt := filepath.Join(filepath.Dir(repoRoot), "wt", "feat-x")
	if st, err := os.Stat(expectedWt); err != nil || !st.IsDir() {
		t.Fatalf("expected worktree at %s, got err=%v", expectedWt, err)
	}

	// Task row present with status=open
	s.mu.Lock()
	tl := s.tasks["myproject"]
	s.mu.Unlock()
	if tl == nil || len(tl.Tasks) != 1 || tl.Tasks[0].Slug != "feat-x" || tl.Tasks[0].Status != statusOpen {
		t.Fatalf("task list not populated correctly: %+v", tl)
	}

	// Room exists with bundle as first message.
	s.mu.Lock()
	r := s.rooms["feat-x"]
	s.mu.Unlock()
	if r == nil {
		t.Fatalf("room feat-x should exist")
	}
	r.mu.Lock()
	if len(r.log) != 1 {
		r.mu.Unlock()
		t.Fatalf("expected 1 bundle post, got %d", len(r.log))
	}
	body := r.log[0].Body
	r.mu.Unlock()
	if !bytes.Contains([]byte(body), []byte("Do the X thing")) {
		t.Fatalf("bundle should contain brief, got %q", body)
	}
	if !bytes.Contains([]byte(body), []byte("src/x/**")) {
		t.Fatalf("bundle should include owned_paths, got %q", body)
	}
	if !bytes.Contains([]byte(body), []byte("feat-y")) {
		t.Fatalf("bundle should include contract slug, got %q", body)
	}
}

// TestTaskPublishIsIdempotent re-runs publish with the same payload and
// asserts no new worktree/room posts.
func TestTaskPublishIsIdempotent(t *testing.T) {
	repoRoot := mustInitGitRepo(t)
	s := newFixtureState()
	mustRegisterFor(t, s, "sup", "supervisor", "myproject", repoRoot, 1)

	payload := map[string]any{
		"from":      "sup",
		"project":   "myproject",
		"repo_root": repoRoot,
		"workstreams": []any{
			map[string]any{"slug": "feat-x", "branch": "feat/x", "brief": "first pass"},
		},
	}
	r1 := s.opTaskPublish(Request{Args: payload})
	if !r1.OK {
		t.Fatalf("first publish failed: %+v", r1)
	}
	s.mu.Lock()
	room := s.rooms["feat-x"]
	s.mu.Unlock()
	room.mu.Lock()
	postsBefore := len(room.log)
	room.mu.Unlock()

	r2 := s.opTaskPublish(Request{Args: payload})
	if !r2.OK {
		t.Fatalf("second publish failed: %+v", r2)
	}
	room.mu.Lock()
	postsAfter := len(room.log)
	room.mu.Unlock()

	if postsAfter != postsBefore {
		t.Fatalf("republish must not re-post bundle: %d → %d", postsBefore, postsAfter)
	}
}

// TestTaskClaimAutoJoinsRoomAndReturnsBundle verifies claim joins the worker
// and surfaces the bundle post.
func TestTaskClaimAutoJoinsRoomAndReturnsBundle(t *testing.T) {
	repoRoot := mustInitGitRepo(t)
	s := newFixtureState()
	mustRegisterFor(t, s, "sup", "supervisor", "myproject", repoRoot, 1)
	mustRegisterRole(t, s, "alice", "worker", 2)

	pub := s.opTaskPublish(Request{Args: map[string]any{
		"from": "sup", "project": "myproject", "repo_root": repoRoot,
		"workstreams": []any{map[string]any{"slug": "feat-x", "branch": "feat/x", "brief": "hello"}},
	}})
	if !pub.OK {
		t.Fatalf("publish: %+v", pub)
	}

	resp := s.opTaskClaim(Request{Args: map[string]any{
		"from": "alice", "project": "myproject", "slug": "feat-x",
	}})
	if !resp.OK {
		t.Fatalf("claim failed: %+v", resp)
	}
	data := resp.Data.(map[string]any)
	if data["status"] != statusInProgress {
		t.Fatalf("claim should set status=in-progress, got %v", data["status"])
	}
	if data["owner"] != "alice" {
		t.Fatalf("claim should set owner=alice, got %v", data["owner"])
	}
	bundle, _ := data["bundle"].(map[string]any)
	if bundle == nil {
		t.Fatalf("claim response should include bundle")
	}
	if body, _ := bundle["body"].(string); body == "" {
		t.Fatalf("bundle body should not be empty")
	}

	s.mu.Lock()
	r := s.rooms["feat-x"]
	s.mu.Unlock()
	r.mu.Lock()
	isMember := r.members["alice"]
	r.mu.Unlock()
	if !isMember {
		t.Fatalf("claim should auto-join worker to room")
	}
}

// TestTaskBulletinReturnsOpenRowsRegardlessOfRole verifies a stranger agent
// (not supervisor, not owner) can see open rows.
func TestTaskBulletinReturnsOpenRowsRegardlessOfRole(t *testing.T) {
	s := newFixtureState()
	mustRegisterRole(t, s, "sup", "supervisor", 1)
	mustRegisterRole(t, s, "stranger", "worker", 2)

	tl := seedTaskList(s, "myproject", "sup")
	tl.Tasks = []Task{
		{Slug: "open-a", Status: statusOpen, Brief: "line one\nline two"},
		{Slug: "assigned-b", Status: statusInProgress, Owner: "someone-else"},
		{Slug: "open-c", Status: statusOpen, Brief: "another"},
	}

	resp := s.opTaskBulletin(Request{Args: map[string]any{
		"from": "stranger", "project": "myproject",
	}})
	if !resp.OK {
		t.Fatalf("bulletin failed: %+v", resp)
	}
	data := resp.Data.(map[string]any)
	rows, _ := data["tasks"].([]any)
	if len(rows) != 2 {
		t.Fatalf("bulletin should return 2 open rows, got %d: %+v", len(rows), rows)
	}
	first := rows[0].(map[string]any)
	if first["brief_summary"] != "line one" {
		t.Fatalf("brief_summary should be first non-empty line, got %q", first["brief_summary"])
	}
}

// TestTaskReassignRewiresRoomMembership verifies reassign moves ownership and
// updates room membership.
func TestTaskReassignRewiresRoomMembership(t *testing.T) {
	s := newFixtureState()
	mustRegisterRole(t, s, "sup", "supervisor", 1)
	mustRegisterRole(t, s, "alice", "worker", 2)
	mustRegisterRole(t, s, "bob", "worker", 3)

	tl := seedTaskList(s, "myproject", "sup")
	tl.Tasks = append(tl.Tasks, Task{Slug: "feat-x", Owner: "alice", Status: statusInProgress})
	r := s.ensureRoomWithMembers("feat-x", "sup", []string{"sup", "alice"})

	resp := s.opTaskReassign(Request{Args: map[string]any{
		"from": "sup", "project": "myproject", "slug": "feat-x", "owner": "bob",
	}})
	if !resp.OK {
		t.Fatalf("reassign failed: %+v", resp)
	}

	s.mu.Lock()
	t1 := findTask(tl, "feat-x")
	owner := t1.Owner
	s.mu.Unlock()
	if owner != "bob" {
		t.Fatalf("reassign should set owner=bob, got %s", owner)
	}

	r.mu.Lock()
	aliceMember := r.members["alice"]
	bobMember := r.members["bob"]
	r.mu.Unlock()
	if aliceMember {
		t.Fatalf("old owner alice should be removed from room")
	}
	if !bobMember {
		t.Fatalf("new owner bob should be added to room")
	}
}

// TestBootMigrationConvertsPlansDirToTasks seeds a legacy plans/ tree and
// verifies the one-shot migration renames everything into tasks/ with the
// new schema.
func TestBootMigrationConvertsPlansDirToTasks(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "workspace")
	t.Setenv("LALIA_WORKSPACE", ws)

	legacy := map[string]any{
		"project_id": "myproject",
		"supervisor": "sup",
		"assignments": []any{
			map[string]any{
				"slug":       "feat-old",
				"goal":       "legacy brief",
				"owner":      "alice",
				"status":     "in-progress",
				"updated_at": time.Now().Format(time.RFC3339),
			},
		},
		"updated_at": time.Now().Format(time.RFC3339),
	}
	legacyDir := filepath.Join(ws, "plans", "myproject")
	if err := os.MkdirAll(legacyDir, 0755); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	b, _ := json.MarshalIndent(legacy, "", "  ")
	if err := os.WriteFile(filepath.Join(legacyDir, "plan.json"), b, 0644); err != nil {
		t.Fatalf("write legacy plan.json: %v", err)
	}

	if err := migratePlansToTasks(); err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	// New layout exists.
	newPath := filepath.Join(ws, "tasks", "myproject", "task-list.json")
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("migrated file missing at %s: %v", newPath, err)
	}
	// Old layout gone.
	if _, err := os.Stat(filepath.Join(ws, "plans")); !os.IsNotExist(err) {
		t.Fatalf("plans/ should be removed after migration: err=%v", err)
	}

	// Contents mapped correctly.
	raw, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("read migrated file: %v", err)
	}
	var tl TaskList
	if err := json.Unmarshal(raw, &tl); err != nil {
		t.Fatalf("parse migrated: %v", err)
	}
	if tl.Supervisor != "sup" || len(tl.Tasks) != 1 {
		t.Fatalf("migrated shape wrong: %+v", tl)
	}
	if tl.Tasks[0].Slug != "feat-old" || tl.Tasks[0].Brief != "legacy brief" {
		t.Fatalf("migrated task wrong: %+v", tl.Tasks[0])
	}

	// Idempotent: running again is a no-op.
	if err := migratePlansToTasks(); err != nil {
		t.Fatalf("second migration should succeed: %v", err)
	}
}

// TestInitAndPromptEmitByteIdenticalOutput guards the contract that `lalia
// init <role>` and `lalia prompt <role>` emit the same bytes.
func TestInitAndPromptEmitByteIdenticalOutput(t *testing.T) {
	for _, role := range []string{"worker", "supervisor"} {
		p1, err := promptForRole(role)
		if err != nil {
			t.Fatalf("prompt for role %s: %v", role, err)
		}
		// The cmdInit and cmdPrompt handlers both call promptForRole, so if
		// that single source of truth exists, byte-equality is guaranteed.
		// This test pins the contract so future edits don't drift.
		p2, err := promptForRole(role)
		if err != nil {
			t.Fatalf("second prompt call for %s: %v", role, err)
		}
		if p1 != p2 {
			t.Fatalf("prompt for %s drifted between calls", role)
		}
	}
}

// setupPublishedTask is a small helper: fresh state + git repo + one
// published task named "feat-x" on branch feat/x with the given brief.
func setupPublishedTask(t *testing.T, brief string) (*State, string, string) {
	t.Helper()
	repoRoot := mustInitGitRepo(t)
	s := newFixtureState()
	mustRegisterFor(t, s, "sup", "supervisor", "myproject", repoRoot, 1)
	pub := s.opTaskPublish(Request{Args: map[string]any{
		"from": "sup", "project": "myproject", "repo_root": repoRoot,
		"workstreams": []any{map[string]any{"slug": "feat-x", "branch": "feat/x", "brief": brief}},
	}})
	if !pub.OK {
		t.Fatalf("publish: %+v", pub)
	}
	wt := filepath.Join(filepath.Dir(repoRoot), "wt", "feat-x")
	return s, repoRoot, wt
}

// TestTaskUnpublishPreservesWorktreeByDefault: default unpublish on a
// clean/unowned task drops the row and archives the room but leaves the
// worktree on disk.
func TestTaskUnpublishPreservesWorktreeByDefault(t *testing.T) {
	s, _, wt := setupPublishedTask(t, "typo")

	resp := s.opTaskUnpublish(Request{Args: map[string]any{
		"from": "sup", "project": "myproject", "slug": "feat-x",
	}})
	if !resp.OK {
		t.Fatalf("unpublish should succeed: %+v", resp)
	}
	data := resp.Data.(map[string]any)
	if data["worktree_removed"] != false {
		t.Fatalf("default unpublish must NOT remove worktree, got %v", data["worktree_removed"])
	}
	if data["worktree_preserved"] != "default" {
		t.Fatalf("expected worktree_preserved=default, got %v", data["worktree_preserved"])
	}

	// Row gone.
	s.mu.Lock()
	tl := s.tasks["myproject"]
	s.mu.Unlock()
	if findTask(tl, "feat-x") != nil {
		t.Fatalf("row should be gone")
	}
	// Worktree intact on disk.
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("worktree must still exist on disk after default unpublish, err=%v", err)
	}
	// Room archived.
	s.mu.Lock()
	r := s.rooms["feat-x"]
	s.mu.Unlock()
	r.mu.Lock()
	archived := r.Archived
	r.mu.Unlock()
	if !archived {
		t.Fatalf("room should be archived")
	}
}

// TestTaskUnpublishForceKeepsWorktreeIntact: --force allows dropping a
// claimed task's row but must not touch the worktree.
func TestTaskUnpublishForceKeepsWorktreeIntact(t *testing.T) {
	s, _, wt := setupPublishedTask(t, "hi")
	mustRegisterRole(t, s, "alice", "worker", 2)
	if r := s.opTaskClaim(Request{Args: map[string]any{
		"from": "alice", "project": "myproject", "slug": "feat-x",
	}}); !r.OK {
		t.Fatalf("claim: %+v", r)
	}

	// Without --force: refused.
	resp := s.opTaskUnpublish(Request{Args: map[string]any{
		"from": "sup", "project": "myproject", "slug": "feat-x",
	}})
	if resp.OK {
		t.Fatalf("claimed task unpublish without --force should fail")
	}

	// With --force: row gone, worktree intact.
	resp2 := s.opTaskUnpublish(Request{Args: map[string]any{
		"from": "sup", "project": "myproject", "slug": "feat-x", "force": true,
	}})
	if !resp2.OK {
		t.Fatalf("--force unpublish: %+v", resp2)
	}
	data := resp2.Data.(map[string]any)
	if data["worktree_removed"] != false {
		t.Fatalf("--force (without --wipe-worktree) must NOT remove worktree")
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("worktree must still exist after --force unpublish, err=%v", err)
	}
}

// TestTaskUnpublishWipeWithExpiredOwnerWipes: --wipe-worktree with an
// owner whose lease has expired wipes the worktree.
func TestTaskUnpublishWipeWithExpiredOwnerWipes(t *testing.T) {
	s, _, wt := setupPublishedTask(t, "hi")
	mustRegisterRole(t, s, "alice", "worker", 2)
	if r := s.opTaskClaim(Request{Args: map[string]any{
		"from": "alice", "project": "myproject", "slug": "feat-x",
	}}); !r.OK {
		t.Fatalf("claim: %+v", r)
	}
	// Expire alice's lease.
	s.mu.Lock()
	a := s.agentByName("alice")
	a.ExpiresAt = time.Now().Add(-time.Minute)
	s.mu.Unlock()

	resp := s.opTaskUnpublish(Request{Args: map[string]any{
		"from": "sup", "project": "myproject", "slug": "feat-x",
		"force": true, "wipe_worktree": true,
	}})
	if !resp.OK {
		t.Fatalf("wipe with expired owner should succeed: %+v", resp)
	}
	data := resp.Data.(map[string]any)
	if data["worktree_removed"] != true {
		t.Fatalf("worktree_removed should be true, got %v", data["worktree_removed"])
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("worktree should be gone from disk, err=%v", err)
	}
}

// TestTaskUnpublishWipeRefusesLiveOwner: --wipe-worktree with a live
// owner lease is refused. Whole call fails; nothing changes.
func TestTaskUnpublishWipeRefusesLiveOwner(t *testing.T) {
	s, _, wt := setupPublishedTask(t, "hi")
	mustRegisterRole(t, s, "alice", "worker", 2)
	if r := s.opTaskClaim(Request{Args: map[string]any{
		"from": "alice", "project": "myproject", "slug": "feat-x",
	}}); !r.OK {
		t.Fatalf("claim: %+v", r)
	}
	// alice's lease is live from register.

	resp := s.opTaskUnpublish(Request{Args: map[string]any{
		"from": "sup", "project": "myproject", "slug": "feat-x",
		"force": true, "wipe_worktree": true,
	}})
	if resp.OK {
		t.Fatalf("wipe with live owner should refuse")
	}
	// Structured error mentions the owner.
	errDetail, _ := resp.Data.(map[string]any)["error"].(ErrorDetail)
	if errDetail.Reason != "owner_lease_live" {
		t.Fatalf("expected reason=owner_lease_live, got %+v", resp.Data)
	}
	// State unchanged: row + worktree intact.
	s.mu.Lock()
	tl := s.tasks["myproject"]
	s.mu.Unlock()
	if findTask(tl, "feat-x") == nil {
		t.Fatalf("row should still exist after refused wipe")
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("worktree should still exist after refused wipe, err=%v", err)
	}
}

// TestTaskUnpublishEvictOwnerOverridesLiveLease: --evict-owner allows
// wipe to proceed over a live lease.
func TestTaskUnpublishEvictOwnerOverridesLiveLease(t *testing.T) {
	s, _, wt := setupPublishedTask(t, "hi")
	mustRegisterRole(t, s, "alice", "worker", 2)
	if r := s.opTaskClaim(Request{Args: map[string]any{
		"from": "alice", "project": "myproject", "slug": "feat-x",
	}}); !r.OK {
		t.Fatalf("claim: %+v", r)
	}

	resp := s.opTaskUnpublish(Request{Args: map[string]any{
		"from": "sup", "project": "myproject", "slug": "feat-x",
		"force": true, "wipe_worktree": true, "evict_owner": true,
	}})
	if !resp.OK {
		t.Fatalf("wipe with --evict-owner over live lease: %+v", resp)
	}
	data := resp.Data.(map[string]any)
	if data["worktree_removed"] != true {
		t.Fatalf("worktree_removed should be true after evict, got %v", data["worktree_removed"])
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("worktree should be gone after evict, err=%v", err)
	}
}

// TestTaskUnpublishRefusesDirtyWorktreeEvenWithEvict: dirty worktree is
// a hard gate that no flag overrides.
func TestTaskUnpublishRefusesDirtyWorktreeEvenWithEvict(t *testing.T) {
	s, _, wt := setupPublishedTask(t, "hi")
	if err := os.WriteFile(filepath.Join(wt, "scratch"), []byte("wip\n"), 0644); err != nil {
		t.Fatalf("dirty worktree: %v", err)
	}

	resp := s.opTaskUnpublish(Request{Args: map[string]any{
		"from": "sup", "project": "myproject", "slug": "feat-x",
		"force": true, "wipe_worktree": true, "evict_owner": true,
	}})
	if resp.OK {
		t.Fatalf("dirty worktree must refuse even with --evict-owner")
	}
	// Worktree + row intact.
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("worktree should still exist, err=%v", err)
	}
	s.mu.Lock()
	tl := s.tasks["myproject"]
	s.mu.Unlock()
	if findTask(tl, "feat-x") == nil {
		t.Fatalf("row should still exist after refused dirty-wipe")
	}
}

// TestTaskUnpublishWorkerForbidden: only supervisors may unpublish.
func TestTaskUnpublishWorkerForbidden(t *testing.T) {
	s, _, _ := setupPublishedTask(t, "hi")
	mustRegisterRole(t, s, "alice", "worker", 2)

	resp := s.opTaskUnpublish(Request{Args: map[string]any{
		"from": "alice", "project": "myproject", "slug": "feat-x",
		"force": true, "wipe_worktree": true,
	}})
	if resp.OK {
		t.Fatalf("worker must not be able to unpublish")
	}
	if resp.Code != CodeUnauthorized {
		t.Fatalf("expected unauthorized, got code=%d", resp.Code)
	}
}

// TestRepublishClearsArchivedFlag: publish → unpublish → republish of
// the same slug un-archives the room so new posts go through.
func TestRepublishClearsArchivedFlag(t *testing.T) {
	s, repoRoot, _ := setupPublishedTask(t, "first")

	// Unpublish.
	up := s.opTaskUnpublish(Request{Args: map[string]any{
		"from": "sup", "project": "myproject", "slug": "feat-x",
	}})
	if !up.OK {
		t.Fatalf("unpublish: %+v", up)
	}
	s.mu.Lock()
	r := s.rooms["feat-x"]
	s.mu.Unlock()
	r.mu.Lock()
	archivedAfterUnpublish := r.Archived
	r.mu.Unlock()
	if !archivedAfterUnpublish {
		t.Fatalf("room should be archived after unpublish")
	}

	// Republish same slug.
	pub := s.opTaskPublish(Request{Args: map[string]any{
		"from": "sup", "project": "myproject", "repo_root": repoRoot,
		"workstreams": []any{map[string]any{"slug": "feat-x", "branch": "feat/x", "brief": "second try"}},
	}})
	if !pub.OK {
		t.Fatalf("republish: %+v", pub)
	}

	r.mu.Lock()
	archivedAfterRepublish := r.Archived
	r.mu.Unlock()
	if archivedAfterRepublish {
		t.Fatalf("archived flag must be cleared on republish")
	}

	// Post works now.
	post := s.opPost(Request{Args: map[string]any{
		"from": "sup", "room": "feat-x", "body": "after republish",
	}})
	if !post.OK {
		t.Fatalf("post to republished room should succeed: %+v", post)
	}
}

// TestAgentsIncludesLeaseStatus: opAgents returns lease_status correctly
// computed from ExpiresAt.
func TestAgentsIncludesLeaseStatus(t *testing.T) {
	s := newFixtureState()
	mustRegisterRole(t, s, "alive", "worker", 1)
	mustRegisterRole(t, s, "stale", "worker", 2)
	// Force stale's lease into the past.
	s.mu.Lock()
	s.agentByName("stale").ExpiresAt = time.Now().Add(-time.Minute)
	s.mu.Unlock()

	resp := s.opAgents()
	if !resp.OK {
		t.Fatalf("opAgents: %+v", resp)
	}
	rows, _ := resp.Data.([]any)
	found := map[string]string{}
	for _, row := range rows {
		m := row.(map[string]any)
		name, _ := m["name"].(string)
		status, _ := m["lease_status"].(string)
		found[name] = status
	}
	if found["alive"] != "live" {
		t.Fatalf("expected alive.lease_status=live, got %q", found["alive"])
	}
	if found["stale"] != "expired" {
		t.Fatalf("expected stale.lease_status=expired, got %q", found["stale"])
	}
}

// TestTaskPublishPerWorkstreamAtomicity verifies that a colliding slug does
// not block other slugs in the same publish call. The colliding slug lands
// in failed_slugs with an error; the other slug succeeds.
func TestTaskPublishPerWorkstreamAtomicity(t *testing.T) {
	repoRoot := mustInitGitRepo(t)

	// Pre-create a worktree that will collide with one of the publish slugs.
	existingWt := filepath.Join(filepath.Dir(repoRoot), "wt", "collide")
	if err := os.MkdirAll(filepath.Dir(existingWt), 0o755); err != nil {
		t.Fatalf("mkdir wt dir: %v", err)
	}
	cmd := exec.Command("git", "-C", repoRoot, "worktree", "add", "-b", "pre-existing", existingWt)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pre-existing worktree: %v\n%s", err, out)
	}

	s := newFixtureState()
	mustRegisterFor(t, s, "sup", "supervisor", "myproject", repoRoot, 1)

	resp := s.opTaskPublish(Request{Args: map[string]any{
		"from": "sup", "project": "myproject", "repo_root": repoRoot,
		"workstreams": []any{
			// Collides: wt/collide exists with branch `pre-existing`, not `other`.
			map[string]any{"slug": "collide", "branch": "other"},
			// Clean: should succeed.
			map[string]any{"slug": "clean", "branch": "feat/clean", "brief": "ok"},
		},
	}})
	if !resp.OK {
		t.Fatalf("publish should report partial success, got outright failure: %+v", resp)
	}
	data := resp.Data.(map[string]any)
	ok, _ := data["ok"].([]any)
	failed, _ := data["failed"].([]any)
	if len(ok) != 1 {
		t.Fatalf("expected 1 ok slug, got %+v", ok)
	}
	if len(failed) != 1 {
		t.Fatalf("expected 1 failed slug, got %+v", failed)
	}
	if failedMap := failed[0].(map[string]any); failedMap["slug"] != "collide" {
		t.Fatalf("expected collide to fail, got %v", failedMap)
	}

	// Clean worktree exists.
	cleanWt := filepath.Join(filepath.Dir(repoRoot), "wt", "clean")
	if _, err := os.Stat(cleanWt); err != nil {
		t.Fatalf("clean worktree should exist at %s: %v", cleanWt, err)
	}
}

// TestTaskPublishProjectIdentityMismatch verifies publish rejects payloads
// whose project does not match the caller's registered project.
func TestTaskPublishProjectIdentityMismatch(t *testing.T) {
	repoRoot := mustInitGitRepo(t)
	s := newFixtureState()
	mustRegisterFor(t, s, "sup", "supervisor", "myproject", repoRoot, 1)

	resp := s.opTaskPublish(Request{Args: map[string]any{
		"from": "sup", "project": "otherproject", "repo_root": repoRoot,
		"workstreams": []any{map[string]any{"slug": "feat-x", "branch": "feat/x"}},
	}})
	if resp.OK {
		t.Fatalf("publish with mismatched project should fail")
	}
	if resp.Code != CodeProjectIdentityMismatch {
		t.Fatalf("expected ProjectIdentityMismatch, got code=%d: %+v", resp.Code, resp)
	}
}
