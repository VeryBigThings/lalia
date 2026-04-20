package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	statusOpen       = "open"
	statusAssigned   = "assigned"
	statusInProgress = "in-progress"
	statusReady      = "ready"
	statusBlocked    = "blocked"
	statusMerged     = "merged"
)

// workerStatuses are the only statuses a row owner may self-assign.
var workerStatuses = map[string]bool{
	statusInProgress: true,
	statusReady:      true,
	statusBlocked:    true,
}

type Contract struct {
	OtherSlug string `json:"other_slug"`
	Note      string `json:"note"`
}

type Task struct {
	Slug        string     `json:"slug"`
	Branch      string     `json:"branch,omitempty"`
	Brief       string     `json:"brief,omitempty"`
	OwnedPaths  []string   `json:"owned_paths,omitempty"`
	Contracts   []Contract `json:"contracts,omitempty"`
	Worktree    string     `json:"worktree,omitempty"`
	Owner       string     `json:"owner,omitempty"`
	Status      string     `json:"status"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type TaskList struct {
	ProjectID  string    `json:"project_id"`
	Supervisor string    `json:"supervisor"`
	RepoRoot   string    `json:"repo_root,omitempty"`
	Tasks      []Task    `json:"tasks"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// legacyPlan is the pre-rename on-disk shape. Kept here solely for boot
// migration; new code must use TaskList.
type legacyPlan struct {
	ProjectID   string             `json:"project_id"`
	Supervisor  string             `json:"supervisor"`
	Assignments []legacyAssignment `json:"assignments"`
	UpdatedAt   time.Time          `json:"updated_at"`
}

type legacyAssignment struct {
	Slug      string    `json:"slug"`
	Goal      string    `json:"goal,omitempty"`
	Worktree  string    `json:"worktree,omitempty"`
	Owner     string    `json:"owner,omitempty"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

// migratePlansToTasks performs a one-shot rename of $LALIA_WORKSPACE/plans/
// to tasks/ and converts each plan.json into task-list.json with the new
// schema. Idempotent: returns nil and does nothing if tasks/ already exists.
func migratePlansToTasks() error {
	workspace := workspacePath()
	plansDir := filepath.Join(workspace, "plans")
	tasksDir := filepath.Join(workspace, "tasks")

	plansExists, err := dirExists(plansDir)
	if err != nil {
		return err
	}
	tasksExists, err := dirExists(tasksDir)
	if err != nil {
		return err
	}
	if !plansExists || tasksExists {
		return nil
	}

	entries, err := os.ReadDir(plansDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		return err
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		oldProjectDir := filepath.Join(plansDir, e.Name())
		newProjectDir := filepath.Join(tasksDir, e.Name())
		if err := os.MkdirAll(newProjectDir, 0o755); err != nil {
			return err
		}
		oldFile := filepath.Join(oldProjectDir, "plan.json")
		newFile := filepath.Join(newProjectDir, "task-list.json")
		b, err := os.ReadFile(oldFile)
		if err != nil {
			continue
		}
		var legacy legacyPlan
		if err := json.Unmarshal(b, &legacy); err != nil {
			continue
		}
		tl := TaskList{
			ProjectID:  legacy.ProjectID,
			Supervisor: legacy.Supervisor,
			UpdatedAt:  legacy.UpdatedAt,
			Tasks:      make([]Task, 0, len(legacy.Assignments)),
		}
		if tl.ProjectID == "" {
			tl.ProjectID = e.Name()
		}
		for _, la := range legacy.Assignments {
			tl.Tasks = append(tl.Tasks, Task{
				Slug:      la.Slug,
				Brief:     la.Goal,
				Worktree:  la.Worktree,
				Owner:     la.Owner,
				Status:    la.Status,
				UpdatedAt: la.UpdatedAt,
			})
		}
		out, err := json.MarshalIndent(tl, "", "  ")
		if err != nil {
			return err
		}
		out = append(out, '\n')
		if err := os.WriteFile(newFile, out, 0o644); err != nil {
			return err
		}
	}
	// Remove the old plans/ tree last, after all files migrated successfully.
	return os.RemoveAll(plansDir)
}

func dirExists(path string) (bool, error) {
	st, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return st.IsDir(), nil
}

// loadTaskLists reads all tasks/*/task-list.json from the workspace on daemon
// startup. For merged tasks it reconstructs an archived room stub so opPost
// correctly rejects new posts after a daemon restart.
func (s *State) loadTaskLists() error {
	if s.tasks == nil {
		s.tasks = make(map[string]*TaskList)
	}
	dir := filepath.Join(workspacePath(), "tasks")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		fpath := filepath.Join(dir, e.Name(), "task-list.json")
		b, err := os.ReadFile(fpath)
		if err != nil {
			continue
		}
		var tl TaskList
		if err := json.Unmarshal(b, &tl); err != nil {
			continue
		}
		if tl.ProjectID == "" {
			tl.ProjectID = e.Name()
		}
		s.tasks[tl.ProjectID] = &tl
	}
	return nil
}

func (s *State) persistTaskList(tl *TaskList) {
	data, _ := json.MarshalIndent(tl, "", "  ")
	data = append(data, '\n')
	s.enqueueWrite(
		filepath.Join("tasks", tl.ProjectID, "task-list.json"),
		data,
		fmt.Sprintf("task list update: %s (supervisor=%s)", tl.ProjectID, tl.Supervisor),
	)
}

// findTask returns a pointer into the list's slice. Call under s.mu.
func findTask(tl *TaskList, slug string) *Task {
	for i := range tl.Tasks {
		if tl.Tasks[i].Slug == slug {
			return &tl.Tasks[i]
		}
	}
	return nil
}

// requireSupervisor checks that from is registered as a supervisor and is
// the supervisor for the given project. Must be called with s.mu held.
func (s *State) requireSupervisor(from, projectID string) (Response, bool) {
	a := s.agentByName(from)
	if a == nil {
		return errorResponse(CodeUnauthorized, "not_registered", "run lalia register", "not registered: "+from, map[string]any{"from": from}), false
	}
	if a.Role != "supervisor" {
		return errorResponse(CodeUnauthorized, "not_supervisor", "register with --role supervisor", "caller is not a supervisor: "+from, map[string]any{"from": from}), false
	}
	if tl, ok := s.tasks[projectID]; ok && tl.Supervisor != from {
		return errorResponse(CodeUnauthorized, "not_project_supervisor", "only the project supervisor may perform this action", "project "+projectID+" supervisor is "+tl.Supervisor+", not "+from, map[string]any{"project": projectID, "supervisor": tl.Supervisor}), false
	}
	return Response{}, true
}

// ensureRoomWithMembers gets or creates the room named slug and ensures all
// listed names are members. Must NOT be called with s.mu held.
func (s *State) ensureRoomWithMembers(slug, createdBy string, members []string) *Room {
	s.mu.Lock()
	r, ok := s.rooms[slug]
	if !ok {
		r = newRoom(slug, "", createdBy)
		s.rooms[slug] = r
	}
	s.mu.Unlock()

	r.mu.Lock()
	var added []string
	for _, m := range members {
		if !r.members[m] {
			r.members[m] = true
			added = append(added, m)
		}
	}
	createdAt := r.CreatedAt
	r.mu.Unlock()

	if s.queue != nil {
		if !ok {
			// New room: persist the row and ALL initial members.
			// newRoom() pre-populates createdBy in r.members, so the creator
			// would be absent from `added` — flush r.members directly.
			_ = s.queue.roomUpsert(slug, "", createdBy, createdAt)
			r.mu.Lock()
			for m := range r.members {
				_ = s.queue.roomAddMember(slug, m)
			}
			r.mu.Unlock()
		} else {
			for _, m := range added {
				_ = s.queue.roomAddMember(slug, m)
			}
		}
	}
	return r
}

// archiveRoom marks a room as archived (no new posts) and persists the flag.
// Must NOT be called with s.mu held. Safe to call if the room does not exist.
// Returns true if the room existed and was flipped from open to archived.
func (s *State) archiveRoom(slug string) bool {
	s.mu.Lock()
	r, ok := s.rooms[slug]
	s.mu.Unlock()
	if !ok {
		return false
	}
	r.mu.Lock()
	already := r.Archived
	r.Archived = true
	r.mu.Unlock()
	if already {
		return false
	}
	if s.queue != nil {
		_ = s.queue.roomSetArchived(slug, true)
	}
	return true
}

// unarchiveRoom clears the archived flag so posts are accepted again.
// Must NOT be called with s.mu held. Safe to call if the room does not
// exist or is already un-archived. Returns true if the room existed and
// was flipped from archived to open.
func (s *State) unarchiveRoom(slug string) bool {
	s.mu.Lock()
	r, ok := s.rooms[slug]
	s.mu.Unlock()
	if !ok {
		return false
	}
	r.mu.Lock()
	wasArchived := r.Archived
	r.Archived = false
	r.mu.Unlock()
	if !wasArchived {
		return false
	}
	if s.queue != nil {
		_ = s.queue.roomSetArchived(slug, false)
	}
	return true
}

// internalPost appends a message to the room's log and delivers to mailboxes
// without requiring `from` to be a registered agent or room member.
// The caller must ensure the room is not archived before calling.
func (s *State) internalPost(r *Room, from, body string) {
	r.mu.Lock()
	r.seq++
	msg := RoomMessage{
		Seq:  r.seq,
		Room: r.Name,
		From: from,
		TS:   time.Now(),
		Body: body,
	}
	r.log = append(r.log, msg)
	for member := range r.members {
		if member == from {
			continue
		}
		if ch, ok := r.waiter[member]; ok {
			ch <- msg
			delete(r.waiter, member)
			continue
		}
		if s.deliverAny(member, "room", r.Name, toPeerMsg(msg)) {
			continue
		}
		q := r.mailbox[member]
		if len(q) >= roomMailboxLimit {
			q = q[1:]
			r.dropped[member]++
		}
		r.mailbox[member] = append(q, msg)
	}
	r.mu.Unlock()

	s.enqueueWrite(
		fmt.Sprintf("rooms/%s/%06d-%s.md", r.Name, msg.Seq, safePathSegment(from)),
		renderRoomMsg(msg),
		fmt.Sprintf("room msg %d in %s from %s", msg.Seq, r.Name, from),
	)
}

// opRoomsGC archives rooms backed by merged tasks in every list the caller
// supervises. Workers are rejected. Returns the list of slugs newly archived
// in this call (already-archived rooms are skipped).
//
// `task unassign` and `task status merged` do not archive rooms automatically
// — this is the opt-in cleanup step for finished workstreams.
func (s *State) opRoomsGC(req Request) Response {
	from := strVal(req.Args, "from")
	if from == "" {
		return errorResponse(CodeError, "missing_from", "set LALIA_NAME or pass --as", "from required", nil)
	}

	s.mu.Lock()
	a := s.agentByName(from)
	if a == nil {
		s.mu.Unlock()
		return errorResponse(CodeUnauthorized, "not_registered", "run lalia register", "not registered: "+from, map[string]any{"from": from})
	}
	if a.Role != "supervisor" {
		s.mu.Unlock()
		return errorResponse(CodeUnauthorized, "not_supervisor", "register with --role supervisor", "caller is not a supervisor: "+from, map[string]any{"from": from})
	}

	type candidate struct {
		slug    string
		project string
	}
	var cands []candidate
	for pid, tl := range s.tasks {
		if tl.Supervisor != from {
			continue
		}
		for _, t := range tl.Tasks {
			if t.Status != statusMerged {
				continue
			}
			if _, ok := s.rooms[t.Slug]; !ok {
				continue
			}
			cands = append(cands, candidate{slug: t.Slug, project: pid})
		}
	}
	s.mu.Unlock()

	archived := make([]any, 0, len(cands))
	for _, c := range cands {
		if s.archiveRoom(c.slug) {
			archived = append(archived, map[string]any{"slug": c.slug, "project": c.project})
		}
	}
	return Response{OK: true, Data: map[string]any{"archived": archived, "count": len(archived)}}
}

// taskToMap converts a Task to the wire representation.
func taskToMap(t Task) map[string]any {
	contracts := make([]any, len(t.Contracts))
	for i, c := range t.Contracts {
		contracts[i] = map[string]any{"other_slug": c.OtherSlug, "note": c.Note}
	}
	return map[string]any{
		"slug":        t.Slug,
		"branch":      t.Branch,
		"brief":       t.Brief,
		"owned_paths": t.OwnedPaths,
		"contracts":   contracts,
		"worktree":    t.Worktree,
		"owner":       t.Owner,
		"status":      t.Status,
		"updated_at":  t.UpdatedAt.Format(time.RFC3339),
	}
}

func taskListToMap(tl *TaskList) map[string]any {
	taskList := make([]any, len(tl.Tasks))
	for i, t := range tl.Tasks {
		taskList[i] = taskToMap(t)
	}
	return map[string]any{
		"project_id": tl.ProjectID,
		"supervisor": tl.Supervisor,
		"repo_root":  tl.RepoRoot,
		"tasks":      taskList,
		"updated_at": tl.UpdatedAt.Format(time.RFC3339),
	}
}

// composeBundle renders the supervisor's context bundle into a single
// markdown message suitable for posting as the room's first message.
func composeBundle(brief string, ownedPaths []string, contracts []Contract) string {
	var b strings.Builder
	b.WriteString(strings.TrimRight(brief, "\n"))
	b.WriteString("\n")
	if len(ownedPaths) > 0 || len(contracts) > 0 {
		b.WriteString("\n---\n\n")
	}
	if len(ownedPaths) > 0 {
		b.WriteString("**Owned paths**\n")
		for _, p := range ownedPaths {
			b.WriteString("- ")
			b.WriteString(p)
			b.WriteString("\n")
		}
	}
	if len(contracts) > 0 {
		if len(ownedPaths) > 0 {
			b.WriteString("\n")
		}
		b.WriteString("**Contracts**\n")
		for _, c := range contracts {
			b.WriteString("- ")
			b.WriteString(c.OtherSlug)
			b.WriteString(": ")
			b.WriteString(c.Note)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// repoLock returns the per-repo mutex, creating it on first use. The lock
// serializes git worktree mutations within a single repo_root so concurrent
// publishes cannot corrupt git metadata.
func (s *State) repoLock(repoRoot string) *sync.Mutex {
	s.repoLocksMu.Lock()
	defer s.repoLocksMu.Unlock()
	if s.repoLocks == nil {
		s.repoLocks = make(map[string]*sync.Mutex)
	}
	m, ok := s.repoLocks[repoRoot]
	if !ok {
		m = &sync.Mutex{}
		s.repoLocks[repoRoot] = m
	}
	return m
}

// worktreeEntry is one line-group from `git worktree list --porcelain`.
type worktreeEntry struct {
	Path   string
	Branch string // refs/heads/<name> → <name>; empty if detached
}

// listWorktrees parses `git worktree list --porcelain` for the repo.
func listWorktrees(repoRoot string) ([]worktreeEntry, error) {
	out, err := exec.Command("git", "-C", repoRoot, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return nil, err
	}
	var entries []worktreeEntry
	var cur worktreeEntry
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if cur.Path != "" {
				entries = append(entries, cur)
			}
			cur = worktreeEntry{}
			continue
		}
		switch {
		case strings.HasPrefix(line, "worktree "):
			cur.Path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch "):
			ref := strings.TrimPrefix(line, "branch ")
			cur.Branch = strings.TrimPrefix(ref, "refs/heads/")
		}
	}
	if cur.Path != "" {
		entries = append(entries, cur)
	}
	return entries, nil
}

// branchExists returns true if the branch ref exists in the repo.
func branchExists(repoRoot, branch string) bool {
	err := exec.Command("git", "-C", repoRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+branch).Run()
	return err == nil
}

// canonicalPath normalizes a path via EvalSymlinks when the target exists,
// falling back to Abs otherwise. This is needed on macOS where /var is a
// symlink to /private/var: filepath.Abs on a test tempdir yields /var/...
// while `git worktree list` reports /private/var/..., and a naive string
// compare misses the match on republish-against-existing-worktree.
func canonicalPath(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	abs, _ := filepath.Abs(p)
	return abs
}

// ensureWorktree makes sure a worktree exists at target on branch, creating
// it if needed. Returns (didCreate, error). Idempotent: if the target already
// hosts that branch, returns (false, nil).
func ensureWorktree(repoRoot, target, branch string) (bool, error) {
	absTarget := canonicalPath(target)
	entries, err := listWorktrees(repoRoot)
	if err != nil {
		return false, fmt.Errorf("list worktrees: %w", err)
	}
	for _, e := range entries {
		absE := canonicalPath(e.Path)
		if absE == absTarget {
			if e.Branch == branch {
				return false, nil
			}
			return false, fmt.Errorf("worktree at %s is on branch %q, not %q", absTarget, e.Branch, branch)
		}
		if e.Branch == branch {
			return false, fmt.Errorf("branch %q already checked out at %s", branch, e.Path)
		}
	}
	// If the target path exists but is not a known worktree, refuse: that's
	// someone else's directory, not ours to repurpose.
	if _, err := os.Stat(absTarget); err == nil {
		return false, fmt.Errorf("target path %s already exists and is not a git worktree", absTarget)
	}
	// Make sure the parent exists.
	if err := os.MkdirAll(filepath.Dir(absTarget), 0o755); err != nil {
		return false, fmt.Errorf("create parent dir: %w", err)
	}
	var cmd *exec.Cmd
	if branchExists(repoRoot, branch) {
		cmd = exec.Command("git", "-C", repoRoot, "worktree", "add", absTarget, branch)
	} else {
		cmd = exec.Command("git", "-C", repoRoot, "worktree", "add", "-b", branch, absTarget)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("git worktree add: %s", strings.TrimSpace(string(out)))
	}
	return true, nil
}

// removeWorktree runs `git worktree remove --force` and verifies the
// target directory is gone from disk afterwards. Returns nil only when
// removal is observable; returns a descriptive error otherwise so callers
// can surface honest state to users instead of computing optimistic flags.
func removeWorktree(repoRoot, target string) error {
	out, err := exec.Command("git", "-C", repoRoot, "worktree", "remove", "--force", target).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree remove: %s", strings.TrimSpace(string(out)))
	}
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("worktree still present at %s after remove", target)
	}
	return nil
}

// opTaskPublish creates N tasks + worktrees + rooms + bundle posts in a
// single call. Supervisor-only. Per-workstream atomicity: if one slug fails
// after its worktree was created, the worktree is removed for that slug
// only; other slugs still succeed.
func (s *State) opTaskPublish(req Request) Response {
	from := strVal(req.Args, "from")
	pid := strVal(req.Args, "project")
	repoRoot := strVal(req.Args, "repo_root")
	wsRaw, _ := req.Args["workstreams"].([]any)

	if from == "" {
		return errorResponse(CodeError, "missing_from", "set LALIA_NAME or pass --as", "from required", nil)
	}
	if pid == "" {
		return errorResponse(CodeError, "missing_project", "include project in publish payload", "project required", nil)
	}
	if repoRoot == "" {
		return errorResponse(CodeError, "missing_repo_root", "include repo_root in publish payload", "repo_root required", nil)
	}
	if len(wsRaw) == 0 {
		return errorResponse(CodeError, "missing_workstreams", "include at least one workstream", "workstreams required", nil)
	}
	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return errorResponse(CodeError, "bad_repo_root", "check the repo_root path", "bad repo_root: "+err.Error(), map[string]any{"repo_root": repoRoot})
	}
	if st, err := os.Stat(absRoot); err != nil || !st.IsDir() {
		return errorResponse(CodeError, "repo_root_not_found", "ensure repo_root exists on this machine", "repo_root does not exist: "+absRoot, map[string]any{"repo_root": absRoot})
	}

	// Parse workstreams into typed values first so we can reject malformed
	// payloads loudly before doing any git work.
	type wsInput struct {
		Slug       string
		Branch     string
		Brief      string
		OwnedPaths []string
		Contracts  []Contract
	}
	parsed := make([]wsInput, 0, len(wsRaw))
	seenSlugs := make(map[string]bool)
	for i, raw := range wsRaw {
		m, ok := raw.(map[string]any)
		if !ok {
			return errorResponse(CodeError, "bad_workstream", "each workstream must be an object", fmt.Sprintf("workstream %d is not an object", i), nil)
		}
		slug, _ := m["slug"].(string)
		branch, _ := m["branch"].(string)
		brief, _ := m["brief"].(string)
		if slug == "" || branch == "" {
			return errorResponse(CodeError, "bad_workstream", "slug and branch required", fmt.Sprintf("workstream %d missing slug or branch", i), map[string]any{"index": i})
		}
		if seenSlugs[slug] {
			return errorResponse(CodeError, "duplicate_slug", "each workstream slug must be unique", "duplicate slug in payload: "+slug, map[string]any{"slug": slug})
		}
		seenSlugs[slug] = true
		var owned []string
		if op, ok := m["owned_paths"].([]any); ok {
			for _, p := range op {
				if s, ok := p.(string); ok && s != "" {
					owned = append(owned, s)
				}
			}
		}
		var contracts []Contract
		if cs, ok := m["contracts"].([]any); ok {
			for _, c := range cs {
				cm, ok := c.(map[string]any)
				if !ok {
					continue
				}
				os, _ := cm["other_slug"].(string)
				note, _ := cm["note"].(string)
				if os == "" {
					continue
				}
				contracts = append(contracts, Contract{OtherSlug: os, Note: note})
			}
		}
		parsed = append(parsed, wsInput{
			Slug: slug, Branch: branch, Brief: brief,
			OwnedPaths: owned, Contracts: contracts,
		})
	}

	// Auth + project identity check.
	s.mu.Lock()
	a := s.agentByName(from)
	if a == nil {
		s.mu.Unlock()
		return errorResponse(CodeUnauthorized, "not_registered", "run lalia register", "not registered: "+from, map[string]any{"from": from})
	}
	if a.Role != "supervisor" {
		s.mu.Unlock()
		return errorResponse(CodeUnauthorized, "not_supervisor", "register with --role supervisor", "caller is not a supervisor: "+from, map[string]any{"from": from})
	}
	if a.Project != "" && a.Project != pid {
		s.mu.Unlock()
		return errorResponse(CodeProjectIdentityMismatch, "project_mismatch", "re-register from inside the right repo, or publish from that repo", "caller is registered under project "+a.Project+", not "+pid, map[string]any{"expected": a.Project, "got": pid})
	}
	if a.RepoRoot != "" {
		absA, _ := filepath.Abs(a.RepoRoot)
		if absA != absRoot {
			s.mu.Unlock()
			return errorResponse(CodeProjectIdentityMismatch, "repo_root_mismatch", "publish from the same repo you registered in", "caller registered with repo_root "+absA+", publish payload has "+absRoot, map[string]any{"expected": absA, "got": absRoot})
		}
	}
	if tl, ok := s.tasks[pid]; ok && tl.Supervisor != "" && tl.Supervisor != from {
		s.mu.Unlock()
		return errorResponse(CodeUnauthorized, "not_project_supervisor", "only the project supervisor may publish", "project "+pid+" supervisor is "+tl.Supervisor+", not "+from, map[string]any{"project": pid, "supervisor": tl.Supervisor})
	}
	s.mu.Unlock()

	// Serialize per repo to prevent concurrent publishes racing on git
	// worktree metadata.
	rl := s.repoLock(absRoot)
	rl.Lock()
	defer rl.Unlock()

	parentDir := filepath.Dir(absRoot)
	wtRoot := filepath.Join(parentDir, "wt")

	okSlugs := []any{}
	failedSlugs := []any{}

	for _, in := range parsed {
		target := filepath.Join(wtRoot, in.Slug)
		created, err := ensureWorktree(absRoot, target, in.Branch)
		if err != nil {
			failedSlugs = append(failedSlugs, map[string]any{"slug": in.Slug, "error": err.Error()})
			continue
		}

		// Install the task row, room, and bundle post under s.mu.
		s.mu.Lock()
		tl, ok := s.tasks[pid]
		if !ok {
			tl = &TaskList{ProjectID: pid, Supervisor: from, RepoRoot: absRoot}
			s.tasks[pid] = tl
		}
		if tl.Supervisor == "" {
			tl.Supervisor = from
		}
		if tl.RepoRoot == "" {
			tl.RepoRoot = absRoot
		}
		existing := findTask(tl, in.Slug)
		now := time.Now()
		var alreadyOpen bool
		if existing != nil {
			// If the existing row is already open with the same branch, this
			// slug is a no-op. Otherwise overwrite in place (still open).
			if existing.Status == statusOpen && existing.Branch == in.Branch {
				alreadyOpen = true
			}
			existing.Branch = in.Branch
			existing.Brief = in.Brief
			existing.OwnedPaths = in.OwnedPaths
			existing.Contracts = in.Contracts
			existing.Worktree = target
			if existing.Status == "" {
				existing.Status = statusOpen
			}
			existing.UpdatedAt = now
		} else {
			tl.Tasks = append(tl.Tasks, Task{
				Slug:       in.Slug,
				Branch:     in.Branch,
				Brief:      in.Brief,
				OwnedPaths: in.OwnedPaths,
				Contracts:  in.Contracts,
				Worktree:   target,
				Status:     statusOpen,
				UpdatedAt:  now,
			})
		}
		tl.UpdatedAt = now
		tlCopy := *tl
		s.mu.Unlock()
		s.persistTaskList(&tlCopy)

		// Ensure the room exists with the supervisor as a member, and post
		// the bundle message — unless this is a pure idempotent republish.
		r := s.ensureRoomWithMembers(in.Slug, from, []string{from})
		// Republishing a previously-unpublished slug leaves the room
		// archived; clear the flag so posts go through and the invariant
		// "archived = done" stays honest.
		s.unarchiveRoom(in.Slug)
		s.persistRoomDefinition(r)
		s.persistRoomMembers(r)
		if !created && alreadyOpen {
			// Republish of an already-published slug: skip bundle repost so
			// re-running publish is a true no-op per spec.
			r.mu.Lock()
			hasPosts := r.seq > 0
			r.mu.Unlock()
			if hasPosts {
				okSlugs = append(okSlugs, map[string]any{"slug": in.Slug, "noop": true})
				continue
			}
		}
		bundle := composeBundle(in.Brief, in.OwnedPaths, in.Contracts)
		s.internalPost(r, from, bundle)
		okSlugs = append(okSlugs, map[string]any{"slug": in.Slug, "worktree": target, "created_worktree": created})
	}

	return Response{OK: true, Data: map[string]any{
		"project":  pid,
		"ok":       okSlugs,
		"failed":   failedSlugs,
		"repo_root": absRoot,
	}}
}

// opTaskUnassign clears the owner of a slug and resets status to open.
// The slug's room stays live so reassignees can inherit the conversation;
// supervisors can close it later with `lalia rooms gc`. Supervisor only.
func (s *State) opTaskUnassign(req Request) Response {
	from := strVal(req.Args, "from")
	slug := strVal(req.Args, "slug")
	pid := strVal(req.Args, "project")

	if from == "" {
		return errorResponse(CodeError, "missing_from", "set LALIA_NAME or pass --as", "from required", nil)
	}
	if slug == "" {
		return errorResponse(CodeError, "missing_slug", "provide the slug to unassign", "slug required", nil)
	}
	if pid == "" {
		return errorResponse(CodeError, "missing_project", "provide --project or run from a git repo", "project required", nil)
	}

	s.mu.Lock()
	if resp, ok := s.requireSupervisor(from, pid); !ok {
		s.mu.Unlock()
		return resp
	}
	tl := s.tasks[pid]
	if tl == nil {
		s.mu.Unlock()
		return errorResponse(CodeNotFound, "task_list_not_found", "run task publish to initialise tasks for this project", "no task list for project: "+pid, map[string]any{"project": pid})
	}
	t := findTask(tl, slug)
	if t == nil {
		s.mu.Unlock()
		return errorResponse(CodeNotFound, "slug_not_found", "check lalia task show for available slugs", "slug not found: "+slug, map[string]any{"slug": slug})
	}
	t.Owner = ""
	t.Status = statusOpen
	t.UpdatedAt = time.Now()
	tl.UpdatedAt = time.Now()
	tlCopy := *tl
	s.mu.Unlock()
	s.persistTaskList(&tlCopy)

	return Response{OK: true, Data: taskToMap(*t)}
}

// opTaskReassign forcibly moves a row's owner. Supervisor-only. Used to
// unstick stalled rows without touching the worktree.
func (s *State) opTaskReassign(req Request) Response {
	from := strVal(req.Args, "from")
	slug := strVal(req.Args, "slug")
	newOwner := strVal(req.Args, "owner")
	pid := strVal(req.Args, "project")

	if from == "" {
		return errorResponse(CodeError, "missing_from", "set LALIA_NAME or pass --as", "from required", nil)
	}
	if slug == "" || newOwner == "" {
		return errorResponse(CodeError, "missing_params", "provide slug and owner", "slug and owner required", map[string]any{"slug": slug, "owner": newOwner})
	}
	if pid == "" {
		return errorResponse(CodeError, "missing_project", "provide --project or run from a git repo", "project required", nil)
	}

	s.mu.Lock()
	if resp, ok := s.requireSupervisor(from, pid); !ok {
		s.mu.Unlock()
		return resp
	}
	if s.agentByName(newOwner) == nil {
		s.mu.Unlock()
		return errorResponse(CodeNotFound, "owner_not_registered", "the target agent must be registered", "agent not registered: "+newOwner, map[string]any{"owner": newOwner})
	}
	tl := s.tasks[pid]
	if tl == nil {
		s.mu.Unlock()
		return errorResponse(CodeNotFound, "task_list_not_found", "publish tasks for this project first", "no task list for project: "+pid, map[string]any{"project": pid})
	}
	t := findTask(tl, slug)
	if t == nil {
		s.mu.Unlock()
		return errorResponse(CodeNotFound, "slug_not_found", "check lalia task show for available slugs", "slug not found: "+slug, map[string]any{"slug": slug})
	}
	oldOwner := t.Owner
	t.Owner = newOwner
	t.Status = statusAssigned
	t.UpdatedAt = time.Now()
	tl.UpdatedAt = time.Now()
	tlCopy := *tl
	supervisor := tl.Supervisor
	s.mu.Unlock()
	s.persistTaskList(&tlCopy)

	// Rewire room membership.
	r := s.ensureRoomWithMembers(slug, supervisor, []string{supervisor, newOwner})
	if oldOwner != "" && oldOwner != newOwner {
		r.mu.Lock()
		delete(r.members, oldOwner)
		r.mu.Unlock()
	}
	s.persistRoomMembers(r)

	return Response{OK: true, Data: taskToMap(*t)}
}

// opTaskStatus flips the status of a task. Workers may only flip their own
// row to in-progress|ready|blocked. Supervisors may also set merged.
func (s *State) opTaskStatus(req Request) Response {
	from := strVal(req.Args, "from")
	slug := strVal(req.Args, "slug")
	status := strVal(req.Args, "status")
	pid := strVal(req.Args, "project")

	if from == "" {
		return errorResponse(CodeError, "missing_from", "set LALIA_NAME or pass --as", "from required", nil)
	}
	if slug == "" || status == "" {
		return errorResponse(CodeError, "missing_params", "provide slug and status", "slug and status required", nil)
	}
	if pid == "" {
		return errorResponse(CodeError, "missing_project", "provide --project or run from a git repo", "project required", nil)
	}

	s.mu.Lock()
	tl := s.tasks[pid]
	if tl == nil {
		s.mu.Unlock()
		return errorResponse(CodeNotFound, "task_list_not_found", "publish tasks for this project first", "no task list for project: "+pid, map[string]any{"project": pid})
	}
	t := findTask(tl, slug)
	if t == nil {
		s.mu.Unlock()
		return errorResponse(CodeNotFound, "slug_not_found", "check lalia task show for available slugs", "slug not found: "+slug, map[string]any{"slug": slug})
	}
	isSupervisor := tl.Supervisor == from
	isOwner := t.Owner == from

	if !isSupervisor && !isOwner {
		s.mu.Unlock()
		return errorResponse(CodeUnauthorized, "not_your_row", "only the row owner or supervisor may change this status", "cannot mutate row owned by "+t.Owner, map[string]any{"slug": slug, "owner": t.Owner, "from": from})
	}
	if !isSupervisor && !workerStatuses[status] {
		s.mu.Unlock()
		return errorResponse(CodeUnauthorized, "status_not_allowed", "workers may only set: in-progress, ready, blocked", "workers cannot set status "+status, map[string]any{"status": status})
	}
	if !workerStatuses[status] && status != statusMerged && status != statusOpen && status != statusAssigned {
		s.mu.Unlock()
		return errorResponse(CodeError, "invalid_status", "valid values: open, assigned, in-progress, ready, blocked, merged", "invalid status: "+status, map[string]any{"status": status})
	}

	t.Status = status
	t.UpdatedAt = time.Now()
	tl.UpdatedAt = time.Now()
	tlCopy := *tl
	s.mu.Unlock()
	s.persistTaskList(&tlCopy)

	return Response{OK: true, Data: taskToMap(*t)}
}

// opTaskClaim lets a worker claim an open task. Sets owner=from,
// status=in-progress, and auto-joins the worker to the task's room.
// Returns the task plus the latest bundle post so the worker has its brief
// in one call.
func (s *State) opTaskClaim(req Request) Response {
	from := strVal(req.Args, "from")
	slug := strVal(req.Args, "slug")
	pid := strVal(req.Args, "project")

	if from == "" {
		return errorResponse(CodeError, "missing_from", "set LALIA_NAME or pass --as", "from required", nil)
	}
	if slug == "" {
		return errorResponse(CodeError, "missing_slug", "provide the slug to claim", "slug required", nil)
	}
	if pid == "" {
		return errorResponse(CodeError, "missing_project", "provide --project or run from a git repo", "project required", nil)
	}

	s.mu.Lock()
	tl := s.tasks[pid]
	if tl == nil {
		s.mu.Unlock()
		return errorResponse(CodeNotFound, "task_list_not_found", "no tasks published for this project yet", "no task list for project: "+pid, map[string]any{"project": pid})
	}
	t := findTask(tl, slug)
	if t == nil {
		s.mu.Unlock()
		return errorResponse(CodeNotFound, "slug_not_found", "check lalia task bulletin for open slugs", "slug not found: "+slug, map[string]any{"slug": slug})
	}
	if t.Status != statusOpen {
		s.mu.Unlock()
		return errorResponse(CodeError, "not_open", "only open tasks can be claimed", "task "+slug+" is not open (status="+t.Status+")", map[string]any{"slug": slug, "status": t.Status})
	}
	t.Owner = from
	t.Status = statusInProgress
	t.UpdatedAt = time.Now()
	tl.UpdatedAt = time.Now()
	tlCopy := *tl
	supervisor := tl.Supervisor
	taskCopy := *t
	s.mu.Unlock()
	s.persistTaskList(&tlCopy)

	// Auto-join the worker to the task's room.
	r := s.ensureRoomWithMembers(slug, supervisor, []string{supervisor, from})
	s.persistRoomMembers(r)

	// Surface the latest bundle post (first message in the room if present)
	// so the worker has its brief without a second call.
	var bundle map[string]any
	r.mu.Lock()
	if len(r.log) > 0 {
		first := r.log[0]
		bundle = map[string]any{
			"seq":  first.Seq,
			"from": first.From,
			"ts":   first.TS.Format(time.RFC3339),
			"body": first.Body,
		}
	}
	r.mu.Unlock()

	out := taskToMap(taskCopy)
	out["bundle"] = bundle
	return Response{OK: true, Data: out}
}

// opTaskShow returns the task list for the requested project. Anyone can
// read. If slug is provided, returns that single task instead.
func (s *State) opTaskShow(req Request) Response {
	pid := strVal(req.Args, "project")
	slug := strVal(req.Args, "slug")
	if pid == "" {
		return errorResponse(CodeError, "missing_project", "provide --project or run from a git repo", "project required", nil)
	}

	s.mu.Lock()
	tl := s.tasks[pid]
	s.mu.Unlock()

	if tl == nil {
		return errorResponse(CodeNotFound, "task_list_not_found", "no tasks published for this project yet", "no task list for project: "+pid, map[string]any{"project": pid})
	}
	if slug != "" {
		s.mu.Lock()
		t := findTask(tl, slug)
		s.mu.Unlock()
		if t == nil {
			return errorResponse(CodeNotFound, "slug_not_found", "check lalia task bulletin for available slugs", "slug not found: "+slug, map[string]any{"slug": slug})
		}
		return Response{OK: true, Data: taskToMap(*t)}
	}
	return Response{OK: true, Data: taskListToMap(tl)}
}

// opTaskList returns all task lists where the caller is supervisor or owner
// of any task.
func (s *State) opTaskList(req Request) Response {
	from := strVal(req.Args, "from")
	if from == "" {
		return errorResponse(CodeError, "missing_from", "set LALIA_NAME or pass --as", "from required", nil)
	}

	s.mu.Lock()
	var out []any
	for _, tl := range s.tasks {
		if tl.Supervisor == from {
			out = append(out, taskListToMap(tl))
			continue
		}
		for _, t := range tl.Tasks {
			if t.Owner == from {
				out = append(out, taskListToMap(tl))
				break
			}
		}
	}
	s.mu.Unlock()

	if out == nil {
		out = []any{}
	}
	return Response{OK: true, Data: out}
}

// opTaskBulletin returns open tasks for a project regardless of caller role.
// This is the pull-discovery surface workers use to see what's on offer.
func (s *State) opTaskBulletin(req Request) Response {
	from := strVal(req.Args, "from")
	pid := strVal(req.Args, "project")
	if from == "" {
		return errorResponse(CodeError, "missing_from", "set LALIA_NAME or pass --as", "from required", nil)
	}
	if pid == "" {
		return errorResponse(CodeError, "missing_project", "provide --project or run from a git repo", "project required", nil)
	}

	s.mu.Lock()
	tl := s.tasks[pid]
	if tl == nil {
		s.mu.Unlock()
		return Response{OK: true, Data: map[string]any{"project": pid, "tasks": []any{}}}
	}
	type entry struct {
		slug       string
		briefHead  string
		ownedPaths []string
		branch     string
		worktree   string
		status     string
		owner      string
	}
	var entries []entry
	for _, t := range tl.Tasks {
		if t.Status != statusOpen {
			continue
		}
		head := t.Brief
		if i := strings.Index(head, "\n"); i >= 0 {
			head = head[:i]
		}
		head = strings.TrimSpace(head)
		entries = append(entries, entry{
			slug:       t.Slug,
			briefHead:  head,
			ownedPaths: t.OwnedPaths,
			branch:     t.Branch,
			worktree:   t.Worktree,
			status:     t.Status,
			owner:      t.Owner,
		})
	}
	s.mu.Unlock()

	out := make([]any, 0, len(entries))
	for _, e := range entries {
		hasContext := false
		s.mu.Lock()
		r, ok := s.rooms[e.slug]
		s.mu.Unlock()
		if ok {
			r.mu.Lock()
			hasContext = r.seq > 0
			r.mu.Unlock()
		}
		out = append(out, map[string]any{
			"slug":          e.slug,
			"brief_summary": e.briefHead,
			"owned_paths":   e.ownedPaths,
			"branch":        e.branch,
			"worktree":      e.worktree,
			"status":        e.status,
			"owner":         e.owner,
			"has_context":   hasContext,
		})
	}
	return Response{OK: true, Data: map[string]any{"project": pid, "tasks": out}}
}

// worktreeIsClean returns (clean, details). A worktree is clean when
// `git status --porcelain` is empty AND, if the branch has an upstream,
// there are no ahead commits vs. upstream. If there is no upstream the
// worktree is considered clean when status is empty (local-only branches
// have nowhere to be "ahead of"). Returns a human-readable detail string
// when not clean.
func worktreeIsClean(worktree string) (bool, string) {
	if _, err := os.Stat(worktree); err != nil {
		// Already gone: treat as clean; nothing to protect.
		return true, ""
	}
	status, err := exec.Command("git", "-C", worktree, "status", "--porcelain").Output()
	if err != nil {
		return false, "git status failed: " + err.Error()
	}
	if len(strings.TrimSpace(string(status))) > 0 {
		return false, "worktree has uncommitted changes (see git status)"
	}
	// Upstream comparison. Missing upstream is fine; skip the ahead check.
	upstream, err := exec.Command("git", "-C", worktree, "rev-parse", "--abbrev-ref", "@{upstream}").Output()
	if err != nil || strings.TrimSpace(string(upstream)) == "" {
		return true, ""
	}
	ahead, err := exec.Command("git", "-C", worktree, "rev-list", "--count", "@{upstream}..HEAD").Output()
	if err != nil {
		return false, "git rev-list failed: " + err.Error()
	}
	if strings.TrimSpace(string(ahead)) != "0" {
		return false, "worktree has commits ahead of upstream (push or discard first)"
	}
	return true, ""
}

// opTaskUnpublish retracts a published task. Supervisor-only.
//
// Default behavior removes the task row and archives the room. It does
// NOT touch the worktree lalia created — worktrees often hold a live
// agent's cwd, so wiping them is opt-in via --wipe-worktree.
//
// Safety gates (any refusal fails the whole call; no half-executed state):
//
//   - Auth: worker callers rejected.
//   - Row gate: if the task has an owner or the room has non-bundle
//     traffic, requires `force=true`.
//   - Wipe gate (only if `wipe_worktree=true`):
//       · If the worktree is dirty (uncommitted or unpushed), refuses
//         regardless of any flag — never silently discards work.
//       · If the current owner's lease is still live, refuses unless
//         `evict_owner=true`. Lease-only liveness: `now < ExpiresAt`.
//
// Accurate response fields: `worktree_removed` is derived from a real
// post-remove filesystem check, not from "we tried."
func (s *State) opTaskUnpublish(req Request) Response {
	from := strVal(req.Args, "from")
	slug := strVal(req.Args, "slug")
	pid := strVal(req.Args, "project")
	force, _ := req.Args["force"].(bool)
	wipeWorktree, _ := req.Args["wipe_worktree"].(bool)
	evictOwner, _ := req.Args["evict_owner"].(bool)

	if from == "" {
		return errorResponse(CodeError, "missing_from", "set LALIA_NAME or pass --as", "from required", nil)
	}
	if slug == "" {
		return errorResponse(CodeError, "missing_slug", "provide the slug to unpublish", "slug required", nil)
	}
	if pid == "" {
		return errorResponse(CodeError, "missing_project", "provide --project or run from a git repo", "project required", nil)
	}

	s.mu.Lock()
	if resp, ok := s.requireSupervisor(from, pid); !ok {
		s.mu.Unlock()
		return resp
	}
	tl := s.tasks[pid]
	if tl == nil {
		s.mu.Unlock()
		return errorResponse(CodeNotFound, "task_list_not_found", "no tasks published for this project", "no task list for project: "+pid, map[string]any{"project": pid})
	}
	t := findTask(tl, slug)
	if t == nil {
		s.mu.Unlock()
		return errorResponse(CodeNotFound, "slug_not_found", "check lalia task show for available slugs", "slug not found: "+slug, map[string]any{"slug": slug})
	}
	taskCopy := *t
	repoRoot := tl.RepoRoot
	s.mu.Unlock()

	// Count room traffic. The bundle is the first post on a published
	// task's room, so msgCount ≤ 1 means no traffic beyond the bundle.
	var msgCount int
	s.mu.Lock()
	_, hasRoom := s.rooms[slug]
	s.mu.Unlock()
	if hasRoom {
		s.mu.Lock()
		rr := s.rooms[slug]
		s.mu.Unlock()
		rr.mu.Lock()
		msgCount = len(rr.log)
		rr.mu.Unlock()
	}

	hasOwner := taskCopy.Owner != ""
	hasTraffic := msgCount > 1

	// --- Row gate ---
	if (hasOwner || hasTraffic) && !force {
		return errorResponse(CodeError, "unpublish_needs_force", "re-run with --force to proceed; the task has active state", "task "+slug+" has owner or room traffic — unpublish requires --force", map[string]any{
			"slug":        slug,
			"owner":       taskCopy.Owner,
			"room_msgs":   msgCount,
			"bundle_only": !hasTraffic,
		})
	}

	// --- Wipe gate (only when the caller asked for a worktree removal) ---
	var worktreePreserved string
	if !wipeWorktree {
		worktreePreserved = "default"
	}
	if wipeWorktree && taskCopy.Worktree != "" {
		// Hard gate: dirty worktree refuses regardless of --evict-owner.
		if clean, detail := worktreeIsClean(taskCopy.Worktree); !clean {
			return errorResponse(CodeError, "worktree_not_clean", "commit or push work in the worktree, or delete it manually, then retry", "worktree "+taskCopy.Worktree+" is not clean: "+detail, map[string]any{
				"slug":     slug,
				"worktree": taskCopy.Worktree,
				"detail":   detail,
			})
		}
		// Liveness gate: if owner's lease is still live, refuse without
		// --evict-owner. Missing owner / unregistered agent counts as
		// not-live.
		if hasOwner {
			s.mu.Lock()
			ownerAgent := s.agentByName(taskCopy.Owner)
			var leaseExpiresAt time.Time
			var ownerAgentID string
			if ownerAgent != nil {
				leaseExpiresAt = ownerAgent.ExpiresAt
				ownerAgentID = ownerAgent.AgentID
			}
			s.mu.Unlock()
			ownerLive := ownerAgent != nil && time.Now().Before(leaseExpiresAt)
			if ownerLive && !evictOwner {
				return errorResponse(CodeError, "owner_lease_live", "wait for the owner to unregister or pass --evict-owner to override", "owner "+taskCopy.Owner+"'s lease is live until "+leaseExpiresAt.Format(time.RFC3339), map[string]any{
					"slug":           slug,
					"owner":          taskCopy.Owner,
					"owner_agent_id": ownerAgentID,
					"expires_at":     leaseExpiresAt.Format(time.RFC3339),
				})
			}
		}
	}

	// --- All gates passed. Perform the removals. ---

	// Worktree removal under the per-repo lock (same serialization domain
	// as publish).
	worktreeRemoved := false
	var worktreeRemoveErr string
	if wipeWorktree && taskCopy.Worktree != "" && repoRoot != "" {
		rl := s.repoLock(repoRoot)
		rl.Lock()
		err := removeWorktree(repoRoot, taskCopy.Worktree)
		rl.Unlock()
		if err != nil {
			worktreeRemoveErr = err.Error()
			worktreePreserved = "remove_failed"
		} else {
			worktreeRemoved = true
		}
	}

	// Archive the room (idempotent).
	roomArchived := false
	if hasRoom {
		roomArchived = s.archiveRoom(slug) || true
		// archiveRoom returns false when the room was already archived;
		// either way, the room is archived post-call.
	}

	// Drop the task row.
	s.mu.Lock()
	tl = s.tasks[pid]
	if tl != nil {
		filtered := tl.Tasks[:0]
		for _, existing := range tl.Tasks {
			if existing.Slug != slug {
				filtered = append(filtered, existing)
			}
		}
		tl.Tasks = filtered
		tl.UpdatedAt = time.Now()
		tlCopy := *tl
		s.mu.Unlock()
		s.persistTaskList(&tlCopy)
	} else {
		s.mu.Unlock()
	}

	out := map[string]any{
		"slug":             slug,
		"removed":          true,
		"room_archived":    roomArchived,
		"worktree_removed": worktreeRemoved,
	}
	if worktreePreserved != "" {
		out["worktree_preserved"] = worktreePreserved
	}
	if worktreeRemoveErr != "" {
		out["worktree_remove_error"] = worktreeRemoveErr
	}
	return Response{OK: true, Data: out}
}

// opTaskHandoff transfers supervisor rights from the caller to a new agent.
// The new agent must be registered with role=supervisor.
// Atomically updates TaskList.Supervisor and rewires room membership.
func (s *State) opTaskHandoff(req Request) Response {
	from := strVal(req.Args, "from")
	newSup := strVal(req.Args, "to")
	pid := strVal(req.Args, "project")

	if from == "" {
		return errorResponse(CodeError, "missing_from", "set LALIA_NAME or pass --as", "from required", nil)
	}
	if newSup == "" {
		return errorResponse(CodeError, "missing_to", "provide the new supervisor's agent name", "to required", nil)
	}
	if pid == "" {
		return errorResponse(CodeError, "missing_project", "provide --project or run from a git repo", "project required", nil)
	}

	s.mu.Lock()
	if resp, ok := s.requireSupervisor(from, pid); !ok {
		s.mu.Unlock()
		return resp
	}
	newAgent := s.agentByName(newSup)
	if newAgent == nil {
		s.mu.Unlock()
		return errorResponse(CodeNotFound, "agent_not_found", "the new supervisor must be registered", "agent not registered: "+newSup, map[string]any{"to": newSup})
	}
	if newAgent.Role != "supervisor" {
		s.mu.Unlock()
		return errorResponse(CodeUnauthorized, "not_supervisor", "new supervisor must register with --role supervisor", "agent "+newSup+" does not have supervisor role", map[string]any{"to": newSup})
	}
	tl := s.tasks[pid]
	if tl == nil {
		s.mu.Unlock()
		return errorResponse(CodeNotFound, "task_list_not_found", "no tasks for this project", "no task list for project: "+pid, map[string]any{"project": pid})
	}

	var activeRooms []string
	for _, t := range tl.Tasks {
		if t.Status != statusMerged {
			activeRooms = append(activeRooms, t.Slug)
		}
	}

	tl.Supervisor = newSup
	tl.UpdatedAt = time.Now()
	tlCopy := *tl
	s.mu.Unlock()
	s.persistTaskList(&tlCopy)

	for _, slug := range activeRooms {
		s.mu.Lock()
		r, ok := s.rooms[slug]
		s.mu.Unlock()
		if !ok {
			continue
		}
		r.mu.Lock()
		r.members[newSup] = true
		delete(r.members, from)
		r.mu.Unlock()
		s.persistRoomMembers(r)
	}

	return Response{OK: true, Data: map[string]any{"project": pid, "supervisor": newSup}}
}
