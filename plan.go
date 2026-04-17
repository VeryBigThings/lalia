package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

type Assignment struct {
	Slug             string    `json:"slug"`
	Goal             string    `json:"goal,omitempty"`
	Worktree         string    `json:"worktree,omitempty"`
	Owner            string    `json:"owner,omitempty"`
	Status           string    `json:"status"`
	Kickoff          string    `json:"kickoff,omitempty"`
	KickoffDelivered bool      `json:"kickoff_delivered,omitempty"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type Plan struct {
	ProjectID   string       `json:"project_id"`
	Supervisor  string       `json:"supervisor"`
	Assignments []Assignment `json:"assignments"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

// loadPlans reads all plans/*/plan.json from the workspace on daemon startup.
// For merged assignments it reconstructs an archived room stub so opPost
// correctly rejects new posts after a daemon restart.
func (s *State) loadPlans() error {
	if s.plans == nil {
		s.plans = make(map[string]*Plan)
	}
	dir := filepath.Join(workspacePath(), "plans")
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
		fpath := filepath.Join(dir, e.Name(), "plan.json")
		b, err := os.ReadFile(fpath)
		if err != nil {
			continue
		}
		var p Plan
		if err := json.Unmarshal(b, &p); err != nil {
			continue
		}
		if p.ProjectID == "" {
			p.ProjectID = e.Name()
		}
		s.plans[p.ProjectID] = &p
		// Recreate archived room stubs for merged assignments.
		for _, asgn := range p.Assignments {
			if asgn.Status == statusMerged {
				if _, ok := s.rooms[asgn.Slug]; !ok {
					r := newRoom(asgn.Slug, "", p.Supervisor)
					r.Archived = true
					s.rooms[asgn.Slug] = r
				} else {
					s.rooms[asgn.Slug].Archived = true
				}
			}
		}
	}
	return nil
}

func (s *State) persistPlan(p *Plan) {
	data, _ := json.MarshalIndent(p, "", "  ")
	data = append(data, '\n')
	s.enqueueWrite(
		filepath.Join("plans", p.ProjectID, "plan.json"),
		data,
		fmt.Sprintf("plan update: %s (supervisor=%s)", p.ProjectID, p.Supervisor),
	)
}

// findAssignment returns a pointer into the plan's slice. Call under s.mu.
func findAssignment(p *Plan, slug string) *Assignment {
	for i := range p.Assignments {
		if p.Assignments[i].Slug == slug {
			return &p.Assignments[i]
		}
	}
	return nil
}

// requireSupervisor checks that from is registered as a supervisor and is
// the supervisor for the given project. Must be called with s.mu held.
func (s *State) requireSupervisor(from, projectID string) (Response, bool) {
	a := s.agentByName(from)
	if a == nil {
		return errorResponse(CodeUnauthorized, "not_registered", "run lesche register", "not registered: "+from, map[string]any{"from": from}), false
	}
	if a.Role != "supervisor" {
		return errorResponse(CodeUnauthorized, "not_supervisor", "register with --role supervisor", "caller is not a supervisor: "+from, map[string]any{"from": from}), false
	}
	if p, ok := s.plans[projectID]; ok && p.Supervisor != from {
		return errorResponse(CodeUnauthorized, "not_project_supervisor", "only the project supervisor may perform this action", "project "+projectID+" supervisor is "+p.Supervisor+", not "+from, map[string]any{"project": projectID, "supervisor": p.Supervisor}), false
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
	for _, m := range members {
		r.members[m] = true
	}
	r.mu.Unlock()
	return r
}

// archiveRoom marks a room as archived (no new posts). Must NOT be called
// with s.mu held. Safe to call if the room does not exist.
func (s *State) archiveRoom(slug string) {
	s.mu.Lock()
	r, ok := s.rooms[slug]
	s.mu.Unlock()
	if !ok {
		return
	}
	r.mu.Lock()
	r.Archived = true
	r.mu.Unlock()
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

// deliverKickoffs posts any undelivered kickoff messages to the appropriate
// assignment rooms. Called from opRegister after the agent is indexed.
func (s *State) deliverKickoffs(agentName string) {
	type item struct {
		projectID string
		asgnIdx   int
	}
	s.mu.Lock()
	var work []item
	for pid, plan := range s.plans {
		for i, asgn := range plan.Assignments {
			if asgn.Owner == agentName && asgn.Kickoff != "" && !asgn.KickoffDelivered {
				work = append(work, item{pid, i})
			}
		}
	}
	s.mu.Unlock()

	for _, w := range work {
		s.mu.Lock()
		plan, ok := s.plans[w.projectID]
		if !ok || w.asgnIdx >= len(plan.Assignments) {
			s.mu.Unlock()
			continue
		}
		asgn := plan.Assignments[w.asgnIdx]
		supervisor := plan.Supervisor
		s.mu.Unlock()

		r := s.ensureRoomWithMembers(asgn.Slug, supervisor, []string{supervisor, agentName})
		s.internalPost(r, supervisor, asgn.Kickoff)

		s.mu.Lock()
		if p, ok := s.plans[w.projectID]; ok && w.asgnIdx < len(p.Assignments) {
			p.Assignments[w.asgnIdx].KickoffDelivered = true
			p.UpdatedAt = time.Now()
			planCopy := *p
			s.mu.Unlock()
			s.persistPlan(&planCopy)
		} else {
			s.mu.Unlock()
		}
	}
}

// assignmentToMap converts an Assignment to the wire representation.
func assignmentToMap(a Assignment) map[string]any {
	return map[string]any{
		"slug":              a.Slug,
		"goal":              a.Goal,
		"worktree":          a.Worktree,
		"owner":             a.Owner,
		"status":            a.Status,
		"kickoff_delivered": a.KickoffDelivered,
		"updated_at":        a.UpdatedAt.Format(time.RFC3339),
	}
}

func planToMap(p *Plan) map[string]any {
	asgnList := make([]any, len(p.Assignments))
	for i, a := range p.Assignments {
		asgnList[i] = assignmentToMap(a)
	}
	return map[string]any{
		"project_id":  p.ProjectID,
		"supervisor":  p.Supervisor,
		"assignments": asgnList,
		"updated_at":  p.UpdatedAt.Format(time.RFC3339),
	}
}

// opPlanCreate creates a new open assignment in the project plan.
// Supervisor only. Creates the plan record if it doesn't exist yet.
func (s *State) opPlanCreate(req Request) Response {
	from := strVal(req.Args, "from")
	slug := strVal(req.Args, "slug")
	goal := strVal(req.Args, "goal")
	pid := strVal(req.Args, "project")

	if from == "" {
		return errorResponse(CodeError, "missing_from", "set LESCHE_NAME or pass --as", "from required", nil)
	}
	if slug == "" {
		return errorResponse(CodeError, "missing_slug", "provide a slug for the new assignment", "slug required", nil)
	}
	if pid == "" {
		return errorResponse(CodeError, "missing_project", "provide --project or run from a git repo", "project required", nil)
	}

	s.mu.Lock()
	if resp, ok := s.requireSupervisor(from, pid); !ok {
		s.mu.Unlock()
		return resp
	}
	plan := s.plans[pid]
	if plan == nil {
		plan = &Plan{ProjectID: pid, Supervisor: from}
		s.plans[pid] = plan
	}
	if findAssignment(plan, slug) != nil {
		s.mu.Unlock()
		return errorResponse(CodeError, "slug_exists", "choose a different slug or assign the existing one", "assignment already exists: "+slug, map[string]any{"slug": slug})
	}
	plan.Assignments = append(plan.Assignments, Assignment{
		Slug:      slug,
		Goal:      goal,
		Status:    statusOpen,
		UpdatedAt: time.Now(),
	})
	plan.UpdatedAt = time.Now()
	planCopy := *plan
	s.mu.Unlock()
	s.persistPlan(&planCopy)

	return Response{OK: true, Data: map[string]any{"slug": slug, "project": pid, "status": statusOpen}}
}

// opPlanAssign assigns a slug to a worker. Supervisor only.
// Auto-creates the assignment-scoped room and joins both supervisor and owner.
// Verifies the worktree path exists before writing.
func (s *State) opPlanAssign(req Request) Response {
	from := strVal(req.Args, "from")
	slug := strVal(req.Args, "slug")
	owner := strVal(req.Args, "owner")
	worktree := strVal(req.Args, "worktree")
	goal := strVal(req.Args, "goal")
	kickoff := strVal(req.Args, "kickoff")
	pid := strVal(req.Args, "project")

	if from == "" {
		return errorResponse(CodeError, "missing_from", "set LESCHE_NAME or pass --as", "from required", nil)
	}
	if slug == "" || owner == "" {
		return errorResponse(CodeError, "missing_params", "provide slug and owner", "slug and owner required", map[string]any{"slug": slug, "owner": owner})
	}
	if pid == "" {
		return errorResponse(CodeError, "missing_project", "provide --project or run from a git repo", "project required", nil)
	}
	if worktree != "" {
		if _, err := os.Stat(worktree); err != nil {
			return errorResponse(CodeError, "worktree_not_found", "ensure the worktree path exists on this machine", "worktree path does not exist: "+worktree, map[string]any{"worktree": worktree})
		}
	}

	s.mu.Lock()
	if resp, ok := s.requireSupervisor(from, pid); !ok {
		s.mu.Unlock()
		return resp
	}
	if s.agentByName(owner) == nil {
		s.mu.Unlock()
		return errorResponse(CodeNotFound, "owner_not_registered", "the target agent must be registered", "agent not registered: "+owner, map[string]any{"owner": owner})
	}
	plan := s.plans[pid]
	if plan == nil {
		plan = &Plan{ProjectID: pid, Supervisor: from}
		s.plans[pid] = plan
	}
	asgn := findAssignment(plan, slug)
	if asgn == nil {
		plan.Assignments = append(plan.Assignments, Assignment{Slug: slug})
		asgn = &plan.Assignments[len(plan.Assignments)-1]
	}
	if goal != "" {
		asgn.Goal = goal
	}
	asgn.Owner = owner
	asgn.Status = statusAssigned
	asgn.Worktree = worktree
	if kickoff != "" {
		asgn.Kickoff = kickoff
		asgn.KickoffDelivered = false
	}
	asgn.UpdatedAt = time.Now()
	plan.UpdatedAt = time.Now()
	planCopy := *plan
	s.mu.Unlock()
	s.persistPlan(&planCopy)

	// Auto-create and join the slug-named room with supervisor + owner.
	s.ensureRoomWithMembers(slug, from, []string{from, owner})
	s.persistRoomDefinition(s.rooms[slug])
	s.persistRoomMembers(s.rooms[slug])

	return Response{OK: true, Data: assignmentToMap(*asgn)}
}

// opPlanUnassign clears the owner of a slug and resets status to open.
// Archives the assignment room (no new posts). Supervisor only.
func (s *State) opPlanUnassign(req Request) Response {
	from := strVal(req.Args, "from")
	slug := strVal(req.Args, "slug")
	pid := strVal(req.Args, "project")

	if from == "" {
		return errorResponse(CodeError, "missing_from", "set LESCHE_NAME or pass --as", "from required", nil)
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
	plan := s.plans[pid]
	if plan == nil {
		s.mu.Unlock()
		return errorResponse(CodeNotFound, "plan_not_found", "run plan create to initialise a plan for this project", "no plan for project: "+pid, map[string]any{"project": pid})
	}
	asgn := findAssignment(plan, slug)
	if asgn == nil {
		s.mu.Unlock()
		return errorResponse(CodeNotFound, "slug_not_found", "check lesche plan show for available slugs", "slug not found: "+slug, map[string]any{"slug": slug})
	}
	asgn.Owner = ""
	asgn.Status = statusOpen
	asgn.UpdatedAt = time.Now()
	plan.UpdatedAt = time.Now()
	planCopy := *plan
	s.mu.Unlock()
	s.persistPlan(&planCopy)

	s.archiveRoom(slug)

	return Response{OK: true, Data: assignmentToMap(*asgn)}
}

// opPlanStatus flips the status of an assignment. Workers may only flip
// their own row to in-progress|ready|blocked. Supervisors may also set merged.
func (s *State) opPlanStatus(req Request) Response {
	from := strVal(req.Args, "from")
	slug := strVal(req.Args, "slug")
	status := strVal(req.Args, "status")
	pid := strVal(req.Args, "project")

	if from == "" {
		return errorResponse(CodeError, "missing_from", "set LESCHE_NAME or pass --as", "from required", nil)
	}
	if slug == "" || status == "" {
		return errorResponse(CodeError, "missing_params", "provide slug and status", "slug and status required", nil)
	}
	if pid == "" {
		return errorResponse(CodeError, "missing_project", "provide --project or run from a git repo", "project required", nil)
	}

	s.mu.Lock()
	plan := s.plans[pid]
	if plan == nil {
		s.mu.Unlock()
		return errorResponse(CodeNotFound, "plan_not_found", "run plan create to initialise a plan for this project", "no plan for project: "+pid, map[string]any{"project": pid})
	}
	asgn := findAssignment(plan, slug)
	if asgn == nil {
		s.mu.Unlock()
		return errorResponse(CodeNotFound, "slug_not_found", "check lesche plan show for available slugs", "slug not found: "+slug, map[string]any{"slug": slug})
	}
	isSupervisor := plan.Supervisor == from
	isOwner := asgn.Owner == from

	if !isSupervisor && !isOwner {
		s.mu.Unlock()
		return errorResponse(CodeUnauthorized, "not_your_row", "only the row owner or supervisor may change this status", "cannot mutate row owned by "+asgn.Owner, map[string]any{"slug": slug, "owner": asgn.Owner, "from": from})
	}
	if !isSupervisor && !workerStatuses[status] {
		s.mu.Unlock()
		return errorResponse(CodeUnauthorized, "status_not_allowed", "workers may only set: in-progress, ready, blocked", "workers cannot set status "+status, map[string]any{"status": status})
	}
	if !workerStatuses[status] && status != statusMerged && status != statusOpen && status != statusAssigned {
		s.mu.Unlock()
		return errorResponse(CodeError, "invalid_status", "valid values: open, assigned, in-progress, ready, blocked, merged", "invalid status: "+status, map[string]any{"status": status})
	}

	archive := status == statusMerged
	asgn.Status = status
	asgn.UpdatedAt = time.Now()
	plan.UpdatedAt = time.Now()
	planCopy := *plan
	s.mu.Unlock()
	s.persistPlan(&planCopy)

	if archive {
		s.archiveRoom(slug)
	}

	return Response{OK: true, Data: assignmentToMap(*asgn)}
}

// opPlanClaim lets a worker claim an open assignment. Sets owner=from,
// status=in-progress. Verifies the worktree path if provided.
func (s *State) opPlanClaim(req Request) Response {
	from := strVal(req.Args, "from")
	slug := strVal(req.Args, "slug")
	worktree := strVal(req.Args, "worktree")
	pid := strVal(req.Args, "project")

	if from == "" {
		return errorResponse(CodeError, "missing_from", "set LESCHE_NAME or pass --as", "from required", nil)
	}
	if slug == "" {
		return errorResponse(CodeError, "missing_slug", "provide the slug to claim", "slug required", nil)
	}
	if pid == "" {
		return errorResponse(CodeError, "missing_project", "provide --project or run from a git repo", "project required", nil)
	}
	if worktree != "" {
		if _, err := os.Stat(worktree); err != nil {
			return errorResponse(CodeError, "worktree_not_found", "ensure the worktree path exists on this machine", "worktree path does not exist: "+worktree, map[string]any{"worktree": worktree})
		}
	}

	s.mu.Lock()
	plan := s.plans[pid]
	if plan == nil {
		s.mu.Unlock()
		return errorResponse(CodeNotFound, "plan_not_found", "run plan create to initialise a plan for this project", "no plan for project: "+pid, map[string]any{"project": pid})
	}
	asgn := findAssignment(plan, slug)
	if asgn == nil {
		s.mu.Unlock()
		return errorResponse(CodeNotFound, "slug_not_found", "check lesche plan show for available slugs", "slug not found: "+slug, map[string]any{"slug": slug})
	}
	if asgn.Status != statusOpen {
		s.mu.Unlock()
		return errorResponse(CodeError, "not_open", "only open assignments can be claimed", "assignment "+slug+" is not open (status="+asgn.Status+")", map[string]any{"slug": slug, "status": asgn.Status})
	}
	asgn.Owner = from
	asgn.Status = statusInProgress
	if worktree != "" {
		asgn.Worktree = worktree
	}
	asgn.UpdatedAt = time.Now()
	plan.UpdatedAt = time.Now()
	planCopy := *plan
	s.mu.Unlock()
	s.persistPlan(&planCopy)

	return Response{OK: true, Data: assignmentToMap(*asgn)}
}

// opPlanShow returns the plan for the requested project. Anyone can read.
func (s *State) opPlanShow(req Request) Response {
	pid := strVal(req.Args, "project")
	if pid == "" {
		return errorResponse(CodeError, "missing_project", "provide --project or run from a git repo", "project required", nil)
	}

	s.mu.Lock()
	plan := s.plans[pid]
	s.mu.Unlock()

	if plan == nil {
		return errorResponse(CodeNotFound, "plan_not_found", "no plan exists for this project yet", "no plan for project: "+pid, map[string]any{"project": pid})
	}
	return Response{OK: true, Data: planToMap(plan)}
}

// opPlanList returns all plans where the caller is supervisor or an owner.
func (s *State) opPlanList(req Request) Response {
	from := strVal(req.Args, "from")
	if from == "" {
		return errorResponse(CodeError, "missing_from", "set LESCHE_NAME or pass --as", "from required", nil)
	}

	s.mu.Lock()
	var out []any
	for _, plan := range s.plans {
		if plan.Supervisor == from {
			out = append(out, planToMap(plan))
			continue
		}
		for _, a := range plan.Assignments {
			if a.Owner == from {
				out = append(out, planToMap(plan))
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

// opPlanHandoff transfers supervisor rights from the caller to a new agent.
// The new agent must be registered with role=supervisor.
// Atomically updates Plan.Supervisor and rewires room membership.
func (s *State) opPlanHandoff(req Request) Response {
	from := strVal(req.Args, "from")
	newSup := strVal(req.Args, "to")
	pid := strVal(req.Args, "project")

	if from == "" {
		return errorResponse(CodeError, "missing_from", "set LESCHE_NAME or pass --as", "from required", nil)
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
	plan := s.plans[pid]
	if plan == nil {
		s.mu.Unlock()
		return errorResponse(CodeNotFound, "plan_not_found", "no plan exists for this project", "no plan for project: "+pid, map[string]any{"project": pid})
	}

	// Collect non-archived room names for membership rewiring.
	var activeRooms []string
	for _, asgn := range plan.Assignments {
		if asgn.Status != statusMerged {
			activeRooms = append(activeRooms, asgn.Slug)
		}
	}

	plan.Supervisor = newSup
	plan.UpdatedAt = time.Now()
	planCopy := *plan
	s.mu.Unlock()
	s.persistPlan(&planCopy)

	// Rewire room membership: add new supervisor, remove old supervisor.
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
